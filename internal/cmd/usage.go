package cmd

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/neutry/pvdu/internal/k8s"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/ui"
)

var (
	force        bool
	timeout      time.Duration
	concurrency  int
	maxDepth     int
	excludes     []string
	outputFormat string
	noTUI        bool
	tempPodImage string
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

	usageCmd.Flags().BoolVarP(&force, "force", "f", false, "Auto-create temp pod, skip confirmation")
	usageCmd.Flags().DurationVarP(&timeout, "timeout", "t", 30*time.Second, "Timeout for temp pod creation + scan")
	usageCmd.Flags().IntVarP(&concurrency, "concurrency", "c", 3, "Max parallel PVC scans")
	usageCmd.Flags().IntVarP(&maxDepth, "max-depth", "d", 0, "Directory depth for scanner (0 = unlimited)")
	usageCmd.Flags().StringSliceVarP(&excludes, "exclude", "e", nil, "Paths to exclude from scan (repeatable)")
	usageCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "Output format (table, json, yaml, wide)")
	usageCmd.Flags().BoolVar(&noTUI, "no-tui", false, "Skip Bubble Tea UI, print final table directly")
	usageCmd.Flags().StringVarP(&tempPodImage, "image", "i", "alpine:latest", "Image for temp pods")
	usageCmd.Flags().SortFlags = false
}

func runUsage(cmd *cobra.Command, args []string) error {
	pvcName := ""
	if len(args) == 1 {
		pvcName = args[0]
	} else if len(args) == 2 {
		if args[0] != "pvc" {
			return fmt.Errorf("usage: pvdu usage [pvc <name>]")
		}
		pvcName = args[1]
	}

	clientset, config, err := k8s.BuildClient(kubeconfig, ctx)
	if err != nil {
		return fmt.Errorf("build k8s client: %w", err)
	}

	ns := namespace
	if allNamespaces {
		ns = ""
	}
	if ns == "" && !allNamespaces && pvcName == "" {
		ns = "default"
	}

	ctxBg := context.Background()
	var pvcs []k8s.PVCInfo

	if pvcName != "" {
		targetNs := ns
		if targetNs == "" {
			targetNs = "default"
		}
		info, err := k8s.GetPVCByName(ctxBg, clientset, targetNs, pvcName)
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

	if noTUI {
		return runHeadless(ctxBg, clientset, config, results, pvcs)
	}

	p := tea.NewProgram(ui.InitialModel(results, ns, allNamespaces))
	go func() {
		p.Send(model.DiscoveredMsg{Count: len(results)})
		scanAll(ctxBg, clientset, config, results, pvcs, p)
		p.Send(model.ScanDoneMsg{})
	}()

	if _, err := p.Run(); err != nil {
		return err
	}

	return nil
}

func runHeadless(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, results []*model.ScanResult, pvcs []k8s.PVCInfo) error {
	scanAll(ctx, clientset, config, results, pvcs, nil)
	fmt.Println(ui.RenderTable(results))
	return nil
}

type scanAllFn func()

func scanAll(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, results []*model.ScanResult, pvcs []k8s.PVCInfo, program *tea.Program) {
	sem := make(chan struct{}, concurrency)

	for i, pvc := range pvcs {
		sem <- struct{}{}
		go func(idx int, pvcInfo k8s.PVCInfo) {
			defer func() { <-sem }()
			scanPVC(ctx, clientset, config, results[idx], pvcInfo, program)
		}(i, pvc)
	}

	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}
}

func scanPVC(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, result *model.ScanResult, pvcInfo k8s.PVCInfo, program *tea.Program) {
	result.Status = model.StatusScanning
	sendProgress(program, result.PVCName, "", 0, false, "")

	if len(pvcInfo.Mounts) > 0 {
		mount := pvcInfo.Mounts[0]
		sr, err := k8s.UploadAndScanPVC(ctx, clientset, config, pvcInfo.Namespace, mount.PodName, mount.MountPath, &pvcInfo, maxDepth, excludes)
		if err != nil {
			result.Status = model.StatusError
			result.Error = err.Error()
			sendProgress(program, result.PVCName, "", 0, true, err.Error())
			return
		}
		result.UsedBytes = sr.UsedBytes
		result.Status = sr.Status
		if sr.Status == model.StatusError {
			sendProgress(program, result.PVCName, "", 0, true, sr.Error)
			return
		}
		sendProgress(program, result.PVCName, "", result.UsedBytes, true, "")
		return
	}

	if force {
		podName, err := k8s.CreateTempPod(ctx, clientset, pvcInfo.Namespace, pvcInfo.Name, tempPodImage)
		if err != nil {
			result.Status = model.StatusError
			result.Error = fmt.Sprintf("create temp pod: %v", err)
			sendProgress(program, result.PVCName, "", 0, true, result.Error)
			return
		}

		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		err = k8s.WaitForPodReady(waitCtx, clientset, pvcInfo.Namespace, podName, timeout)
		cancel()
		if err != nil {
			k8s.DeletePod(context.Background(), clientset, pvcInfo.Namespace, podName)
			result.Status = model.StatusError
			result.Error = fmt.Sprintf("wait pod: %v", err)
			sendProgress(program, result.PVCName, "", 0, true, result.Error)
			return
		}

		sr, err := k8s.UploadAndScanPVC(ctx, clientset, config, pvcInfo.Namespace, podName, "/mnt", &pvcInfo, maxDepth, excludes)
		k8s.DeletePod(context.Background(), clientset, pvcInfo.Namespace, podName)

		if err != nil {
			result.Status = model.StatusError
			result.Error = err.Error()
			sendProgress(program, result.PVCName, "", 0, true, err.Error())
			return
		}
		result.UsedBytes = sr.UsedBytes
		result.Status = sr.Status
		if sr.Status == model.StatusError {
			sendProgress(program, result.PVCName, "", 0, true, sr.Error)
			return
		}
		sendProgress(program, result.PVCName, "", result.UsedBytes, true, "")
		return
	}

	result.Status = model.StatusSkipped
	result.Error = "not mounted (use --force to auto-create temp pod)"
	sendProgress(program, result.PVCName, "", 0, true, result.Error)
}

func sendProgress(program *tea.Program, pvcName, path string, size int64, done bool, errStr string) {
	if program == nil {
		return
	}
	program.Send(model.ProgressUpdate{
		PVCName: pvcName,
		Path:    path,
		Size:    size,
		Done:    done,
		Err:     errStr,
	})
}
