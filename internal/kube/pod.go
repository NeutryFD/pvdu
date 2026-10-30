package kube

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const scannerLabel = "pvdu.neutry.io/managed"

// PodRef identifies exactly the pod created by a scan, even if its name is reused.
type PodRef struct {
	Name string
	UID  types.UID
}

// BuildTempPodName returns a human-readable prefix, not a unique identity.
func BuildTempPodName(pvcName string) string {
	name := "pvdu-scanner-" + pvcName
	if len(name) > 63 {
		name = name[:62]
	}
	return strings.TrimRight(name, "-.")
}

// RemoveStalePod only reclaims terminal, explicitly managed scanner pods.
func RemoveStalePod(ctx context.Context, client kubernetes.Interface, namespace, name string, timeout time.Duration) error {
	pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get stale pod %s: %w", name, err)
	}
	if pod.Labels[scannerLabel] != "true" || (pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed) {
		return fmt.Errorf("refuse to reclaim active or unmanaged pod %s", name)
	}
	if err := DeleteScanPod(ctx, client, namespace, PodRef{Name: name, UID: pod.UID}); err != nil {
		return err
	}
	err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		return fmt.Errorf("stale pod %s not confirmed gone: %w", name, err)
	}
	return nil
}

// CreateScanPod never removes an existing pod. Kubernetes generates its identity.
func CreateScanPod(ctx context.Context, client kubernetes.Interface, pvc PVCInfo, image string) (PodRef, error) {
	if pvc.VolumeMode == corev1.PersistentVolumeBlock {
		return PodRef{}, fmt.Errorf("block-mode PVCs cannot be scanned as filesystems")
	}
	for _, mode := range pvc.AccessModes {
		if mode == corev1.ReadWriteOncePod && pvc.InUse {
			return PodRef{}, fmt.Errorf("ReadWriteOncePod PVC is already in use; scan its existing whole-volume mount")
		}
	}
	no := false
	activeDeadline := int64(600)
	if deadline, ok := ctx.Deadline(); ok {
		activeDeadline = max(1, int64(time.Until(deadline).Seconds())+1)
	}
	prefix := BuildTempPodName(pvc.Name)
	if len(prefix) > 57 {
		prefix = strings.TrimRight(prefix[:57], "-.")
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: prefix + "-",
			Namespace:    pvc.Namespace,
			Labels:       map[string]string{scannerLabel: "true", "app": "pvdu-scanner"},
			Annotations:  map[string]string{"pvdu.neutry.io/pvc": pvc.Name},
		},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &no,
			ActiveDeadlineSeconds:        &activeDeadline,
			RestartPolicy:                corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name: "scanner", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"sh", "-c", "sleep infinity"},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &no,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("512Mi")},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "pvc", MountPath: "/mnt", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{Name: "pvc", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc.Name, ReadOnly: true}}}},
		},
	}
	// Keep attached RWO volumes on their current node, while retaining scheduler
	// admission and PV node-affinity checks (unlike assigning spec.nodeName).
	for _, mode := range pvc.AccessModes {
		if mode == corev1.ReadWriteOnce && pvc.NodeName != "" {
			pod.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{pvc.NodeName}}}}}}}}
		}
	}
	created, err := client.CoreV1().Pods(pvc.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return PodRef{}, fmt.Errorf("create temp pod: %w", err)
	}
	return PodRef{Name: created.Name, UID: created.UID}, nil
}

func WaitForPodReady(ctx context.Context, client kubernetes.Interface, namespace, name string, timeout time.Duration) error {
	var detail string
	err := wait.PollUntilContextTimeout(ctx, time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
				detail = condition.Message
			}
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != "scanner" {
				continue
			}
			if status.State.Waiting != nil {
				detail = status.State.Waiting.Reason + ": " + status.State.Waiting.Message
			}
			if status.State.Terminated != nil {
				return false, fmt.Errorf("scanner terminated: %s", status.State.Terminated.Reason)
			}
			if status.State.Running != nil && status.Ready {
				return true, nil
			}
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return false, fmt.Errorf("pod %s entered phase %s", name, pod.Status.Phase)
		}
		return false, nil
	})
	if err != nil && detail != "" {
		return fmt.Errorf("wait pod %s (%s): %w", name, detail, err)
	}
	return err
}

func DeleteScanPod(ctx context.Context, client kubernetes.Interface, namespace string, pod PodRef) error {
	if pod.UID == "" {
		return fmt.Errorf("refuse to delete pod %s without its creation UID", pod.Name)
	}
	grace := int64(0)
	err := client.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &grace, Preconditions: &metav1.Preconditions{UID: &pod.UID}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete pod %s: %w", pod.Name, err)
	}
	return nil
}

// CreateTempPod is retained for callers migrating to the UID-aware API.
func CreateTempPod(ctx context.Context, client kubernetes.Interface, namespace, pvc, image string) (string, error) {
	pod, err := CreateScanPod(ctx, client, PVCInfo{Name: pvc, Namespace: namespace}, image)
	return pod.Name, err
}

// DeletePod reclaims only terminal managed pods. Active scan cleanup uses DeleteScanPod.
func DeletePod(ctx context.Context, client kubernetes.Interface, namespace, name string) error {
	return RemoveStalePod(ctx, client, namespace, name, 30*time.Second)
}
