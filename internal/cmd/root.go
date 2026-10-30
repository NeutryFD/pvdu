package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var version = "dev"

type options struct {
	kubeconfig, context, namespace, pvc, output, image, logLevel string
	allNamespaces, force                                         bool
	timeout, cleanupTimeout                                      time.Duration
	concurrency, maxDepth, workers                               int
	excludes                                                     []string
}

func defaultOptions() options {
	return options{namespace: "default", output: "default", image: "alpine:3.23", logLevel: "warn", timeout: 120 * time.Second, cleanupTimeout: 10 * time.Second, concurrency: 3}
}

// NewCommand creates independent options, dependencies, and output state.
func NewCommand(deps Dependencies) *cobra.Command {
	r := newRunner(deps)
	o := &r.options
	c := &cobra.Command{
		Use: "pvdu [pvc <name>]", Short: "PVC Disk Usage — apparent file sizes of Kubernetes PVCs",
		Long:    "Compare requested and PV capacity with apparent file sizes from a directory walk. Does not measure allocated blocks or deleted-but-open files.",
		Version: version, SilenceUsage: true,
		Args: func(c *cobra.Command, args []string) error {
			if len(args) > 2 || (len(args) == 2 && args[0] != "pvc") {
				return fmt.Errorf("expected no arguments, <name>, or pvc <name>")
			}
			if len(args) > 0 && o.pvc != "" {
				return fmt.Errorf("use either --pvc or a positional PVC name")
			}
			if len(args) > 0 {
				o.pvc = args[len(args)-1]
			}
			return nil
		},
		PreRunE: func(c *cobra.Command, _ []string) error { return o.validate(c.Flags().Changed("namespace")) },
		RunE:    func(c *cobra.Command, _ []string) error { return r.run(c) },
	}
	f := c.Flags()
	f.StringVar(&o.kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	f.StringVar(&o.context, "context", "", "Kubernetes context")
	f.StringVarP(&o.namespace, "namespace", "n", o.namespace, "Kubernetes namespace")
	f.BoolVarP(&o.allNamespaces, "all-namespaces", "A", false, "Scan all namespaces")
	f.StringVarP(&o.pvc, "pvc", "p", "", "PVC name (requires a single namespace)")
	f.BoolVarP(&o.force, "force", "f", false, "Use a temporary pod if an existing whole-volume mount scan fails")
	f.DurationVarP(&o.timeout, "timeout", "t", o.timeout, "Budget for each scan attempt; forced fallback gets a fresh budget")
	f.DurationVar(&o.cleanupTimeout, "cleanup-timeout", o.cleanupTimeout, "Independent temporary-pod cleanup budget")
	f.IntVarP(&o.concurrency, "concurrency", "c", o.concurrency, "Maximum concurrent PVC scans")
	f.IntVarP(&o.maxDepth, "max-depth", "d", 0, "Directory depth (0 = unlimited)")
	f.IntVarP(&o.workers, "workers", "w", 0, "Scanner workers (0 = auto)")
	f.StringSliceVarP(&o.excludes, "exclude", "e", nil, "Paths excluded from totals (repeatable)")
	f.StringVarP(&o.output, "output", "o", o.output, "Output: default, table, json, yaml")
	f.StringVarP(&o.image, "image", "i", o.image, "Temporary pod image with sh, mktemp, cat, chmod, rm")
	f.StringVar(&o.logLevel, "log-level", o.logLevel, "Log level: debug, info, warn, error")
	return c
}

func (o options) validate(explicitNamespace bool) error {
	if o.concurrency <= 0 {
		return fmt.Errorf("--concurrency must be positive")
	}
	if o.timeout <= 0 || o.cleanupTimeout <= 0 {
		return fmt.Errorf("--timeout and --cleanup-timeout must be positive")
	}
	if o.maxDepth < 0 || o.workers < 0 {
		return fmt.Errorf("--max-depth and --workers must be nonnegative")
	}
	if o.allNamespaces && (o.pvc != "" || explicitNamespace) {
		return fmt.Errorf("--all-namespaces cannot be combined with a PVC selector or --namespace")
	}
	if o.namespace == "" && !o.allNamespaces {
		return fmt.Errorf("--namespace cannot be empty")
	}
	if o.image == "" {
		return fmt.Errorf("--image cannot be empty")
	}
	switch o.output {
	case "default", "table", "json", "yaml":
	default:
		return fmt.Errorf("invalid output format %q", o.output)
	}
	switch o.logLevel {
	case "", "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log level %q", o.logLevel)
	}
	return nil
}

func Execute() error { return NewCommand(Dependencies{}).Execute() }
