package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NeutryFD/dirwalker"
	"github.com/neutry/pvdu/internal/model"
	"github.com/neutry/pvdu/internal/render"
)

// The header is two lines: the title and a line for input or messages.
const headerLines = 2

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "pvdu"
	return v
}

func (m *Model) render() string {
	if m.width == 0 {
		return "Loading…"
	}
	var body string
	switch m.screen {
	case screenDetail:
		body = m.detailView()
	case screenLogs:
		body = m.logsView()
	default:
		body = m.table.View()
	}
	message := ""
	if m.flash != "" {
		message = m.st.message.Render(m.flash)
	}
	return m.overlay(lipgloss.JoinVertical(lipgloss.Left, m.headerView(), clip(body, m.bodyHeight()), message, m.helpView()))
}

// helpView clips the help to the screen width. Bubbles' help truncates the
// short help only when its ellipsis fits too, and otherwise prints every key.
func (m *Model) helpView() string {
	return lipgloss.NewStyle().MaxWidth(m.width).Render(m.help.View(m.keys))
}

// footerHeight is the message line plus the help.
func (m *Model) footerHeight() int { return 1 + lipgloss.Height(m.helpView()) }

func (m *Model) headerView() string {
	ns := m.namespace
	if ns == "" {
		ns = "all"
	}
	context := m.cfg.Context
	if context == "" {
		context = "current"
	}
	title := m.st.title.Render("pvdu") + m.st.label.Render("  context ") + context + m.st.label.Render("  namespace ") + ns +
		m.st.label.Render("  PVCs ") + fmt.Sprint(len(m.visible))
	// Progress and confirmations are pop-ups and messages sit at the bottom;
	// this line holds text input and the active filter.
	var status string
	switch {
	case m.mode == inputFilter:
		status = m.input.View()
	case m.filter != "":
		status = m.st.label.Render("filter: " + m.filter + "  (esc clears)")
	}
	return title + "\n" + status
}

func (m *Model) bodyHeight() int {
	return max(1, m.height-headerLines-m.footerHeight())
}

func (m *Model) layout() {
	if m.width == 0 {
		return
	}
	m.help.SetWidth(m.width)
	m.table.SetWidth(m.width)
	m.table.SetHeight(m.bodyHeight())
	m.refresh()
	m.applySelection()
}

func (m *Model) allNamespaces() bool { return m.namespace == "" }

const minNameWidth = 8

// columns fits the table to the terminal width. It hides name columns in
// priority order until the rest get minNameWidth, then shares the width left by
// the fixed columns between them in proportion to their longest value, so
// the table always spans the screen. It returns the columns and the row
// indices they show. Each cell has one space of padding on each side.
func (m *Model) columns(rows []table.Row) ([]table.Column, []int) {
	schema := render.Columns(m.allNamespaces())
	keep, fixed, drops := []int{}, []int{}, []int{}
	fixedWidth := 0
	for i, column := range schema {
		if column.Width > 0 {
			fixed = append(fixed, i)
			fixedWidth += column.Width + 2
		} else {
			keep = append(keep, i)
		}
		if column.HidePriority > 0 {
			drops = append(drops, i)
		}
	}
	slices.SortFunc(drops, func(a, b int) int { return schema[a].HidePriority - schema[b].HidePriority })
	freeFor := func(names int) int {
		return m.width - fixedWidth - 2*names
	}
	for _, drop := range drops {
		if freeFor(len(keep)) >= len(keep)*minNameWidth {
			break
		}
		keep = slices.DeleteFunc(keep, func(i int) bool { return i == drop })
	}
	widths := make([]int, len(keep))
	total := 0
	for j, i := range keep {
		widths[j] = lipgloss.Width(schema[i].Title)
		for _, row := range rows {
			widths[j] = max(widths[j], lipgloss.Width(row[i]))
		}
		total += widths[j]
	}
	if free := freeFor(len(keep)); free != total && free >= len(keep)*minNameWidth {
		used := 0
		for j := range widths {
			widths[j] = max(minNameWidth, widths[j]*free/total)
			used += widths[j]
		}
		// The minimum can overshoot the share; the widest columns give the
		// excess back. Rounding can fall short; the last column takes that.
		for ; used > free; used-- {
			widest := 0
			for j := range widths {
				if widths[j] > widths[widest] {
					widest = j
				}
			}
			widths[widest]--
		}
		widths[len(widths)-1] += free - used
	}
	columns := make([]table.Column, 0, len(keep)+len(fixed))
	for j, i := range keep {
		columns = append(columns, table.Column{Title: schema[i].Title, Width: widths[j]})
	}
	for _, i := range fixed {
		keep = append(keep, i)
		columns = append(columns, table.Column{Title: schema[i].Title, Width: schema[i].Width})
	}
	return columns, keep
}

// refresh recomputes the visible PVCs, table rows and columns, keeping the
// cursor on the same PVC when it is still visible.
func (m *Model) refresh() {
	current := m.selected()
	needle := strings.ToLower(m.filter)
	m.visible = m.visible[:0]
	for _, k := range m.order {
		if needle == "" || strings.Contains(strings.ToLower(k), needle) {
			m.visible = append(m.visible, k)
		}
	}
	rows := make([]table.Row, 0, len(m.visible))
	cursor := min(m.table.Cursor(), max(0, len(m.visible)-1))
	for i, k := range m.visible {
		if k == current {
			cursor = i
		}
		rows = append(rows, m.row(m.entries[k]))
	}
	if m.width == 0 {
		return
	}
	columns, keep := m.columns(rows)
	for i, row := range rows {
		shown := make(table.Row, len(keep))
		for j, k := range keep {
			shown[j] = row[k]
		}
		rows[i] = shown
	}
	// Clear the rows first: the table renders old rows with new columns, and
	// the number of columns changes with the namespace and the width.
	m.table.SetRows(nil)
	m.table.SetColumns(columns)
	m.table.SetRows(rows)
	m.table.SetCursor(cursor)
}

func (m *Model) row(e *entry) table.Row {
	columns := render.Columns(m.allNamespaces())
	row := make(table.Row, len(columns))
	for i, column := range columns {
		row[i] = column.Value(e.result)
		switch column.Title {
		case "PVC":
			prefix := "  "
			if e.marked {
				prefix = "● "
			}
			row[i] = prefix + row[i]
		case "STATUS":
			row[i] = m.statusLabel(e)
		}
	}
	return row
}

func (m *Model) statusLabel(e *entry) string {
	label := render.StatusLabel(e.result)
	// Table cells are plain text: styling would break the selected row's
	// highlight, so the animation keeps the cell colour.
	active := e.result.Status != "" && e.result.Status != model.StatusPending
	if e.running && active && e.result.Status != model.StatusDone && e.result.Status != model.StatusError {
		return busy.frame(m.elapsed()) + " " + label
	}
	return label
}

func (m *Model) detailView() string {
	e := m.entries[m.selected()]
	if e == nil {
		return "No PVC selected."
	}
	p, r := e.pvc, e.result
	var b strings.Builder
	line := func(label, value string) {
		fmt.Fprintf(&b, "%s %s\n", m.st.label.Render(fmt.Sprintf("%-12s", label)), value)
	}
	b.WriteString(m.st.title.Render(p.Namespace+"/"+p.Name) + "\n\n")
	line("Requested", orDash(p.RequestedStr))
	line("PV", fmt.Sprintf("%s (%s)", orDash(p.PVName), formatBytes(p.PVBytes)))
	line("Storage", orDash(p.StorageClass))
	line("Volume mode", orDash(string(p.VolumeMode)))
	modes := make([]string, len(p.AccessModes))
	for i, mode := range p.AccessModes {
		modes[i] = string(mode)
	}
	line("Access", orDash(strings.Join(modes, ", ")))
	line("Pod node", orDash(p.NodeName))
	line("In use", fmt.Sprint(p.InUse))
	b.WriteString("\n" + m.st.label.Render("Mounts") + "\n")
	if len(p.Mounts) == 0 {
		b.WriteString("  none: a scan uses a temporary pod\n")
	}
	for _, mount := range p.Mounts {
		sub := ""
		if !mount.WholeVolume() {
			sub = "  (subPath, not scanned)"
		}
		fmt.Fprintf(&b, "  %s/%s:%s%s\n", mount.PodName, mount.ContainerName, mount.MountPath, sub)
	}
	b.WriteString("\n" + m.st.label.Render("Last scan") + "\n")
	if r.Status == "" {
		b.WriteString("  not scanned yet; press s\n")
		return b.String()
	}
	status := m.st.busy
	switch r.Status {
	case model.StatusDone:
		status = m.st.finished
	case model.StatusError:
		status = m.st.fail
	}
	line("  Status", status.Render(m.statusLabel(e)))
	if !e.scannedAt.IsZero() {
		line("  Finished", e.scannedAt.Format("15:04:05"))
	}
	if r.Status == model.StatusDone {
		line("  Used", fmt.Sprintf("%s (%s of requested)", dirwalker.FormatBytesShort(r.UsedBytes), usagePercent(r)))
	}
	if r.PodName != "" {
		scanned := r.PodName + ":" + r.ScanPath
		if r.PodName != r.MountingPodName {
			scanned += m.st.label.Render("  (temporary pod, deleted after the scan)")
		}
		line("  Scanned", scanned)
	}
	if r.Error != "" {
		line("  Error", m.st.fail.Render(r.Error))
	}
	for _, w := range r.Warnings {
		line("  Warning", m.st.message.Render(w))
	}
	return b.String()
}

func (m *Model) logsView() string {
	height := m.bodyHeight() - 1
	end := max(0, len(m.logs)-m.logShift)
	start := max(0, end-height)
	header := m.st.label.Render(fmt.Sprintf("Logs (%d lines; ↑/↓ scroll, esc back)", len(m.logs)))
	if len(m.logs) == 0 {
		return header + "\nNo log lines yet."
	}
	return header + "\n" + strings.Join(m.logs[start:end], "\n")
}

// clip keeps content within height lines and pads it so the help stays at
// the bottom of the screen.
func clip(content string, height int) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func usagePercent(r model.ScanResult) string {
	return render.UsagePercent(r)
}

func formatBytes(n int64) string {
	return render.FormatBytes(n)
}

func orDash(s string) string {
	return render.OrDash(s)
}
