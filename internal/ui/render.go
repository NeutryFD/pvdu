package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/NeutryFD/dirwalker"
	"github.com/neutry/pvdu/internal/model"
	"gopkg.in/yaml.v3"
)

// WriteResults preserves the structured schema even when no PVCs were found.
func WriteResults(w io.Writer, format string, results []*model.ScanResult) error {
	if results == nil {
		results = []*model.ScanResult{}
	}
	switch format {
	case "json":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(results)
	case "yaml":
		encoder := yaml.NewEncoder(w)
		defer encoder.Close()
		return encoder.Encode(results)
	case "default", "table":
		_, err := io.WriteString(w, render(results, format == "table"))
		return err
	default:
		return fmt.Errorf("invalid output format %q", format)
	}
}

func RenderJSON(results []*model.ScanResult) string {
	var b strings.Builder
	_ = WriteResults(&b, "json", results)
	return strings.TrimSuffix(b.String(), "\n")
}
func RenderYAML(results []*model.ScanResult) string {
	var b strings.Builder
	_ = WriteResults(&b, "yaml", results)
	return b.String()
}
func RenderDefault(results []*model.ScanResult) string { return render(results, false) }
func RenderTable(results []*model.ScanResult) string   { return render(results, true) }

func render(results []*model.ScanResult, borders bool) string {
	if len(results) == 0 {
		return "No PVCs found.\n"
	}
	rows := [][]string{{"NAMESPACE", "POD", "PVC", "PATH", "REQUESTED", "PV SIZE", "USED", "%"}}
	var diagnostics []string
	for _, r := range results {
		row := []string{r.Namespace, r.PodName, r.PVCName, r.ScanPath, r.RequestedStr, "-", "-", "-"}
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
			row[6] = "ERROR"
			diagnostics = append(diagnostics, fmt.Sprintf("%s/%s: %s", r.Namespace, r.PVCName, r.Error))
		}
		diagnostics = append(diagnostics, r.Warnings...)
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
	if len(diagnostics) > 0 {
		b.WriteString("\nDiagnostics:\n  " + strings.Join(diagnostics, "\n  ") + "\n")
	}
	return b.String()
}
