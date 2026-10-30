package scanner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/NeutryFD/dirwalker"
	"github.com/go-logr/logr"
	"github.com/neutry/pvdu/internal/model"
	"k8s.io/klog/v2"
)

type scanLine struct {
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
	Size *int64 `json:"size"`
}

type boundedDiagnostic struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *boundedDiagnostic) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (b *boundedDiagnostic) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := max(0, 16*1024-b.buffer.Len())
	_, _ = b.buffer.Write(p[:min(n, remaining)])
	return n, nil
}

// Stream joins the producer before returning, including when parsing fails.
// Stream must obey cancellation, as remotecommand.StreamWithContext does.
func Stream(ctx context.Context, stream func(context.Context, io.Writer, io.Writer) error) (int64, string, error) {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r, w := io.Pipe()
	done := make(chan error, 1)
	var diagnostic boundedDiagnostic
	ctx = klog.NewContext(ctx, logr.FromSlogHandler(slog.NewTextHandler(&diagnostic, &slog.HandlerOptions{Level: slog.LevelWarn})))
	go func() {
		err := stream(ctx, w, &diagnostic)
		_ = w.CloseWithError(err)
		done <- err
	}()
	used, parseErr := parseOutput(r, nil, func() {
		NotifyProgress(ctx, model.StatusScanning, "")
	})
	_ = r.CloseWithError(parseErr)
	if parseErr != nil {
		cancel()
	}
	execErr := <-done
	if parent.Err() != nil {
		return 0, diagnostic.String(), parent.Err()
	}
	if parseErr != nil && execErr != nil && !errors.Is(parseErr, execErr) {
		if errors.Is(execErr, context.Canceled) || errors.Is(execErr, io.ErrClosedPipe) {
			return 0, diagnostic.String(), parseErr
		}
		return 0, diagnostic.String(), errors.Join(parseErr, execErr)
	}
	if execErr != nil {
		return 0, diagnostic.String(), execErr
	}
	return used, diagnostic.String(), parseErr
}

// ParseOutput requires one terminal done record and bounds each record.
func ParseOutput(r io.Reader, progress dirwalker.ProgressFn) (int64, error) {
	return parseOutput(r, progress, nil)
}

func parseOutput(r io.Reader, progress dirwalker.ProgressFn, started func()) (int64, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var total int64
	done := false
	seenRecord := false
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		if done {
			return 0, fmt.Errorf("unexpected record after scanner completion")
		}
		var line scanLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return 0, fmt.Errorf("decode scanner record: %w", err)
		}
		if line.Size != nil && *line.Size < 0 {
			return 0, fmt.Errorf("scanner %s record has negative size", line.Type)
		}
		if line.Type == "scan-start" {
			if seenRecord {
				return 0, fmt.Errorf("unexpected scanner start record")
			}
			seenRecord = true
			if started != nil {
				started()
			}
			continue
		}
		seenRecord = true
		switch line.Type {
		case "done":
			if line.Size == nil {
				return 0, fmt.Errorf("scanner completion record is missing size")
			}
			total, done = *line.Size, true
		case "file", "dir", "progress":
			if line.Path == "" {
				return 0, fmt.Errorf("scanner record requires a path")
			}
			if progress != nil {
				var size int64
				if line.Size != nil {
					size = *line.Size
				}
				progress(line.Path, size, line.Type != "file")
			}
		default:
			return 0, fmt.Errorf("unknown scanner record type %q", line.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read scanner output: %w", err)
	}
	if !done {
		return 0, fmt.Errorf("scanner output has no completion record")
	}
	return total, nil
}

func ParseProgress(output string, progress dirwalker.ProgressFn) (int64, error) {
	return ParseOutput(strings.NewReader(output), progress)
}
