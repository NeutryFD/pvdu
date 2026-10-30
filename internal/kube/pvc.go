package kube

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type PodMount struct {
	PodName       string
	ContainerName string
	MountPath     string
	SubPath       string
	SubPathExpr   string
	NodeName      string
}

func (m PodMount) WholeVolume() bool { return m.SubPath == "" && m.SubPathExpr == "" }

type PVCInfo struct {
	Name           string
	Namespace      string
	RequestedBytes int64
	RequestedStr   string
	PVName         string
	PVBytes        int64
	PVStr          string
	Mounts         []PodMount
	VolumeMode     corev1.PersistentVolumeMode
	AccessModes    []corev1.PersistentVolumeAccessMode
	NodeName       string
	InUse          bool
}

func ListPVCs(ctx context.Context, client kubernetes.Interface, namespace string) ([]PVCInfo, error) {
	pvcs, err := client.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list PVCs: %w", err)
	}
	return describePVCs(ctx, client, pvcs.Items)
}

func GetPVCByName(ctx context.Context, client kubernetes.Interface, namespace, name string) (*PVCInfo, error) {
	pvc, err := client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get PVC %s/%s: %w", namespace, name, err)
	}
	infos, err := describePVCs(ctx, client, []corev1.PersistentVolumeClaim{*pvc})
	if err != nil {
		return nil, err
	}
	return &infos[0], nil
}

type mountIndex struct {
	mounts map[string][]PodMount
	nodes  map[string]string
	inUse  map[string]bool
}

func describePVCs(ctx context.Context, client kubernetes.Interface, pvcs []corev1.PersistentVolumeClaim) ([]PVCInfo, error) {
	indices := map[string]mountIndex{}
	pvs := map[string]*corev1.PersistentVolume{}
	infos := make([]PVCInfo, 0, len(pvcs))
	for _, pvc := range pvcs {
		index, ok := indices[pvc.Namespace]
		if !ok {
			pods, err := client.CoreV1().Pods(pvc.Namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, fmt.Errorf("list pods in %s: %w", pvc.Namespace, err)
			}
			index = indexMounts(pods.Items)
			indices[pvc.Namespace] = index
		}
		info := PVCInfo{Name: pvc.Name, Namespace: pvc.Namespace, PVName: pvc.Spec.VolumeName, AccessModes: pvc.Spec.AccessModes, Mounts: index.mounts[pvc.Name], NodeName: index.nodes[pvc.Name], InUse: index.inUse[pvc.Name]}
		if pvc.Spec.VolumeMode != nil {
			info.VolumeMode = *pvc.Spec.VolumeMode
		}
		if req, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			info.RequestedBytes, info.RequestedStr = req.Value(), req.String()
		}
		if pvc.Spec.VolumeName != "" {
			pv, ok := pvs[pvc.Spec.VolumeName]
			if !ok {
				var err error
				pv, err = client.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
				if err != nil {
					return nil, fmt.Errorf("get PV %s for %s/%s: %w", pvc.Spec.VolumeName, pvc.Namespace, pvc.Name, err)
				}
				pvs[pvc.Spec.VolumeName] = pv
			}
			if capacity, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
				info.PVBytes, info.PVStr = capacity.Value(), capacity.String()
			}
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func indexMounts(pods []corev1.Pod) mountIndex {
	index := mountIndex{map[string][]PodMount{}, map[string]string{}, map[string]bool{}}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim == nil {
				continue
			}
			claim := volume.PersistentVolumeClaim.ClaimName
			index.inUse[claim] = true
			if pod.Spec.NodeName != "" {
				index.nodes[claim] = pod.Spec.NodeName
			}
			if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
				continue
			}
			for _, container := range pod.Spec.Containers {
				ready := false
				for _, status := range pod.Status.ContainerStatuses {
					if status.Name == container.Name && status.Ready && status.State.Running != nil {
						ready = true
					}
				}
				if !ready {
					continue
				}
				for _, mount := range container.VolumeMounts {
					if mount.Name == volume.Name {
						index.mounts[claim] = append(index.mounts[claim], PodMount{PodName: pod.Name, ContainerName: container.Name, MountPath: mount.MountPath, SubPath: mount.SubPath, SubPathExpr: mount.SubPathExpr, NodeName: pod.Spec.NodeName})
					}
				}
			}
		}
	}
	return index
}
