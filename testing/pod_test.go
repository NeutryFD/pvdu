package pvdu_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/neutry/pvdu/internal/k8s"
)

const testNamespace = "default"

func isAlphanumeric(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func staleTempPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
	}
}

func TestBuildTempPodNameShortPVC(t *testing.T) {
	got := k8s.BuildTempPodName("data")
	want := "pvdu-scanner-data"
	if got != want {
		t.Fatalf("BuildTempPodName(\"data\") = %q, want %q", got, want)
	}
	if len(got) > 63 {
		t.Fatalf("name %q is %d chars, want at most 63", got, len(got))
	}
	if !isAlphanumeric(got[0]) || !isAlphanumeric(got[len(got)-1]) {
		t.Fatalf("name %q must start and end with an alphanumeric character", got)
	}
}

func TestCreateTempPodTruncatedName(t *testing.T) {
	pvc := strings.Repeat("a", 80)
	want := "pvdu-scanner-" + strings.Repeat("a", 49)
	cs := fake.NewSimpleClientset()

	got, err := k8s.CreateTempPod(context.Background(), cs, testNamespace, pvc, "scan-image:v1")
	if err != nil {
		t.Fatalf("CreateTempPod returned error: %v", err)
	}
	if got != want {
		t.Fatalf("CreateTempPod returned name %q, want %q", got, want)
	}

	if _, getErr := cs.CoreV1().Pods(testNamespace).Get(context.Background(), want, metav1.GetOptions{}); getErr != nil {
		t.Fatalf("pod %q was not created: %v", want, getErr)
	}
}

func TestCreateTempPodNoCreateOnStaleDeleteFail(t *testing.T) {
	cs := fake.NewSimpleClientset(staleTempPod("pvdu-scanner-data"))

	deniedErr := errors.New("delete denied")
	cs.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, deniedErr
	})

	_, err := k8s.CreateTempPod(context.Background(), cs, testNamespace, "data", "scan-image:v1")
	if err == nil {
		t.Fatalf("expected a non-nil error, got nil")
	}
	if !errors.Is(err, deniedErr) {
		t.Fatalf("error %v must wrap the stale-delete failure %q", err, "delete denied")
	}
	for _, a := range cs.Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource == "pods" {
			t.Fatalf("unexpected Create request for a pod after stale-delete failure")
		}
	}
}

func TestCreateTempPodDeleteOrderedBeforeCreate(t *testing.T) {
	cs := fake.NewSimpleClientset(staleTempPod("pvdu-scanner-data"))

	got, err := k8s.CreateTempPod(context.Background(), cs, testNamespace, "data", "scan-image:v1")
	if err != nil {
		t.Fatalf("CreateTempPod returned error: %v", err)
	}
	if got != "pvdu-scanner-data" {
		t.Fatalf("CreateTempPod returned name %q, want %q", got, "pvdu-scanner-data")
	}

	created, getErr := cs.CoreV1().Pods(testNamespace).Get(context.Background(), "pvdu-scanner-data", metav1.GetOptions{})
	if getErr != nil {
		t.Fatalf("pod %q was not created: %v", "pvdu-scanner-data", getErr)
	}
	if created.Name != "pvdu-scanner-data" || created.Namespace != testNamespace {
		t.Fatalf("unexpected created pod %s/%s", created.Namespace, created.Name)
	}

	deleteIdx, createIdx := -1, -1
	for i, a := range cs.Actions() {
		if a.GetResource().Resource != "pods" {
			continue
		}
		switch a.GetVerb() {
		case "delete":
			if deleteIdx == -1 {
				deleteIdx = i
			}
		case "create":
			if createIdx == -1 {
				createIdx = i
			}
		}
	}
	if deleteIdx == -1 {
		t.Fatalf("expected a Delete request for pod %q", "pvdu-scanner-data")
	}
	if createIdx == -1 {
		t.Fatalf("expected a Create request for pod %q", "pvdu-scanner-data")
	}
	if deleteIdx > createIdx {
		t.Fatalf("Delete (action %d) must be recorded before Create (action %d)", deleteIdx, createIdx)
	}
}

func TestRemoveStalePodDeadlineExpiry(t *testing.T) {
	pod := staleTempPod("pvdu-scanner-data")
	cs := fake.NewSimpleClientset(pod)

	cs.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, pod, nil
	})

	start := time.Now()
	err := k8s.RemoveStalePod(context.Background(), cs, testNamespace, "pvdu-scanner-data", time.Second)
	if err == nil {
		t.Fatalf("expected a non-nil error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v must indicate the deadline was exceeded", err)
	}
	if !strings.Contains(err.Error(), "not confirmed gone") {
		t.Fatalf("error %q must indicate the stale pod was not confirmed gone within the deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("RemoveStalePod took %v, expected bounded by the 1s deadline", elapsed)
	}
	if _, getErr := cs.CoreV1().Pods(testNamespace).Get(context.Background(), "pvdu-scanner-data", metav1.GetOptions{}); getErr != nil {
		t.Fatalf("pod %q should still exist after the call, got: %v", "pvdu-scanner-data", getErr)
	}
}

func TestRemoveStalePodWrappedError(t *testing.T) {
	pod := staleTempPod("pvdu-scanner-data")
	cs := fake.NewSimpleClientset(pod)

	stuckErr := errors.New("the pod is stuck")
	cs.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, stuckErr
	})
	cs.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, stuckErr
	})

	err := k8s.RemoveStalePod(context.Background(), cs, testNamespace, "pvdu-scanner-data", time.Second)
	if err == nil {
		t.Fatalf("expected a non-nil error, got nil")
	}
	if !errors.Is(err, stuckErr) {
		t.Fatalf("error %v must wrap the cause %q", err, "the pod is stuck")
	}
}

func TestRemoveStalePodPollWrappedError(t *testing.T) {
	pod := staleTempPod("pvdu-scanner-data")
	cs := fake.NewSimpleClientset(pod)
	stuckErr := errors.New("the pod status is unavailable")
	gets := 0
	cs.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if gets == 0 {
			gets++
			return true, pod, nil
		}
		return true, nil, stuckErr
	})

	err := k8s.RemoveStalePod(context.Background(), cs, testNamespace, pod.Name, time.Second)
	if err == nil {
		t.Fatalf("expected a non-nil error, got nil")
	}
	if !errors.Is(err, stuckErr) {
		t.Fatalf("error %v must wrap the poll Get cause %q", err, stuckErr)
	}
}

func TestRemoveStalePodDeletesAndWaitsGone(t *testing.T) {
	pod := staleTempPod("pvdu-scanner-data")
	cs := fake.NewSimpleClientset(pod)

	gets := 0
	cs.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if gets < 2 {
			gets++
			return true, pod, nil
		}
		return false, nil, nil
	})

	err := k8s.RemoveStalePod(context.Background(), cs, testNamespace, "pvdu-scanner-data", time.Second)
	if err != nil {
		t.Fatalf("RemoveStalePod returned error: %v", err)
	}

	var deleteWithGraceZero bool
	getAfterDelete := 0
	deleted := false
	for _, a := range cs.Actions() {
		switch {
		case a.GetVerb() == "delete" && a.GetResource().Resource == "pods":
			grace := a.(k8stesting.DeleteAction).GetDeleteOptions().GracePeriodSeconds
			if grace != nil && *grace == 0 {
				deleteWithGraceZero = true
			}
			deleted = true
		case a.GetVerb() == "get" && a.GetResource().Resource == "pods":
			if deleted {
				getAfterDelete++
			}
		}
	}
	if !deleteWithGraceZero {
		t.Fatalf("expected a Delete for pod %q with GracePeriodSeconds 0", "pvdu-scanner-data")
	}
	if getAfterDelete < 2 {
		t.Fatalf("expected Get to be called repeatedly until NotFound, got %d Get(s) after Delete", getAfterDelete)
	}
}

func TestRemoveStalePodMissingPodNil(t *testing.T) {
	cs := fake.NewSimpleClientset()
	err := k8s.RemoveStalePod(context.Background(), cs, testNamespace, "pvdu-scanner-data", time.Second)
	if err != nil {
		t.Fatalf("RemoveStalePod on missing pod returned error: %v", err)
	}
	for _, a := range cs.Actions() {
		if a.GetVerb() == "delete" && a.GetResource().Resource == "pods" {
			t.Fatalf("unexpected Delete request issued for pods")
		}
	}
}

func TestBuildTempPodNameSingleLabel(t *testing.T) {
	got := k8s.BuildTempPodName("my.pvc-name")
	want := "pvdu-scanner-my.pvc-name"
	if got != want {
		t.Fatalf("BuildTempPodName(\"my.pvc-name\") = %q, want %q", got, want)
	}
	if strings.Contains(got, "/") {
		t.Fatalf("name %q must not contain a namespace qualifier", got)
	}
	if len(got) > 63 {
		t.Fatalf("name %q is %d chars, want at most 63", got, len(got))
	}
}

func TestBuildTempPodNameTrimsWholePVCPortion(t *testing.T) {
	got := k8s.BuildTempPodName(strings.Repeat("-", 60))
	want := "pvdu-scanner"
	if got != want {
		t.Fatalf("BuildTempPodName(60 dashes) = %q, want %q", got, want)
	}
	if got == "" {
		t.Fatalf("name must be non-empty")
	}
	if len(got) > 63 {
		t.Fatalf("name %q is %d chars, want at most 63", got, len(got))
	}
	if !isAlphanumeric(got[0]) || !isAlphanumeric(got[len(got)-1]) {
		t.Fatalf("name %q must start and end with an alphanumeric character", got)
	}
}

func TestBuildTempPodNameTrimsTrailingDashes(t *testing.T) {
	pvc := strings.Repeat("x", 48) + strings.Repeat("-", 20)
	got := k8s.BuildTempPodName(pvc)
	want := "pvdu-scanner-" + strings.Repeat("x", 48)
	if got != want {
		t.Fatalf("BuildTempPodName(48 x + 20 dashes) = %q, want %q", got, want)
	}
	if len(got) != 61 {
		t.Fatalf("name %q is %d chars, want exactly 61", got, len(got))
	}
	if !isAlphanumeric(got[len(got)-1]) {
		t.Fatalf("name %q must end with an alphanumeric character", got)
	}
}

func TestBuildTempPodNameTruncatesTo63(t *testing.T) {
	pvc := strings.Repeat("a", 80)
	got := k8s.BuildTempPodName(pvc)
	want := "pvdu-scanner-" + strings.Repeat("a", 49)
	if got != want {
		t.Fatalf("BuildTempPodName(80 a's) = %q, want %q", got, want)
	}
	if len(got) != 62 {
		t.Fatalf("name %q is %d chars, want exactly 62", got, len(got))
	}
	if len(got) > 63 {
		t.Fatalf("name %q is %d chars, want at most 63", got, len(got))
	}
	if !isAlphanumeric(got[len(got)-1]) {
		t.Fatalf("name %q must end with an alphanumeric character", got)
	}
}
