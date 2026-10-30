package pvdu_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func testPVC() kube.PVCInfo { return kube.PVCInfo{Name: "claim", Namespace: "ns"} }

func TestTempPodCleanupSuccess(t *testing.T) {
	events := []string{}
	result, _ := scanPVC(t, context.Background(), hooks{
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) {
			events = append(events, "create")
			return "pod", nil
		},
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error {
			events = append(events, "ready")
			return nil
		},
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
			events = append(events, "scan")
			return &model.ScanResult{Status: model.StatusDone}, nil
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error {
			events = append(events, "delete")
			return nil
		},
	}, testPVC())
	if strings.Join(events, ",") != "create,ready,scan,delete" || result.Status != model.StatusDone {
		t.Fatalf("events/result = %v/%#v", events, result)
	}
}

func TestTempPodCleanupCreateFailureDoesNotDelete(t *testing.T) {
	deleted := false
	scanPVC(t, context.Background(), hooks{
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) {
			return "", errors.New("create failed")
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted = true; return nil },
	}, testPVC())
	if deleted {
		t.Fatal("delete called after create failed")
	}
}

func TestTempPodCleanupReadinessFailure(t *testing.T) {
	deleted := 0
	scanPVC(t, context.Background(), hooks{
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error {
			return errors.New("not ready")
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted++; return nil },
	}, testPVC())
	if deleted != 1 {
		t.Fatalf("delete count = %d", deleted)
	}
}

func TestTempPodCleanupScanFailure(t *testing.T) {
	deleted := 0
	result, _ := scanPVC(t, context.Background(), hooks{
		CreateTempPod:   func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
			return nil, errors.New("scan failed")
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted++; return nil },
	}, testPVC())
	if deleted != 1 || result.Status != model.StatusError {
		t.Fatalf("delete/status = %d/%q", deleted, result.Status)
	}
}

func TestTempPodCleanupTimeout(t *testing.T) {
	result, _ := scanPVC(t, context.Background(), hooks{
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(ctx context.Context, _ kubernetes.Interface, _, _ string, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { return nil },
	}, testPVC(), "--timeout=1ms")
	if result.Status != model.StatusError || !strings.Contains(result.Error, "timed out after") {
		t.Fatalf("result = %#v", result)
	}
}

func TestTempPodCleanupCancellationUsesBackgroundContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanupCalled, cleanupErr := false, error(nil)
	scanPVC(t, ctx, hooks{
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error {
			cancel()
			return context.Canceled
		},
		DeletePod: func(ctx context.Context, _ kubernetes.Interface, _, _ string) error {
			// Inspect inside the hook: the context is released once cleanup returns.
			cleanupCalled, cleanupErr = true, ctx.Err()
			return nil
		},
	}, testPVC())
	if !cleanupCalled || cleanupErr != nil {
		t.Fatalf("cleanup called/context error = %v/%v", cleanupCalled, cleanupErr)
	}
}

func TestTempPodCleanupFailureIsDeferredWithoutReplacingResult(t *testing.T) {
	result, run := scanPVC(t, context.Background(), hooks{
		CreateTempPod:   func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
			return &model.ScanResult{Status: model.StatusDone, UsedBytes: 42}, nil
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { return errors.New("delete failed") },
	}, testPVC())
	if result.Status != model.StatusDone || result.UsedBytes != 42 || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "delete temp pod failed") || run.stderr != "" {
		t.Fatalf("result/stderr = %#v/%q", result, run.stderr)
	}
}

func TestUsageSignalsSuppressFinalOutput(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			started, waiting := make(chan struct{}), make(chan struct{})
			deleted := 0
			h := hooks{
				ListPVCs: func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error) {
					return []kube.PVCInfo{testPVC()}, nil
				},
				CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) {
					close(started)
					return "pod", nil
				},
				WaitForPodReady: func(ctx context.Context, _ kubernetes.Interface, _, _ string, _ time.Duration) error {
					close(waiting)
					<-ctx.Done()
					return ctx.Err()
				},
				DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted++; return nil },
			}
			done := make(chan struct{})
			var run commandRun
			go func() { run = runCommand(context.Background(), h, "-n", "ns", "-c", "1"); close(done) }()
			select {
			case <-started:
			case <-done:
				t.Fatal("command exited before starting scan")
			}
			select {
			case <-waiting:
			case <-done:
				t.Fatal("command exited before waiting for cancellation")
			}
			if killErr := syscall.Kill(os.Getpid(), sig); killErr != nil {
				t.Fatal(killErr)
			}
			<-done
			if run.err == nil || run.stdout != "" || !strings.Contains(run.stderr, "interrupted") || deleted != 1 {
				t.Fatalf("err/stdout/stderr/deletes = %v/%q/%q/%d", run.err, run.stdout, run.stderr, deleted)
			}
		})
	}
}

func TestCompletedPVCRemainsInternalAfterInterruption(t *testing.T) {
	started := make(chan struct{})
	var startOnce sync.Once
	h := hooks{
		ListPVCs: func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error) {
			mount := []kube.PodMount{{PodName: "existing", ContainerName: "scanner", MountPath: "/mnt"}}
			return []kube.PVCInfo{{Name: "done", Namespace: "ns", Mounts: mount}, {Name: "next", Namespace: "ns", Mounts: mount}}, nil
		},
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, pvc *kube.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
			if pvc.Name == "done" {
				return &model.ScanResult{Status: model.StatusDone}, nil
			}
			startOnce.Do(func() { close(started) })
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	done := make(chan struct{})
	var run commandRun
	go func() { run = runCommand(context.Background(), h, "-n", "ns", "-c", "1"); close(done) }()
	select {
	case <-started:
	case <-done:
		t.Fatal("command exited before second PVC started")
	}
	if killErr := syscall.Kill(os.Getpid(), syscall.SIGINT); killErr != nil {
		t.Fatal(killErr)
	}
	<-done
	if run.err == nil || run.stdout != "" || !strings.Contains(run.stderr, "interrupted") {
		t.Fatalf("err/output = %v/%q/%q", run.err, run.stdout, run.stderr)
	}
}

func TestNonForceExistingPodDoesNotUseTempCleanup(t *testing.T) {
	created, deleted := false, false
	result, _ := scanPVC(t, context.Background(), hooks{
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
			return &model.ScanResult{Status: model.StatusDone}, nil
		},
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) {
			created = true
			return "", nil
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted = true; return nil },
	}, kube.PVCInfo{Name: "claim", Namespace: "ns", Mounts: []kube.PodMount{{PodName: "existing", MountPath: "/mnt"}}})
	if created || deleted || result.Status != model.StatusDone {
		t.Fatalf("created/deleted/status = %v/%v/%q", created, deleted, result.Status)
	}
}

func TestUsageSinglePVCUsesProductionDiscoveryHook(t *testing.T) {
	called := false
	run := runCommand(context.Background(), hooks{
		GetPVCByName: func(context.Context, kubernetes.Interface, string, string) (*kube.PVCInfo, error) {
			called = true
			return &kube.PVCInfo{Name: "claim", Namespace: "default", Mounts: []kube.PodMount{{PodName: "existing", MountPath: "/mnt"}}}, nil
		},
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
			return &model.ScanResult{Status: model.StatusDone}, nil
		},
	}, "pvc", "claim")
	if run.err != nil || !called || !strings.Contains(run.stdout, "claim") {
		t.Fatalf("err/called/stdout = %v/%v/%q", run.err, called, run.stdout)
	}
}

func TestUsageAllPVCsUsesProductionListHook(t *testing.T) {
	called := false
	run := runCommand(context.Background(), hooks{
		ListPVCs: func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error) {
			called = true
			return nil, nil
		},
	})
	if run.err != nil || !called || !strings.Contains(run.stdout, "No PVCs found.") {
		t.Fatalf("err/called/stdout = %v/%v/%q", run.err, called, run.stdout)
	}
}

func TestPanickingScanIsReportedAfterCleanup(t *testing.T) {
	deleted := 0
	result, run := scanPVC(t, context.Background(), hooks{
		CreateTempPod:   func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
			panic("scanner panic")
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted++; return nil },
	}, testPVC())
	if deleted != 1 || result.Status != model.StatusError || !strings.Contains(result.Error, "scanner panic") || run.err == nil {
		t.Fatalf("deletes/result/err = %d/%#v/%v", deleted, result, run.err)
	}
}
