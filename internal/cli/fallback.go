package cli

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/scanner"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func (r *runner) scanWithTempPod(ctx context.Context, client kubernetes.Interface, config *rest.Config, result *model.ScanResult, pvc kube.PVCInfo) {
	if ctx.Err() != nil {
		r.fail(result, ctx, ctx.Err())
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, r.options.timeout)
	defer cancel()
	result.PodName, result.ScanPath = "", "/mnt"
	scanner.NotifyProgress(ctx, model.StatusCreatingPod, "")
	r.messageAt(slog.LevelInfo, fmt.Sprintf("%s/%s: creating temporary pod", pvc.Namespace, pvc.Name))
	pod, err := r.deps.CreatePod(opCtx, client, pvc, r.options.image)
	if err != nil {
		r.fail(result, opCtx, err)
		return
	}
	result.PodName, result.ScanPath = pod.Name, "/mnt"
	scanner.NotifyProgress(ctx, model.StatusWaitingPod, "")
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), r.options.cleanupTimeout)
		defer cleanupCancel()
		if err := r.deps.DeletePod(cleanupCtx, client, pvc.Namespace, pod); err != nil {
			warning := fmt.Sprintf("delete temp pod failed: %s/%s: %v", pvc.Namespace, pod.Name, err)
			result.Warnings = append(result.Warnings, warning)
		}
	}()
	if err := r.deps.WaitForPodReady(opCtx, client, pvc.Namespace, pod.Name, r.options.timeout); err != nil {
		r.fail(result, opCtx, err)
		return
	}
	scanner.NotifyProgress(ctx, model.StatusPodReady, "")
	r.messageAt(slog.LevelInfo, fmt.Sprintf("%s/%s: uploading scanner and scanning temporary pod %s:/mnt", pvc.Namespace, pvc.Name, pod.Name))
	sr, err := r.deps.UploadAndScan(opCtx, client, config, pvc.Namespace, pod.Name, "scanner", "/tmp", "/mnt", &pvc, r.options.maxDepth, r.options.excludes, r.options.workers, false)
	if ctx.Err() != nil {
		r.fail(result, ctx, ctx.Err())
		return
	}
	if err := scanError(sr, err); err != nil {
		r.fail(result, opCtx, err)
		return
	}
	result.UsedBytes, result.Status, result.Error = sr.UsedBytes, model.StatusDone, ""
}
