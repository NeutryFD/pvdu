package scanner

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// Binaries is initialized once at startup, keyed by Linux architecture.
var Binaries map[string][]byte

// UploadError indicates failure before the scanner starts, not a scan failure.
type UploadError struct{ Err error }

func (e *UploadError) Error() string { return "upload scanner: " + e.Err.Error() }
func (e *UploadError) Unwrap() error { return e.Err }

func UploadAndScan(ctx context.Context, client kubernetes.Interface, config *rest.Config, namespace, podName, containerName, writableDir, mountPath string, pvc *kube.PVCInfo, maxDepth int, excludes []string, workers int) (*model.ScanResult, error) {
	argv, err := ExecCommand(writableDir+"/.pvdu-scanner", mountPath, maxDepth, excludes, workers)
	if err != nil {
		return nil, err
	}
	gzipArgv, err := CompressedExecCommand(writableDir+"/.pvdu-scanner", mountPath, maxDepth, excludes, workers)
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
	binary, err := ForPlatform(node.Status.NodeInfo.OperatingSystem, node.Status.NodeInfo.Architecture)
	if err != nil {
		return nil, err
	}
	scan := func(argv []string, payload []byte) (int64, string, error) {
		executor, err := podExecutor(client, config, namespace, podName, containerName, argv)
		if err != nil {
			return 0, "", err
		}
		NotifyProgress(ctx, "", fmt.Sprintf("exec %s/%s container=%s argv=%q", namespace, podName, containerName, argv))
		NotifyProgress(ctx, model.StatusUploading, "")
		return Stream(ctx, func(ctx context.Context, out, diagnostic io.Writer) error {
			return executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: bytes.NewReader(payload), Stdout: out, Stderr: diagnostic})
		})
	}
	// Exec streams are slow on some clusters, so send the scanner compressed
	// and fall back to the plain binary only where the pod lacks gunzip.
	used, diagnostic, err := scan(gzipArgv, compressedScanner(node.Status.NodeInfo.Architecture, binary))
	if exitStatus(err) == exitNoGunzip {
		used, diagnostic, err = scan(argv, binary)
	}
	if err != nil {
		diagnostic = strings.Join(strings.Fields(diagnostic), " ")
		cause := err
		if diagnostic != "" {
			cause = fmt.Errorf("%w (stderr: %s)", err, diagnostic)
		}
		if exitStatus(err) == 125 {
			return nil, &UploadError{Err: cause}
		}
		return nil, fmt.Errorf("scan: %w", cause)
	}
	return &model.ScanResult{Namespace: pvc.Namespace, PVCName: pvc.Name, PodName: podName, ScanPath: mountPath, RequestedBytes: pvc.RequestedBytes, RequestedStr: pvc.RequestedStr, PVBytes: pvc.PVBytes, UsedBytes: used, Status: model.StatusDone}, nil
}

// exitNoGunzip is the remote exit status when a compressed upload cannot be
// decompressed; the shell exits before reading stdin.
const exitNoGunzip = 124

func exitStatus(err error) int {
	var exit interface{ ExitStatus() int }
	if errors.As(err, &exit) {
		return exit.ExitStatus()
	}
	return -1
}

var compressedScanners sync.Map // arch -> func() []byte

// compressedScanner gzips each architecture's scanner once, on first use.
func compressedScanner(arch string, binary []byte) []byte {
	compress, _ := compressedScanners.LoadOrStore(arch, sync.OnceValue(func() []byte {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, _ = w.Write(binary)
		_ = w.Close()
		return buf.Bytes()
	}))
	return compress.(func() []byte)()
}

func ForPlatform(os, arch string) ([]byte, error) {
	if os != "linux" {
		return nil, fmt.Errorf("unsupported pod OS %q: Linux required", os)
	}
	binary := Binaries[arch]
	if len(binary) == 0 {
		return nil, fmt.Errorf("no embedded scanner for linux/%s (supported: amd64, arm64; run make build)", arch)
	}
	return binary, nil
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
