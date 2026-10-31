package tui

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/neutry/pvdu/internal/engine"
	"github.com/neutry/pvdu/internal/kube"
	"github.com/neutry/pvdu/internal/model"
)

const maxLogLines = 1000

type screen int

const (
	screenList screen = iota
	screenDetail
	screenLogs
)

type inputMode int

const (
	inputNone inputMode = iota
	inputFilter
	inputNamespace
	// inputConfirmForce waits for y before a scan that may create a pod.
	inputConfirmForce
	inputOptions
)

// entry is a PVC and its latest scan. The engine owns the result being
// scanned; entry only keeps the snapshots it receives.
type entry struct {
	pvc       kube.PVCInfo
	result    model.ScanResult
	scanID    int
	running   bool
	cancel    context.CancelFunc
	marked    bool
	scannedAt time.Time
}

type (
	pvcsMsg struct {
		gen  int
		pvcs []kube.PVCInfo
		err  error
	}
	scanEventMsg struct {
		key   string
		id    int
		event engine.Event
	}
	scanDoneMsg struct {
		key    string
		id     int
		ok     bool
		result model.ScanResult
	}
	logMsg  string
	quitMsg struct{}
)

// Model is the Bubble Tea model of the TUI. Scan workers talk to it only
// through bus; every field is owned by the Update goroutine.
type Model struct {
	ctx    context.Context
	cancel context.CancelFunc
	cfg    Config

	bus       chan tea.Msg
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	namespace string
	gen       int
	loading   bool
	entries   map[string]*entry
	order     []string
	visible   []string
	filter    string
	selectPVC string

	table   table.Model
	input   textinput.Model
	mode    inputMode
	confirm []string
	// confirmAll marks a confirmation for every listed PVC.
	confirmAll bool
	help       help.Model
	keys       keyMap
	screen     screen
	logs       []string
	logShift   int
	flash      string
	flashID    int

	logLevel string
	theme    string
	st       styles

	opts        settings
	optsCursor  int
	optsEditing bool
	optsErr     string

	nsNames   []string
	nsLoading bool
	nsErr     error
	nsCursor  int

	nextID        int
	running       int
	quitting      bool
	width, height int

	ticking      bool
	started, now time.Time
}

func New(ctx context.Context, cfg Config) *Model {
	ctx, cancel := context.WithCancel(ctx)
	if cfg.ListTimeout <= 0 {
		cfg.ListTimeout = 30 * time.Second
	}
	m := &Model{
		ctx: ctx, cancel: cancel, cfg: cfg,
		bus: make(chan tea.Msg, 256), done: make(chan struct{}),
		namespace: cfg.Namespace, selectPVC: cfg.PVC, entries: map[string]*entry{},
		table: table.New(table.WithFocused(true), table.WithKeyMap(tableKeyMap())),
		input: textinput.New(), help: help.New(), keys: newKeyMap(),
		started: time.Now(), logLevel: cfg.LogLevel,
	}
	m.applyTheme(cfg.Theme)
	m.now = m.started
	return m
}

func (m *Model) Init() tea.Cmd {
	// load must run first: it starts the loading state the clock animates.
	return tea.Batch(m.load(), m.startTicking(), m.listen(), m.watchContext())
}

// Close cancels every scan and waits until their temporary pods are deleted.
// Run calls it after the program exits, whatever the reason.
func (m *Model) Close() {
	m.cancel()
	m.closeOnce.Do(func() { close(m.done) })
	m.wg.Wait()
}

// Logf adds a line to the log view. It is safe to call from any goroutine
// except the Update goroutine, which appends directly.
func (m *Model) Logf(format string, args ...any) {
	line := time.Now().Format("15:04:05 ") + fmt.Sprintf(format, args...)
	select {
	case m.bus <- logMsg(line):
	case <-m.done:
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
	case pvcsMsg:
		return m, m.onPVCs(msg)
	case clearFlashMsg:
		if int(msg) == m.flashID {
			m.flash = ""
		}
	case scanEventMsg:
		m.onEvent(msg)
		return m, m.listen()
	case logMsg:
		// Client-go only logs warnings and errors here.
		if slog.LevelWarn >= m.minLogLevel() {
			m.appendLog(string(msg))
		}
		return m, m.listen()
	case scanDoneMsg:
		return m, m.onDone(msg)
	case quitMsg:
		return m, m.quit()
	case tickMsg:
		return m, m.onTick(time.Time(msg))
	case namespacesMsg:
		m.onNamespaces(msg)
	case tea.KeyPressMsg:
		return m, m.onKey(msg)
	}
	return m, nil
}

// listen delivers the next message from scan workers or klog.
func (m *Model) listen() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-m.bus:
			return msg
		case <-m.done:
			return nil
		}
	}
}

func (m *Model) watchContext() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
			return quitMsg{}
		case <-m.done:
			return nil
		}
	}
}

func (m *Model) load() tea.Cmd {
	m.gen++
	m.loading = true
	gen, ns := m.gen, m.namespace
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, m.cfg.ListTimeout)
		defer cancel()
		pvcs, err := m.cfg.Engine.ListPVCs(ctx, ns)
		return pvcsMsg{gen: gen, pvcs: pvcs, err: err}
	}
}

func (m *Model) onPVCs(msg pvcsMsg) tea.Cmd {
	// A namespace switch makes older lists stale.
	if msg.gen != m.gen {
		return nil
	}
	m.loading = false
	if msg.err != nil {
		if m.ctx.Err() == nil {
			text := "list PVCs: " + msg.err.Error()
			m.logAt(slog.LevelError, text)
			return m.say(text)
		}
		return nil
	}
	m.order = m.order[:0]
	for _, pvc := range msg.pvcs {
		k := pvcKey(pvc.Namespace, pvc.Name)
		m.order = append(m.order, k)
		if e, ok := m.entries[k]; ok {
			e.pvc = pvc
			continue
		}
		result := engine.NewResult(pvc)
		result.Status = ""
		m.entries[k] = &entry{pvc: pvc, result: *result}
	}
	m.refresh()
	m.applySelection()
	return nil
}

// flashDuration is how long a message stays at the bottom of the screen.
const flashDuration = 5 * time.Second

type clearFlashMsg int

// say shows text at the bottom of the screen until a newer message replaces
// it or flashDuration passes.
func (m *Model) say(text string) tea.Cmd {
	m.flashID++
	m.flash = text
	id := m.flashID
	return tea.Tick(flashDuration, func(time.Time) tea.Msg { return clearFlashMsg(id) })
}

// applySelection moves the cursor to the PVC named by --pvc once the table
// has both rows and columns.
func (m *Model) applySelection() {
	if m.selectPVC == "" || len(m.table.Columns()) == 0 || m.loading {
		return
	}
	for i, k := range m.visible {
		if m.entries[k].pvc.Name == m.selectPVC {
			m.table.SetCursor(i)
			break
		}
	}
	m.selectPVC = ""
}

func (m *Model) onEvent(msg scanEventMsg) {
	ev := msg.event
	switch ev.Kind {
	case engine.EventStatus:
		// Scan events and the final result travel separately, so snapshots
		// can arrive after the result and must not replace it.
		if e := m.entries[msg.key]; e != nil && e.scanID == msg.id && e.running {
			e.result = ev.Result
			m.refresh()
		}
	case engine.EventDetail:
		m.logAt(slog.LevelDebug, msg.key+": "+ev.Detail)
	case engine.EventLog:
		m.logAt(ev.Level, ev.Message)
	}
}

// startScan scans the PVC with key k. A forced scan may fall back to a
// temporary pod when exec into the mounting pod fails, as with --force.
func (m *Model) startScan(k string, force bool) tea.Cmd {
	e := m.entries[k]
	if e == nil || e.running || m.quitting {
		return nil
	}
	m.nextID++
	id := m.nextID
	ctx, cancel := context.WithCancel(m.ctx)
	pvc := e.pvc
	result := engine.NewResult(pvc)
	e.scanID, e.running, e.cancel, e.result = id, true, cancel, *result
	m.running++
	m.wg.Add(1)
	handle := func(ev engine.Event) {
		select {
		case m.bus <- scanEventMsg{key: k, id: id, event: ev}:
		case <-m.done:
		}
	}
	scan := func() tea.Msg {
		defer m.wg.Done()
		defer cancel()
		scan := m.cfg.Engine.Scan
		if force {
			scan = m.cfg.Engine.ScanForced
		}
		ok := scan(ctx, id, result, pvc, handle)
		return scanDoneMsg{key: k, id: id, ok: ok, result: *result}
	}
	return tea.Batch(scan, m.startTicking())
}

func (m *Model) onDone(msg scanDoneMsg) tea.Cmd {
	m.running--
	if e := m.entries[msg.key]; e != nil && e.scanID == msg.id {
		e.running, e.cancel = false, nil
		e.result = msg.result
		if !msg.ok {
			e.result.Status, e.result.Error = model.StatusError, "cancelled before it started"
		}
		e.scannedAt = time.Now()
		if e.result.Status == model.StatusError {
			m.logAt(slog.LevelError, msg.key+": "+e.result.Error)
		}
		for _, w := range e.result.Warnings {
			m.logAt(slog.LevelWarn, msg.key+": "+w)
		}
		m.refresh()
	}
	if m.quitting && m.running == 0 {
		return tea.Quit
	}
	return nil
}

// targets are the marked visible PVCs, or the selected one when none is
// marked.
func (m *Model) targets() []string {
	var marked []string
	for _, k := range m.visible {
		if m.entries[k].marked {
			marked = append(marked, k)
		}
	}
	if len(marked) > 0 {
		return marked
	}
	if k := m.selected(); k != "" {
		return []string{k}
	}
	return nil
}

// scanMany scans keys and clears their marks.
func (m *Model) scanMany(keys []string, force bool) tea.Cmd {
	var cmds []tea.Cmd
	for _, k := range keys {
		if e := m.entries[k]; e != nil {
			e.marked = false
		}
		cmds = append(cmds, m.startScan(k, force))
	}
	m.refresh()
	return tea.Batch(cmds...)
}

// askForce asks before a forced scan of the idle PVCs among keys, because it
// may create pods in the cluster. all says keys are the whole list, which
// the question names.
func (m *Model) askForce(keys []string, all bool) {
	var idle []string
	for _, k := range keys {
		if e := m.entries[k]; e != nil && !e.running {
			idle = append(idle, k)
		}
	}
	if len(idle) > 0 {
		m.mode, m.confirm, m.confirmAll = inputConfirmForce, idle, all
	}
}

func (m *Model) quit() tea.Cmd {
	if !m.quitting {
		m.quitting = true
		for _, e := range m.entries {
			if e.cancel != nil {
				e.cancel()
			}
		}
	}
	if m.running == 0 {
		return tea.Quit
	}
	// Stays until exit: no clearing tick, unlike say.
	m.flashID++
	m.flash = fmt.Sprintf("Stopping %d scan(s) and deleting temporary pods…", m.running)
	return nil
}

func (m *Model) onKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+c" {
		return m.quit()
	}
	if m.quitting {
		return nil
	}
	if m.mode != inputNone {
		return m.onInputKey(msg)
	}
	switch m.screen {
	case screenDetail:
		return m.onDetailKey(msg)
	case screenLogs:
		return m.onLogsKey(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.quit()
	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.layout()
	case key.Matches(msg, m.keys.Scan):
		return m.scanMany(m.targets(), false)
	case key.Matches(msg, m.keys.Force):
		m.askForce(m.targets(), false)
	case key.Matches(msg, m.keys.ForceAll):
		m.askForce(m.visible, true)
	case key.Matches(msg, m.keys.Mark):
		if e := m.entries[m.selected()]; e != nil {
			e.marked = !e.marked
			m.refresh()
			m.table.MoveDown(1)
		}
	case key.Matches(msg, m.keys.ScanAll):
		return m.scanMany(m.visible, false)
	case key.Matches(msg, m.keys.Cancel):
		if e := m.entries[m.selected()]; e != nil && e.cancel != nil {
			e.cancel()
		}
	case key.Matches(msg, m.keys.Open):
		if m.selected() != "" {
			m.screen = screenDetail
		}
	case key.Matches(msg, m.keys.Filter):
		return m.openInput(inputFilter, "filter: ", m.filter)
	case key.Matches(msg, m.keys.Namespace):
		return m.openNamespacePicker()
	case key.Matches(msg, m.keys.Options):
		m.openOptions()
	case key.Matches(msg, m.keys.Refresh):
		return tea.Batch(m.load(), m.startTicking())
	case key.Matches(msg, m.keys.Logs):
		m.screen, m.logShift = screenLogs, 0
	case msg.String() == "esc" && m.filter != "":
		m.filter = ""
		m.refresh()
	default:
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return cmd
	}
	return nil
}

func (m *Model) onDetailKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.Quit):
		m.screen = screenList
	case key.Matches(msg, m.keys.Scan):
		return m.startScan(m.selected(), false)
	case key.Matches(msg, m.keys.Force):
		m.askForce([]string{m.selected()}, false)
	case key.Matches(msg, m.keys.Cancel):
		if e := m.entries[m.selected()]; e != nil && e.cancel != nil {
			e.cancel()
		}
	case key.Matches(msg, m.keys.Logs):
		m.screen, m.logShift = screenLogs, 0
	}
	return nil
}

func (m *Model) onLogsKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.Logs), key.Matches(msg, m.keys.Quit):
		m.screen = screenList
	case key.Matches(msg, m.keys.Up):
		m.logShift = min(m.logShift+1, max(0, len(m.logs)-1))
	case key.Matches(msg, m.keys.Down):
		m.logShift = max(0, m.logShift-1)
	}
	return nil
}

func (m *Model) openInput(mode inputMode, prompt, value string) tea.Cmd {
	m.mode = mode
	m.input.Prompt = prompt
	m.input.SetValue(value)
	m.input.CursorEnd()
	return m.input.Focus()
}

func (m *Model) onInputKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.mode == inputConfirmForce {
		keys := m.confirm
		m.mode, m.confirm = inputNone, nil
		if msg.String() == "y" || msg.String() == "Y" {
			return m.scanMany(keys, true)
		}
		return m.say("Force scan cancelled")
	}
	if m.mode == inputNamespace {
		return m.onPickerKey(msg)
	}
	if m.mode == inputOptions {
		return m.onOptionsKey(msg)
	}
	switch msg.String() {
	case "enter":
		m.closeInput()
		return nil
	case "esc":
		if m.mode == inputFilter {
			m.filter = ""
			m.refresh()
		}
		m.closeInput()
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.mode == inputFilter {
		m.filter = m.input.Value()
		m.refresh()
	}
	return cmd
}

func (m *Model) closeInput() {
	m.mode = inputNone
	m.input.Blur()
}

// logAt adds a timestamped line to the log view when level reaches the log
// level.
func (m *Model) logAt(level slog.Level, text string) {
	if level >= m.minLogLevel() {
		m.appendLog(time.Now().Format("15:04:05 ") + level.String() + " " + text)
	}
}

// minLogLevel is the lowest level the log view keeps; like --log-level, it
// defaults to warn.
func (m *Model) minLogLevel() slog.Level {
	switch m.logLevel {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	}
	return slog.LevelWarn
}

func (m *Model) appendLog(line string) {
	m.logs = append(m.logs, line)
	if len(m.logs) > maxLogLines {
		m.logs = m.logs[len(m.logs)-maxLogLines:]
	}
}

func (m *Model) selected() string {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.visible) {
		return ""
	}
	return m.visible[i]
}

func pvcKey(namespace, name string) string { return namespace + "/" + name }
