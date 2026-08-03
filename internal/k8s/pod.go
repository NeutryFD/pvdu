package k8s

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

func BuildTempPodName(pvcName string) string {
	const (
		prefix     = "pvdu-scanner-"
		maxNameLen = 63
		maxPvcLen  = 49
	)
	candidate := prefix + pvcName
	if len(candidate) <= maxNameLen {
		return candidate
	}
	return strings.TrimRight(prefix+pvcName[:maxPvcLen], "-.")
}

const (
	stalePodPollInterval   = 100 * time.Millisecond
	stalePodRemovalTimeout = 30 * time.Second
)

func RemoveStalePod(ctx context.Context, clientset kubernetes.Interface, namespace, podName string, deadline time.Duration) error {
	_, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get stale pod %s: %w", podName, err)
	}

	grace := int64(0)
	err = clientset.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{
		GracePeriodSeconds: &grace,
	})
	if err != nil {
		return fmt.Errorf("delete stale pod %s: %w", podName, err)
	}

	pollErr := wait.PollUntilContextTimeout(ctx, stalePodPollInterval, deadline, true, func(ctx context.Context) (bool, error) {
		_, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	})
	if pollErr != nil {
		return fmt.Errorf("stale pod %s not confirmed gone within %s: %w", podName, deadline, pollErr)
	}
	return nil
}

func CreateTempPod(ctx context.Context, clientset kubernetes.Interface, namespace, pvcName, image string) (string, error) {
	podName := BuildTempPodName(pvcName)

	if err := RemoveStalePod(ctx, clientset, namespace, podName, stalePodRemovalTimeout); err != nil {
		return "", fmt.Errorf("remove stale pod %s: %w", podName, err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels: map[string]string{
				"app": "pvdu-scanner",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:            "scanner",
					Image:           image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{"sh", "-c", "sleep infinity"},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "pvc",
							MountPath: "/mnt",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "pvc",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName,
							ReadOnly:  true,
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}

	_, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("create temp pod: %w", err)
	}

	return podName, nil
}

func WaitForPodReady(ctx context.Context, clientset kubernetes.Interface, namespace, podName string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		switch pod.Status.Phase {
		case corev1.PodRunning:
			return true, nil
		case corev1.PodFailed, corev1.PodUnknown:
			return false, fmt.Errorf("pod %s entered phase %s", podName, pod.Status.Phase)
		}
		return false, nil
	})
}

func DeletePod(ctx context.Context, clientset kubernetes.Interface, namespace, podName string) error {
	grace := int64(0)
	err := clientset.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{
		GracePeriodSeconds: &grace,
	})
	if err != nil {
		return fmt.Errorf("delete pod %s: %w", podName, err)
	}
	return nil
}
