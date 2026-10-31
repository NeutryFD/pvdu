package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/scanner"
	corev1 "k8s.io/api/core/v1"
)

// ScanAll scans pvcs[i] into results[i], starting scans in order, and returns
// once every started scan has finished. Cancelling ctx stops new scans from
// starting and interrupts running ones; temporary pods are still deleted.
func (e *Engine) ScanAll(ctx context.Context, pvcs []kube.PVCInfo, results []*model.ScanResult, handle Handler) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for i, pvc := range pvcs {
		if !e.acquire(ctx) {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer e.release()
			e.scan(ctx, i, results[i], pvc, handle)
		}()
	}
}

// Scan scans one PVC into result once a concurrency slot is free, and returns
// false without scanning when ctx ends first.
func (e *Engine) Scan(ctx context.Context, index int, result *model.ScanResult, pvc kube.PVCInfo, handle Handler) bool {
	if !e.acquire(ctx) {
		return false
	}
	defer e.release()
	e.scan(ctx, index, result, pvc, handle)
	return true
}

func (e *Engine) acquire(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case e.sem <- struct{}{}:
	}
	if ctx.Err() != nil {
		e.release()
		return false
	}
	return true
}

func (e *Engine) release() { <-e.sem }

// scanTask is the state of one PVC scan.
type scanTask struct {
	e      *Engine
	index  int
	result *model.ScanResult
	pvc    kube.PVCInfo
	handle Handler
}

func (e *Engine) scan(ctx context.Context, index int, result *model.ScanResult, pvc kube.PVCInfo, handle Handler) {
	t := &scanTask{e: e, index: index, result: result, pvc: pvc, handle: handle}
	// A panic in one scan must not kill the others or leave the receiver waiting
	// for a final status. Deferred pod cleanup has already run while unwinding.
	defer func() {
		if p := recover(); p != nil {
			result.Status, result.Error = model.StatusError, fmt.Sprintf("scan panicked: %v", p)
			t.publish()
		}
	}()
	t.run(ctx)
}

func (t *scanTask) publish() {
	if t.handle != nil {
		t.handle(Event{Kind: EventStatus, Index: t.index, Result: snapshot(t.result)})
	}
}

func (t *scanTask) log(level slog.Level, message string) {
	if t.handle != nil {
		t.handle(Event{Kind: EventLog, Index: t.index, Level: level, Message: message})
	}
}

func (t *scanTask) run(ctx context.Context) {
	e, result, pvc := t.e, t.result, t.pvc
	ctx = scanner.WithObserver(ctx, func(status model.ScanStatus, detail string) {
		if status != "" {
			result.Status = status
			t.publish()
		}
		if detail != "" && t.handle != nil {
			t.handle(Event{Kind: EventDetail, Index: t.index, Detail: detail})
		}
	})
	result.Status = model.StatusPreparing
	t.publish()
	defer t.publish()
	if ctx.Err() != nil {
		t.fail(ctx, ctx.Err())
		return
	}
	if pvc.VolumeMode == corev1.PersistentVolumeBlock {
		t.fail(ctx, fmt.Errorf("block-mode PVC is unsupported"))
		return
	}
	for _, mount := range pvc.Mounts {
		if !mount.WholeVolume() {
			continue
		}
		result.PodName, result.ScanPath = mount.PodName, mount.MountPath
		scanCtx, cancel := context.WithTimeout(ctx, e.options.Timeout)
		t.log(slog.LevelInfo, fmt.Sprintf("%s/%s: scanning %s:%s", pvc.Namespace, pvc.Name, mount.PodName, mount.MountPath))
		// Never upload into application storage: a failed scratch upload can use
		// a temporary pod with Force, but cannot overwrite PVC contents.
		sr, err := e.deps.UploadAndScan(scanCtx, e.client, e.config, pvc.Namespace, mount.PodName, mount.ContainerName, "/tmp", mount.MountPath, &pvc, e.options.MaxDepth, e.options.Excludes, e.options.Workers)
		err = scanError(sr, err)
		if err != nil {
			switch {
			case ctx.Err() != nil:
				t.fail(ctx, ctx.Err())
			case e.options.Force:
				if e.options.Debug {
					result.Warnings = append(result.Warnings, fmt.Sprintf("existing pod scan failed; temporary-pod fallback: %v", err))
				}
				cancel()
				t.scanWithTempPod(ctx)
				if result.Status == model.StatusError {
					result.Error = fmt.Sprintf("existing pod scan: %v; temporary pod scan: %s", err, result.Error)
				}
				return
			default:
				t.fail(scanCtx, err)
			}
			cancel()
			return
		}
		cancel()
		result.UsedBytes, result.Status, result.Error = sr.UsedBytes, model.StatusDone, ""
		return
	}
	t.scanWithTempPod(ctx)
}

func (t *scanTask) scanWithTempPod(ctx context.Context) {
	e, result, pvc := t.e, t.result, t.pvc
	if ctx.Err() != nil {
		t.fail(ctx, ctx.Err())
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, e.options.Timeout)
	defer cancel()
	result.PodName, result.ScanPath = "", "/mnt"
	scanner.NotifyProgress(ctx, model.StatusCreatingPod, "")
	t.log(slog.LevelInfo, fmt.Sprintf("%s/%s: creating temporary pod", pvc.Namespace, pvc.Name))
	pod, err := e.deps.CreatePod(opCtx, e.client, pvc, e.options.Image)
	if err != nil {
		t.fail(opCtx, err)
		return
	}
	result.PodName, result.ScanPath = pod.Name, "/mnt"
	scanner.NotifyProgress(ctx, model.StatusWaitingPod, "")
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), e.options.CleanupTimeout)
		defer cleanupCancel()
		if err := e.deps.DeletePod(cleanupCtx, e.client, pvc.Namespace, pod); err != nil {
			warning := fmt.Sprintf("delete temp pod failed: %s/%s: %v", pvc.Namespace, pod.Name, err)
			result.Warnings = append(result.Warnings, warning)
		}
	}()
	if err := e.deps.WaitForPodReady(opCtx, e.client, pvc.Namespace, pod.Name, e.options.Timeout); err != nil {
		t.fail(opCtx, err)
		return
	}
	scanner.NotifyProgress(ctx, model.StatusPodReady, "")
	t.log(slog.LevelInfo, fmt.Sprintf("%s/%s: uploading scanner and scanning temporary pod %s:/mnt", pvc.Namespace, pvc.Name, pod.Name))
	sr, err := e.deps.UploadAndScan(opCtx, e.client, e.config, pvc.Namespace, pod.Name, "scanner", "/tmp", "/mnt", &pvc, e.options.MaxDepth, e.options.Excludes, e.options.Workers)
	if ctx.Err() != nil {
		t.fail(ctx, ctx.Err())
		return
	}
	if err := scanError(sr, err); err != nil {
		t.fail(opCtx, err)
		return
	}
	result.UsedBytes, result.Status, result.Error = sr.UsedBytes, model.StatusDone, ""
}

func (t *scanTask) fail(ctx context.Context, err error) {
	message := err.Error()
	if ctx.Err() == context.Canceled {
		message = "interrupted"
	} else if ctx.Err() == context.DeadlineExceeded {
		message = fmt.Sprintf("timed out after %s (try --timeout/-t)", t.e.options.Timeout)
	}
	t.result.Status, t.result.Error = model.StatusError, message
}

func scanError(result *model.ScanResult, err error) error {
	if err != nil {
		return err
	}
	if result == nil {
		return fmt.Errorf("scanner returned no result")
	}
	if result.Status != model.StatusDone {
		return fmt.Errorf("scanner failed: %s", result.Error)
	}
	return nil
}
