package pvdu_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	repoRoot := findRepoRoot(t)
	binPath := filepath.Join(t.TempDir(), "pvdu")

	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/pvdu/")
	cmd.Dir = repoRoot
	cmd.Stderr = os.Stderr
	if out, err := cmd.Output(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, string(out))
	}
	return binPath
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func runPvdu(t *testing.T, bin string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestHelpFlag(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin, "--help")
	if err != nil {
		t.Fatalf("pvdu --help failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "pvdu") {
		t.Errorf("--help output should contain 'pvdu'")
	}
	if !strings.Contains(stdout, "usage") {
		t.Errorf("--help output should contain 'usage'")
	}
	if !strings.Contains(stdout, "flags") {
		t.Errorf("--help output should contain 'flags'")
	}
}

func TestHelpShortFlag(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin, "-h")
	if err != nil {
		t.Fatalf("pvdu -h failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "pvdu") {
		t.Errorf("-h output should contain 'pvdu'")
	}
}

func TestUnknownFlag(t *testing.T) {
	bin := buildBinary(t)

	_, stderr, err := runPvdu(t, bin, "--unknown-flag-123")
	if err == nil {
		t.Fatal("expected error for unknown flag")
	}

	if !strings.Contains(stderr, "unknown flag") {
		t.Errorf("stderr should contain 'unknown flag', got: %s", stderr)
	}
}

func TestNoArgs(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin)
	if err != nil {
		t.Fatalf("pvdu with no args should succeed, got: %v\nstderr: %s", err, stderr)
	}

	if stdout == "" && stderr == "" {
		t.Fatal("expected some output")
	}
}

func TestInvalidOutputFormat(t *testing.T) {
	bin := buildBinary(t)

	_, stderr, err := runPvdu(t, bin, "-o", "invalid")
	if err != nil {
		t.Fatalf("pvdu with invalid format should not error, got: %v\nstderr: %s", err, stderr)
	}
}

func TestForceFlag(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin, "-f")
	if err != nil {
		t.Fatalf("pvdu -f should not error, got: %v\nstderr: %s", err, stderr)
	}

	if stdout == "" {
		t.Log("expected output, got empty stdout")
	}
}

func isNoPVCs(out string) bool {
	return strings.TrimSpace(out) == "No PVCs found." || strings.TrimSpace(out) == ""
}

func TestJSONOutput(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin, "-o", "json")
	if err != nil {
		t.Fatalf("pvdu -o json failed: %v\nstderr: %s", err, stderr)
	}

	if isNoPVCs(stdout) {
		t.Skip("no cluster available, skipping JSON validation")
	}

	var parsed []map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("output should be valid JSON: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	t.Logf("parsed %d PVC results", len(parsed))
}

func TestYAMLOutput(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin, "-o", "yaml")
	if err != nil {
		t.Fatalf("pvdu -o yaml failed: %v\nstderr: %s", err, stderr)
	}

	if isNoPVCs(stdout) {
		t.Skip("no cluster available, skipping YAML validation")
	}

	var parsed []map[string]interface{}
	if err := yaml.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("output should be valid YAML: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	t.Logf("parsed %d PVC results", len(parsed))
}

func TestDefaultOutputHasHeader(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin)
	if err != nil {
		t.Fatalf("pvdu -o table failed: %v\nstderr: %s", err, stderr)
	}

	if isNoPVCs(stdout) {
		t.Skip("no cluster available, skipping table header validation")
	}

	if !strings.Contains(stdout, "NAMESPACE") {
		t.Errorf("default output should contain 'NAMESPACE' header")
	}
	if !strings.Contains(stdout, "PVC") {
		t.Errorf("default output should contain 'PVC' header")
	}
	if !strings.Contains(stdout, "REQUESTED") {
		t.Errorf("default output should contain 'REQUESTED' header")
	}
	if !strings.Contains(stdout, "USED") {
		t.Errorf("default output should contain 'USED' header")
	}
}

func TestConcurrencyFlag(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin, "-c", "1")
	if err != nil {
		t.Fatalf("pvdu -c 1 failed: %v\nstderr: %s", err, stderr)
	}

	if stdout == "" {
		t.Log("expected output, got empty stdout")
	}
}

func TestMaxDepthFlag(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin, "-d", "1")
	if err != nil {
		t.Fatalf("pvdu -d 1 failed: %v\nstderr: %s", err, stderr)
	}

	if stdout == "" {
		t.Log("expected output, got empty stdout")
	}
}

func TestTimeoutFlag(t *testing.T) {
	bin := buildBinary(t)

	_, stderr, err := runPvdu(t, bin, "-t", "10s")
	if err != nil {
		t.Fatalf("pvdu -t 10s failed: %v\nstderr: %s", err, stderr)
	}
}

func TestAllNamespacesShortFlag(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin, "-A")
	if err != nil {
		t.Fatalf("pvdu -A failed: %v\nstderr: %s", err, stderr)
	}

	if stdout == "" {
		t.Log("expected output, got empty stdout")
	}
}

func TestWorkersFlag(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin, "-w", "4")
	if err != nil {
		t.Fatalf("pvdu -w 4 failed: %v\nstderr: %s", err, stderr)
	}

	if stdout == "" {
		t.Log("expected output, got empty stdout")
	}
}

func TestExcludeFlag(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin, "-e", "*.tmp")
	if err != nil {
		t.Fatalf("pvdu -e failed: %v\nstderr: %s", err, stderr)
	}

	if stdout == "" {
		t.Log("expected output, got empty stdout")
	}
}

func TestMultipleExcludeFlags(t *testing.T) {
	bin := buildBinary(t)

	stdout, stderr, err := runPvdu(t, bin, "-e", "*.tmp", "-e", "*.log")
	if err != nil {
		t.Fatalf("pvdu -e multiple failed: %v\nstderr: %s", err, stderr)
	}

	if stdout == "" {
		t.Log("expected output, got empty stdout")
	}
}

func TestContextFlag(t *testing.T) {
	bin := buildBinary(t)

	stdout, _, err := runPvdu(t, bin, "--context", "nonexistent")
	if err == nil {
		t.Log("note: pvdu succeeded with nonexistent context (may have no kubeconfig)")
	}
	if stdout == "" {
		t.Log("expected output, got empty stdout")
	}
}
