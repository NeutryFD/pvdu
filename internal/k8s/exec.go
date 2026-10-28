package k8s

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/NeutryFD/dirwalker"
	"github.com/neutry/pvdu/internal/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// ScannerBinaries is initialized once at startup, keyed by Linux architecture.
var ScannerBinaries map[string][]byte

type scanLine struct {
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
	Size *int64 `json:"size"`
}

// UploadError indicates failure before the scanner starts, not a scan failure.
type UploadError struct{ Err error }

func (e *UploadError) Error() string { return "upload scanner: " + e.Err.Error() }
func (e *UploadError) Unwrap() error { return e.Err }

func UploadAndScanPVC(ctx context.Context, client kubernetes.Interface, config *rest.Config, namespace, podName, containerName, writableDir, mountPath string, pvc *PVCInfo, maxDepth int, excludes []string, workers int, reportFiles bool) (*model.ScanResult, error) {
	if reportFiles {
		return nil, fmt.Errorf("file-level output is not supported; omit --files")
	}
	argv, err := ScannerExecCommand(writableDir+"/.pvdu-scanner", mountPath, maxDepth, excludes, workers, false)
	if err != nil {
		return nil, err
	}
	pod, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("resolve scan pod: %w", err)
	}
	node, err := client.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("resolve scanner architecture: %w", err)
	}
	binary, err := ScannerForPlatform(node.Status.NodeInfo.OperatingSystem, node.Status.NodeInfo.Architecture)
	if err != nil {
		return nil, err
	}
	executor, err := podExecutor(client, config, namespace, podName, containerName, argv)
	if err != nil {
		return nil, err
	}
	used, diagnostic, err := ScanStream(ctx, func(ctx context.Context, out, diagnostic io.Writer) error {
		return executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: bytes.NewReader(binary), Stdout: out, Stderr: diagnostic})
	})
	if err != nil {
		var exit interface{ ExitStatus() int }
		if errors.As(err, &exit) && exit.ExitStatus() == 125 {
			return nil, &UploadError{Err: fmt.Errorf("%w (%s)", err, diagnostic)}
		}
		return nil, fmt.Errorf("scan: %w (stderr: %s)", err, diagnostic)
	}
	return &model.ScanResult{Namespace: pvc.Namespace, PVCName: pvc.Name, PodName: podName, ScanPath: mountPath, RequestedBytes: pvc.RequestedBytes, RequestedStr: pvc.RequestedStr, PVBytes: pvc.PVBytes, UsedBytes: used, Status: model.StatusDone}, nil
}

func ScannerForPlatform(os, arch string) ([]byte, error) {
	if os != "linux" {
		return nil, fmt.Errorf("unsupported pod OS %q: Linux required", os)
	}
	binary := ScannerBinaries[arch]
	if len(binary) == 0 {
		return nil, fmt.Errorf("no embedded scanner for linux/%s (supported: amd64, arm64; run make build)", arch)
	}
	return binary, nil
}

// ScannerExecCommand never interpolates caller values into shell source.
// The path argument selects scratch space; its basename is never overwritten.
func ScannerExecCommand(scannerPath, mountPath string, maxDepth int, excludes []string, workers int, reportFiles bool) ([]string, error) {
	if maxDepth < 0 || workers < 0 {
		return nil, fmt.Errorf("max-depth and workers must be nonnegative")
	}
	for _, exclude := range excludes {
		if strings.ContainsAny(exclude, ",\n") {
			return nil, fmt.Errorf("exclude pattern %q contains a comma or newline", exclude)
		}
	}
	excludeStr := strings.Join(append(append([]string{}, excludes...), ".pvdu-scan.*"), ",")
	files := ""
	if reportFiles {
		files = "--files"
	}
	script := `umask 077
dir=$(mktemp -d "${1%/*}/.pvdu-scan.XXXXXX") || exit 125
trap 'rm -rf "$dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cat > "$dir/scanner" && chmod 700 "$dir/scanner" || exit 125
"$dir/scanner" "$2" --max-depth="$3" --exclude="$4" --workers="$5" --output=json-lines $6`
	return []string{"sh", "-c", script, "pvdu-sh", scannerPath, mountPath, fmt.Sprint(maxDepth), excludeStr, fmt.Sprint(workers), files}, nil
}

func podExecutor(client kubernetes.Interface, config *rest.Config, namespace, pod, container string, argv []string) (remotecommand.Executor, error) {
	req := client.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").Param("container", container).
		VersionedParams(&corev1.PodExecOptions{Command: argv, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("create executor: %w", err)
	}
	return executor, nil
}

type boundedDiagnostic struct{ buffer bytes.Buffer }

func (b *boundedDiagnostic) String() string { return b.buffer.String() }

func (b *boundedDiagnostic) Write(p []byte) (int, error) {
	n := len(p)
	remaining := max(0, 16*1024-b.buffer.Len())
	_, _ = b.buffer.Write(p[:min(n, remaining)])
	return n, nil
}

// ScanStream joins the producer before returning, including when parsing fails.
// Stream must obey cancellation, as remotecommand.StreamWithContext does.
func ScanStream(ctx context.Context, stream func(context.Context, io.Writer, io.Writer) error) (int64, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r, w := io.Pipe()
	done := make(chan error, 1)
	var diagnostic boundedDiagnostic
	go func() {
		err := stream(ctx, w, &diagnostic)
		_ = w.CloseWithError(err)
		done <- err
	}()
	used, parseErr := ParseScannerOutput(r, nil)
	_ = r.CloseWithError(parseErr)
	if parseErr != nil {
		cancel()
	}
	execErr := <-done
	if execErr != nil {
		return 0, diagnostic.String(), execErr
	}
	return used, diagnostic.String(), parseErr
}

// ParseScannerOutput requires one terminal done record and bounds each record.
func ParseScannerOutput(r io.Reader, progress dirwalker.ProgressFn) (int64, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var total int64
	done := false
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
		if line.Size == nil || *line.Size < 0 {
			return 0, fmt.Errorf("scanner record requires a nonnegative size")
		}
		switch line.Type {
		case "done":
			total, done = *line.Size, true
		case "file", "dir", "progress":
			if line.Path == "" {
				return 0, fmt.Errorf("scanner record requires a path")
			}
			if progress != nil {
				progress(line.Path, *line.Size, line.Type != "file")
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

func ParseScannerProgress(output string, progress dirwalker.ProgressFn) (int64, error) {
	return ParseScannerOutput(strings.NewReader(output), progress)
}
