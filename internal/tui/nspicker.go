package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// maxPickerRows bounds the namespaces shown at once in the picker.
const maxPickerRows = 10

type namespacesMsg struct {
	names []string
	err   error
}

// nsOption is a picker entry; value "" stands for every namespace.
type nsOption struct{ label, value string }

// openNamespacePicker shows the namespace pop-up and lists the cluster's
// namespaces again, so new ones appear.
func (m *Model) openNamespacePicker() tea.Cmd {
	m.mode = inputNamespace
	m.nsLoading, m.nsErr, m.nsCursor = true, nil, 0
	m.input.Prompt = "› "
	m.input.SetValue("")
	list := func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, m.cfg.ListTimeout)
		defer cancel()
		names, err := m.cfg.Engine.ListNamespaces(ctx)
		return namespacesMsg{names: names, err: err}
	}
	return tea.Batch(m.input.Focus(), list, m.startTicking())
}

func (m *Model) onNamespaces(msg namespacesMsg) {
	m.nsLoading = false
	m.nsNames, m.nsErr = msg.names, msg.err
	// Start on the namespace being shown.
	for i, o := range m.nsOptions() {
		if o.value == m.namespace {
			m.nsCursor = i
		}
	}
}

// nsOptions are the namespaces matching the typed text, after the entry for
// every namespace.
func (m *Model) nsOptions() []nsOption {
	needle := strings.ToLower(strings.TrimSpace(m.input.Value()))
	var options []nsOption
	if strings.Contains("all namespaces", needle) {
		options = append(options, nsOption{label: "all namespaces", value: ""})
	}
	for _, name := range m.nsNames {
		if strings.Contains(strings.ToLower(name), needle) {
			options = append(options, nsOption{label: name, value: name})
		}
	}
	return options
}

func (m *Model) onPickerKey(msg tea.KeyPressMsg) tea.Cmd {
	options := m.nsOptions()
	switch msg.String() {
	case "up", "ctrl+p":
		m.nsCursor = max(0, m.nsCursor-1)
		return nil
	case "down", "ctrl+n":
		m.nsCursor = min(max(0, len(options)-1), m.nsCursor+1)
		return nil
	case "esc":
		m.closeInput()
		return nil
	case "enter":
		// Without a match, such as when namespaces cannot be listed, the
		// typed text is the namespace.
		value := strings.TrimSpace(m.input.Value())
		if len(options) > 0 {
			value = options[min(m.nsCursor, len(options)-1)].value
		} else if value == "" {
			return nil
		}
		m.closeInput()
		return m.switchNamespace(value)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.nsCursor = 0
	return cmd
}

func (m *Model) switchNamespace(namespace string) tea.Cmd {
	if namespace == m.namespace {
		return nil
	}
	m.namespace, m.filter = namespace, ""
	m.order, m.visible = nil, nil
	m.refresh()
	return tea.Batch(m.load(), m.startTicking())
}

func (m *Model) namespacePopup() string {
	if m.mode != inputNamespace {
		return ""
	}
	lines := []string{m.st.popupTitle.Render("Namespace"), m.input.View(), ""}
	options := m.nsOptions()
	switch {
	case m.nsLoading:
		lines = append(lines, m.st.busy.Render(busy.frame(m.elapsed()))+" Loading namespaces…")
	case m.nsErr != nil:
		lines = append(lines, m.st.fail.Render("Cannot list namespaces: "+m.nsErr.Error()), "Type a namespace and press enter.")
	case len(options) == 0:
		lines = append(lines, m.st.label.Render("No match; enter switches to the typed namespace."))
	default:
		width := 0
		for _, o := range options {
			width = max(width, lipgloss.Width(o.label))
		}
		// Keep the cursor inside the visible window.
		start := min(max(0, m.nsCursor-maxPickerRows+1), max(0, len(options)-maxPickerRows))
		end := min(len(options), start+maxPickerRows)
		if start > 0 {
			lines = append(lines, m.st.label.Render(fmt.Sprintf("  ↑ %d more", start)))
		}
		for i := start; i < end; i++ {
			o := options[i]
			label := fmt.Sprintf("%-*s", width, o.label)
			if o.value == m.namespace {
				label += m.st.label.Render("  current")
			} else {
				label += "         "
			}
			if i == m.nsCursor {
				lines = append(lines, m.st.selected.Render("› "+label))
			} else {
				lines = append(lines, "  "+label)
			}
		}
		if end < len(options) {
			lines = append(lines, m.st.label.Render(fmt.Sprintf("  ↓ %d more", len(options)-end)))
		}
	}
	lines = append(lines, "", m.st.label.Render("type to filter · ↑/↓ choose · enter switch · esc cancel"))
	return m.st.dialogBox.MaxWidth(max(30, m.width-2)).Render(strings.Join(lines, "\n"))
}
