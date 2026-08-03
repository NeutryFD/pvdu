package pvdu_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neutry/pvdu/internal/cmd"
	"github.com/neutry/pvdu/internal/k8s"
	"github.com/neutry/pvdu/internal/model"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func timeoutPVC() k8s.PVCInfo { return k8s.PVCInfo{Name: "claim", Namespace: "ns"} }

func timeoutScan(t *testing.T, operationTimeout time.Duration, hooks cmd.TestingHooks) *model.ScanResult {
	t.Helper()
	restoreHooks := cmd.SetTestingHooks(hooks)
	t.Cleanup(restoreHooks)
	restoreOptions := cmd.SetTestingOptions(operationTimeout, 1)
	t.Cleanup(restoreOptions)
	result := &model.ScanResult{}
	cmd.RunTempPodScanForTesting(context.Background(), fake.NewSimpleClientset(), nil, result, timeoutPVC())
	return result
}

func TestScanTimeoutTotalBudgetSpansTemporaryPodLifecycle(t *testing.T) {
	var deadlines [3]time.Time
	var deleted atomic.Int32
	result := timeoutScan(t, 20*time.Millisecond, cmd.TestingHooks{
		CreateTempPod: func(ctx context.Context, _ kubernetes.Interface, _, _, _ string) (string, error) {
			deadlines[0], _ = ctx.Deadline()
			time.Sleep(8 * time.Millisecond)
			return "pod", nil
		},
		WaitForPodReady: func(ctx context.Context, _ kubernetes.Interface, _, _ string, _ time.Duration) error {
			deadlines[1], _ = ctx.Deadline()
			time.Sleep(16 * time.Millisecond)
			return nil
		},
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, _ *k8s.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
			deadlines[2], _ = ctx.Deadline()
			return nil, ctx.Err()
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted.Add(1); return nil },
	})
	if result.Status != model.StatusError || result.Error != "timed out after 20ms (try --timeout/-t)" {
		t.Fatalf("result = %#v", result)
	}
	if deadlines[0] != deadlines[1] || deadlines[1] != deadlines[2] || deleted.Load() != 1 {
		t.Fatalf("deadlines/deletes = %#v/%d", deadlines, deleted.Load())
	}
}

func TestScanTimeoutDuringCreationDoesNotDelete(t *testing.T) {
	var cancelled atomic.Bool
	var deleted atomic.Int32
	result := timeoutScan(t, 5*time.Millisecond, cmd.TestingHooks{
		CreateTempPod: func(ctx context.Context, _ kubernetes.Interface, _, _, _ string) (string, error) {
			<-ctx.Done()
			cancelled.Store(true)
			return "", ctx.Err()
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted.Add(1); return nil },
	})
	if !cancelled.Load() || deleted.Load() != 0 || result.Error != "timed out after 5ms (try --timeout/-t)" {
		t.Fatalf("cancelled/deletes/result = %v/%d/%#v", cancelled.Load(), deleted.Load(), result)
	}
}

func TestScanTimeoutDuringReadinessDeletesPod(t *testing.T) {
	var deleted atomic.Int32
	result := timeoutScan(t, 5*time.Millisecond, cmd.TestingHooks{
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(ctx context.Context, _ kubernetes.Interface, _, _ string, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted.Add(1); return nil },
	})
	if deleted.Load() != 1 || result.Error != "timed out after 5ms (try --timeout/-t)" {
		t.Fatalf("deletes/result = %d/%#v", deleted.Load(), result)
	}
}

func TestScanTimeoutDuringScannerCancelsAndDeletesPod(t *testing.T) {
	var cancelled atomic.Bool
	var deleted atomic.Int32
	result := timeoutScan(t, 5*time.Millisecond, cmd.TestingHooks{
		CreateTempPod:   func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, _ *k8s.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
			<-ctx.Done()
			cancelled.Store(true)
			return nil, ctx.Err()
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted.Add(1); return nil },
	})
	if !cancelled.Load() || deleted.Load() != 1 || result.Error != "timed out after 5ms (try --timeout/-t)" {
		t.Fatalf("cancelled/deletes/result = %v/%d/%#v", cancelled.Load(), deleted.Load(), result)
	}
}

func TestScanTimeoutUsesConfiguredDuration(t *testing.T) {
	result := timeoutScan(t, 37*time.Millisecond, cmd.TestingHooks{
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(ctx context.Context, _ kubernetes.Interface, _, _ string, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if !strings.EqualFold(result.Error, "timed out after 37ms (try --timeout/-t)") {
		t.Fatalf("error = %q", result.Error)
	}
}

func mountedTimeoutPVC() k8s.PVCInfo {
	pvc := timeoutPVC()
	pvc.Mounts = []k8s.PodMount{{PodName: "existing", ContainerName: "scanner", MountPath: "/mnt"}}
	return pvc
}

func TestExistingMountScanHasIndependentTimeout(t *testing.T) {
	var cancelled atomic.Bool
	var tempCreated atomic.Bool
	result := &model.ScanResult{}
	restoreHooks := cmd.SetTestingHooks(cmd.TestingHooks{
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, pod, _, _, _ string, _ *k8s.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
			if pod != "existing" {
				tempCreated.Store(true)
			}
			<-ctx.Done()
			cancelled.Store(true)
			return nil, ctx.Err()
		},
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) {
			tempCreated.Store(true)
			return "pod", nil
		},
	})
	defer restoreHooks()
	restoreOptions := cmd.SetTestingOptions(5*time.Millisecond, 1)
	defer restoreOptions()
	cmd.RunPVCScanForTesting(context.Background(), fake.NewSimpleClientset(), nil, result, mountedTimeoutPVC(), false)
	if !cancelled.Load() || tempCreated.Load() || result.Error != "timed out after 5ms (try --timeout/-t)" {
		t.Fatalf("cancelled/temp-created/result = %v/%v/%#v", cancelled.Load(), tempCreated.Load(), result)
	}
}

func TestExistingMountTimeoutFallsBackUsingActiveParent(t *testing.T) {
	var fallbackCreated atomic.Bool
	var fallbackContextActive atomic.Bool
	var temporaryLifecycle atomic.Int32
	restoreHooks := cmd.SetTestingHooks(cmd.TestingHooks{
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, pod, _, _, _ string, _ *k8s.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
			if pod == "existing" {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			temporaryLifecycle.Add(1)
			return &model.ScanResult{Status: model.StatusDone}, nil
		},
		CreateTempPod: func(ctx context.Context, _ kubernetes.Interface, _, _, _ string) (string, error) {
			fallbackCreated.Store(true)
			fallbackContextActive.Store(ctx.Err() == nil)
			return "temporary", nil
		},
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		DeletePod:       func(context.Context, kubernetes.Interface, string, string) error { return nil },
	})
	defer restoreHooks()
	restoreOptions := cmd.SetTestingOptions(5*time.Millisecond, 1)
	defer restoreOptions()
	result := &model.ScanResult{}
	cmd.RunPVCScanForTesting(context.Background(), fake.NewSimpleClientset(), nil, result, mountedTimeoutPVC(), true)
	if !fallbackCreated.Load() || !fallbackContextActive.Load() || temporaryLifecycle.Load() != 1 || result.Status != model.StatusDone {
		t.Fatalf("created/context/lifecycle/result = %v/%v/%d/%#v", fallbackCreated.Load(), fallbackContextActive.Load(), temporaryLifecycle.Load(), result)
	}
}

func TestParentCancellationPreventsForcedFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var tempCreated atomic.Bool
	restoreHooks := cmd.SetTestingHooks(cmd.TestingHooks{
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, _ *k8s.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
			cancel()
			return nil, ctx.Err()
		},
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) {
			tempCreated.Store(true)
			return "pod", nil
		},
	})
	defer restoreHooks()
	restoreOptions := cmd.SetTestingOptions(time.Second, 1)
	defer restoreOptions()
	result := &model.ScanResult{}
	cmd.RunPVCScanForTesting(ctx, fake.NewSimpleClientset(), nil, result, mountedTimeoutPVC(), true)
	if tempCreated.Load() || result.Error != "interrupted" {
		t.Fatalf("temp-created/result = %v/%#v", tempCreated.Load(), result)
	}
}

func TestParentCancellationIsInterruptedNotTimeoutAndCleansPod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var deleted atomic.Int32
	restoreHooks := cmd.SetTestingHooks(cmd.TestingHooks{
		CreateTempPod:   func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, _ *k8s.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted.Add(1); return nil },
	})
	defer restoreHooks()
	restoreOptions := cmd.SetTestingOptions(time.Second, 1)
	defer restoreOptions()
	result := &model.ScanResult{}
	cmd.RunTempPodScanForTesting(ctx, fake.NewSimpleClientset(), nil, result, timeoutPVC())
	if result.Error != "interrupted" || strings.Contains(result.Error, "timed out") || deleted.Load() != 1 {
		t.Fatalf("result/deletes = %#v/%d", result, deleted.Load())
	}
}

func TestNonTimeoutErrorsPreserveOriginalMessages(t *testing.T) {
	t.Run("readiness", func(t *testing.T) {
		result := timeoutScan(t, time.Second, cmd.TestingHooks{
			CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
			WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error {
				return errors.New("pod never became ready")
			},
		})
		if !strings.Contains(result.Error, "pod never became ready") || strings.Contains(result.Error, "timed out after") {
			t.Fatalf("error = %q", result.Error)
		}
	})
	t.Run("scanner", func(t *testing.T) {
		result := timeoutScan(t, time.Second, cmd.TestingHooks{
			CreateTempPod:   func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
			WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
			UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *k8s.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
				return nil, errors.New("scanner exploded")
			},
		})
		if !strings.Contains(result.Error, "scanner exploded") || strings.Contains(result.Error, "timed out after") {
			t.Fatalf("error = %q", result.Error)
		}
	})
}
