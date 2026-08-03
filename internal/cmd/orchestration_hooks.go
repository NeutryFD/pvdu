package cmd

import (
	"context"
	"time"

	"github.com/neutry/pvdu/internal/k8s"
	"github.com/neutry/pvdu/internal/model"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// TestingHooks replaces external operations while the real command path runs.
// A nil field keeps the production implementation.
type TestingHooks struct {
	BuildClient     func(string, string) (kubernetes.Interface, *rest.Config, error)
	ListPVCs        func(context.Context, kubernetes.Interface, string) ([]k8s.PVCInfo, error)
	GetPVCByName    func(context.Context, kubernetes.Interface, string, string) (*k8s.PVCInfo, error)
	CreateTempPod   func(context.Context, kubernetes.Interface, string, string, string) (string, error)
	WaitForPodReady func(context.Context, kubernetes.Interface, string, string, time.Duration) error
	DeletePod       func(context.Context, kubernetes.Interface, string, string) error
	UploadAndScan   func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *k8s.PVCInfo, int, []string, int, bool) (*model.ScanResult, error)
}

func setTestingHooks(h TestingHooks) func() {
	previous := TestingHooks{
		BuildClient:     buildClient,
		ListPVCs:        listPVCs,
		GetPVCByName:    getPVCByName,
		CreateTempPod:   createTempPod,
		WaitForPodReady: waitForPodReady,
		DeletePod:       deletePod,
		UploadAndScan:   uploadAndScan,
	}
	if h.BuildClient != nil {
		buildClient = h.BuildClient
	}
	if h.ListPVCs != nil {
		listPVCs = h.ListPVCs
	}
	if h.GetPVCByName != nil {
		getPVCByName = h.GetPVCByName
	}
	if h.CreateTempPod != nil {
		createTempPod = h.CreateTempPod
	}
	if h.WaitForPodReady != nil {
		waitForPodReady = h.WaitForPodReady
	}
	if h.DeletePod != nil {
		deletePod = h.DeletePod
	}
	if h.UploadAndScan != nil {
		uploadAndScan = h.UploadAndScan
	}
	return func() {
		buildClient = previous.BuildClient
		listPVCs = previous.ListPVCs
		getPVCByName = previous.GetPVCByName
		createTempPod = previous.CreateTempPod
		waitForPodReady = previous.WaitForPodReady
		deletePod = previous.DeletePod
		uploadAndScan = previous.UploadAndScan
	}
}

// SetTestingHooks installs dependency hooks for external tests and returns a restore function.
func SetTestingHooks(h TestingHooks) func() { return setTestingHooks(h) }

// SetTestingOptions temporarily changes command options needed by deterministic tests.
func SetTestingOptions(operationTimeout time.Duration, scanConcurrency int) func() {
	previousTimeout, previousConcurrency, previousKubeconfig := timeout, concurrency, kubeconfig
	timeout, concurrency = operationTimeout, scanConcurrency
	kubeconfig = "__pvdu_missing_test_kubeconfig__"
	return func() {
		timeout, concurrency = previousTimeout, previousConcurrency
		kubeconfig = previousKubeconfig
	}
}

// RunTempPodScanForTesting invokes the production temporary-pod scan path.
func RunTempPodScanForTesting(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, result *model.ScanResult, pvc k8s.PVCInfo) {
	scanWithTempPod(ctx, clientset, config, result, pvc, 0)
}

// RunPVCScanForTesting invokes the production PVC branch-selection path.
func RunPVCScanForTesting(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, result *model.ScanResult, pvc k8s.PVCInfo, forceMode bool) {
	previous := force
	force = forceMode
	scanPVC(ctx, clientset, config, result, pvc, 0)
	force = previous
}

// RunUsageForTesting invokes the production command orchestration path.
func RunUsageForTesting(args ...string) error { return runUsage(nil, args) }
