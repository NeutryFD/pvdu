package cli

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
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
