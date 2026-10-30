package render

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/NeutryFD/dirwalker"
	"github.com/neutry/pvdu/internal/model"
)

func render(results []*model.ScanResult, borders bool) string {
	return renderTable(results, borders, true)
}

func renderTable(results []*model.ScanResult, borders, diagnosticsEnabled bool) string {
	if len(results) == 0 {
		return "No PVCs found.\n"
	}
	rows := [][]string{{"NAMESPACE", "POD", "PVC", "PATH", "REQUESTED", "PV SIZE", "USED", "%", "STATUS"}}
	var diagnostics []string
	for _, r := range results {
		row := []string{r.Namespace, r.PodName, r.PVCName, r.ScanPath, r.RequestedStr, "-", "-", "-", statusLabel(r.Status)}
		for i := range row {
			if row[i] == "" {
				row[i] = "-"
			}
		}
		if r.PVBytes > 0 {
			row[5] = dirwalker.FormatBytesShort(r.PVBytes)
		}
		if r.Status == model.StatusDone {
			row[6] = dirwalker.FormatBytesShort(r.UsedBytes)
			if r.RequestedBytes > 0 {
				row[7] = fmt.Sprintf("%.0f%%", 100*float64(r.UsedBytes)/float64(r.RequestedBytes))
			}
		} else if r.Status == model.StatusError {
			if r.Error != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s/%s: %s", r.Namespace, r.PVCName, singleLine(r.Error)))
			}
		}
		for _, warning := range r.Warnings {
			diagnostics = append(diagnostics, singleLine(warning))
		}
		rows = append(rows, row)
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, value := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(value))
		}
	}
	var b strings.Builder
	separator := func() {
		b.WriteByte('+')
		for _, width := range widths {
			b.WriteString(strings.Repeat("-", width+2))
			b.WriteByte('+')
		}
		b.WriteByte('\n')
	}
	if borders {
		separator()
	}
	for n, row := range rows {
		if borders {
			b.WriteString("| ")
		}
		for i, value := range row {
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(value))
			if i >= 4 {
				b.WriteString(pad + value)
			} else {
				b.WriteString(value + pad)
			}
			if borders {
				b.WriteString(" | ")
			} else if i < len(row)-1 {
				b.WriteString("  ")
			}
		}
		b.WriteByte('\n')
		if borders && n == 0 {
			separator()
		}
	}
	if borders {
		separator()
	}
	if diagnosticsEnabled && len(diagnostics) > 0 {
		b.WriteString("\nDiagnostics:\n  " + strings.Join(diagnostics, "\n  ") + "\n")
	}
	return b.String()
}

func singleLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func statusLabel(status model.ScanStatus) string {
	if status == model.StatusPending {
		return "WAITING"
	}
	return strings.ToUpper(string(status))
}
