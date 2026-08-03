package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/NeutryFD/dirwalker"
	"github.com/neutry/pvdu/internal/model"
)

var ScannerBinary []byte

type scanLine struct {
	Type  string `json:"type"`
	Path  string `json:"path,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Human string `json:"human,omitempty"`
}

func UploadAndScanPVC(ctx context.Context, clientset kubernetes.Interface, config *rest.Config, namespace, podName, containerName, writableDir, mountPath string, pvcInfo *PVCInfo, maxDepth int, excludes []string, workers int, reportFiles bool) (*model.ScanResult, error) {
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

	scannerPath := writableDir + "/.pvdu-scanner"

	execCmd, err := ScannerExecCommand(scannerPath, mountPath, maxDepth, excludes, workers, reportFiles)
	if err != nil {
		return nil, err
	}

	stdout, stderr, err := ExecInPodStream(ctx, clientset, config, namespace, podName, containerName, execCmd, bytes.NewReader(ScannerBinary))
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

// ScannerExecCommand builds the argv for the in-pod scan. The sh -c script is a
// fixed constant; every dynamic value (scanner path, mount path, excludes, ...)
// is passed as a positional parameter ($1..$6) so pod-spec-controlled values
// can never be parsed as shell code.
func ScannerExecCommand(scannerPath, mountPath string, maxDepth int, excludes []string, workers int, reportFiles bool) ([]string, error) {
	for _, e := range excludes {
		if strings.ContainsRune(e, ',') || strings.ContainsRune(e, '\n') {
			return nil, fmt.Errorf("exclude pattern %q contains a comma or newline, which the scanner uses as the exclude separator", e)
		}
	}

	excludeStr := strings.Join(excludes, ",")
	if excludeStr != "" {
		excludeStr += ","
	}
	excludeStr += ".pvdu-scanner"

	filesStr := ""
	if reportFiles {
		filesStr = "--files"
	}

	script := `cat > "$1" && chmod +x "$1" && "$1" "$2" --max-depth="$3" --exclude="$4" --workers="$5" --output=json-lines $6; rc=$?; rm -f "$1"; exit $rc`

	return []string{
		"sh", "-c", script,
		"pvdu-sh",          // $0
		scannerPath,        // $1
		mountPath,          // $2
		fmt.Sprintf("%d", maxDepth), // $3
		excludeStr,         // $4
		fmt.Sprintf("%d", workers),  // $5
		filesStr,           // $6
	}, nil
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

func ParseScannerProgress(output string, progressFn dirwalker.ProgressFn) int64 {
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
		} else if progressFn != nil {
			isDir := line.Type == "progress" || line.Type == "dir"
			progressFn(line.Path, line.Size, isDir)
		}
	}
	return total
}
