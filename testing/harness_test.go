package pvdu_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/neutry/pvdu/internal/cli"
	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// hooks lets tests replace the external boundaries of the command. Omitted
// functions fall back to a fake client; everything else uses production code.
type hooks struct {
	BuildClient     func(string, string) (kubernetes.Interface, *rest.Config, error)
	ListPVCs        func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error)
	GetPVCByName    func(context.Context, kubernetes.Interface, string, string) (*kube.PVCInfo, error)
	CreateTempPod   func(context.Context, kubernetes.Interface, string, string, string) (string, error)
	WaitForPodReady func(context.Context, kubernetes.Interface, string, string, time.Duration) error
	DeletePod       func(context.Context, kubernetes.Interface, string, string) error
	UploadAndScan   func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int) (*model.ScanResult, error)
}

func (h hooks) dependencies() cli.Dependencies {
	d := cli.Dependencies{
		BuildClient:     h.BuildClient,
		ListPVCs:        h.ListPVCs,
		GetPVCByName:    h.GetPVCByName,
		WaitForPodReady: h.WaitForPodReady,
		UploadAndScan:   h.UploadAndScan,
	}
	if d.BuildClient == nil {
		d.BuildClient = func(string, string) (kubernetes.Interface, *rest.Config, error) {
			return fake.NewSimpleClientset(), &rest.Config{}, nil
		}
	}
	if h.CreateTempPod != nil {
		d.CreatePod = func(ctx context.Context, client kubernetes.Interface, pvc kube.PVCInfo, image string) (kube.PodRef, error) {
			name, err := h.CreateTempPod(ctx, client, pvc.Namespace, pvc.Name, image)
			return kube.PodRef{Name: name}, err
		}
	}
	if h.DeletePod != nil {
		d.DeletePod = func(ctx context.Context, client kubernetes.Interface, namespace string, pod kube.PodRef) error {
			return h.DeletePod(ctx, client, namespace, pod.Name)
		}
	}
	return d
}

type commandRun struct {
	stdout, stderr string
	err            error
}

// runCommand executes a fresh command tree, capturing its output streams.
func runCommand(ctx context.Context, h hooks, args ...string) commandRun {
	var stdout, stderr bytes.Buffer
	c := cli.NewCommand(h.dependencies())
	c.SetOut(&stdout)
	c.SetErr(&stderr)
	c.SetArgs(args)
	err := c.ExecuteContext(ctx)
	return commandRun{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

// scanPVC runs the command for exactly one PVC and returns its JSON result.
// The result is empty when the command printed nothing, such as after an
// interruption.
func scanPVC(t *testing.T, ctx context.Context, h hooks, pvc kube.PVCInfo, args ...string) (*model.ScanResult, commandRun) {
	t.Helper()
	h.ListPVCs = func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error) {
		return []kube.PVCInfo{pvc}, nil
	}
	run := runCommand(ctx, h, append([]string{"-n", pvc.Namespace, "-o", "json", "-c", "1"}, args...)...)
	result := &model.ScanResult{}
	if run.stdout != "" {
		var results []*model.ScanResult
		if err := json.Unmarshal([]byte(run.stdout), &results); err != nil || len(results) != 1 {
			t.Fatalf("decode results: %v: %q", err, run.stdout)
		}
		result = results[0]
	}
	return result, run
}
