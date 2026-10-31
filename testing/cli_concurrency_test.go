package pvdu_test

import (
	"context"
	"strings"
	"testing"

	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestCLIConcurrencyZeroMeansNoLimit(t *testing.T) {
	h := hooks{
		ListPVCs: func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error) {
			return []kube.PVCInfo{mountedPVC("a"), mountedPVC("b")}, nil
		},
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int) (*model.ScanResult, error) {
			return &model.ScanResult{Status: model.StatusDone, UsedBytes: 1}, nil
		},
	}
	if run := runCommand(context.Background(), h, "-n", "ns", "-o", "json", "-c", "0"); run.err != nil {
		t.Fatalf("-c 0: %v\n%s", run.err, run.stderr)
	}
	run := runCommand(context.Background(), h, "-n", "ns", "-c", "-1")
	if run.err == nil || !strings.Contains(run.err.Error(), "--concurrency cannot be negative; 0 means no limit") {
		t.Fatalf("-c -1: err = %v", run.err)
	}
}
