package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func (r *runner) runTUI(cmd *cobra.Command) error {
	o := r.options
	in, inFile := cmd.InOrStdin().(*os.File)
	out, outFile := cmd.OutOrStdout().(*os.File)
	if !inFile || !outFile || !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return fmt.Errorf("--tui needs an interactive terminal on stdin and stdout")
	}
	// The TUI turns SIGINT into its own graceful quit, so only SIGTERM cancels
	// the context here.
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM)
	defer stop()
	client, config, err := r.deps.BuildClient(o.kubeconfig, o.context)
	if err != nil {
		return fmt.Errorf("build k8s client: %w", err)
	}
	ns := o.namespace
	if o.allNamespaces {
		ns = ""
	}
	return tui.Run(ctx, tui.Config{
		Engine: r.newEngine(client, config), Namespace: ns, Context: kube.ContextName(o.kubeconfig, o.context),
		PVC: o.pvc, ListTimeout: o.timeout, LogLevel: o.logLevel, Theme: o.theme,
	}, in, out)
}
