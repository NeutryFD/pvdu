package pvdu_test

import (
	"context"
	"strings"
	"testing"

	"github.com/neutry/pvdu/internal/kube"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestListNamespacesSorted(t *testing.T) {
	ns := func(name string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	names, err := kube.ListNamespaces(context.Background(), fake.NewSimpleClientset(ns("monitoring"), ns("default"), ns("kube-system")))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, ","); got != "default,kube-system,monitoring" {
		t.Fatalf("namespaces = %s", got)
	}
}

func TestListPVCsReportsStorageClass(t *testing.T) {
	fast := "fast"
	claim := func(name, volume string, class *string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: volume, StorageClassName: class},
		}
	}
	volume := func(name, class string) *corev1.PersistentVolume {
		return &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.PersistentVolumeSpec{
				StorageClassName: class,
				Capacity:         corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		}
	}
	client := fake.NewSimpleClientset(
		claim("explicit", "pv-explicit", &fast), volume("pv-explicit", "fast"),
		// A claim using the default class may leave it unset; the PV knows it.
		claim("defaulted", "pv-defaulted", nil), volume("pv-defaulted", "local-path"),
		claim("pending", "", nil),
	)
	infos, err := kube.ListPVCs(context.Background(), client, "ns")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, info := range infos {
		got[info.Name] = info.StorageClass
	}
	want := map[string]string{"explicit": "fast", "defaulted": "local-path", "pending": ""}
	for name, class := range want {
		if got[name] != class {
			t.Errorf("%s storage class = %q, want %q", name, got[name], class)
		}
	}
}
