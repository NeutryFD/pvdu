package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/neutry/pvdu/internal/k8s"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/ui"
	"github.com/NeutryFD/dirwalker"
)

var (
	force             bool
	timeout           time.Duration
	concurrency       int
	maxDepth          int
	excludes          []string
	outputFormat      string
	tempPodImage      string
	workers           int
	reportFiles       bool
)

var usageCmd = &cobra.Command{
	Use:   "usage [pvc <name>]",
	Short: "Show real storage usage of PVCs",
	Long: `Show real storage usage of PVCs by comparing PVC requested size, PV capacity,
and actual filesystem usage from a WalkDir scan.

Scans all PVCs in the namespace, or a specific PVC if name is provided.`,
	Args: cobra.MaximumNArgs(2),
	RunE: runUsage,
}

func init() {
	rootCmd.AddCommand(usageCmd)

	rootCmd.PersistentFlags().BoolVarP(&force, "force", "f", false, "Auto-create temp pod, skip confirmation")
	rootCmd.PersistentFlags().DurationVarP(&timeout, "timeout", "t", 120*time.Second, "Timeout for temp pod creation + scan")
	rootCmd.PersistentFlags().IntVarP(&concurrency, "concurrency", "c", 3, "Max parallel PVC scans")
	rootCmd.PersistentFlags().IntVarP(&maxDepth, "max-depth", "d", 0, "Directory depth for scanner (0 = unlimited)")
	rootCmd.PersistentFlags().StringSliceVarP(&excludes, "exclude", "e", nil, "Paths to exclude from scan (repeatable)")
	rootCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "default", "Output format (default, table, json, yaml)")
	rootCmd.PersistentFlags().StringVarP(&tempPodImage, "image", "i", "alpine:latest", "Image for temp pods")
	rootCmd.PersistentFlags().IntVarP(&workers, "workers", "w", 0, "Scanner workers (0 = auto)")
	rootCmd.PersistentFlags().BoolVar(&reportFiles, "files", false, "Report individual file sizes in scan output")
}

func runUsage(cmd *cobra.Command, args []string) error {
	pvc := pvcName
	if pvc == "" {
		if len(args) == 1 {
			pvc = args[0]
		} else if len(args) == 2 && args[0] == "pvc" {
			pvc = args[1]
		}
	}

	clientset, config, err := k8s.BuildClient(kubeconfig, ctx)
	if err != nil {
		return fmt.Errorf("build k8s client: %w", err)
	}

	ns := namespace
	if allNamespaces {
		ns = ""
	}
	if ns == "" && !allNamespaces && pvc == "" {
		ns = "default"
	}

	ctxBg := context.Background()
	var pvcs []k8s.PVCInfo

	if pvc != "" {
		targetNs := ns
		if targetNs == "" {
			targetNs = "default"
		}
		info, err := k8s.GetPVCByName(ctxBg, clientset, targetNs, pvc)
		if err != nil {
			return fmt.Errorf("get PVC: %w", err)
		}
		pvcs = []k8s.PVCInfo{*info}
	} else {
		pvcs, err = k8s.ListPVCs(ctxBg, clientset, ns)
		if err != nil {
			return fmt.Errorf("list PVCs: %w", err)
		}
	}

	if len(pvcs) == 0 {
		fmt.Println("No PVCs found.")
		return nil
	}

	results := make([]*model.ScanResult, len(pvcs))
	for i, p := range pvcs {
		results[i] = &model.ScanResult{
			Namespace:      p.Namespace,
			PVCName:        p.Name,
			RequestedBytes: p.RequestedBytes,
			RequestedStr:   p.RequestedStr,
			PVBytes:        p.PVBytes,
			Status:         model.StatusPending,
		}
	}

	fmt.Fprintf(os.Stderr, "Discovered %d PVCs\n", len(pvcs))
	pvcNames := make([]string, len(pvcs))
	for i, p := range pvcs {
		pvcNames[i] = p.Name
	}
	initProgress(pvcNames)
	scanAll(ctxBg, clientset, config, results, pvcs)
	finishProgress()

	switch outputFormat {
	case "json":
		fmt.Println(ui.RenderJSON(results))
	case "yaml":
		fmt.Println(ui.RenderYAML(results))
	case "table":
		fmt.Println(ui.RenderTable(results))
	default:
		fmt.Println(ui.RenderDefault(results))
	}

	return nil
}

func scanAll(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, results []*model.ScanResult, pvcs []k8s.PVCInfo) {
	sem := make(chan struct{}, concurrency)

	for i, pvc := range pvcs {
		sem <- struct{}{}
		go func(idx int, pvcInfo k8s.PVCInfo) {
			defer func() { <-sem }()
			scanPVC(ctx, clientset, config, results[idx], pvcInfo, idx)
		}(i, pvc)
	}

	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}
}

func scanPVC(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, result *model.ScanResult, pvcInfo k8s.PVCInfo, idx int) {
	result.Status = model.StatusScanning
	slog.Debug("scanning PVC", "namespace", pvcInfo.Namespace, "pvc", pvcInfo.Name)

	scanPath := "/mnt"
	if len(pvcInfo.Mounts) > 0 {
		scanPath = pvcInfo.Mounts[0].MountPath
		result.ScanPath = pvcInfo.Mounts[0].MountPath
		result.PodName = pvcInfo.Mounts[0].PodName
	}
	sendProgress(idx, result.PVCName, "scanning "+scanPath, 0, false, "")

	reportError := func(msg string) {
		result.Error = msg
		slog.Error("scan failed", "namespace", pvcInfo.Namespace, "pvc", pvcInfo.Name, "error", msg)
		sendProgress(idx, result.PVCName, "", 0, true, msg)
	}

	if len(pvcInfo.Mounts) > 0 {
		mount := pvcInfo.Mounts[0]
		slog.Debug("using existing pod mount", "pod", mount.PodName, "container", mount.ContainerName, "path", mount.MountPath)

		scanWithWritableDir := func(writableDir string) (*model.ScanResult, error) {
			return k8s.UploadAndScanPVC(ctx, clientset, config, pvcInfo.Namespace, mount.PodName, mount.ContainerName, writableDir, mount.MountPath, &pvcInfo, maxDepth, excludes, workers, reportFiles)
		}

		sr, err := scanWithWritableDir("/tmp")
		if err != nil {
			slog.Debug("/tmp not writable, trying mount path", "error", err)
			sr, err = scanWithWritableDir(mount.MountPath)
		}
		if err != nil {
			if force {
				scanWithTempPod(ctx, clientset, config, result, pvcInfo, idx)
			} else {
				reportError(err.Error())
			}
			return
		}
		result.UsedBytes = sr.UsedBytes
		result.Status = sr.Status
		if sr.Status == model.StatusError {
			if force {
				scanWithTempPod(ctx, clientset, config, result, pvcInfo, idx)
			} else {
				reportError(sr.Error)
			}
			return
		}
		slog.Debug("scan complete", "namespace", pvcInfo.Namespace, "pvc", pvcInfo.Name, "usedBytes", result.UsedBytes)
		sendProgress(idx, result.PVCName, "", result.UsedBytes, true, "")
		return
	}

	scanWithTempPod(ctx, clientset, config, result, pvcInfo, idx)
}

func scanWithTempPod(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, result *model.ScanResult, pvcInfo k8s.PVCInfo, idx int) {
	scanPath := "/mnt"
	if len(pvcInfo.Mounts) > 0 {
		scanPath = pvcInfo.Mounts[0].MountPath
	}
	slog.Debug("creating temp pod", "image", tempPodImage, "scanPath", scanPath)

	sendProgress(idx, result.PVCName, "creating pod", 0, false, "")
	podName, err := k8s.CreateTempPod(ctx, clientset, pvcInfo.Namespace, pvcInfo.Name, tempPodImage)
	if err != nil {
		result.Status = model.StatusError
		result.Error = fmt.Sprintf("create temp pod: %v (try --timeout/-t)", err)
		slog.Error("create temp pod failed", "namespace", pvcInfo.Namespace, "pvc", pvcInfo.Name, "error", err)
		sendProgress(idx, result.PVCName, "", 0, true, result.Error)
		return
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	err = k8s.WaitForPodReady(waitCtx, clientset, pvcInfo.Namespace, podName, timeout)
	cancel()
	if err != nil {
		k8s.DeletePod(context.Background(), clientset, pvcInfo.Namespace, podName)
		result.Status = model.StatusError
		result.Error = fmt.Sprintf("wait pod: %v (try --timeout/-t)", err)
		slog.Error("wait temp pod failed", "namespace", pvcInfo.Namespace, "pvc", pvcInfo.Name, "error", err)
		sendProgress(idx, result.PVCName, "", 0, true, result.Error)
		return
	}

	slog.Debug("temp pod ready, uploading scanner")
	sendProgress(idx, result.PVCName, "scanning "+scanPath, 0, false, "")
	sr, err := k8s.UploadAndScanPVC(ctx, clientset, config, pvcInfo.Namespace, podName, "scanner", "/tmp", "/mnt", &pvcInfo, maxDepth, excludes, workers, reportFiles)
	k8s.DeletePod(context.Background(), clientset, pvcInfo.Namespace, podName)

	if err != nil {
		result.Status = model.StatusError
		result.Error = err.Error()
		slog.Error("scan in temp pod failed", "namespace", pvcInfo.Namespace, "pvc", pvcInfo.Name, "error", err)
		sendProgress(idx, result.PVCName, "", 0, true, result.Error)
		return
	}
	result.UsedBytes = sr.UsedBytes
	result.Status = sr.Status
	if sr.Status == model.StatusError {
		result.Error = sr.Error
		slog.Error("scan in temp pod failed", "namespace", pvcInfo.Namespace, "pvc", pvcInfo.Name, "error", sr.Error)
		sendProgress(idx, result.PVCName, "", 0, true, sr.Error)
		return
	}
	slog.Debug("scan complete", "namespace", pvcInfo.Namespace, "pvc", pvcInfo.Name, "usedBytes", result.UsedBytes)
	sendProgress(idx, result.PVCName, "", result.UsedBytes, true, "")
}

var (
	progressMu    sync.Mutex
	progressCount int
)

func initProgress(names []string) {
	progressMu.Lock()
	defer progressMu.Unlock()
	progressCount = len(names)
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "%s: waiting...\n", name)
	}
	if progressCount > 0 {
		fmt.Fprintf(os.Stderr, "\033[%dA", progressCount)
	}
}

func finishProgress() {
	progressMu.Lock()
	defer progressMu.Unlock()
	if progressCount > 0 {
		fmt.Fprint(os.Stderr, "\033[A")
		fmt.Fprint(os.Stderr, "\033[2K\r")
		for i := 0; i < progressCount; i++ {
			fmt.Fprint(os.Stderr, "\n\033[2K\r")
		}
		fmt.Fprintf(os.Stderr, "\033[%dA", progressCount)
	}
}

func sendProgress(index int, pvcName, path string, size int64, done bool, errStr string) {
	progressMu.Lock()
	defer progressMu.Unlock()

	if index > 0 {
		fmt.Fprintf(os.Stderr, "\033[%dB", index)
	}
	fmt.Fprint(os.Stderr, "\033[2K\r")

	if errStr != "" {
		fmt.Fprintf(os.Stderr, "%s: error: %s", pvcName, errStr)
	} else if done && size >= 0 {
		fmt.Fprintf(os.Stderr, "%s: done %s", pvcName, dirwalker.FormatBytes(size))
	} else if path != "" {
		fmt.Fprintf(os.Stderr, "%s: %s", pvcName, path)
	}
	fmt.Fprint(os.Stderr, "\n")

	fmt.Fprintf(os.Stderr, "\033[%dA", index+1)
}
