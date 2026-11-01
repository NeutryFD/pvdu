package render

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/neutry/pvdu/internal/model"
)

func render(results []*model.ScanResult, borders bool) string {
	return renderTable(results, borders, true)
}

func renderTable(results []*model.ScanResult, borders, diagnosticsEnabled bool, options ...TableOptions) string {
	if len(results) == 0 {
		return "No PVCs found.\n"
	}
	columns := Columns(showNamespace(options))
	headers := make([]string, len(columns))
	for i, column := range columns {
		headers[i] = column.Title
	}
	rows := [][]string{headers}
	var diagnostics []string
	for _, r := range results {
		row := make([]string, len(columns))
		for i, column := range columns {
			row[i] = column.Value(*r)
		}
		if r.Status == model.StatusError {
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
			if columns[i].RightAlign {
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
