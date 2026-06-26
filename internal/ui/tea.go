package ui

import (
	"fmt"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/neutry/pvdu/internal/model"
)

type state int

const (
	stateDiscovering state = iota
	stateScanning
	stateDone
)

func InitialModel(results []*model.ScanResult, ns string, allNs bool) tea.Model {
	nsLabel := ns
	if ns == "" && allNs {
		nsLabel = "all namespaces"
	}

	return &modelUI{
		state:   stateDiscovering,
		results: results,
		ns:      nsLabel,
		lines:   make(map[string]string),
	}
}

type modelUI struct {
	state   state
	results []*model.ScanResult
	ns      string
	lines   map[string]string
	mu      sync.Mutex
}

func (m *modelUI) Init() tea.Cmd {
	return nil
}

func (m *modelUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}
		if m.state == stateDone {
			return m, tea.Quit
		}
		return m, nil
	case model.DiscoveredMsg:
		m.state = stateScanning
		return m, nil
	case model.ProgressUpdate:
		m.mu.Lock()
		if msg.Err != "" {
			m.lines[msg.PVCName] = fmt.Sprintf("%s  error: %s", msg.PVCName, msg.Err)
		} else if msg.Done {
			m.lines[msg.PVCName] = fmt.Sprintf("%s  done  %s", msg.PVCName, formatBytesShort(msg.Size))
		} else {
			m.lines[msg.PVCName] = fmt.Sprintf("%s  scanning...  %s  %s", msg.PVCName, msg.Path, formatBytesShort(msg.Size))
		}
		m.mu.Unlock()
		return m, nil
	case model.ScanDoneMsg:
		m.state = stateDone
		return m, nil
	}
	return m, nil
}

func (m *modelUI) View() string {
	switch m.state {
	case stateDiscovering:
		return m.viewDiscovering()
	case stateScanning:
		return m.viewScanning()
	case stateDone:
		return m.viewDone()
	}
	return ""
}

func (m *modelUI) viewDiscovering() string {
	header := headerStyle.Render(fmt.Sprintf(" pvdu - scanning PVCs in %s ", m.ns))
	count := len(m.results)
	return fmt.Sprintf("%s\n\n  Discovering PVCs... found %d PVCs\n\n  Press q or Ctrl+C to quit\n", header, count)
}

func (m *modelUI) viewScanning() string {
	header := headerStyle.Render(fmt.Sprintf(" pvdu - scanning PVCs in %s ", m.ns))
	m.mu.Lock()
	var lines []string
	for _, r := range m.results {
		if line, ok := m.lines[r.PVCName]; ok {
			lines = append(lines, "  "+line)
		} else {
			lines = append(lines, fmt.Sprintf("  %s  waiting...", r.PVCName))
		}
	}
	m.mu.Unlock()
	return fmt.Sprintf("%s\n\n%s\n\n  Press q or Ctrl+C to quit\n", header, strings.Join(lines, "\n"))
}

func (m *modelUI) viewDone() string {
	return RenderTable(m.results) + "\n"
}

func RenderTable(results []*model.ScanResult) string {
	var b strings.Builder

	hdrStyle := lipgloss.NewStyle().Bold(true)

	colWidths := map[string]int{
		"ns":   12,
		"pvc":  24,
		"req":  12,
		"pv":   12,
		"used": 12,
		"pct":  8,
	}

	for _, r := range results {
		if len(r.Namespace) > colWidths["ns"] {
			colWidths["ns"] = len(r.Namespace)
		}
		if len(r.PVCName) > colWidths["pvc"] {
			colWidths["pvc"] = len(r.PVCName)
		}
	}

	row := func(cols ...string) {
		fmt.Fprintf(&b, "|")
		for _, c := range cols {
			fmt.Fprintf(&b, " %s |", c)
		}
		fmt.Fprintf(&b, "\n")
	}

	sep := func() {
		fmt.Fprintf(&b, "+")
		for _, w := range []int{colWidths["ns"], colWidths["pvc"], colWidths["req"], colWidths["pv"], colWidths["used"], colWidths["pct"]} {
			fmt.Fprintf(&b, "%s+", strings.Repeat("-", w+2))
		}
		fmt.Fprintf(&b, "\n")
	}

	rpad := func(s string, w int) string {
		if len(s) >= w {
			return s
		}
		return s + strings.Repeat(" ", w-len(s))
	}

	lpad := func(s string, w int) string {
		if len(s) >= w {
			return s
		}
		return strings.Repeat(" ", w-len(s)) + s
	}

	center := func(s string, w int) string {
		if len(s) >= w {
			return s
		}
		left := (w - len(s)) / 2
		right := w - len(s) - left
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
	}

	sep()
	row(
		center("NAMESPACE", colWidths["ns"]),
		center("PVC", colWidths["pvc"]),
		rpad("REQUESTED", colWidths["req"]),
		rpad("PV SIZE", colWidths["pv"]),
		rpad("USED", colWidths["used"]),
		rpad("%", colWidths["pct"]),
	)
	sep()

	for _, r := range results {
		req := r.RequestedStr
		if req == "" {
			req = "-"
		}
		pvStr := "-"
		if r.PVBytes > 0 {
			pvStr = formatBytesShort(r.PVBytes)
		}
		usedStr := "-"
		pctStr := "-"
		if r.Status == model.StatusDone && r.UsedBytes >= 0 {
			usedStr = formatBytesShort(r.UsedBytes)
			if r.RequestedBytes > 0 {
				pct := float64(r.UsedBytes) / float64(r.RequestedBytes) * 100
				pctStr = fmt.Sprintf("%.0f%%", pct)
			}
		} else if r.Status == model.StatusError {
			usedStr = "ERROR"
		}

		row(
			r.Namespace,
			r.PVCName,
			lpad(req, colWidths["req"]),
			lpad(pvStr, colWidths["pv"]),
			lpad(usedStr, colWidths["used"]),
			lpad(pctStr, colWidths["pct"]),
		)
	}
	sep()

	return hdrStyle.Render(b.String())
}

func formatBytesShort(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ci", float64(b)/float64(div), "KMGTPE"[exp])
}
