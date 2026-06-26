package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/scanner"
)

var ScannerBinary []byte

type scanLine struct {
	Type  string `json:"type"`
	Path  string `json:"path,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Human string `json:"human,omitempty"`
}

func UploadAndScanPVC(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, namespace, podName, mountPath string, pvcInfo *PVCInfo, maxDepth int, excludes []string) (*model.ScanResult, error) {
	result := &model.ScanResult{
		Namespace:      pvcInfo.Namespace,
		PVCName:        pvcInfo.Name,
		RequestedBytes: pvcInfo.RequestedBytes,
		RequestedStr:   pvcInfo.RequestedStr,
		PVBytes:        pvcInfo.PVBytes,
		Status:         model.StatusScanning,
	}

	if len(ScannerBinary) == 0 {
		return nil, fmt.Errorf("scanner binary not embedded (run make build)")
	}

	excludeStr := ""
	for i, e := range excludes {
		if i > 0 {
			excludeStr += ","
		}
		excludeStr += e
	}

	depthStr := fmt.Sprintf("%d", maxDepth)

	execCmd := []string{"sh", "-c",
		fmt.Sprintf("cat > /tmp/pvdu-scanner && chmod +x /tmp/pvdu-scanner && /tmp/pvdu-scanner --path=%s --max-depth=%s --exclude=%s",
			mountPath, depthStr, excludeStr),
	}

	stdout, stderr, err := ExecInPodStream(ctx, clientset, config, namespace, podName, "", execCmd, bytes.NewReader(ScannerBinary))
	if err != nil {
		result.Status = model.StatusError
		result.Error = fmt.Sprintf("exec error: %v (stderr: %s)", err, stderr)
		return result, nil
	}

	used, err := parseScannerOutput(stdout)
	if err != nil {
		result.Status = model.StatusError
		result.Error = fmt.Sprintf("parse error: %v", err)
		return result, nil
	}

	result.UsedBytes = used
	result.Status = model.StatusDone
	return result, nil
}

func ExecInPodStream(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, namespace, podName, container string, cmd []string, stdin io.Reader) (string, string, error) {
	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec").
		Param("container", container).
		VersionedParams(&corev1.PodExecOptions{
			Command: cmd,
			Stdin:   true,
			Stdout:  true,
			Stderr:  true,
			TTY:     false,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	return stdout.String(), stderr.String(), err
}

func parseScannerOutput(output string) (int64, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(output)))
	var total int64
	for {
		var line scanLine
		err := dec.Decode(&line)
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		if line.Type == "done" {
			total = line.Size
		}
	}
	return total, nil
}

func ParseScannerProgress(output string, progressFn scanner.ProgressFn) int64 {
	dec := json.NewDecoder(bytes.NewReader([]byte(output)))
	var total int64
	for {
		var line scanLine
		err := dec.Decode(&line)
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		if line.Type == "done" {
			total = line.Size
		} else if line.Type == "progress" && progressFn != nil {
			progressFn(line.Path, line.Size)
		}
	}
	return total
}
