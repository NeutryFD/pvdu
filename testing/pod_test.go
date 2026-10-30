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

	"github.com/neutry/pvdu/internal/kube"
)

const testNamespace = "default"

func isAlphanumeric(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// staleTempPod is a finished pvdu-managed pod, the only kind RemoveStalePod reclaims.
func staleTempPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pvdu-scanner-data",
			Namespace: testNamespace,
			UID:       "stale-uid",
			Labels:    map[string]string{"pvdu.neutry.io/managed": "true"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodFailed},
	}
}

func createdPods(cs *fake.Clientset) []*corev1.Pod {
	var pods []*corev1.Pod
	for _, a := range cs.Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource == "pods" {
			pods = append(pods, a.(k8stesting.CreateAction).GetObject().(*corev1.Pod))
		}
	}
	return pods
}

func TestBuildTempPodNameShortPVC(t *testing.T) {
	got := kube.BuildTempPodName("data")
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

func TestCreateScanPodTruncatesGenerateName(t *testing.T) {
	cs := fake.NewSimpleClientset()
	pvc := kube.PVCInfo{Name: strings.Repeat("a", 80), Namespace: testNamespace}

	if _, err := kube.CreateScanPod(context.Background(), cs, pvc, "scan-image:v1"); err != nil {
		t.Fatalf("CreateScanPod returned error: %v", err)
	}
	pods := createdPods(cs)
	if len(pods) != 1 {
		t.Fatalf("created %d pods, want 1", len(pods))
	}
	prefix := pods[0].GenerateName
	if !strings.HasPrefix(prefix, "pvdu-scanner-") || !strings.HasSuffix(prefix, "-") {
		t.Fatalf("GenerateName %q must be a pvdu-scanner- prefix ending in a dash", prefix)
	}
	// The API server appends a 5 character suffix, so the whole name must stay within 63.
	if len(prefix)+5 > 63 {
		t.Fatalf("GenerateName %q leaves no room for the generated suffix", prefix)
	}
	if pods[0].Labels["pvdu.neutry.io/managed"] != "true" {
		t.Fatalf("pod must be labelled as pvdu-managed, got %v", pods[0].Labels)
	}
}

func TestCreateScanPodNeverDeletesExistingPod(t *testing.T) {
	cs := fake.NewSimpleClientset(staleTempPod())

	if _, err := kube.CreateScanPod(context.Background(), cs, kube.PVCInfo{Name: "data", Namespace: testNamespace}, "scan-image:v1"); err != nil {
		t.Fatalf("CreateScanPod returned error: %v", err)
	}
	for _, a := range cs.Actions() {
		if a.GetVerb() == "delete" && a.GetResource().Resource == "pods" {
			t.Fatal("CreateScanPod must never delete an existing pod")
		}
	}
	if len(createdPods(cs)) != 1 {
		t.Fatalf("expected exactly one pod to be created")
	}
}

func TestCreateScanPodMountsClaimReadOnly(t *testing.T) {
	cs := fake.NewSimpleClientset()
	if _, err := kube.CreateScanPod(context.Background(), cs, kube.PVCInfo{Name: "data", Namespace: testNamespace}, "scan-image:v1"); err != nil {
		t.Fatalf("CreateScanPod returned error: %v", err)
	}
	pods := createdPods(cs)
	if len(pods) != 1 {
		t.Fatalf("created %d pods, want 1", len(pods))
	}
	spec := pods[0].Spec
	if len(spec.Volumes) != 1 || spec.Volumes[0].PersistentVolumeClaim == nil || !spec.Volumes[0].PersistentVolumeClaim.ReadOnly {
		t.Fatalf("claim volume must be read-only: %#v", spec.Volumes)
	}
	if mounts := spec.Containers[0].VolumeMounts; len(mounts) != 1 || !mounts[0].ReadOnly || mounts[0].MountPath != "/mnt" {
		t.Fatalf("container mount must be read-only at /mnt: %#v", mounts)
	}
}

func TestRemoveStalePodDeadlineExpiry(t *testing.T) {
	pod := staleTempPod()
	cs := fake.NewSimpleClientset(pod)

	cs.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, pod, nil
	})

	start := time.Now()
	err := kube.RemoveStalePod(context.Background(), cs, testNamespace, "pvdu-scanner-data", time.Second)
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
	pod := staleTempPod()
	cs := fake.NewSimpleClientset(pod)

	stuckErr := errors.New("the pod is stuck")
	cs.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, stuckErr
	})
	cs.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, stuckErr
	})

	err := kube.RemoveStalePod(context.Background(), cs, testNamespace, "pvdu-scanner-data", time.Second)
	if err == nil {
		t.Fatalf("expected a non-nil error, got nil")
	}
	if !errors.Is(err, stuckErr) {
		t.Fatalf("error %v must wrap the cause %q", err, "the pod is stuck")
	}
}

func TestRemoveStalePodPollWrappedError(t *testing.T) {
	pod := staleTempPod()
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

	err := kube.RemoveStalePod(context.Background(), cs, testNamespace, pod.Name, time.Second)
	if err == nil {
		t.Fatalf("expected a non-nil error, got nil")
	}
	if !errors.Is(err, stuckErr) {
		t.Fatalf("error %v must wrap the poll Get cause %q", err, stuckErr)
	}
}

func TestRemoveStalePodDeletesAndWaitsGone(t *testing.T) {
	pod := staleTempPod()
	cs := fake.NewSimpleClientset(pod)

	gets := 0
	cs.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if gets < 2 {
			gets++
			return true, pod, nil
		}
		return false, nil, nil
	})

	err := kube.RemoveStalePod(context.Background(), cs, testNamespace, "pvdu-scanner-data", time.Second)
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
	err := kube.RemoveStalePod(context.Background(), cs, testNamespace, "pvdu-scanner-data", time.Second)
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
	got := kube.BuildTempPodName("my.pvc-name")
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
	got := kube.BuildTempPodName(strings.Repeat("-", 60))
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
	got := kube.BuildTempPodName(pvc)
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
	got := kube.BuildTempPodName(pvc)
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
