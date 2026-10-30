package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/render"
	"github.com/neutry/pvdu/internal/scanner"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Dependencies describes external boundaries; omitted functions use production implementations.
type Dependencies struct {
	BuildClient     func(string, string) (kubernetes.Interface, *rest.Config, error)
	ListPVCs        func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error)
	GetPVCByName    func(context.Context, kubernetes.Interface, string, string) (*kube.PVCInfo, error)
	CreatePod       func(context.Context, kubernetes.Interface, kube.PVCInfo, string) (kube.PodRef, error)
	WaitForPodReady func(context.Context, kubernetes.Interface, string, string, time.Duration) error
	DeletePod       func(context.Context, kubernetes.Interface, string, kube.PodRef) error
	UploadAndScan   func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int, bool) (*model.ScanResult, error)
}

type runner struct {
	options      options
	deps         Dependencies
	errOut       io.Writer
	terminal     bool
	liveTable    *render.LiveTable
	debugMu      sync.Mutex
	debugDetails []string
}

type synchronizedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

func newRunner(d Dependencies) *runner {
	if d.BuildClient == nil {
		d.BuildClient = kube.BuildClient
	}
	if d.ListPVCs == nil {
		d.ListPVCs = kube.ListPVCs
	}
	if d.GetPVCByName == nil {
		d.GetPVCByName = kube.GetPVCByName
	}
	if d.CreatePod == nil {
		d.CreatePod = kube.CreateScanPod
	}
	if d.WaitForPodReady == nil {
		d.WaitForPodReady = kube.WaitForPodReady
	}
	if d.DeletePod == nil {
		d.DeletePod = kube.DeleteScanPod
	}
	if d.UploadAndScan == nil {
		d.UploadAndScan = scanner.UploadAndScan
	}
	return &runner{options: defaultOptions(), deps: d, errOut: os.Stderr}
}

func (r *runner) run(cmd *cobra.Command) error {
	o := r.options
	// Validate scanner arguments before even building a Kubernetes client.
	if _, err := scanner.ExecCommand("/tmp/scanner", "/mnt", o.maxDepth, o.excludes, o.workers, false); err != nil {
		return err
	}
	if file, ok := cmd.ErrOrStderr().(*os.File); ok {
		r.terminal = term.IsTerminal(int(file.Fd()))
	}
	r.errOut = &synchronizedWriter{w: cmd.ErrOrStderr()}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client, config, err := r.deps.BuildClient(o.kubeconfig, o.context)
	if err != nil {
		return fmt.Errorf("build k8s client: %w", err)
	}
	ns := o.namespace
	if o.allNamespaces {
		ns = ""
	}
	var pvcs []kube.PVCInfo
	discoveryCtx, discoveryCancel := context.WithTimeout(ctx, o.timeout)
	defer discoveryCancel()
	if o.pvc != "" {
		pvc, err := r.deps.GetPVCByName(discoveryCtx, client, ns, o.pvc)
		if err != nil {
			return fmt.Errorf("get PVC: %w", err)
		}
		pvcs = []kube.PVCInfo{*pvc}
	} else {
		pvcs, err = r.deps.ListPVCs(discoveryCtx, client, ns)
		if err != nil {
			return fmt.Errorf("list PVCs: %w", err)
		}
	}
	discoveryCancel()
	results := make([]*model.ScanResult, len(pvcs))
	for i, pvc := range pvcs {
		result := &model.ScanResult{Namespace: pvc.Namespace, PVCName: pvc.Name, RequestedBytes: pvc.RequestedBytes, RequestedStr: pvc.RequestedStr, PVBytes: pvc.PVBytes, Status: model.StatusPending}
		for _, mount := range pvc.Mounts {
			if mount.WholeVolume() {
				result.PodName, result.ScanPath = mount.PodName, mount.MountPath
				break
			}
		}
		results[i] = result
	}
	if o.output == "default" || o.output == "table" {
		if file, ok := cmd.OutOrStdout().(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			liveTable, err := render.NewLiveTable(cmd.OutOrStdout(), o.output, results)
			if err != nil {
				return fmt.Errorf("start live table: %w", err)
			}
			r.liveTable = liveTable
			defer func() { r.liveTable = nil }()
		}
	}
	r.scanAll(ctx, client, config, results, pvcs)
	if r.liveTable != nil {
		if err := r.liveTable.Finish(); err != nil {
			return fmt.Errorf("finish live table: %w", err)
		}
	}
	if ctx.Err() != nil {
		r.messageAt(slog.LevelWarn, "interrupted")
		return ctx.Err()
	}
	if r.liveTable == nil {
		if err := render.WriteResults(cmd.OutOrStdout(), o.output, results); err != nil {
			return err
		}
	}
	if len(r.debugDetails) > 0 {
		fmt.Fprintln(r.errOut, "\nDebug:")
		for _, detail := range r.debugDetails {
			fmt.Fprintln(r.errOut, "  "+detail)
		}
	}
	failed := 0
	for _, result := range results {
		if result.Status == model.StatusError {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d PVC scans failed", failed, len(results))
	}
	return nil
}

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

func (r *runner) scanWithTempPod(ctx context.Context, client kubernetes.Interface, config *rest.Config, result *model.ScanResult, pvc kube.PVCInfo) {
	if ctx.Err() != nil {
		r.fail(result, ctx, ctx.Err())
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, r.options.timeout)
	defer cancel()
	result.PodName, result.ScanPath = "", "/mnt"
	scanner.NotifyProgress(ctx, model.StatusCreatingPod, "")
	r.messageAt(slog.LevelInfo, fmt.Sprintf("%s/%s: creating temporary pod", pvc.Namespace, pvc.Name))
	pod, err := r.deps.CreatePod(opCtx, client, pvc, r.options.image)
	if err != nil {
		r.fail(result, opCtx, err)
		return
	}
	result.PodName, result.ScanPath = pod.Name, "/mnt"
	scanner.NotifyProgress(ctx, model.StatusWaitingPod, "")
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), r.options.cleanupTimeout)
		defer cleanupCancel()
		if err := r.deps.DeletePod(cleanupCtx, client, pvc.Namespace, pod); err != nil {
			warning := fmt.Sprintf("delete temp pod failed: %s/%s: %v", pvc.Namespace, pod.Name, err)
			result.Warnings = append(result.Warnings, warning)
		}
	}()
	if err := r.deps.WaitForPodReady(opCtx, client, pvc.Namespace, pod.Name, r.options.timeout); err != nil {
		r.fail(result, opCtx, err)
		return
	}
	scanner.NotifyProgress(ctx, model.StatusPodReady, "")
	r.messageAt(slog.LevelInfo, fmt.Sprintf("%s/%s: uploading scanner and scanning temporary pod %s:/mnt", pvc.Namespace, pvc.Name, pod.Name))
	sr, err := r.deps.UploadAndScan(opCtx, client, config, pvc.Namespace, pod.Name, "scanner", "/tmp", "/mnt", &pvc, r.options.maxDepth, r.options.excludes, r.options.workers, false)
	if ctx.Err() != nil {
		r.fail(result, ctx, ctx.Err())
		return
	}
	if err := scanError(sr, err); err != nil {
		r.fail(result, opCtx, err)
		return
	}
	result.UsedBytes, result.Status, result.Error = sr.UsedBytes, model.StatusDone, ""
}

func (r *runner) fail(result *model.ScanResult, ctx context.Context, err error) {
	message := err.Error()
	if ctx.Err() == context.Canceled {
		message = "interrupted"
	} else if ctx.Err() == context.DeadlineExceeded {
		message = fmt.Sprintf("timed out after %s (try --timeout/-t)", r.options.timeout)
	}
	result.Status, result.Error = model.StatusError, message
}

func (r *runner) message(message string) {
	r.messageAt(slog.LevelInfo, message)
}

func (r *runner) messageAt(level slog.Level, message string) {
	if r.liveTable != nil {
		return
	}
	minimum := slog.LevelWarn
	switch r.options.logLevel {
	case "debug":
		minimum = slog.LevelDebug
	case "info":
		minimum = slog.LevelInfo
	case "error":
		minimum = slog.LevelError
	}
	if level < minimum {
		return
	}
	if r.terminal {
		message = "\r\033[2K" + message
	}
	fmt.Fprintln(r.errOut, message)
}
