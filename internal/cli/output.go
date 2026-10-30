package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/neutry/pvdu/internal/model"
)

type synchronizedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

func (r *runner) fail(result *model.ScanResult, ctx context.Context, err error) {
	message := err.Error()
	if ctx.Err() == context.Canceled {
		message = "interrupted"
	} else if ctx.Err() == context.DeadlineExceeded {
		message = fmt.Sprintf("timed out after %s (try --timeout/-t)", r.options.timeout)
	}
	result.Status, result.Error = model.StatusError, message
}

func (r *runner) messageAt(level slog.Level, message string) {
	if r.liveTable != nil {
		return
	}
	minimum := slog.LevelWarn
	switch r.options.logLevel {
	case "debug":
		minimum = slog.LevelDebug
	case "info":
		minimum = slog.LevelInfo
	case "error":
		minimum = slog.LevelError
	}
	if level < minimum {
		return
	}
	if r.terminal {
		message = "\r\033[2K" + message
	}
	fmt.Fprintln(r.errOut, message)
}
