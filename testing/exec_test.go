package pvdu_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/neutry/pvdu/internal/k8s"
)

func TestScannerExecCommand(t *testing.T) {
	cmd, err := k8s.ScannerExecCommand("/tmp/.pvdu-scanner", "/mnt", 3, []string{"*.tmp", "lost+found"}, 4, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cmd) != 10 {
		t.Fatalf("expected 10 argv elements (sh, -c, script, $0..$6), got %d: %v", len(cmd), cmd)
	}
	if cmd[0] != "sh" || cmd[1] != "-c" {
		t.Errorf("expected sh -c, got %q %q", cmd[0], cmd[1])
	}

	script := cmd[2]
	if !strings.Contains(script, `"$1"`) || !strings.Contains(script, `"$2"`) || !strings.Contains(script, `--max-depth="$3"`) {
		t.Errorf("script should reference positional params: %s", script)
	}
	if strings.Contains(script, "/tmp/.pvdu-scanner") || strings.Contains(script, "/mnt") || strings.Contains(script, "*.tmp") {
		t.Errorf("script must not contain dynamic values: %s", script)
	}

	wantParams := []string{
		"pvdu-sh",                        // $0
		"/tmp/.pvdu-scanner",             // $1 scanner path
		"/mnt",                           // $2 mount path
		"3",                              // $3 max-depth
		"*.tmp,lost+found,.pvdu-scanner", // $4 excludes
		"4",                              // $5 workers
		"--files",                        // $6 files
	}
	if got := cmd[3:]; !reflect.DeepEqual(got, wantParams) {
		t.Errorf("positional params mismatch\n got: %v\nwant: %v", got, wantParams)
	}
}

func TestScannerExecCommandNoReportFiles(t *testing.T) {
	cmd, err := k8s.ScannerExecCommand("/tmp/.pvdu-scanner", "/mnt", 0, nil, 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	params := cmd[3:]
	if params[6] != "" {
		t.Errorf("expected empty files param, got %q", params[6])
	}
	if params[3] != "0" || params[5] != "0" {
		t.Errorf("expected depth/workers 0, got %q/%q", params[3], params[5])
	}
	if params[4] != ".pvdu-scanner" {
		t.Errorf("expected only .pvdu-scanner exclusion, got %q", params[4])
	}
}

func TestScannerExecCommandNoInjection(t *testing.T) {
	malicious := []string{
		`/mnt; touch /tmp/pwned`,
		`/data'; rm -rf /; echo '`,
		`$(id > /tmp/x)`,
		`/a/$(nc 1.2.3.4 4444)`,
		`/mnt && chmod -R 777 /`,
		`/mnt|sh -i`,
	}
	for _, m := range malicious {
		cmd, err := k8s.ScannerExecCommand("/tmp/.pvdu-scanner", m, 0, nil, 0, false)
		if err != nil {
			t.Fatalf("mount path %q: unexpected error: %v", m, err)
		}
		if strings.Contains(cmd[2], m) {
			t.Errorf("mount path %q leaked into script: %s", m, cmd[2])
		}
		if cmd[5] != m {
			t.Errorf("mount path %q must be a positional argv element verbatim, got %q", m, cmd[5])
		}
	}
}

func TestScannerExecCommandRejectsCommaExclude(t *testing.T) {
	_, err := k8s.ScannerExecCommand("/tmp/.pvdu-scanner", "/mnt", 0, []string{"/var/,cache"}, 0, false)
	if err == nil {
		t.Fatal("expected error for comma in exclude pattern")
	}
	if !strings.Contains(err.Error(), "comma") {
		t.Errorf("error should mention comma: %v", err)
	}

	_, err = k8s.ScannerExecCommand("/tmp/.pvdu-scanner", "/mnt", 0, []string{"ok", "bad\nexclude"}, 0, false)
	if err == nil {
		t.Fatal("expected error for newline in exclude pattern")
	}
}
