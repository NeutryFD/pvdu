// Package engine runs PVC scans independently of how their progress is shown,
// so the CLI and an interactive UI can drive the same scan lifecycle.
package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/scanner"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Dependencies describes external boundaries; omitted functions use production implementations.
type Dependencies struct {
	ListPVCs        func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error)
	GetPVCByName    func(context.Context, kubernetes.Interface, string, string) (*kube.PVCInfo, error)
	CreatePod       func(context.Context, kubernetes.Interface, kube.PVCInfo, string) (kube.PodRef, error)
	WaitForPodReady func(context.Context, kubernetes.Interface, string, string, time.Duration) error
	DeletePod       func(context.Context, kubernetes.Interface, string, kube.PodRef) error
	UploadAndScan   func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int) (*model.ScanResult, error)
}

// Options are the scan settings shared by every PVC scan of an Engine.
type Options struct {
	Image          string
	Force          bool
	Timeout        time.Duration
	CleanupTimeout time.Duration
	Concurrency    int
	MaxDepth       int
	Workers        int
	Excludes       []string
	// Debug keeps the existing-pod error as a warning when the temporary-pod
	// fallback runs.
	Debug bool
}

type EventKind int

const (
	// EventStatus carries a snapshot of a result whose status or fields changed.
	EventStatus EventKind = iota
	// EventDetail carries debug-only detail, such as the remote exec command.
	EventDetail
	// EventLog carries a progress message at Level.
	EventLog
)

// Event reports scan progress. Index is the position of the PVC in the scan
// request; Result is a copy the receiver may keep.
type Event struct {
	Kind    EventKind
	Index   int
	Result  model.ScanResult
	Detail  string
	Level   slog.Level
	Message string
}

// Handler receives events from concurrent scan workers and must be safe for
// concurrent use. It runs on the worker goroutine, so it should return quickly.
type Handler func(Event)

// Engine scans PVCs with a shared concurrency limit.
type Engine struct {
	client  kubernetes.Interface
	config  *rest.Config
	options Options
	deps    Dependencies
	sem     chan struct{}
}

func New(client kubernetes.Interface, config *rest.Config, options Options, deps Dependencies) *Engine {
	if deps.ListPVCs == nil {
		deps.ListPVCs = kube.ListPVCs
	}
	if deps.GetPVCByName == nil {
		deps.GetPVCByName = kube.GetPVCByName
	}
	if deps.CreatePod == nil {
		deps.CreatePod = kube.CreateScanPod
	}
	if deps.WaitForPodReady == nil {
		deps.WaitForPodReady = kube.WaitForPodReady
	}
	if deps.DeletePod == nil {
		deps.DeletePod = kube.DeleteScanPod
	}
	if deps.UploadAndScan == nil {
		deps.UploadAndScan = scanner.UploadAndScan
	}
	options.Concurrency = max(1, options.Concurrency)
	return &Engine{client: client, config: config, options: options, deps: deps, sem: make(chan struct{}, options.Concurrency)}
}

func (e *Engine) ListPVCs(ctx context.Context, namespace string) ([]kube.PVCInfo, error) {
	return e.deps.ListPVCs(ctx, e.client, namespace)
}

func (e *Engine) GetPVC(ctx context.Context, namespace, name string) (*kube.PVCInfo, error) {
	return e.deps.GetPVCByName(ctx, e.client, namespace, name)
}

// NewResult returns the pending result of a PVC, pointing at its first
// whole-volume mount when one exists.
func NewResult(pvc kube.PVCInfo) *model.ScanResult {
	result := &model.ScanResult{Namespace: pvc.Namespace, PVCName: pvc.Name, RequestedBytes: pvc.RequestedBytes, RequestedStr: pvc.RequestedStr, PVBytes: pvc.PVBytes, Status: model.StatusPending}
	for _, mount := range pvc.Mounts {
		if mount.WholeVolume() {
			result.PodName, result.ScanPath = mount.PodName, mount.MountPath
			break
		}
	}
	return result
}

func snapshot(result *model.ScanResult) model.ScanResult {
	copy := *result
	copy.Warnings = append([]string(nil), result.Warnings...)
	return copy
}
