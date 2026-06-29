package ui

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/neutry/pvdu/internal/model"
	"gopkg.in/yaml.v3"
)

var ansiStrip = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiStrip.ReplaceAllString(s, "")
}

func TestRenderTable_Done(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace:      "default",
			PVCName:        "data-test-0",
			PodName:        "pod-test-0",
			ScanPath:       "/data",
			RequestedBytes: 10 * 1024 * 1024 * 1024,
			RequestedStr:   "10Gi",
			PVBytes:        10 * 1024 * 1024 * 1024,
			UsedBytes:      5 * 1024 * 1024 * 1024,
			Status:         model.StatusDone,
		},
	}

	out := stripANSI(RenderTable(results))

	if !strings.Contains(out, "default") {
		t.Errorf("table should contain namespace")
	}
	if !strings.Contains(out, "data-test-0") {
		t.Errorf("table should contain PVC name")
	}
	if !strings.Contains(out, "pod-test-0") {
		t.Errorf("table should contain pod name")
	}
	if !strings.Contains(out, "/data") {
		t.Errorf("table should contain scan path")
	}
	if !strings.Contains(out, "5.0Gi") {
		t.Errorf("table should contain used bytes")
	}
	if !strings.Contains(out, "50%") {
		t.Errorf("table should contain percentage")
	}
}

func TestRenderTable_EmptyPath(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace:      "monitoring",
			PVCName:        "data-no-pod",
			PodName:        "pod-no-pod",
			ScanPath:       "",
			RequestedBytes: 30 * 1024 * 1024 * 1024,
			RequestedStr:   "30Gi",
			PVBytes:        30 * 1024 * 1024 * 1024,
			UsedBytes:      2 * 1024 * 1024 * 1024,
			Status:         model.StatusDone,
		},
	}

	out := stripANSI(RenderTable(results))

	if !strings.Contains(out, "-") {
		t.Errorf("table should show '-' for empty path")
	}
	if strings.Contains(out, "/mnt") {
		t.Errorf("table should NOT contain /mnt for PVCs without mounts")
	}
}

func TestRenderTable_Error(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace: "default",
			PVCName:   "data-error",
			PodName:   "",
			Status:    model.StatusError,
			Error:     "exec failed: connection refused",
		},
	}

	out := stripANSI(RenderTable(results))

	if !strings.Contains(out, "ERROR") {
		t.Errorf("table should show ERROR for failed scans")
	}
	if !strings.Contains(out, "exec failed") {
		t.Errorf("table should include error message in Errors section")
	}
}

func TestRenderTable_Pending(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace: "default",
			PVCName:   "data-pending",
			PodName:   "",
			Status:    model.StatusPending,
		},
	}

	out := stripANSI(RenderTable(results))

	if strings.Contains(out, "ERROR") {
		t.Errorf("table should NOT show ERROR for pending scans")
	}
}

func TestRenderTable_ZeroSize(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace:      "default",
			PVCName:        "data-empty",
			PodName:        "pod-empty",
			ScanPath:       "/data",
			RequestedBytes: 10 * 1024,
			RequestedStr:   "10Ki",
			PVBytes:        10 * 1024,
			UsedBytes:      0,
			Status:         model.StatusDone,
		},
	}

	out := stripANSI(RenderTable(results))

	if !strings.Contains(out, "0 B") {
		t.Errorf("table should show '0 B' for empty PVC")
	}
	if !strings.Contains(out, "0%") {
		t.Errorf("table should show '0%%' for empty PVC")
	}
}

func TestRenderTable_FullSize(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace:      "default",
			PVCName:        "data-full",
			PodName:        "pod-full",
			ScanPath:       "/data",
			RequestedBytes: 1024,
			RequestedStr:   "1.0Ki",
			PVBytes:        1024,
			UsedBytes:      1024,
			Status:         model.StatusDone,
		},
	}

	out := stripANSI(RenderTable(results))

	if !strings.Contains(out, "100%") {
		t.Errorf("table should show 100%% for full PVC")
	}
}

func TestRenderTable_MultipleResults(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace:      "default",
			PVCName:        "data-a",
			PodName:        "pod-a",
			ScanPath:       "/mnt/data",
			RequestedBytes: 10 * 1024 * 1024 * 1024,
			RequestedStr:   "10Gi",
			PVBytes:        10 * 1024 * 1024 * 1024,
			UsedBytes:      1 * 1024 * 1024 * 1024,
			Status:         model.StatusDone,
		},
		{
			Namespace:      "monitoring",
			PVCName:        "data-b",
			PodName:        "",
			ScanPath:       "",
			RequestedBytes: 20 * 1024 * 1024 * 1024,
			RequestedStr:   "20Gi",
			PVBytes:        20 * 1024 * 1024 * 1024,
			UsedBytes:      0,
			Status:         model.StatusDone,
		},
		{
			Namespace: "kube-system",
			PVCName:   "data-c",
			PodName:   "",
			Status:    model.StatusError,
			Error:     "timeout",
		},
	}

	out := stripANSI(RenderTable(results))

	if !strings.Contains(out, "data-a") {
		t.Errorf("table should contain first PVC")
	}
	if !strings.Contains(out, "data-b") {
		t.Errorf("table should contain second PVC")
	}
	if !strings.Contains(out, "data-c") {
		t.Errorf("table should contain third PVC")
	}
	if !strings.Contains(out, "ERROR") {
		t.Errorf("table should show ERROR for failed PVC")
	}
	if !strings.Contains(out, "timeout") {
		t.Errorf("table should include error message")
	}
}

func TestRenderJSON(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace:      "default",
			PVCName:        "data-test",
			PodName:        "pod-test",
			ScanPath:       "/data",
			RequestedBytes: 1073741824,
			RequestedStr:   "1.0Gi",
			PVBytes:        1073741824,
			UsedBytes:      524288000,
			Status:         model.StatusDone,
		},
	}

	out := RenderJSON(results)

	var parsed []model.ScanResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("RenderJSON output is not valid JSON: %v", err)
	}

	if len(parsed) != 1 {
		t.Fatalf("expected 1 result, got %d", len(parsed))
	}

	if parsed[0].Namespace != "default" || parsed[0].PVCName != "data-test" || parsed[0].PodName != "pod-test" {
		t.Errorf("JSON unmarshal produced wrong data: %+v", parsed[0])
	}
}

func TestRenderYAML(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace:      "default",
			PVCName:        "data-test",
			PodName:        "pod-test",
			ScanPath:       "/data",
			RequestedBytes: 1073741824,
			RequestedStr:   "1.0Gi",
			PVBytes:        1073741824,
			UsedBytes:      524288000,
			Status:         model.StatusDone,
		},
	}

	out := RenderYAML(results)

	var parsed []model.ScanResult
	if err := yaml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("RenderYAML output is not valid YAML: %v", err)
	}

	if len(parsed) != 1 {
		t.Fatalf("expected 1 result, got %d", len(parsed))
	}

	if parsed[0].Namespace != "default" || parsed[0].PVCName != "data-test" || parsed[0].PodName != "pod-test" {
		t.Errorf("YAML unmarshal produced wrong data: %+v", parsed[0])
	}
}

func TestRenderJSON_Empty(t *testing.T) {
	out := RenderJSON([]*model.ScanResult{})

	var parsed []model.ScanResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("RenderJSON empty output is not valid JSON: %v", err)
	}

	if len(parsed) != 0 {
		t.Errorf("expected empty array, got %d items", len(parsed))
	}
}

func TestRenderYAML_Empty(t *testing.T) {
	out := RenderYAML([]*model.ScanResult{})

	var parsed []model.ScanResult
	if err := yaml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("RenderYAML empty output is not valid YAML: %v", err)
	}
}

func TestRenderJSON_ErrorResult(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace: "monitoring",
			PVCName:   "data-broken",
			Status:    model.StatusError,
			Error:     "create temp pod: failed",
		},
	}

	out := RenderJSON(results)

	var parsed []model.ScanResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("RenderJSON error output is not valid JSON: %v", err)
	}

	if parsed[0].Error != "create temp pod: failed" {
		t.Errorf("expected error field to be present, got %q", parsed[0].Error)
	}
}

func TestRenderTable_MultipleScansAndErrors(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace:      "ns-a",
			PVCName:        "pvc-ok",
			PodName:        "pod-ok",
			ScanPath:       "/data",
			RequestedBytes: 10 * 1024 * 1024 * 1024,
			RequestedStr:   "10Gi",
			PVBytes:        10 * 1024 * 1024 * 1024,
			UsedBytes:      1 * 1024 * 1024 * 1024,
			Status:         model.StatusDone,
		},
		{
			Namespace: "ns-b",
			PVCName:   "pvc-err",
			PodName:   "",
			Status:    model.StatusError,
			Error:     "timeout waiting for pod",
		},
		{
			Namespace:      "ns-c",
			PVCName:        "pvc-no-path",
			PodName:        "pod-no-path",
			ScanPath:       "",
			RequestedBytes: 5 * 1024 * 1024 * 1024,
			RequestedStr:   "5Gi",
			PVBytes:        5 * 1024 * 1024 * 1024,
			UsedBytes:      256 * 1024 * 1024,
			Status:         model.StatusDone,
		},
	}

	out := stripANSI(RenderTable(results))

	if !strings.Contains(out, "Errors:") {
		t.Errorf("table should contain 'Errors:' section")
	}

	if !strings.Contains(out, "pvc-ok") || !strings.Contains(out, "pvc-err") || !strings.Contains(out, "pvc-no-path") {
		t.Errorf("table should contain all PVC names")
	}

	if !strings.Contains(out, "10%") {
		t.Errorf("table should show 10%% for 1/10 Gi")
	}
}

func TestRenderTable_BoundaryPercentage(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace:      "test",
			PVCName:        "pvc-0pct",
			PodName:        "pod-0pct",
			ScanPath:       "/data",
			RequestedBytes: 100,
			RequestedStr:   "100 B",
			PVBytes:        100,
			UsedBytes:      0,
			Status:         model.StatusDone,
		},
		{
			Namespace:      "test",
			PVCName:        "pvc-99pct",
			PodName:        "pod-99pct",
			ScanPath:       "/data",
			RequestedBytes: 100,
			RequestedStr:   "100 B",
			PVBytes:        100,
			UsedBytes:      99,
			Status:         model.StatusDone,
		},
	}

	out := stripANSI(RenderTable(results))

	if !strings.Contains(out, "0%") {
		t.Errorf("table should show 0%% for 0-byte used")
	}
	if !strings.Contains(out, "99%") {
		t.Errorf("table should show 99%% for 99/100 bytes")
	}
}

func TestRenderTable_NoRequestedBytes(t *testing.T) {
	results := []*model.ScanResult{
		{
			Namespace:      "test",
			PVCName:        "pvc-no-req",
			PodName:        "pod-no-req",
			ScanPath:       "/data",
			RequestedBytes: 0,
			RequestedStr:   "",
			PVBytes:        1073741824,
			UsedBytes:      524288000,
			Status:         model.StatusDone,
		},
	}

	out := stripANSI(RenderTable(results))

	if strings.Contains(out, "%") && strings.Count(out, "%") > 1 {
		t.Errorf("table should not show percentage when RequestedBytes is 0")
	}
}
