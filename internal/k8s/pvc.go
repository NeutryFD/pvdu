package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type PodMount struct {
	PodName   string
	MountPath string
}

type PVCInfo struct {
	Name           string
	Namespace      string
	RequestedBytes int64
	RequestedStr   string
	PVName         string
	PVBytes        int64
	PVStr          string
	Mounts         []PodMount
}

func ListPVCs(ctx context.Context, clientset kubernetes.Interface, namespace string) ([]PVCInfo, error) {
	pvcs, err := clientset.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list PVCs: %w", err)
	}

	var infos []PVCInfo
	for _, pvc := range pvcs.Items {
		info := PVCInfo{
			Name:      pvc.Name,
			Namespace: pvc.Namespace,
			PVName:    pvc.Spec.VolumeName,
		}

		if req, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			info.RequestedBytes = req.Value()
			info.RequestedStr = req.String()
		}

		if pvc.Spec.VolumeName != "" {
			pv, err := clientset.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
			if err == nil {
				if cap, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
					info.PVBytes = cap.Value()
					info.PVStr = cap.String()
				}
			}
		}

		info.Mounts = findPodsByPVC(ctx, clientset, pvc.Namespace, pvc.Name)
		infos = append(infos, info)
	}

	return infos, nil
}

func GetPVCByName(ctx context.Context, clientset kubernetes.Interface, namespace, name string) (*PVCInfo, error) {
	pvc, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get PVC %s/%s: %w", namespace, name, err)
	}

	info := &PVCInfo{
		Name:      pvc.Name,
		Namespace: pvc.Namespace,
		PVName:    pvc.Spec.VolumeName,
	}

	if req, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		info.RequestedBytes = req.Value()
		info.RequestedStr = req.String()
	}

	if pvc.Spec.VolumeName != "" {
		pv, err := clientset.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		if err == nil {
			if cap, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
				info.PVBytes = cap.Value()
				info.PVStr = cap.String()
			}
		}
	}

	info.Mounts = findPodsByPVC(ctx, clientset, namespace, name)
	return info, nil
}

func findPodsByPVC(ctx context.Context, clientset kubernetes.Interface, namespace, pvcName string) []PodMount {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}

	var mounts []PodMount
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		for _, vol := range pod.Spec.Volumes {
			if vol.PersistentVolumeClaim == nil || vol.PersistentVolumeClaim.ClaimName != pvcName {
				continue
			}

			for _, c := range pod.Spec.Containers {
				for _, vm := range c.VolumeMounts {
					if vm.Name == vol.Name {
						mounts = append(mounts, PodMount{
							PodName:   pod.Name,
							MountPath: vm.MountPath,
						})
					}
				}
			}
		}
	}
	return mounts
}
