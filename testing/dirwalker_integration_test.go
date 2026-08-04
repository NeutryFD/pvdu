package pvdu_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NeutryFD/dirwalker"
)

// scanLine mirrors internal/k8s/exec.go's wire contract for the scanner's
// json-lines output. This is the integration contract between pvdu and the
// dirwalker binary built at the pinned module version.
type scanLine struct {
	Type  string `json:"type"`
	Path  string `json:"path,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Human string `json:"human,omitempty"`
}

// buildDirwalker builds the scanner binary from the pinned dirwalker module
// version, so the tests exercise the exact binary `make scanner` embeds.
func buildDirwalker(t *testing.T) string {
	t.Helper()
	repoRoot := findRepoRoot(t)
	bin := filepath.Join(t.TempDir(), "dirwalker")

	cmd := exec.Command("go", "build", "-o", bin, "github.com/NeutryFD/dirwalker/cmd/dirwalker")
	cmd.Dir = repoRoot
	cmd.Stderr = os.Stderr
	if out, err := cmd.Output(); err != nil {
		t.Fatalf("build dirwalker failed: %v\n%s", err, string(out))
	}
	return bin
}

func writeScanFixture(t *testing.T, root string, files map[string]int64) int64 {
	t.Helper()
	var total int64
	for name, size := range files {
		dir := filepath.Join(root, filepath.Dir(name))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		total += size
	}
	return total
}

func TestScannerJSONLinesSchema(t *testing.T) {
	bin := buildDirwalker(t)

	root := t.TempDir()
	wantTotal := writeScanFixture(t, root, map[string]int64{
		"a.txt":      5,
		"sub/b.txt":  15,
		"sub/deep/c": 100,
	})

	cases := []struct {
		name      string
		extraArgs []string
		wantHuman bool
	}{
		{name: "machine-only", wantHuman: false},
		{name: "with-human", extraArgs: []string{"--human"}, wantHuman: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{root, "--output=json-lines", "--files"}, c.extraArgs...)
			stdout, stderr, err := runPvdu(t, bin, args...)
			if err != nil {
				t.Fatalf("dirwalker scan failed: %v\nstderr: %s", err, stderr)
			}

			var doneLines int
			var gotTotal int64
			lineCount := 0
			for _, raw := range strings.Split(strings.TrimSpace(stdout), "\n") {
				if raw == "" {
					continue
				}
				lineCount++
				var line scanLine
				if err := json.Unmarshal([]byte(raw), &line); err != nil {
					t.Fatalf("line %d is not valid under pvdu scanLine schema: %v\nraw: %s", lineCount, err, raw)
				}
				if line.Type == "done" {
					doneLines++
					gotTotal = line.Size
					if c.wantHuman && line.Human == "" {
						t.Errorf("line %d: expected human total with --human", lineCount)
					}
					if !c.wantHuman && line.Human != "" {
						t.Errorf("line %d: human total should be omitted without --human", lineCount)
					}
					continue
				}
				if line.Type != "file" && line.Type != "dir" {
					t.Errorf("line %d: unexpected type %q", lineCount, line.Type)
				}
				if line.Path == "" {
					t.Errorf("line %d: missing path", lineCount)
				}
				if line.Size < 0 {
					t.Errorf("line %d: negative size %d", lineCount, line.Size)
				}
				if c.wantHuman && line.Human == "" {
					t.Errorf("line %d: expected human size with --human", lineCount)
				}
				if !c.wantHuman && line.Human != "" {
					t.Errorf("line %d: human size should be omitted without --human", lineCount)
				}
			}

			if doneLines != 1 {
				t.Errorf("expected exactly one 'done' line, got %d", doneLines)
			}
			if gotTotal != wantTotal {
				t.Errorf("done.size = %d, want %d (sum of fixture files)", gotTotal, wantTotal)
			}
			if stdout == "" {
				t.Fatal("expected some output lines")
			}
		})
	}
}

func TestScannerDoneLineOnlyWithoutFilesFlag(t *testing.T) {
	bin := buildDirwalker(t)

	root := t.TempDir()
	wantTotal := writeScanFixture(t, root, map[string]int64{"only.txt": 7})

	stdout, stderr, err := runPvdu(t, bin, root, "--output=json-lines")
	if err != nil {
		t.Fatalf("dirwalker scan failed: %v\nstderr: %s", err, stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		t.Fatal("expected at least a done line")
	}
	var done scanLine
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &done); err != nil {
		t.Fatalf("last line not valid: %v\nraw: %s", err, lines[len(lines)-1])
	}
	if done.Type != "done" {
		t.Errorf("last line type = %q, want done", done.Type)
	}
	if done.Size != wantTotal {
		t.Errorf("done.size = %d, want %d", done.Size, wantTotal)
	}
}

func TestScannerFormatBytesContract(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
	}
	for _, c := range cases {
		if got := dirwalker.FormatBytes(c.in); got != c.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := dirwalker.FormatBytesShort(1536); got != "1.5Ki" {
		t.Errorf("FormatBytesShort(1536) = %q, want 1.5Ki", got)
	}
	if got := dirwalker.FormatBytesShort(0); got != "0 B" {
		t.Errorf("FormatBytesShort(0) = %q, want 0 B", got)
	}
}

func TestScannerNegativeMaxDepthErrors(t *testing.T) {
	bin := buildDirwalker(t)

	_, stderr, err := runPvdu(t, bin, t.TempDir(), "--max-depth=-1")
	if err == nil {
		t.Fatal("expected scanner to exit non-zero for negative --max-depth")
	}
	if !strings.Contains(stderr, "invalid maxDepth") {
		t.Errorf("stderr should mention invalid maxDepth, got: %s", stderr)
	}
}

func TestScannerReadErrorPropagates(t *testing.T) {
	bin := buildDirwalker(t)

	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, stderr, err := runPvdu(t, bin, root, "--output=json-lines")
	if err == nil {
		t.Fatal("expected scanner to exit non-zero when a subdirectory becomes unreadable mid-walk")
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("stderr should carry the scan error, got: %s", stderr)
	}
}
