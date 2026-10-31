// Package engine runs PVC scans independently of how their progress is shown,
// so the CLI and an interactive UI can drive the same scan lifecycle.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/scanner"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Dependencies describes external boundaries; omitted functions use production implementations.
type Dependencies struct {
	ListNamespaces  func(context.Context, kubernetes.Interface) ([]string, error)
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
	// Concurrency limits the scans running at once; 0 means no limit.
	Concurrency int
	MaxDepth    int
	Workers     int
	Excludes    []string
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
	client kubernetes.Interface
	config *rest.Config
	deps   Dependencies

	mu      sync.Mutex
	options Options
	// sem holds one token per running scan. SetOptions replaces it when the
	// concurrency changes and closes changed, so waiting scans move to the
	// new limit; running scans release the token of the sem they took. It is
	// nil when there is no limit.
	sem     chan struct{}
	changed chan struct{}
}

// Validate reports options a scan cannot use.
func (o Options) Validate() error {
	switch {
	case o.Image == "":
		return fmt.Errorf("image cannot be empty")
	case o.Concurrency < 0:
		return fmt.Errorf("concurrency cannot be negative; 0 means no limit")
	case o.Timeout <= 0 || o.CleanupTimeout <= 0:
		return fmt.Errorf("timeout and cleanup timeout must be positive")
	case o.MaxDepth < 0 || o.Workers < 0:
		return fmt.Errorf("max depth and workers must be nonnegative")
	}
	_, err := scanner.ExecCommand("/tmp/scanner", "/mnt", o.MaxDepth, o.Excludes, o.Workers)
	return err
}

// Options returns the settings new scans use.
func (e *Engine) Options() Options {
	e.mu.Lock()
	defer e.mu.Unlock()
	o := e.options
	o.Excludes = slices.Clone(o.Excludes)
	return o
}

// SetOptions changes the settings of scans that start from now on; running
// scans keep theirs. A new concurrency also applies to scans waiting for a
// slot, while running scans still count against the old limit until they
// finish.
func (e *Engine) SetOptions(o Options) error {
	if err := o.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	o.Excludes = slices.Clone(o.Excludes)
	if o.Concurrency != e.options.Concurrency {
		e.sem = newSem(o.Concurrency)
		close(e.changed)
		e.changed = make(chan struct{})
	}
	e.options = o
	return nil
}

func New(client kubernetes.Interface, config *rest.Config, options Options, deps Dependencies) *Engine {
	if deps.ListNamespaces == nil {
		deps.ListNamespaces = kube.ListNamespaces
	}
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
	options.Concurrency = max(0, options.Concurrency)
	return &Engine{client: client, config: config, options: options, deps: deps,
		sem: newSem(options.Concurrency), changed: make(chan struct{})}
}

// newSem returns the slots of a concurrency limit, or nil for 0, no limit.
func newSem(concurrency int) chan struct{} {
	if concurrency == 0 {
		return nil
	}
	return make(chan struct{}, concurrency)
}

func (e *Engine) ListNamespaces(ctx context.Context) ([]string, error) {
	return e.deps.ListNamespaces(ctx, e.client)
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
