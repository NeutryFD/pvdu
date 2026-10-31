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

	"github.com/neutry/pvdu/internal/engine"
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
	return &runner{options: defaultOptions(), deps: d, errOut: os.Stderr}
}

// newEngine passes the scan boundaries on; the engine fills in production
// implementations for the omitted ones.
func (r *runner) newEngine(client kubernetes.Interface, config *rest.Config) *engine.Engine {
	o := r.options
	return engine.New(client, config, engine.Options{
		Image: o.image, Force: o.force, Timeout: o.timeout, CleanupTimeout: o.cleanupTimeout,
		Concurrency: o.concurrency, MaxDepth: o.maxDepth, Workers: o.workers, Excludes: o.excludes,
		Debug: o.logLevel == "debug",
	}, engine.Dependencies{
		ListPVCs: r.deps.ListPVCs, GetPVCByName: r.deps.GetPVCByName, CreatePod: r.deps.CreatePod,
		WaitForPodReady: r.deps.WaitForPodReady, DeletePod: r.deps.DeletePod, UploadAndScan: r.deps.UploadAndScan,
	})
}

// handle shows engine events on the live table or as log lines.
func (r *runner) handle(event engine.Event) {
	switch event.Kind {
	case engine.EventStatus:
		if r.liveTable != nil {
			// Finish reports output errors after all scan workers have stopped.
			_ = r.liveTable.Update(event.Index, event.Result)
		}
	case engine.EventDetail:
		if r.options.logLevel == "debug" {
			r.debugMu.Lock()
			r.debugDetails = append(r.debugDetails, event.Detail)
			r.debugMu.Unlock()
		}
	case engine.EventLog:
		r.messageAt(event.Level, event.Message)
	}
}

func (r *runner) run(cmd *cobra.Command) error {
	o := r.options
	// Validate scanner arguments before even building a Kubernetes client.
	if _, err := scanner.ExecCommand("/tmp/scanner", "/mnt", o.maxDepth, o.excludes, o.workers); err != nil {
		return err
	}
	if o.tui {
		return r.runTUI(cmd)
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
	scans := r.newEngine(client, config)
	ns := o.namespace
	if o.allNamespaces {
		ns = ""
	}
	var pvcs []kube.PVCInfo
	discoveryCtx, discoveryCancel := context.WithTimeout(ctx, o.timeout)
	defer discoveryCancel()
	if o.pvc != "" {
		pvc, err := scans.GetPVC(discoveryCtx, ns, o.pvc)
		if err != nil {
			return fmt.Errorf("get PVC: %w", err)
		}
		pvcs = []kube.PVCInfo{*pvc}
	} else {
		pvcs, err = scans.ListPVCs(discoveryCtx, ns)
		if err != nil {
			return fmt.Errorf("list PVCs: %w", err)
		}
	}
	discoveryCancel()
	results := make([]*model.ScanResult, len(pvcs))
	for i, pvc := range pvcs {
		results[i] = engine.NewResult(pvc)
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
	scans.ScanAll(ctx, pvcs, results, r.handle)
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
