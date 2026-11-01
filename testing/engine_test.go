package pvdu_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neutry/pvdu/internal/engine"
	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func mountedPVC(name string) kube.PVCInfo {
	return kube.PVCInfo{Name: name, Namespace: "ns", Mounts: []kube.PodMount{{PodName: "app", ContainerName: "c", MountPath: "/data"}}}
}

func newTestEngine(concurrency int, scan func(context.Context) (*model.ScanResult, error)) *engine.Engine {
	return engine.New(fake.NewSimpleClientset(), &rest.Config{}, engine.Options{
		Image: "alpine", Timeout: time.Minute, CleanupTimeout: time.Second, Concurrency: concurrency,
	}, engine.Dependencies{
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, _ *kube.PVCInfo, _ int, _ []string, _ int) (*model.ScanResult, error) {
			return scan(ctx)
		},
	})
}

func TestEngineScanPublishesIndependentSnapshots(t *testing.T) {
	e := newTestEngine(1, func(context.Context) (*model.ScanResult, error) {
		return &model.ScanResult{Status: model.StatusDone, UsedBytes: 42}, nil
	})
	pvc := mountedPVC("claim")
	result := engine.NewResult(pvc)
	var mu sync.Mutex
	var statuses []model.ScanStatus
	var last model.ScanResult
	ok := e.Scan(context.Background(), 7, result, pvc, func(event engine.Event) {
		if event.Kind != engine.EventStatus {
			return
		}
		if event.Index != 7 {
			t.Errorf("event index = %d, want 7", event.Index)
		}
		mu.Lock()
		statuses = append(statuses, event.Result.Status)
		last = event.Result
		mu.Unlock()
	})
	if !ok {
		t.Fatal("Scan returned false")
	}
	if len(statuses) == 0 || statuses[0] != model.StatusPreparing || last.Status != model.StatusDone || last.UsedBytes != 42 {
		t.Fatalf("statuses = %v, last = %#v", statuses, last)
	}
	if last.PodName != "app" || last.ScanPath != "/data" {
		t.Fatalf("last event mount = %s:%s, want app:/data", last.PodName, last.ScanPath)
	}
	result.Warnings = append(result.Warnings, "later")
	if len(last.Warnings) != 0 {
		t.Fatal("event result shares Warnings with the live result")
	}
}

func TestEngineScanForcedFallsBackWithoutForceOption(t *testing.T) {
	var created atomic.Int32
	e := engine.New(fake.NewSimpleClientset(), &rest.Config{}, engine.Options{
		Image: "alpine", Timeout: time.Minute, CleanupTimeout: time.Second, Concurrency: 1,
	}, engine.Dependencies{
		UploadAndScan: func(_ context.Context, _ kubernetes.Interface, _ *rest.Config, _, pod, _, _, _ string, _ *kube.PVCInfo, _ int, _ []string, _ int) (*model.ScanResult, error) {
			if pod == "app" {
				return nil, fmt.Errorf("exec failed")
			}
			return &model.ScanResult{Status: model.StatusDone, UsedBytes: 7}, nil
		},
		CreatePod: func(context.Context, kubernetes.Interface, kube.PVCInfo, string) (kube.PodRef, error) {
			created.Add(1)
			return kube.PodRef{Name: "tmp"}, nil
		},
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		DeletePod:       func(context.Context, kubernetes.Interface, string, kube.PodRef) error { return nil },
	})
	pvc := mountedPVC("claim")
	plain := engine.NewResult(pvc)
	e.Scan(context.Background(), 0, plain, pvc, nil)
	if plain.Status != model.StatusError || created.Load() != 0 {
		t.Fatalf("Scan without Force: status %s, temporary pods %d", plain.Status, created.Load())
	}
	forced := engine.NewResult(pvc)
	e.ScanForced(context.Background(), 0, forced, pvc, nil)
	if forced.Status != model.StatusDone || forced.UsedBytes != 7 || created.Load() != 1 {
		t.Fatalf("ScanForced: %#v, temporary pods %d", forced, created.Load())
	}
}

func TestEngineSetOptionsValidates(t *testing.T) {
	e := newTestEngine(1, func(context.Context) (*model.ScanResult, error) { return nil, nil })
	for _, bad := range []engine.Options{
		{Image: "", Timeout: time.Minute, CleanupTimeout: time.Second, Concurrency: 1},
		{Image: "alpine", Timeout: time.Minute, CleanupTimeout: time.Second, Concurrency: -1},
		{Image: "alpine", Timeout: 0, CleanupTimeout: time.Second, Concurrency: 1},
		{Image: "alpine", Timeout: time.Minute, CleanupTimeout: time.Second, Concurrency: 1, MaxDepth: -1},
		{Image: "alpine", Timeout: time.Minute, CleanupTimeout: time.Second, Concurrency: 1, Excludes: []string{"a\nb"}},
	} {
		if err := e.SetOptions(bad); err == nil {
			t.Errorf("SetOptions(%+v) accepted invalid options", bad)
		}
	}
	if e.Options().Image != "alpine" {
		t.Fatal("a rejected SetOptions changed the options")
	}
}

func TestEngineConcurrencyChangeAppliesToWaitingScans(t *testing.T) {
	var running, peak atomic.Int32
	release := make(chan struct{})
	e := newTestEngine(1, func(context.Context) (*model.ScanResult, error) {
		now := running.Add(1)
		for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
		}
		<-release
		running.Add(-1)
		return &model.ScanResult{Status: model.StatusDone}, nil
	})
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Go(func() {
			pvc := mountedPVC("claim")
			e.Scan(context.Background(), i, engine.NewResult(pvc), pvc, nil)
		})
	}
	waitFor(t, func() bool { return running.Load() == 1 })
	o := e.Options()
	o.Concurrency = 3
	if err := e.SetOptions(o); err != nil {
		t.Fatal(err)
	}
	// The two waiting scans move to the new limit while the first still runs.
	waitFor(t, func() bool { return running.Load() == 3 })
	close(release)
	wg.Wait()
	if peak.Load() != 3 {
		t.Fatalf("peak concurrent scans = %d, want 3", peak.Load())
	}
}

func TestEngineZeroConcurrencyMeansNoLimit(t *testing.T) {
	var running atomic.Int32
	release := make(chan struct{})
	e := newTestEngine(0, func(context.Context) (*model.ScanResult, error) {
		running.Add(1)
		<-release
		return &model.ScanResult{Status: model.StatusDone}, nil
	})
	pvcs := make([]kube.PVCInfo, 8)
	results := make([]*model.ScanResult, len(pvcs))
	for i := range pvcs {
		pvcs[i] = mountedPVC(fmt.Sprint("claim-", i))
		results[i] = engine.NewResult(pvcs[i])
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ScanAll(context.Background(), pvcs, results, nil)
	}()
	waitFor(t, func() bool { return running.Load() == 8 })
	// Limiting a running engine applies to scans started afterwards.
	o := e.Options()
	o.Concurrency = 1
	if err := e.SetOptions(o); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	for i, r := range results {
		if r.Status != model.StatusDone {
			t.Fatalf("result %d = %s", i, r.Status)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestEngineSharesConcurrencyAcrossScanCalls(t *testing.T) {
	var running, peak atomic.Int32
	e := newTestEngine(2, func(context.Context) (*model.ScanResult, error) {
		now := running.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return &model.ScanResult{Status: model.StatusDone}, nil
	})
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			pvc := mountedPVC("claim")
			e.Scan(context.Background(), i, engine.NewResult(pvc), pvc, nil)
		})
	}
	wg.Wait()
	if got := peak.Load(); got != 2 {
		t.Fatalf("peak concurrent scans = %d, want 2", got)
	}
}

func TestEngineScanGivesUpWhileWaitingForSlot(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	e := newTestEngine(1, func(context.Context) (*model.ScanResult, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return &model.ScanResult{Status: model.StatusDone}, nil
	})
	first := mountedPVC("first")
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Scan(context.Background(), 0, engine.NewResult(first), first, nil)
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	second := mountedPVC("second")
	result := engine.NewResult(second)
	if e.Scan(ctx, 1, result, second, nil) {
		t.Fatal("Scan ran although its context ended while waiting for a slot")
	}
	close(release)
	<-done
	if calls.Load() != 1 || result.Status != model.StatusPending {
		t.Fatalf("calls = %d, waiting result status = %s", calls.Load(), result.Status)
	}
}
