package cmd

import (
	"flag"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var (
	kubeconfig    string
	ctx           string
	namespace     string
	allNamespaces bool
	logLevel      string
	pvcName       string
)

var rootCmd = &cobra.Command{
	Use:   "pvdu [pvc <name>]",
	Short: "PVC Disk Usage — real storage usage of Kubernetes PVCs",
	Long: `pvdu compares PVC requested size, PV capacity, and actual filesystem usage (du)
to detect thin provisioning, deleted-but-open files, or mismatches.

Scans PVC mountpoints using a parallel, depth-aware file walker.`,
	Args: cobra.MaximumNArgs(2),
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		level := slog.LevelError + 1 // suppress all by default
		switch logLevel {
		case "debug":
			level = slog.LevelDebug
		case "info":
			level = slog.LevelInfo
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
		handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
		slog.SetDefault(slog.New(handler))
		return nil
	},
	RunE: runUsage,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	klog.InitFlags(nil)
	if fl := flag.Lookup("logtostderr"); fl != nil {
		fl.Value.Set("false")
	}
	if fl := flag.Lookup("stderrthreshold"); fl != nil {
		fl.Value.Set("FATAL")
	}

	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig file (default ~/.kube/config)")
	rootCmd.PersistentFlags().StringVar(&ctx, "context", "", "Kubernetes context to use")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "Kubernetes namespace")
	rootCmd.PersistentFlags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "Scan all namespaces")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "", "Log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().StringVarP(&pvcName, "pvc", "p", "", "PVC name to scan")
}
