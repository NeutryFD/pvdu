package pvdu_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func timeoutPVC() kube.PVCInfo { return kube.PVCInfo{Name: "claim", Namespace: "ns"} }

func timeoutScan(t *testing.T, operationTimeout time.Duration, h hooks) *model.ScanResult {
	t.Helper()
	result, _ := scanPVC(t, context.Background(), h, timeoutPVC(), "--timeout="+operationTimeout.String())
	return result
}

func TestScanTimeoutTotalBudgetSpansTemporaryPodLifecycle(t *testing.T) {
	var deadlines [3]time.Time
	var deleted atomic.Int32
	result := timeoutScan(t, 20*time.Millisecond, hooks{
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
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, _ *kube.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
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
	result := timeoutScan(t, 5*time.Millisecond, hooks{
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
	result := timeoutScan(t, 5*time.Millisecond, hooks{
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
	result := timeoutScan(t, 5*time.Millisecond, hooks{
		CreateTempPod:   func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, _ *kube.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
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
	result := timeoutScan(t, 37*time.Millisecond, hooks{
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

func mountedTimeoutPVC() kube.PVCInfo {
	pvc := timeoutPVC()
	pvc.Mounts = []kube.PodMount{{PodName: "existing", ContainerName: "scanner", MountPath: "/mnt"}}
	return pvc
}

func TestExistingMountScanHasIndependentTimeout(t *testing.T) {
	var cancelled atomic.Bool
	var tempCreated atomic.Bool
	result, _ := scanPVC(t, context.Background(), hooks{
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, pod, _, _, _ string, _ *kube.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
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
	}, mountedTimeoutPVC(), "--timeout=5ms")
	if !cancelled.Load() || tempCreated.Load() || result.Error != "timed out after 5ms (try --timeout/-t)" {
		t.Fatalf("cancelled/temp-created/result = %v/%v/%#v", cancelled.Load(), tempCreated.Load(), result)
	}
}

func TestExistingMountTimeoutFallsBackUsingActiveParent(t *testing.T) {
	var fallbackCreated atomic.Bool
	var fallbackContextActive atomic.Bool
	var temporaryLifecycle atomic.Int32
	result, _ := scanPVC(t, context.Background(), hooks{
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, pod, _, _, _ string, _ *kube.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
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
	}, mountedTimeoutPVC(), "--timeout=5ms", "--force")
	if !fallbackCreated.Load() || !fallbackContextActive.Load() || temporaryLifecycle.Load() != 1 || result.Status != model.StatusDone {
		t.Fatalf("created/context/lifecycle/result = %v/%v/%d/%#v", fallbackCreated.Load(), fallbackContextActive.Load(), temporaryLifecycle.Load(), result)
	}
}

func TestParentCancellationPreventsForcedFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var tempCreated atomic.Bool
	_, run := scanPVC(t, ctx, hooks{
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, _ *kube.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
			cancel()
			return nil, ctx.Err()
		},
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) {
			tempCreated.Store(true)
			return "pod", nil
		},
	}, mountedTimeoutPVC(), "--force")
	if tempCreated.Load() || run.err == nil || run.stdout != "" || !strings.Contains(run.stderr, "interrupted") {
		t.Fatalf("temp-created/run = %v/%#v", tempCreated.Load(), run)
	}
}

func TestParentCancellationIsInterruptedNotTimeoutAndCleansPod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var deleted atomic.Int32
	_, run := scanPVC(t, ctx, hooks{
		CreateTempPod:   func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, _ *kube.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted.Add(1); return nil },
	}, timeoutPVC())
	if !strings.Contains(run.stderr, "interrupted") || strings.Contains(run.stderr, "timed out") || deleted.Load() != 1 {
		t.Fatalf("run/deletes = %#v/%d", run, deleted.Load())
	}
}

func TestNonTimeoutErrorsPreserveOriginalMessages(t *testing.T) {
	t.Run("readiness", func(t *testing.T) {
		result := timeoutScan(t, time.Second, hooks{
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
		result := timeoutScan(t, time.Second, hooks{
			CreateTempPod:   func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
			WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
			UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *kube.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
				return nil, errors.New("scanner exploded")
			},
		})
		if !strings.Contains(result.Error, "scanner exploded") || strings.Contains(result.Error, "timed out after") {
			t.Fatalf("error = %q", result.Error)
		}
	})
}
