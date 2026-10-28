package cmd

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

	"github.com/neutry/pvdu/internal/k8s"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Dependencies describes external boundaries; omitted functions use production implementations.
type Dependencies struct {
	BuildClient     func(string, string) (kubernetes.Interface, *rest.Config, error)
	ListPVCs        func(context.Context, kubernetes.Interface, string) ([]k8s.PVCInfo, error)
	GetPVCByName    func(context.Context, kubernetes.Interface, string, string) (*k8s.PVCInfo, error)
	CreatePod       func(context.Context, kubernetes.Interface, k8s.PVCInfo, string) (k8s.PodRef, error)
	WaitForPodReady func(context.Context, kubernetes.Interface, string, string, time.Duration) error
	DeletePod       func(context.Context, kubernetes.Interface, string, k8s.PodRef) error
	UploadAndScan   func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *k8s.PVCInfo, int, []string, int, bool) (*model.ScanResult, error)
}

type runner struct {
	options  options
	deps     Dependencies
	errOut   io.Writer
	terminal bool
	logger   *slog.Logger
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
		d.BuildClient = k8s.BuildClient
	}
	if d.ListPVCs == nil {
		d.ListPVCs = k8s.ListPVCs
	}
	if d.GetPVCByName == nil {
		d.GetPVCByName = k8s.GetPVCByName
	}
	if d.CreatePod == nil {
		d.CreatePod = k8s.CreateScanPod
	}
	if d.WaitForPodReady == nil {
		d.WaitForPodReady = k8s.WaitForPodReady
	}
	if d.DeletePod == nil {
		d.DeletePod = k8s.DeleteScanPod
	}
	if d.UploadAndScan == nil {
		d.UploadAndScan = k8s.UploadAndScanPVC
	}
	return &runner{options: defaultOptions(), deps: d, errOut: os.Stderr, logger: slog.Default()}
}

func (r *runner) run(cmd *cobra.Command) error {
	o := r.options
	// Validate scanner arguments before even building a Kubernetes client.
	if _, err := k8s.ScannerExecCommand("/tmp/scanner", "/mnt", o.maxDepth, o.excludes, o.workers, false); err != nil {
		return err
	}
	if file, ok := cmd.ErrOrStderr().(*os.File); ok {
		r.terminal = term.IsTerminal(int(file.Fd()))
	}
	r.errOut = &synchronizedWriter{w: cmd.ErrOrStderr()}
	level := slog.LevelWarn
	switch o.logLevel {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "error":
		level = slog.LevelError
	}
	r.logger = slog.New(slog.NewTextHandler(r.errOut, &slog.HandlerOptions{Level: level}))
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
	var pvcs []k8s.PVCInfo
	discoveryCtx, discoveryCancel := context.WithTimeout(ctx, o.timeout)
	defer discoveryCancel()
	if o.pvc != "" {
		pvc, err := r.deps.GetPVCByName(discoveryCtx, client, ns, o.pvc)
		if err != nil {
			return fmt.Errorf("get PVC: %w", err)
		}
		pvcs = []k8s.PVCInfo{*pvc}
	} else {
		pvcs, err = r.deps.ListPVCs(discoveryCtx, client, ns)
		if err != nil {
			return fmt.Errorf("list PVCs: %w", err)
		}
	}
	discoveryCancel()
	results := make([]*model.ScanResult, len(pvcs))
	for i, pvc := range pvcs {
		results[i] = &model.ScanResult{Namespace: pvc.Namespace, PVCName: pvc.Name, RequestedBytes: pvc.RequestedBytes, RequestedStr: pvc.RequestedStr, PVBytes: pvc.PVBytes, Status: model.StatusPending}
	}
	r.scanAll(ctx, client, config, results, pvcs)
	if ctx.Err() != nil {
		r.message("interrupted")
		return ctx.Err()
	}
	if err := ui.WriteResults(cmd.OutOrStdout(), o.output, results); err != nil {
		return err
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

func (r *runner) scanAll(ctx context.Context, client kubernetes.Interface, config *rest.Config, results []*model.ScanResult, pvcs []k8s.PVCInfo) {
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
			r.scanPVC(ctx, client, config, results[i], pvc)
		}()
	}
}

func (r *runner) scanPVC(ctx context.Context, client kubernetes.Interface, config *rest.Config, result *model.ScanResult, pvc k8s.PVCInfo) {
	result.Status = model.StatusScanning
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
		r.message(fmt.Sprintf("%s/%s: scanning %s:%s", pvc.Namespace, pvc.Name, mount.PodName, mount.MountPath))
		// Never upload into application storage: a failed scratch upload can use
		// a temporary pod with --force, but cannot overwrite PVC contents.
		sr, err := r.deps.UploadAndScan(scanCtx, client, config, pvc.Namespace, mount.PodName, mount.ContainerName, "/tmp", mount.MountPath, &pvc, r.options.maxDepth, r.options.excludes, r.options.workers, false)
		err = scanError(sr, err)
		if err != nil {
			if ctx.Err() != nil {
				r.fail(result, ctx, ctx.Err())
			} else if r.options.force {
				cancel()
				r.scanWithTempPod(ctx, client, config, result, pvc)
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

func (r *runner) scanWithTempPod(ctx context.Context, client kubernetes.Interface, config *rest.Config, result *model.ScanResult, pvc k8s.PVCInfo) {
	if ctx.Err() != nil {
		r.fail(result, ctx, ctx.Err())
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, r.options.timeout)
	defer cancel()
	pod, err := r.deps.CreatePod(opCtx, client, pvc, r.options.image)
	if err != nil {
		r.fail(result, opCtx, err)
		return
	}
	result.PodName, result.ScanPath = pod.Name, "/mnt"
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), r.options.cleanupTimeout)
		defer cleanupCancel()
		if err := r.deps.DeletePod(cleanupCtx, client, pvc.Namespace, pod); err != nil {
			warning := fmt.Sprintf("delete temp pod failed: %s/%s: %v", pvc.Namespace, pod.Name, err)
			result.Warnings = append(result.Warnings, warning)
			r.logger.Error(warning)
		}
	}()
	if err := r.deps.WaitForPodReady(opCtx, client, pvc.Namespace, pod.Name, r.options.timeout); err != nil {
		r.fail(result, opCtx, err)
		return
	}
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
	r.message(fmt.Sprintf("%s/%s: error: %s", result.Namespace, result.PVCName, message))
}

func (r *runner) message(message string) {
	if r.terminal {
		message = "\r\033[2K" + message
	}
	fmt.Fprintln(r.errOut, message)
}
