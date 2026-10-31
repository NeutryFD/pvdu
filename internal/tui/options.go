package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/neutry/pvdu/internal/engine"
)

// settings are what the options pop-up shows and changes: the engine's scan
// options, the log level of the log view and the colour theme.
type settings struct {
	opts     engine.Options
	logLevel string
	theme    string
}

var logLevels = []string{"debug", "info", "warn", "error"}

// optionField is a row of the options pop-up. A field either takes typed
// text (parse) or changes on enter (toggle).
type optionField struct {
	label  string
	value  func(settings) string
	parse  func(*settings, string) error
	toggle func(*settings)
}

var optionFields = []optionField{
	{label: "Image", value: func(s settings) string { return s.opts.Image },
		parse: func(s *settings, v string) error { s.opts.Image = v; return nil }},
	{label: "Concurrency", value: func(s settings) string { return zeroMeans(s.opts.Concurrency, "no limit") },
		parse: func(s *settings, v string) error { return parseInt(v, &s.opts.Concurrency) }},
	{label: "Timeout", value: func(s settings) string { return s.opts.Timeout.String() },
		parse: func(s *settings, v string) error { return parseDuration(v, &s.opts.Timeout) }},
	{label: "Cleanup timeout", value: func(s settings) string { return s.opts.CleanupTimeout.String() },
		parse: func(s *settings, v string) error { return parseDuration(v, &s.opts.CleanupTimeout) }},
	{label: "Max depth", value: func(s settings) string { return zeroMeans(s.opts.MaxDepth, "unlimited") },
		parse: func(s *settings, v string) error { return parseInt(v, &s.opts.MaxDepth) }},
	{label: "Workers", value: func(s settings) string { return zeroMeans(s.opts.Workers, "auto") },
		parse: func(s *settings, v string) error { return parseInt(v, &s.opts.Workers) }},
	{label: "Exclude", value: func(s settings) string { return strings.Join(s.opts.Excludes, ", ") },
		parse: func(s *settings, v string) error {
			s.opts.Excludes = nil
			for p := range strings.SplitSeq(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					s.opts.Excludes = append(s.opts.Excludes, p)
				}
			}
			return nil
		}},
	{label: "Temp-pod fallback", value: func(s settings) string {
		if s.opts.Force {
			return "on: s and S fall back without asking"
		}
		return "off: only f and F fall back"
	}, toggle: func(s *settings) { s.opts.Force = !s.opts.Force }},
	{label: "Log level", value: func(s settings) string { return s.logLevel }, toggle: func(s *settings) {
		i := 0
		for j, l := range logLevels {
			if l == s.logLevel {
				i = j
			}
		}
		s.logLevel = logLevels[(i+1)%len(logLevels)]
	}},
	{label: "Theme", value: func(s settings) string { return s.theme }, toggle: func(s *settings) {
		names := ThemeNames()
		i := 0
		for j, n := range names {
			if n == s.theme {
				i = j
			}
		}
		s.theme = names[(i+1)%len(names)]
	}},
}

func parseInt(v string, dst *int) error {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("%q is not a whole number", v)
	}
	*dst = n
	return nil
}

func parseDuration(v string, dst *time.Duration) error {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("%q is not a duration; use e.g. 90s or 2m", v)
	}
	*dst = d
	return nil
}

func zeroMeans(n int, meaning string) string {
	if n == 0 {
		return "0 (" + meaning + ")"
	}
	return strconv.Itoa(n)
}

// openOptions shows the options pop-up with the settings in use.
func (m *Model) openOptions() {
	level := m.logLevel
	if level == "" {
		level = "warn"
	}
	m.mode = inputOptions
	m.opts = settings{opts: m.cfg.Engine.Options(), logLevel: level, theme: m.theme}
	m.optsCursor, m.optsEditing, m.optsErr = 0, false, ""
}

// applyOption makes a changed setting take effect at once: the engine uses
// it for scans that start from now on, and the log view for new lines.
func (m *Model) applyOption(field optionField, changed settings) tea.Cmd {
	if err := m.cfg.Engine.SetOptions(changed.opts); err != nil {
		m.optsErr = err.Error()
		return nil
	}
	m.opts, m.logLevel, m.optsErr = changed, changed.logLevel, ""
	m.applyTheme(changed.theme)
	return m.say(fmt.Sprintf("%s set to %s", field.label, field.value(changed)))
}

func (m *Model) onOptionsKey(msg tea.KeyPressMsg) tea.Cmd {
	field := optionFields[m.optsCursor]
	if m.optsEditing {
		switch msg.String() {
		case "enter":
			changed := m.opts
			if err := field.parse(&changed, m.input.Value()); err != nil {
				m.optsErr = err.Error()
				return nil
			}
			cmd := m.applyOption(field, changed)
			if m.optsErr == "" {
				m.optsEditing = false
				m.input.Blur()
			}
			return cmd
		case "esc":
			m.optsEditing, m.optsErr = false, ""
			m.input.Blur()
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return cmd
		}
		return nil
	}
	switch msg.String() {
	case "up", "k":
		m.optsCursor = max(0, m.optsCursor-1)
	case "down", "j":
		m.optsCursor = min(len(optionFields)-1, m.optsCursor+1)
	case "enter", "space":
		if field.toggle != nil {
			changed := m.opts
			field.toggle(&changed)
			return m.applyOption(field, changed)
		}
		m.optsEditing, m.optsErr = true, ""
		m.input.Prompt = ""
		// Edit the raw value, not its description such as "0 (auto)".
		value := field.value(m.opts)
		value, _, _ = strings.Cut(value, " (")
		m.input.SetValue(value)
		m.input.CursorEnd()
		return m.input.Focus()
	case "esc", "q", "O":
		m.mode = inputNone
	}
	return nil
}

func (m *Model) optionsPopup() string {
	if m.mode != inputOptions {
		return ""
	}
	labelWidth := 0
	for _, f := range optionFields {
		labelWidth = max(labelWidth, lipgloss.Width(f.label))
	}
	lines := []string{m.st.popupTitle.Render("Options"), m.st.label.Render("Changes apply at once to new scans."), ""}
	for i, f := range optionFields {
		value := f.value(m.opts)
		if value == "" {
			value = "-"
		}
		if i == m.optsCursor && m.optsEditing {
			value = m.input.View()
		}
		row := fmt.Sprintf("%-*s  %s", labelWidth, f.label, value)
		if i == m.optsCursor {
			lines = append(lines, m.st.selected.Render("› "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	lines = append(lines, "")
	if m.optsErr != "" {
		lines = append(lines, m.st.fail.Render(m.optsErr))
	}
	hint := "↑/↓ move · enter edit · esc close"
	if m.optsEditing {
		hint = "enter apply · esc cancel"
	} else if optionFields[m.optsCursor].toggle != nil {
		hint = "↑/↓ move · enter change · esc close"
	}
	lines = append(lines, m.st.label.Render(hint))
	return m.st.dialogBox.MaxWidth(max(30, m.width-2)).Render(strings.Join(lines, "\n"))
}
