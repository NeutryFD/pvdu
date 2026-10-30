package pvdu_test

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/render"
)

func TestLiveTableTerminalRedraw(t *testing.T) {
	if os.Getenv("PVDU_TEST_TERMINAL") == "1" {
		runForceProgressCommand(t, os.Stdout)
		return
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for pseudo-terminal verification")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := `import os, pty, fcntl, termios, struct, subprocess, sys, errno, re
master, slave = pty.openpty()
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 10, 60, 0, 0))
env = dict(os.environ, PVDU_TEST_TERMINAL='1')
p = subprocess.Popen([sys.argv[1], '-test.run=^TestLiveTableTerminalRedraw$', '-test.timeout=10s'], stdout=slave, stderr=slave, env=env)
os.close(slave)
data = b''
while True:
    try:
        chunk = os.read(master, 65536)
    except OSError as e:
        if e.errno == errno.EIO: break
        raise
    if not chunk: break
    data += chunk
os.close(master)
assert p.wait() == 0, data
text = data.decode().replace('\r\n', '\n')
assert '\x1b[?1049h' not in text and '\x1b[2J' not in text, text
assert text.count('\x1b[?25h') == 1, text
frames = re.split(r'\x1b\[\d+A\r\x1b\[J', text.replace('\x1b[?25l', ''))
assert len(frames) >= 8, text
assert frames[-1].count('NAMESPACE') == 1, frames[-1]
for frame in frames[:-1]:
    assert frame.count('NAMESPACE') == 1, frame
    assert all(len(line) <= 59 for line in frame.splitlines()), frame
    assert 'Scanning…' not in frame, frame
for state in ['WAITING', 'SCANNING', 'CREATING POD', 'WAITING POD', 'POD READY', 'UPLOADING', 'DONE']:
    assert state in text, (state, text)
assert text.index('CREATING POD') < text.index('WAITING POD') < text.index('POD READY') < text.index('UPLOADING'), text
assert 'temporary-scanner' in frames[-1], frames[-1]
assert '\nDebug:\n' in frames[-1] and 'argv=[sh -c scanner]' in frames[-1], frames[-1]
assert all('argv=' not in frame for frame in frames[:-1]), frames
`
	command := exec.CommandContext(t.Context(), python, "-c", script, binary)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("terminal verification: %v\n%s", err, output)
	}
}

func TestLiveTableUpdatesStatusAndDefersDiagnostics(t *testing.T) {
	var output bytes.Buffer
	results := []*model.ScanResult{
		{Namespace: "monitoring", PVCName: "claim-a", Status: model.StatusPending},
		{Namespace: "monitoring", PVCName: "claim-b", Status: model.StatusPending},
	}
	live, err := render.NewLiveTable(&output, "default", results)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "STATUS") || !strings.Contains(output.String(), "WAITING") {
		t.Fatalf("initial output missing status column or waiting row: %s", output.String())
	}

	if err := live.Update(0, model.ScanResult{Namespace: "monitoring", PVCName: "claim-a", Status: model.StatusScanning}); err != nil {
		t.Fatal(err)
	}
	if err := live.Update(0, model.ScanResult{Namespace: "monitoring", PVCName: "claim-a", Status: model.StatusDone, UsedBytes: 42}); err != nil {
		t.Fatal(err)
	}
	if err := live.Update(1, model.ScanResult{Namespace: "monitoring", PVCName: "claim-b", Status: model.StatusError, Error: "scanner failed"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "scanner failed") {
		t.Fatal("diagnostic leaked before completion")
	}
	if err := live.Finish(); err != nil {
		t.Fatal(err)
	}

	got := output.String()
	if !strings.Contains(got, "SCANNING") || !strings.Contains(got, "DONE") || !strings.Contains(got, "ERROR") {
		t.Fatalf("status transitions missing from output: %s", got)
	}
	if !strings.Contains(got, "Diagnostics:\n  monitoring/claim-b: scanner failed") {
		t.Fatalf("diagnostic was not shown after final table: %s", got)
	}
}

func TestTableDiagnosticsAreSingleLine(t *testing.T) {
	got := render.RenderDefault([]*model.ScanResult{{
		Namespace: "monitoring", PVCName: "claim", Status: model.StatusError,
		Error:    "upload scanner: exit 125 (mktemp: Read-only file system\n)",
		Warnings: []string{"cleanup failed\n retry later"},
	}})
	if strings.Contains(got, "\n)") || !strings.Contains(got, "file system )") || !strings.Contains(got, "cleanup failed retry later") {
		t.Fatalf("diagnostics are not single-line: %s", got)
	}
	if strings.Count(got, "ERROR") != 1 {
		t.Fatalf("ERROR should only appear in STATUS: %s", got)
	}
}
