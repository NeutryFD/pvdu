package pvdu_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/scanner"
	"k8s.io/klog/v2"
)

func TestScannerShellSignalsUploadCompletion(t *testing.T) {
	argv, err := scanner.ExecCommand(filepath.Join(t.TempDir(), "scanner"), "/data", 0, nil, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
	command.Stdin = strings.NewReader("#!/bin/sh\nprintf '%s\\n' '{\"type\":\"done\",\"size\":42}'\n")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("shell: %v: %s", err, output)
	}
	if !bytes.HasPrefix(output, []byte("{\"type\":\"scan-start\"}\n")) {
		t.Fatalf("missing start event before scanner results: %s", output)
	}
	if size, err := scanner.ParseOutput(bytes.NewReader(output), nil); err != nil || size != 42 {
		t.Fatalf("shell output: bytes=%d err=%v", size, err)
	}
	// A failed upload must not announce the start of a scan.
	argv[4] = "/dev/null/scanner"
	command = exec.CommandContext(t.Context(), argv[0], argv[1:]...)
	output, err = command.CombinedOutput()
	if err == nil || bytes.Contains(output, []byte("scan-start")) {
		t.Fatalf("failed upload announced scanning: %v: %s", err, output)
	}
}

func TestScannerStartEventPrecedesResults(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	ctx = scanner.WithObserver(ctx, func(status model.ScanStatus, _ string) {
		if status == model.StatusScanning {
			close(started)
		}
	})
	used, _, err := scanner.Stream(ctx, func(ctx context.Context, out, _ io.Writer) error {
		// Fragment the event as a network stream can; only the complete record
		// should change state, before any directory results are available.
		if _, err := io.WriteString(out, `{"type":"scan-`); err != nil {
			return err
		}
		select {
		case <-started:
			return errors.New("scan started before a complete event")
		default:
		}
		if _, err := io.WriteString(out, "start\"}\n"); err != nil {
			return err
		}
		select {
		case <-started:
		case <-ctx.Done():
			return ctx.Err()
		}
		_, err := io.WriteString(out, "{\"type\":\"done\",\"size\":42}\n")
		return err
	})
	if err != nil || used != 42 {
		t.Fatalf("got %d bytes, error %v", used, err)
	}
}

func TestScannerStartEventValidation(t *testing.T) {
	for _, tc := range []struct {
		name, input string
	}{
		{"duplicate", "{\"type\":\"scan-start\"}\n{\"type\":\"scan-start\"}\n{\"type\":\"done\",\"size\":0}\n"},
		{"late", "{\"type\":\"dir\",\"path\":\"/data\"}\n{\"type\":\"scan-start\"}\n{\"type\":\"done\",\"size\":0}\n"},
		{"missing completion", "{\"type\":\"scan-start\"}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := scanner.ParseOutput(strings.NewReader(tc.input), nil); err == nil {
				t.Fatal("invalid event sequence accepted")
			}
		})
	}
}

func TestScannerDiagnosticsCaptureLibraryLogs(t *testing.T) {
	want := errors.New("closed network connection")
	_, diagnostic, err := scanner.Stream(t.Context(), func(ctx context.Context, out, stderr io.Writer) error {
		klog.FromContext(ctx).Error(want, "Copying stdin failed")
		_, _ = io.WriteString(stderr, "mktemp: Read-only file system\n")
		return want
	})
	if !errors.Is(err, want) || !strings.Contains(diagnostic, "Copying stdin failed") || !strings.Contains(diagnostic, "Read-only file system") {
		t.Fatalf("lost exec diagnostic: %q, %v", diagnostic, err)
	}
}

func TestScannerDiagnosticsPreserveParseErrorAfterCancellation(t *testing.T) {
	_, _, err := scanner.Stream(t.Context(), func(ctx context.Context, out, stderr io.Writer) error {
		if _, err := fmt.Fprintln(out, `{invalid`); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "decode scanner record") {
		t.Fatalf("expected original parse error, got %v", err)
	}
}

func TestScannerDiagnosticsRetainTransportError(t *testing.T) {
	want := errors.New("connection lost")
	_, _, err := scanner.Stream(t.Context(), func(context.Context, io.Writer, io.Writer) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("lost transport cause: %v", err)
	}
}

func TestScannerAcceptsEmptyDirectoryWithoutSize(t *testing.T) {
	input := "{\"type\":\"dir\",\"path\":\"/empty\"}\n{\"type\":\"done\",\"size\":0}\n"
	got, err := scanner.ParseOutput(strings.NewReader(input), nil)
	if err != nil || got != 0 {
		t.Fatalf("got %d, error %v", got, err)
	}
	_, err = scanner.ParseOutput(strings.NewReader(`{"type":"done"}`), nil)
	if err == nil {
		t.Fatal("missing completion size accepted")
	}
}

func TestScannerShellDecompressesUpload(t *testing.T) {
	if _, err := exec.LookPath("gunzip"); err != nil {
		t.Skip("gunzip required")
	}
	argv, err := scanner.CompressedExecCommand(filepath.Join(t.TempDir(), "scanner"), "/data", 0, nil, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	var payload bytes.Buffer
	gz := gzip.NewWriter(&payload)
	_, _ = io.WriteString(gz, "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"done\",\"size\":42}'\n")
	_ = gz.Close()
	command := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
	command.Stdin = &payload
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("shell: %v: %s", err, output)
	}
	if size, err := scanner.ParseOutput(bytes.NewReader(output), nil); err != nil || size != 42 {
		t.Fatalf("shell output: bytes=%d err=%v: %s", size, err, output)
	}
}

func TestScannerShellWithoutGunzipRequestsPlainUpload(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	argv, err := scanner.CompressedExecCommand(filepath.Join(t.TempDir(), "scanner"), "/data", 0, nil, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), sh, argv[1:]...)
	command.Env = []string{"PATH=" + t.TempDir()}
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 124 || len(output) != 0 {
		t.Fatalf("want silent exit 124 without gunzip, got %v: %s", err, output)
	}
}
