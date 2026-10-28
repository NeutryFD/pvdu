package cmd

import (
	"context"
	"time"

	"github.com/neutry/pvdu/internal/k8s"
	"github.com/neutry/pvdu/internal/model"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// TestingHooks is a deprecated compatibility adapter. New tests should construct
// independent commands with NewCommand and Dependencies instead of global hooks.
type TestingHooks struct {
	BuildClient     func(string, string) (kubernetes.Interface, *rest.Config, error)
	ListPVCs        func(context.Context, kubernetes.Interface, string) ([]k8s.PVCInfo, error)
	GetPVCByName    func(context.Context, kubernetes.Interface, string, string) (*k8s.PVCInfo, error)
	CreateTempPod   func(context.Context, kubernetes.Interface, string, string, string) (string, error)
	WaitForPodReady func(context.Context, kubernetes.Interface, string, string, time.Duration) error
	DeletePod       func(context.Context, kubernetes.Interface, string, string) error
	UploadAndScan   func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *k8s.PVCInfo, int, []string, int, bool) (*model.ScanResult, error)
}

// Legacy test state is never consulted by production commands.
var legacyHooks TestingHooks
var legacyOptions = defaultOptions()

func SetTestingHooks(h TestingHooks) func() {
	previous := legacyHooks
	legacyHooks = h
	return func() { legacyHooks = previous }
}
func SetTestingOptions(timeout time.Duration, concurrency int) func() {
	previous := legacyOptions
	legacyOptions.timeout, legacyOptions.concurrency = timeout, concurrency
	return func() { legacyOptions = previous }
}

func legacyRunner() *runner {
	h := legacyHooks
	d := Dependencies{BuildClient: h.BuildClient, ListPVCs: h.ListPVCs, GetPVCByName: h.GetPVCByName, WaitForPodReady: h.WaitForPodReady, UploadAndScan: h.UploadAndScan}
	if h.CreateTempPod != nil {
		d.CreatePod = func(ctx context.Context, client kubernetes.Interface, pvc k8s.PVCInfo, image string) (k8s.PodRef, error) {
			name, err := h.CreateTempPod(ctx, client, pvc.Namespace, pvc.Name, image)
			return k8s.PodRef{Name: name}, err
		}
	}
	if h.DeletePod != nil {
		d.DeletePod = func(ctx context.Context, client kubernetes.Interface, ns string, pod k8s.PodRef) error {
			return h.DeletePod(ctx, client, ns, pod.Name)
		}
	}
	r := newRunner(d)
	r.options = legacyOptions
	return r
}

func RunTempPodScanForTesting(ctx context.Context, client kubernetes.Interface, config *rest.Config, result *model.ScanResult, pvc k8s.PVCInfo) {
	legacyRunner().scanWithTempPod(ctx, client, config, result, pvc)
}
func RunPVCScanForTesting(ctx context.Context, client kubernetes.Interface, config *rest.Config, result *model.ScanResult, pvc k8s.PVCInfo, force bool) {
	r := legacyRunner()
	r.options.force = force
	r.scanPVC(ctx, client, config, result, pvc)
}
func RunUsageForTesting(args ...string) error {
	r := legacyRunner()
	if len(args) > 0 {
		r.options.pvc = args[len(args)-1]
	}
	c := &cobra.Command{}
	c.SetContext(context.Background())
	return r.run(c)
}
