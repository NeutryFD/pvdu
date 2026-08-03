package pvdu_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/neutry/pvdu/internal/cmd"
	"github.com/neutry/pvdu/internal/k8s"
	"github.com/neutry/pvdu/internal/model"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func testPVC() k8s.PVCInfo { return k8s.PVCInfo{Name: "claim", Namespace: "ns"} }

func TestTempPodCleanupSuccess(t *testing.T) {
	events := []string{}
	restore := cmd.SetTestingHooks(cmd.TestingHooks{
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) {
			events = append(events, "create")
			return "pod", nil
		},
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error {
			events = append(events, "ready")
			return nil
		},
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *k8s.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
			events = append(events, "scan")
			return &model.ScanResult{Status: model.StatusDone}, nil
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error {
			events = append(events, "delete")
			return nil
		},
	})
	defer restore()
	result := &model.ScanResult{}
	cmd.RunTempPodScanForTesting(context.Background(), fake.NewSimpleClientset(), nil, result, testPVC())
	if strings.Join(events, ",") != "create,ready,scan,delete" || result.Status != model.StatusDone {
		t.Fatalf("events/result = %v/%#v", events, result)
	}
}

func TestTempPodCleanupCreateFailureDoesNotDelete(t *testing.T) {
	deleted := false
	restore := cmd.SetTestingHooks(cmd.TestingHooks{
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) {
			return "", errors.New("create failed")
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted = true; return nil },
	})
	defer restore()
	cmd.RunTempPodScanForTesting(context.Background(), fake.NewSimpleClientset(), nil, &model.ScanResult{}, testPVC())
	if deleted {
		t.Fatal("delete called after create failed")
	}
}

func TestTempPodCleanupReadinessFailure(t *testing.T) {
	deleted := 0
	restore := cmd.SetTestingHooks(cmd.TestingHooks{
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error {
			return errors.New("not ready")
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted++; return nil },
	})
	defer restore()
	cmd.RunTempPodScanForTesting(context.Background(), fake.NewSimpleClientset(), nil, &model.ScanResult{}, testPVC())
	if deleted != 1 {
		t.Fatalf("delete count = %d", deleted)
	}
}

func TestTempPodCleanupScanFailure(t *testing.T) {
	deleted := 0
	restore := cmd.SetTestingHooks(cmd.TestingHooks{
		CreateTempPod:   func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *k8s.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
			return nil, errors.New("scan failed")
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted++; return nil },
	})
	defer restore()
	result := &model.ScanResult{}
	cmd.RunTempPodScanForTesting(context.Background(), fake.NewSimpleClientset(), nil, result, testPVC())
	if deleted != 1 || result.Status != model.StatusError {
		t.Fatalf("delete/status = %d/%q", deleted, result.Status)
	}
}

func TestTempPodCleanupTimeout(t *testing.T) {
	restore := cmd.SetTestingHooks(cmd.TestingHooks{
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(ctx context.Context, _ kubernetes.Interface, _, _ string, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { return nil },
	})
	defer restore()
	restoreOptions := cmd.SetTestingOptions(time.Millisecond, 1)
	defer restoreOptions()
	result := &model.ScanResult{}
	cmd.RunTempPodScanForTesting(context.Background(), fake.NewSimpleClientset(), nil, result, testPVC())
	if result.Status != model.StatusError || !strings.Contains(result.Error, "timed out after") {
		t.Fatalf("result = %#v", result)
	}
}

func TestTempPodCleanupCancellationUsesBackgroundContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cleanupCtx context.Context
	restore := cmd.SetTestingHooks(cmd.TestingHooks{
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error {
			cancel()
			return context.Canceled
		},
		DeletePod: func(ctx context.Context, _ kubernetes.Interface, _, _ string) error { cleanupCtx = ctx; return nil },
	})
	defer restore()
	cmd.RunTempPodScanForTesting(ctx, fake.NewSimpleClientset(), nil, &model.ScanResult{}, testPVC())
	if cleanupCtx == nil || cleanupCtx.Err() != nil {
		t.Fatalf("cleanup context = %v", cleanupCtx)
	}
}

func TestTempPodCleanupFailureIsLoggedWithoutReplacingResult(t *testing.T) {
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	restore := cmd.SetTestingHooks(cmd.TestingHooks{
		CreateTempPod:   func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *k8s.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
			return &model.ScanResult{Status: model.StatusDone, UsedBytes: 42}, nil
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { return errors.New("delete failed") },
	})
	defer restore()
	result := &model.ScanResult{}
	cmd.RunTempPodScanForTesting(context.Background(), fake.NewSimpleClientset(), nil, result, testPVC())
	if result.Status != model.StatusDone || result.UsedBytes != 42 || !strings.Contains(logs.String(), "delete temp pod failed") {
		t.Fatalf("result/log = %#v/%q", result, logs.String())
	}
}

func TestUsageSignalsSuppressFinalOutput(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			restoreOptions := cmd.SetTestingOptions(120*time.Second, 1)
			defer restoreOptions()
			started, waiting := make(chan struct{}), make(chan struct{})
			deleted := 0
			restore := cmd.SetTestingHooks(cmd.TestingHooks{
				BuildClient: func(string, string) (kubernetes.Interface, *rest.Config, error) {
					return fake.NewSimpleClientset(), nil, nil
				},
				ListPVCs: func(context.Context, kubernetes.Interface, string) ([]k8s.PVCInfo, error) {
					return []k8s.PVCInfo{testPVC()}, nil
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
			})
			defer restore()
			done := make(chan struct{})
			var err error
			var stdout, stderr string
			go func() { stdout, stderr = captureOutput(func() { err = cmd.RunUsageForTesting() }); close(done) }()
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
			if err == nil || stdout != "" || !strings.Contains(stderr, "interrupted") || deleted != 1 {
				t.Fatalf("err/stdout/stderr/deletes = %v/%q/%q/%d", err, stdout, stderr, deleted)
			}
		})
	}
}

func TestCompletedPVCRemainsInternalAfterInterruption(t *testing.T) {
	started := make(chan struct{})
	var startOnce sync.Once
	restoreOptions := cmd.SetTestingOptions(120*time.Second, 1)
	defer restoreOptions()
	restore := cmd.SetTestingHooks(cmd.TestingHooks{
		BuildClient: func(string, string) (kubernetes.Interface, *rest.Config, error) {
			return fake.NewSimpleClientset(), nil, nil
		},
		ListPVCs: func(context.Context, kubernetes.Interface, string) ([]k8s.PVCInfo, error) {
			mount := []k8s.PodMount{{PodName: "existing", ContainerName: "scanner", MountPath: "/mnt"}}
			return []k8s.PVCInfo{{Name: "done", Namespace: "ns", Mounts: mount}, {Name: "next", Namespace: "ns", Mounts: mount}}, nil
		},
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, pvc *k8s.PVCInfo, _ int, _ []string, _ int, _ bool) (*model.ScanResult, error) {
			if pvc.Name == "done" {
				return &model.ScanResult{Status: model.StatusDone}, nil
			}
			startOnce.Do(func() { close(started) })
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	defer restore()
	done := make(chan struct{})
	var err error
	var stdout, stderr string
	go func() { stdout, stderr = captureOutput(func() { err = cmd.RunUsageForTesting() }); close(done) }()
	select {
	case <-started:
	case <-done:
		t.Fatal("command exited before second PVC started")
	}
	if killErr := syscall.Kill(os.Getpid(), syscall.SIGINT); killErr != nil {
		t.Fatal(killErr)
	}
	<-done
	if err == nil || stdout != "" || !strings.Contains(stderr, "interrupted") {
		t.Fatalf("err/output = %v/%q/%q", err, stdout, stderr)
	}
}

func TestNonForceExistingPodDoesNotUseTempCleanup(t *testing.T) {
	created, deleted := false, false
	restore := cmd.SetTestingHooks(cmd.TestingHooks{
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *k8s.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
			return &model.ScanResult{Status: model.StatusDone}, nil
		},
		CreateTempPod: func(context.Context, kubernetes.Interface, string, string, string) (string, error) {
			created = true
			return "", nil
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted = true; return nil },
	})
	defer restore()
	result := &model.ScanResult{}
	cmd.RunPVCScanForTesting(context.Background(), fake.NewSimpleClientset(), nil, result, k8s.PVCInfo{Name: "claim", Namespace: "ns", Mounts: []k8s.PodMount{{PodName: "existing", MountPath: "/mnt"}}}, false)
	if created || deleted || result.Status != model.StatusDone {
		t.Fatalf("created/deleted/status = %v/%v/%q", created, deleted, result.Status)
	}
}

func TestUsageSinglePVCUsesProductionDiscoveryHook(t *testing.T) {
	restoreOptions := cmd.SetTestingOptions(time.Second, 1)
	defer restoreOptions()
	called := false
	restore := cmd.SetTestingHooks(cmd.TestingHooks{
		BuildClient: func(string, string) (kubernetes.Interface, *rest.Config, error) {
			return fake.NewSimpleClientset(), nil, nil
		},
		GetPVCByName: func(context.Context, kubernetes.Interface, string, string) (*k8s.PVCInfo, error) {
			called = true
			return &k8s.PVCInfo{Name: "claim", Namespace: "default", Mounts: []k8s.PodMount{{PodName: "existing", MountPath: "/mnt"}}}, nil
		},
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *k8s.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
			return &model.ScanResult{Status: model.StatusDone}, nil
		},
	})
	defer restore()
	var err error
	var stdout string
	stdout, _ = captureOutput(func() { err = cmd.RunUsageForTesting("pvc", "claim") })
	if err != nil || !called || !strings.Contains(stdout, "claim") {
		t.Fatalf("err/called/stdout = %v/%v/%q", err, called, stdout)
	}
}

func TestUsageAllPVCsUsesProductionListHook(t *testing.T) {
	restoreOptions := cmd.SetTestingOptions(time.Second, 1)
	defer restoreOptions()
	called := false
	restore := cmd.SetTestingHooks(cmd.TestingHooks{
		BuildClient: func(string, string) (kubernetes.Interface, *rest.Config, error) {
			return fake.NewSimpleClientset(), nil, nil
		},
		ListPVCs: func(context.Context, kubernetes.Interface, string) ([]k8s.PVCInfo, error) {
			called = true
			return nil, nil
		},
	})
	defer restore()

	var err error
	stdout, _ := captureOutput(func() { err = cmd.RunUsageForTesting() })
	if err != nil || !called || !strings.Contains(stdout, "No PVCs found.") {
		t.Fatalf("err/called/stdout = %v/%v/%q", err, called, stdout)
	}
}

func TestTempPodCleanupRunsBeforePanicPropagates(t *testing.T) {
	deleted := false
	restore := cmd.SetTestingHooks(cmd.TestingHooks{
		CreateTempPod:   func(context.Context, kubernetes.Interface, string, string, string) (string, error) { return "pod", nil },
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		UploadAndScan: func(context.Context, kubernetes.Interface, *rest.Config, string, string, string, string, string, *k8s.PVCInfo, int, []string, int, bool) (*model.ScanResult, error) {
			panic("scanner panic")
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, string) error { deleted = true; return nil },
	})
	defer restore()
	defer func() {
		if recover() == nil || !deleted {
			t.Fatal("panic cleanup contract failed")
		}
	}()
	cmd.RunTempPodScanForTesting(context.Background(), fake.NewSimpleClientset(), nil, &model.ScanResult{}, testPVC())
}

func captureOutput(run func()) (string, string) {
	stdoutPipe, stdoutWriter, _ := os.Pipe()
	stderrPipe, stderrWriter, _ := os.Pipe()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter
	run()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	stdoutWriter.Close()
	stderrWriter.Close()
	stdout, _ := io.ReadAll(stdoutPipe)
	stderr, _ := io.ReadAll(stderrPipe)
	return string(stdout), string(stderr)
}
