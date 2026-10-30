package cli

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/scanner"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func (r *runner) scanAll(ctx context.Context, client kubernetes.Interface, config *rest.Config, results []*model.ScanResult, pvcs []kube.PVCInfo) {
	sem := make(chan struct{}, r.options.concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	for i, pvc := range pvcs {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		if ctx.Err() != nil {
			<-sem
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r.scanPVC(ctx, client, config, i, results[i], pvc)
		}()
	}
}

func (r *runner) scanPVC(ctx context.Context, client kubernetes.Interface, config *rest.Config, index int, result *model.ScanResult, pvc kube.PVCInfo) {
	ctx = scanner.WithObserver(ctx, func(status model.ScanStatus, detail string) {
		if status != "" {
			result.Status = status
			r.publishResult(index, result)
		}
		if detail != "" && r.options.logLevel == "debug" {
			r.debugMu.Lock()
			r.debugDetails = append(r.debugDetails, detail)
			r.debugMu.Unlock()
		}
	})
	result.Status = model.StatusPreparing
	r.publishResult(index, result)
	defer r.publishResult(index, result)
	if ctx.Err() != nil {
		r.fail(result, ctx, ctx.Err())
		return
	}
	if pvc.VolumeMode == corev1.PersistentVolumeBlock {
		r.fail(result, ctx, fmt.Errorf("block-mode PVC is unsupported"))
		return
	}
	for _, mount := range pvc.Mounts {
		if !mount.WholeVolume() {
			continue
		}
		result.PodName, result.ScanPath = mount.PodName, mount.MountPath
		scanCtx, cancel := context.WithTimeout(ctx, r.options.timeout)
		r.messageAt(slog.LevelInfo, fmt.Sprintf("%s/%s: scanning %s:%s", pvc.Namespace, pvc.Name, mount.PodName, mount.MountPath))
		// Never upload into application storage: a failed scratch upload can use
		// a temporary pod with --force, but cannot overwrite PVC contents.
		sr, err := r.deps.UploadAndScan(scanCtx, client, config, pvc.Namespace, mount.PodName, mount.ContainerName, "/tmp", mount.MountPath, &pvc, r.options.maxDepth, r.options.excludes, r.options.workers, false)
		err = scanError(sr, err)
		if err != nil {
			if ctx.Err() != nil {
				r.fail(result, ctx, ctx.Err())
			} else if r.options.force {
				if r.options.logLevel == "debug" {
					result.Warnings = append(result.Warnings, fmt.Sprintf("existing pod scan failed; temporary-pod fallback: %v", err))
				}
				cancel()
				r.scanWithTempPod(ctx, client, config, result, pvc)
				if result.Status == model.StatusError {
					result.Error = fmt.Sprintf("existing pod scan: %v; temporary pod scan: %s", err, result.Error)
				}
				return
			} else {
				r.fail(result, scanCtx, err)
			}
			cancel()
			return
		}
		cancel()
		result.UsedBytes, result.Status, result.Error = sr.UsedBytes, model.StatusDone, ""
		return
	}
	r.scanWithTempPod(ctx, client, config, result, pvc)
}

func (r *runner) publishResult(index int, result *model.ScanResult) {
	if r.liveTable == nil {
		return
	}
	// Finish reports output errors after all scan workers have stopped.
	_ = r.liveTable.Update(index, *result)
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
