// Package tui is the interactive --tui mode: list the PVCs of a namespace,
// pick some, scan them, and inspect the results.
package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/go-logr/logr"
	"github.com/neutry/pvdu/internal/engine"
	"k8s.io/klog/v2"
)

// Config is the starting state of the TUI.
type Config struct {
	Engine *engine.Engine
	// Namespace is the namespace listed first; empty lists every namespace.
	Namespace string
	// Context is the Kubernetes context shown in the header.
	Context string
	// PVC, when set, is selected once the first list arrives.
	PVC string
	// ListTimeout bounds each PVC list request.
	ListTimeout time.Duration
	// LogLevel is the lowest level the log view keeps: debug, info, warn
	// (the default) or error.
	LogLevel string
	// Theme names the colour theme; see ThemeNames. Empty or unknown means
	// DefaultTheme.
	Theme string
}

// Run shows the TUI until the user quits or ctx ends, then waits for running
// scans to stop so no temporary pod is left behind.
func Run(ctx context.Context, cfg Config, in io.Reader, out io.Writer) error {
	m := New(ctx, cfg)
	defer m.Close()
	// Client-go logs through the global klog logger; on stderr it would tear
	// the screen, so it goes to the log view instead.
	klog.SetLogger(logr.FromSlogHandler(slog.NewTextHandler(lineWriter(m.Logf), &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer klog.ClearLogger()
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithFilter(filter))
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		err = ctx.Err()
	}
	return err
}

// filter turns SIGINT into the same graceful quit as the q key, which cancels
// scans and waits for their cleanup before exiting.
func filter(_ tea.Model, msg tea.Msg) tea.Msg {
	if _, ok := msg.(tea.InterruptMsg); ok {
		return quitMsg{}
	}
	return msg
}

// lineWriter passes each written line to logf.
type lineWriter func(format string, args ...any)

func (w lineWriter) Write(p []byte) (int, error) {
	for line := range strings.SplitSeq(string(bytes.TrimRight(p, "\n")), "\n") {
		w("%s", line)
	}
	return len(p), nil
}
