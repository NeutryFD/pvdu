package pvdu_test

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/neutry/pvdu/internal/engine"
	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/scanner"
	"github.com/neutry/pvdu/internal/tui"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// tuiDriver runs a TUI model like Bubble Tea does: commands run on their own
// goroutines and their messages reach Update one at a time.
type tuiDriver struct {
	t    *testing.T
	m    *tui.Model
	msgs chan tea.Msg
	quit bool
}

func newTUIDriver(t *testing.T, cfg tui.Config) *tuiDriver {
	t.Helper()
	d := &tuiDriver{t: t, m: tui.New(context.Background(), cfg), msgs: make(chan tea.Msg, 64)}
	t.Cleanup(d.m.Close)
	d.exec(d.m.Init())
	d.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	return d
}

func (d *tuiDriver) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				d.exec(c)
			}
			return
		}
		if msg != nil {
			d.msgs <- msg
		}
	}()
}

func (d *tuiDriver) send(msg tea.Msg) {
	if _, ok := msg.(tea.QuitMsg); ok {
		d.quit = true
		return
	}
	_, cmd := d.m.Update(msg)
	d.exec(cmd)
}

func (d *tuiDriver) press(keys ...string) {
	for _, k := range keys {
		switch k {
		case "enter":
			d.send(tea.KeyPressMsg{Code: tea.KeyEnter})
		case "esc":
			d.send(tea.KeyPressMsg{Code: tea.KeyEscape})
		case "space":
			d.send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
		default:
			for _, r := range k {
				d.send(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
		}
	}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;:?]*[a-zA-Z]`)

func (d *tuiDriver) screen() string { return ansi.ReplaceAllString(d.m.View().Content, "") }

// until processes messages until cond holds for the screen.
func (d *tuiDriver) until(what string, cond func(screen string) bool) {
	d.t.Helper()
	timeout := time.After(5 * time.Second)
	for !cond(d.screen()) {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-timeout:
			d.t.Fatalf("timed out waiting for %s; screen:\n%s", what, d.screen())
		}
	}
}

func (d *tuiDriver) untilQuit() {
	d.t.Helper()
	timeout := time.After(5 * time.Second)
	for !d.quit {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-timeout:
			d.t.Fatalf("program did not quit; screen:\n%s", d.screen())
		}
	}
}

func contains(parts ...string) func(string) bool {
	return func(screen string) bool {
		for _, p := range parts {
			if !strings.Contains(screen, p) {
				return false
			}
		}
		return true
	}
}

func tuiPVCs() []kube.PVCInfo {
	return []kube.PVCInfo{
		{Name: "data-alpha", Namespace: "ns", PVName: "pv-alpha", StorageClass: "fast", RequestedStr: "10Gi", RequestedBytes: 10 << 30, Mounts: []kube.PodMount{{PodName: "alpha-0", ContainerName: "c", MountPath: "/data"}}},
		{Name: "data-beta", Namespace: "ns", RequestedStr: "1Gi", RequestedBytes: 1 << 30, Mounts: []kube.PodMount{{PodName: "beta-0", ContainerName: "c", MountPath: "/data"}}},
	}
}

func tuiEngine(scan func(context.Context, string) (*model.ScanResult, error), deps engine.Dependencies) *engine.Engine {
	deps.ListPVCs = func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error) { return tuiPVCs(), nil }
	deps.UploadAndScan = func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, pod, _, _, _ string, _ *kube.PVCInfo, _ int, _ []string, _ int) (*model.ScanResult, error) {
		return scan(ctx, pod)
	}
	return engine.New(fake.NewSimpleClientset(), &rest.Config{}, engine.Options{
		Image: "alpine", Timeout: time.Minute, CleanupTimeout: time.Second, Concurrency: 2,
	}, deps)
}

func TestTUIFlagRejectsMachineOutput(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		run := runCommand(context.Background(), hooks{}, "--tui", "-o", format)
		if run.err == nil || !strings.Contains(run.err.Error(), "--tui cannot be combined with --output "+format) {
			t.Fatalf("-o %s: err = %v", format, run.err)
		}
	}
}

func TestTUIFlagNeedsTerminal(t *testing.T) {
	built := false
	run := runCommand(context.Background(), hooks{
		BuildClient: func(string, string) (kubernetes.Interface, *rest.Config, error) {
			built = true
			return fake.NewSimpleClientset(), &rest.Config{}, nil
		},
	}, "--tui")
	if run.err == nil || !strings.Contains(run.err.Error(), "--tui needs an interactive terminal") {
		t.Fatalf("err = %v", run.err)
	}
	if built {
		t.Fatal("client built before the terminal check")
	}
}

func TestTUIListsAndScansSelectedPVC(t *testing.T) {
	var scanned atomic.Value
	e := tuiEngine(func(_ context.Context, pod string) (*model.ScanResult, error) {
		scanned.Store(pod)
		return &model.ScanResult{Status: model.StatusDone, UsedBytes: 512 << 20}, nil
	}, engine.Dependencies{})
	d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "ns", Context: "lab", PVC: "data-beta", LogLevel: "info"})
	d.until("PVC list", contains("context lab", "namespace ns", "data-alpha", "data-beta"))
	// --pvc data-beta selected the second row, so s scans it.
	d.press("s")
	d.until("scan result", contains("DONE", "50%"))
	if scanned.Load() != "beta-0" {
		t.Fatalf("scanned pod = %v, want beta-0", scanned.Load())
	}
	d.press("enter")
	d.until("detail view", contains("ns/data-beta", "Used", "beta-0:/data"))
	d.press("l")
	d.until("scan log line", contains("ns/data-beta: scanning beta-0:/data"))
}

var brailleFrame = regexp.MustCompile(`([\x{2800}-\x{28FF}]+) SCANNING`)

func TestTUIAnimatesRunningScanAndStopsWhenIdle(t *testing.T) {
	release := make(chan struct{})
	e := tuiEngine(func(ctx context.Context, _ string) (*model.ScanResult, error) {
		scanner.NotifyProgress(ctx, model.StatusScanning, "")
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &model.ScanResult{Status: model.StatusDone, UsedBytes: 1}, nil
	}, engine.Dependencies{})
	d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "ns"})
	d.until("PVC list", contains("data-alpha"))
	d.press("s")
	d.until("animated scan", func(s string) bool { return brailleFrame.MatchString(s) })
	first := brailleFrame.FindStringSubmatch(d.screen())[1]
	d.until("next frame", func(s string) bool {
		m := brailleFrame.FindStringSubmatch(s)
		return m != nil && m[1] != first
	})
	// Usage scans show progress only in their row, never in a pop-up.
	if strings.Contains(d.screen(), "╭") {
		t.Fatalf("pop-up shown for a usage scan:\n%s", d.screen())
	}
	close(release)
	d.until("finished scan", func(s string) bool { return strings.Contains(s, "DONE") && !brailleFrame.MatchString(s) })
	// The clock stops with the last scan. A tick already scheduled or a late
	// log event may still arrive, but then the TUI must go quiet.
	for range 10 {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-time.After(300 * time.Millisecond):
			return
		}
	}
	t.Fatal("TUI keeps receiving messages while idle")
}

func TestTUIOptionsChangeNewScans(t *testing.T) {
	var depth atomic.Int32
	e := engine.New(fake.NewSimpleClientset(), &rest.Config{}, engine.Options{
		Image: "alpine", Timeout: time.Minute, CleanupTimeout: time.Second, Concurrency: 3,
	}, engine.Dependencies{
		ListPVCs: func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error) { return tuiPVCs(), nil },
		UploadAndScan: func(_ context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, _ *kube.PVCInfo, maxDepth int, _ []string, _ int) (*model.ScanResult, error) {
			depth.Store(int32(maxDepth))
			return &model.ScanResult{Status: model.StatusDone, UsedBytes: 1}, nil
		},
	})
	d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "ns"})
	d.until("PVC list", contains("data-alpha"))
	d.press("O")
	d.until("options pop-up", contains("Options", "Image", "alpine", "Concurrency", "Max depth", "0 (unlimited)", "Log level", "warn"))
	// Concurrency 0 is accepted, shown as no limit and applied at once.
	d.press("j", "enter")
	d.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	d.press("0", "enter")
	d.until("no limit", func(s string) bool { return regexp.MustCompile(`› Concurrency +0 \(no limit\)`).MatchString(s) })
	d.until("applied", contains("Concurrency set to 0 (no limit)"))
	if e.Options().Concurrency != 0 {
		t.Fatalf("concurrency not applied: %+v", e.Options())
	}
	d.press("k")
	// Max depth is the fifth field; editing starts from its raw value.
	for range 4 {
		d.press("j")
	}
	d.press("enter")
	d.until("editing max depth", contains("enter apply · esc cancel"))
	d.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	d.press("x", "enter")
	d.until("parse error", contains(`"x" is not a whole number`))
	d.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	d.press("3", "enter")
	d.until("edited value", func(s string) bool { return regexp.MustCompile(`› Max depth +3`).MatchString(s) })
	d.press("j", "j", "j", "j", "enter")
	d.until("log level changed", func(s string) bool { return regexp.MustCompile(`› Log level +error`).MatchString(s) })
	d.until("applied", contains("Log level set to error"))
	// No save step: closing keeps every change.
	d.press("O")
	d.until("pop-up closed", func(s string) bool { return !strings.Contains(s, "› Log level") })
	if o := e.Options(); o.MaxDepth != 3 || o.Concurrency != 0 || o.Image != "alpine" {
		t.Fatalf("engine options = %+v", o)
	}
	d.press("s")
	d.until("scan with new options", contains("DONE"))
	if depth.Load() != 3 {
		t.Fatalf("scan used max depth %d, want 3", depth.Load())
	}
}

func TestTUIOptionsApplyWithoutSaving(t *testing.T) {
	e := tuiEngine(func(context.Context, string) (*model.ScanResult, error) {
		return &model.ScanResult{Status: model.StatusDone}, nil
	}, engine.Dependencies{})
	d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "ns"})
	d.until("PVC list", contains("data-alpha"))
	d.press("O", "j", "j", "j", "j", "j", "j", "j", "enter")
	d.until("fallback on", contains("on: s and S fall back without asking"))
	if !e.Options().Force {
		t.Fatal("toggling did not apply")
	}
	// Esc while editing a field cancels only that edit.
	d.press("k", "enter")
	d.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	d.press("tmp", "esc")
	d.until("edit cancelled", func(s string) bool { return regexp.MustCompile(`› Exclude +-`).MatchString(s) })
	d.press("esc")
	d.until("pop-up closed", func(s string) bool { return !strings.Contains(s, "Temp-pod fallback") })
	if o := e.Options(); !o.Force || len(o.Excludes) != 0 {
		t.Fatalf("options after closing = %+v", o)
	}
}

// themeAccents are the title colours of the themes as truecolor codes.
var themeAccents = map[string]string{
	"dracula": "38;2;189;147;249", "catppuccin": "38;2;203;166;247", "nord": "38;2;136;192;208",
	"gruvbox": "38;2;250;189;47", "tokyonight": "38;2;122;162;247",
}

func TestTUIThemes(t *testing.T) {
	if got := strings.Join(tui.ThemeNames(), ","); got != "dracula,catppuccin,nord,gruvbox,tokyonight" {
		t.Fatalf("themes = %s", got)
	}
	e := tuiEngine(func(context.Context, string) (*model.ScanResult, error) {
		return &model.ScanResult{Status: model.StatusDone}, nil
	}, engine.Dependencies{})
	for name, accent := range themeAccents {
		d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "ns", Theme: name})
		d.until("PVC list", contains("data-alpha"))
		if !strings.Contains(d.m.View().Content, accent) {
			t.Errorf("theme %s: title colour %s missing", name, accent)
		}
	}
	// An empty theme is the default, dracula.
	d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "ns"})
	d.until("PVC list", contains("data-alpha"))
	if !strings.Contains(d.m.View().Content, themeAccents["dracula"]) {
		t.Error("default theme is not dracula")
	}
	// The Theme option switches live and cycles through every theme.
	d.press("O")
	for range len(optionsBeforeTheme) {
		d.press("j")
	}
	d.press("enter")
	d.until("catppuccin", func(s string) bool { return regexp.MustCompile(`› Theme +catppuccin`).MatchString(s) })
	d.press("esc")
	d.until("pop-up closed", func(s string) bool { return !strings.Contains(s, "› Theme") })
	if !strings.Contains(d.m.View().Content, themeAccents["catppuccin"]) {
		t.Fatal("theme change not applied to the screen")
	}
}

func TestTUISelectionHighlightsWholeRow(t *testing.T) {
	e := tuiEngine(func(context.Context, string) (*model.ScanResult, error) {
		return &model.ScanResult{Status: model.StatusDone}, nil
	}, engine.Dependencies{})
	for name := range themeAccents {
		d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "ns", Theme: name})
		d.until("PVC list", contains("data-alpha"))
		var row string
		for _, line := range strings.Split(d.m.View().Content, "\n") {
			if strings.Contains(line, "data-alpha") {
				row = line
			}
		}
		// The first reset may only follow the row's last cell; an earlier
		// one ends the selection background mid-row.
		reset := strings.Index(row, "\x1b[m")
		if !strings.Contains(row, "48;2;") || reset < 0 || strings.TrimSpace(ansi.ReplaceAllString(row[reset:], "")) != "" {
			t.Fatalf("theme %s: selection does not span the row: %q", name, row)
		}
	}
}

// optionsBeforeTheme are the option rows above Theme.
var optionsBeforeTheme = []string{"Image", "Concurrency", "Timeout", "Cleanup timeout", "Max depth", "Workers", "Exclude", "Temp-pod fallback", "Log level"}

func TestTUIThemeFlag(t *testing.T) {
	run := runCommand(context.Background(), hooks{}, "--tui", "--theme", "solarized")
	if run.err == nil || !strings.Contains(run.err.Error(), `invalid theme "solarized"; choose one of dracula, catppuccin, nord, gruvbox, tokyonight`) {
		t.Fatalf("err = %v", run.err)
	}
}

func TestTUIFitsTerminalWidth(t *testing.T) {
	e := tuiEngine(func(context.Context, string) (*model.ScanResult, error) {
		return &model.ScanResult{Status: model.StatusDone, UsedBytes: 1}, nil
	}, engine.Dependencies{})
	// PV is hidden first when the name columns cannot get 8 columns each;
	// NAMESPACE makes one more name column with every namespace listed.
	for namespace, pvFrom := range map[string]int{"ns": 100, "": 110} {
		d := newTUIDriver(t, tui.Config{Engine: e, Namespace: namespace})
		d.until("PVC list", contains("data-alpha"))
		fitsEveryWidth(t, d, pvFrom)
	}
}

func fitsEveryWidth(t *testing.T, d *tuiDriver, pvFrom int) {
	t.Helper()
	for width := 80; width <= 200; width++ {
		d.send(tea.WindowSizeMsg{Width: width, Height: 12})
		lines := strings.Split(d.screen(), "\n")
		for _, line := range lines {
			// Wider lines wrap in a real terminal and break the layout.
			if w := len([]rune(strings.TrimRight(line, " "))); w > width {
				t.Fatalf("width %d: line of %d columns:\n%s", width, w, line)
			}
		}
		// The table spans the screen: its header rule reaches the last column.
		if !slices.ContainsFunc(lines, func(l string) bool {
			return len([]rune(strings.TrimRight(l, " "))) == width && strings.HasPrefix(l, "─")
		}) {
			t.Fatalf("width %d: table does not fill the screen:\n%s", width, d.screen())
		}
		hasPV := regexp.MustCompile(`PVC +PV `).MatchString(d.screen())
		if width >= pvFrom && !hasPV {
			t.Fatalf("width %d: PV column hidden although it fits:\n%s", width, d.screen())
		}
	}
}

func TestTUIShowsPopupWhileListingPVCs(t *testing.T) {
	release := make(chan struct{})
	e := engine.New(fake.NewSimpleClientset(), &rest.Config{}, engine.Options{Image: "alpine", Timeout: time.Minute, CleanupTimeout: time.Second, Concurrency: 1},
		engine.Dependencies{ListPVCs: func(ctx context.Context, _ kubernetes.Interface, _ string) ([]kube.PVCInfo, error) {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return tuiPVCs(), nil
		}})
	d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "monitoring"})
	popup := regexp.MustCompile(`│ [\x{2800}-\x{28FF}] Loading PVCs in monitoring… +│`)
	d.until("loading pop-up", popup.MatchString)
	// Centred like the confirmation, not in a corner.
	lines := strings.Split(d.screen(), "\n")
	row := slices.IndexFunc(lines, popup.MatchString)
	// Trailing spaces are not rendered, so compare the left margin with the
	// one a centred box of this width has on the 140-column test screen.
	left := len([]rune(lines[row][:strings.Index(lines[row], "│")]))
	box := len([]rune(lines[row][strings.Index(lines[row], "│"):strings.LastIndex(lines[row], "│")])) + 1
	if diff := row - len(lines)/2; diff < -2 || diff > 2 || left != (140-box)/2 {
		t.Fatalf("loading pop-up not centred (row %d of %d, left margin %d, box %d):\n%s", row, len(lines), left, box, d.screen())
	}
	close(release)
	d.until("PVC list without pop-up", func(s string) bool { return strings.Contains(s, "data-alpha") && !strings.Contains(s, "╭") })
}

// pickerEngine lists namespaces with listNS and records which namespace each
// PVC list asked for.
func pickerEngine(listNS func(context.Context, kubernetes.Interface) ([]string, error), asked *atomic.Value) *engine.Engine {
	return engine.New(fake.NewSimpleClientset(), &rest.Config{}, engine.Options{Image: "alpine", Timeout: time.Minute, CleanupTimeout: time.Second, Concurrency: 1},
		engine.Dependencies{
			ListNamespaces: listNS,
			ListPVCs: func(_ context.Context, _ kubernetes.Interface, ns string) ([]kube.PVCInfo, error) {
				asked.Store(ns)
				return []kube.PVCInfo{{Name: "claim-in-" + ns, Namespace: ns}}, nil
			},
		})
}

func TestTUINamespacePickerSwitchesNamespace(t *testing.T) {
	var asked atomic.Value
	e := pickerEngine(func(context.Context, kubernetes.Interface) ([]string, error) {
		return []string{"default", "kube-system", "monitoring"}, nil
	}, &asked)
	d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "default"})
	d.until("PVC list", contains("claim-in-default"))
	d.press("n")
	d.until("picker", func(s string) bool {
		return contains("Namespace", "all namespaces", "kube-system", "monitoring")(s) && regexp.MustCompile(`› default +current`).MatchString(s)
	})
	// Typing filters; enter switches to the highlighted namespace.
	d.press("mon")
	d.until("filtered picker", func(s string) bool {
		return strings.Contains(s, "› monitoring") && !strings.Contains(s, "kube-system")
	})
	d.press("enter")
	d.until("monitoring PVCs", contains("namespace monitoring", "claim-in-monitoring"))
	if asked.Load() != "monitoring" || strings.Contains(d.screen(), "╭") {
		t.Fatalf("listed %v; screen:\n%s", asked.Load(), d.screen())
	}
	// Arrows choose too: up from monitoring reaches all namespaces at the top.
	d.press("n")
	d.until("picker on current", contains("› monitoring"))
	d.send(tea.KeyPressMsg{Code: tea.KeyUp})
	d.send(tea.KeyPressMsg{Code: tea.KeyUp})
	d.send(tea.KeyPressMsg{Code: tea.KeyUp})
	d.press("enter")
	d.until("all namespaces", contains("namespace all"))
	if asked.Load() != "" {
		t.Fatalf("listed %q, want every namespace", asked.Load())
	}
}

func TestTUINamespacePickerAcceptsTypedNameWhenListForbidden(t *testing.T) {
	var asked atomic.Value
	e := pickerEngine(func(context.Context, kubernetes.Interface) ([]string, error) {
		return nil, errors.New(`namespaces is forbidden`)
	}, &asked)
	d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "default"})
	d.until("PVC list", contains("claim-in-default"))
	d.press("n")
	d.until("picker error", contains("Cannot list namespaces", "forbidden", "Type a namespace"))
	d.press("apps", "enter")
	d.until("typed namespace", contains("namespace apps", "claim-in-apps"))
}

func TestTUIColumnsAndSharedAnimation(t *testing.T) {
	e := tuiEngine(func(ctx context.Context, _ string) (*model.ScanResult, error) {
		scanner.NotifyProgress(ctx, model.StatusUploading, "")
		<-ctx.Done()
		return nil, ctx.Err()
	}, engine.Dependencies{})
	d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "ns"})
	d.until("PVC list", contains("data-alpha"))
	header := regexp.MustCompile(`POD +PVC +PV +STORAGE CLASS +REQUESTED +SIZE +USED +% +STATUS`)
	row := regexp.MustCompile(`alpha-0 +data-alpha +pv-alpha +fast +10Gi`)
	if !header.MatchString(d.screen()) || !row.MatchString(d.screen()) {
		t.Fatalf("column order:\n%s", d.screen())
	}

	d.press("s")
	// Every running state shares the scanning animation; only the label changes.
	d.until("animated upload", func(s string) bool {
		return regexp.MustCompile(`[\x{2800}-\x{28FF}] UPLOADING`).MatchString(s)
	})
}

// forceEngine fails exec into the mounting pod and records temporary pods.
func forceEngine(created, deleted *atomic.Int32) *engine.Engine {
	return tuiEngine(func(ctx context.Context, pod string) (*model.ScanResult, error) {
		if pod != "pvdu-tmp" {
			return nil, errors.New("exec: sh not found")
		}
		return &model.ScanResult{Status: model.StatusDone, UsedBytes: 1 << 30}, nil
	}, engine.Dependencies{
		CreatePod: func(context.Context, kubernetes.Interface, kube.PVCInfo, string) (kube.PodRef, error) {
			created.Add(1)
			return kube.PodRef{Name: "pvdu-tmp"}, nil
		},
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		DeletePod: func(context.Context, kubernetes.Interface, string, kube.PodRef) error {
			deleted.Add(1)
			return nil
		},
	})
}

func TestTUIForceScanFallsBackToTempPodAfterConfirm(t *testing.T) {
	var created, deleted atomic.Int32
	d := newTUIDriver(t, tui.Config{Engine: forceEngine(&created, &deleted), Namespace: "ns"})
	d.until("PVC list", contains("data-alpha"))
	d.press("s")
	d.until("plain scan error", contains("ERROR"))
	if created.Load() != 0 {
		t.Fatal("plain scan created a temporary pod")
	}
	d.press("f")
	d.until("confirmation", contains("Force scan ns/data-alpha?", "[y/N]"))
	d.press("y")
	d.until("forced scan", contains("DONE"))
	if created.Load() != 1 || deleted.Load() != 1 {
		t.Fatalf("temporary pods created/deleted = %d/%d, want 1/1", created.Load(), deleted.Load())
	}
	// The list keeps the pod that uses the PVC; only the details name the
	// temporary pod that was scanned.
	if !regexp.MustCompile(`alpha-0 +data-alpha`).MatchString(d.screen()) || strings.Contains(d.screen(), "pvdu-tmp") {
		t.Fatalf("POD column changed to the temporary pod:\n%s", d.screen())
	}
	d.press("enter")
	d.until("detail names the temporary pod", contains("pvdu-tmp:/mnt", "temporary pod, deleted after the scan"))
}

func TestTUIForceScanNeedsYes(t *testing.T) {
	var created, deleted atomic.Int32
	d := newTUIDriver(t, tui.Config{Engine: forceEngine(&created, &deleted), Namespace: "ns"})
	d.until("PVC list", contains("data-alpha"))
	d.press("f")
	popup := regexp.MustCompile(`│ Force scan ns/data-alpha\? +│[\s\S]*│ Continue\? \[y/N\] +│`)
	d.until("confirmation pop-up", popup.MatchString)
	d.press("n")
	d.until("cancelled", contains("Force scan cancelled"))
	if strings.Contains(d.screen(), "PREPARING") || created.Load() != 0 {
		t.Fatalf("scan started without confirmation:\n%s", d.screen())
	}
	// Messages sit on the line above the help, not in the header.
	lines := strings.Split(d.screen(), "\n")
	if len(lines) < 2 || !strings.Contains(lines[len(lines)-2], "Force scan cancelled") || strings.Contains(d.screen(), "╭") {
		t.Fatalf("message not at the bottom or pop-up still shown:\n%s", d.screen())
	}
}

func TestTUIFilterNarrowsList(t *testing.T) {
	e := tuiEngine(func(context.Context, string) (*model.ScanResult, error) {
		return &model.ScanResult{Status: model.StatusDone}, nil
	}, engine.Dependencies{})
	d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "ns"})
	d.until("PVC list", contains("data-alpha", "data-beta"))
	d.press("/", "alp", "enter")
	d.until("filtered list", func(s string) bool { return strings.Contains(s, "data-alpha") && !strings.Contains(s, "data-beta") })
	d.press("esc")
	d.until("cleared filter", contains("data-alpha", "data-beta"))
}

func TestTUIScanPrefersMarkedPVCs(t *testing.T) {
	var calls atomic.Int32
	var scanned atomic.Value
	e := tuiEngine(func(_ context.Context, pod string) (*model.ScanResult, error) {
		calls.Add(1)
		scanned.Store(pod)
		return &model.ScanResult{Status: model.StatusDone, UsedBytes: 1}, nil
	}, engine.Dependencies{})
	d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "ns"})
	d.until("PVC list", contains("data-alpha", "data-beta"))
	// Marking data-alpha moves the cursor to data-beta; s still scans the mark.
	d.press("space", "s")
	d.until("marked scan", func(s string) bool { return strings.Count(s, "DONE") == 1 })
	if calls.Load() != 1 || scanned.Load() != "alpha-0" {
		t.Fatalf("scans = %d of %v, want 1 of alpha-0", calls.Load(), scanned.Load())
	}
	if strings.Contains(d.screen(), "●") {
		t.Fatalf("mark kept after scan:\n%s", d.screen())
	}
	// Without marks, s scans the selected PVC.
	d.press("s")
	d.until("selected scan", func(s string) bool { return strings.Count(s, "DONE") == 2 })
}

func TestTUIScanAllIgnoresMarksAndSelection(t *testing.T) {
	var calls atomic.Int32
	e := tuiEngine(func(context.Context, string) (*model.ScanResult, error) {
		calls.Add(1)
		return &model.ScanResult{Status: model.StatusDone, UsedBytes: 1}, nil
	}, engine.Dependencies{})
	d := newTUIDriver(t, tui.Config{Engine: e, Namespace: "ns"})
	d.until("PVC list", contains("data-alpha", "data-beta"))
	d.press("space", "S")
	d.until("all scanned", func(s string) bool { return strings.Count(s, "DONE") == 2 })
	if calls.Load() != 2 || strings.Contains(d.screen(), "●") {
		t.Fatalf("scans = %d, want 2 and no marks:\n%s", calls.Load(), d.screen())
	}
}

func TestTUIForceScanAllAsksOnceForTheNamespace(t *testing.T) {
	var created, deleted atomic.Int32
	d := newTUIDriver(t, tui.Config{Engine: forceEngine(&created, &deleted), Namespace: "ns"})
	d.until("PVC list", contains("data-alpha", "data-beta"))
	d.press("F")
	d.until("confirmation", contains("Force scan all 2 PVCs in ns?", "[y/N]"))
	d.press("y")
	d.until("forced scans", func(s string) bool { return strings.Count(s, "DONE") == 2 })
	if created.Load() != 2 || deleted.Load() != 2 {
		t.Fatalf("temporary pods created/deleted = %d/%d, want 2/2", created.Load(), deleted.Load())
	}
}

func TestTUIForceScanPrefersMarkedPVCs(t *testing.T) {
	var created, deleted atomic.Int32
	d := newTUIDriver(t, tui.Config{Engine: forceEngine(&created, &deleted), Namespace: "ns"})
	d.until("PVC list", contains("data-alpha", "data-beta"))
	d.press("space", "space", "f")
	d.until("confirmation", contains("Force scan 2 marked PVCs?", "[y/N]"))
	d.press("y")
	d.until("forced scans", func(s string) bool { return strings.Count(s, "DONE") == 2 })
	if created.Load() != 2 || deleted.Load() != 2 {
		t.Fatalf("temporary pods created/deleted = %d/%d, want 2/2", created.Load(), deleted.Load())
	}
}

func TestTUIQuitCancelsScanAndWaitsForTempPodCleanup(t *testing.T) {
	scanning := make(chan struct{})
	var deleted atomic.Bool
	// Without mounts the scan uses a temporary pod.
	d := newTUIDriver(t, tui.Config{Engine: engine.New(fake.NewSimpleClientset(), &rest.Config{}, engine.Options{
		Image: "alpine", Timeout: time.Minute, CleanupTimeout: time.Second, Concurrency: 1,
	}, engine.Dependencies{
		ListPVCs: func(context.Context, kubernetes.Interface, string) ([]kube.PVCInfo, error) {
			return []kube.PVCInfo{{Name: "orphan", Namespace: "ns"}}, nil
		},
		CreatePod: func(context.Context, kubernetes.Interface, kube.PVCInfo, string) (kube.PodRef, error) {
			return kube.PodRef{Name: "pvdu-tmp"}, nil
		},
		WaitForPodReady: func(context.Context, kubernetes.Interface, string, string, time.Duration) error { return nil },
		UploadAndScan: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, _, _, _, _, _ string, _ *kube.PVCInfo, _ int, _ []string, _ int) (*model.ScanResult, error) {
			close(scanning)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		DeletePod: func(context.Context, kubernetes.Interface, string, kube.PodRef) error {
			deleted.Store(true)
			return nil
		},
	}), Namespace: "ns"})
	d.until("PVC list", contains("orphan"))
	d.press("s")
	<-scanning
	d.press("q")
	d.untilQuit()
	if !deleted.Load() {
		t.Fatal("quit returned before the temporary pod was deleted")
	}
}
