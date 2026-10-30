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
	UploadAndScan   func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int) (*model.ScanResult, error)
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
	if _, err := scanner.ExecCommand("/tmp/scanner", "/mnt", o.maxDepth, o.excludes, o.workers); err != nil {
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
