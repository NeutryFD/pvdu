package cmd

import (
	"github.com/spf13/cobra"
)

var (
	kubeconfig    string
	ctx           string
	namespace     string
	allNamespaces bool
	logLevel      string
)

var rootCmd = &cobra.Command{
	Use:   "pvdu",
	Short: "PVC Disk Usage — real storage usage of Kubernetes PVCs",
	Long: `pvdu compares PVC requested size, PV capacity, and actual filesystem usage (du)
to detect thin provisioning, deleted-but-open files, or mismatches.

Scans PVC mountpoints using a parallel, depth-aware file walker.`,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig file (default ~/.kube/config)")
	rootCmd.PersistentFlags().StringVar(&ctx, "context", "", "Kubernetes context to use")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "Kubernetes namespace")
	rootCmd.PersistentFlags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "Scan all namespaces")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "warn", "Log level (debug, info, warn, error)")
}
