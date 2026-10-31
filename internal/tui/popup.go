package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// overlay draws the pop-ups centred over screen: list loading, and above it
// the namespace picker or a confirmation dialog.
func (m *Model) overlay(screen string) string {
	layers := []*lipgloss.Layer{lipgloss.NewLayer(screen)}
	for z, box := range []string{m.progressPopup(), m.namespacePopup(), m.optionsPopup(), m.confirmPopup()} {
		if box == "" {
			continue
		}
		x := max(0, (m.width-lipgloss.Width(box))/2)
		y := max(0, (m.height-lipgloss.Height(box))/2)
		layers = append(layers, lipgloss.NewLayer(box).X(x).Y(y).Z(z+1))
	}
	if len(layers) == 1 {
		return screen
	}
	return lipgloss.NewCompositor(layers...).Render()
}

// progressPopup shows that the PVC list of the namespace is loading. Usage
// scans show their progress in their own row instead.
func (m *Model) progressPopup() string {
	if !m.loading {
		return ""
	}
	ns := m.namespace
	if ns == "" {
		ns = "all namespaces"
	}
	text := m.st.busy.Render(busy.frame(m.elapsed())) + " Loading PVCs in " + ns + "…"
	return m.st.progressBox.MaxWidth(max(20, m.width-2)).Render(text)
}

func (m *Model) confirmPopup() string {
	if m.mode != inputConfirmForce {
		return ""
	}
	question := fmt.Sprintf("Force scan %d marked PVCs?", len(m.confirm))
	detail := "If exec into their pods fails, pvdu creates temporary pods."
	switch {
	case m.confirmAll:
		where := "in " + m.namespace
		if m.namespace == "" {
			where = "in all namespaces"
		}
		if m.filter != "" {
			where += " matching " + m.filter
		}
		question = fmt.Sprintf("Force scan all %d PVCs %s?", len(m.confirm), where)
	case len(m.confirm) == 1:
		ns, _, _ := strings.Cut(m.confirm[0], "/")
		question = fmt.Sprintf("Force scan %s?", m.confirm[0])
		detail = "If exec into its pod fails, pvdu creates a temporary pod in " + ns + "."
	}
	width := min(60, max(20, m.width-6))
	body := lipgloss.JoinVertical(lipgloss.Left,
		m.st.popupTitle.Render(question),
		"",
		lipgloss.NewStyle().Width(width).Render(detail),
		"",
		m.st.message.Render("Continue? [y/N]"),
	)
	return m.st.confirmBox.Render(body)
}
