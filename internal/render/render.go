package render

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/NeutryFD/dirwalker"
	"github.com/neutry/pvdu/internal/model"
	"golang.org/x/term"
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

// LiveTable redraws scan rows in place. Call Update from concurrent workers;
// it serializes snapshots and terminal writes internally.
type LiveTable struct {
	mu       sync.Mutex
	w        io.Writer
	rows     []*model.ScanResult
	borders  bool
	lines    int
	err      error
	terminal *os.File
}

func NewLiveTable(w io.Writer, format string, results []*model.ScanResult) (*LiveTable, error) {
	t := &LiveTable{w: w, borders: format == "table", rows: cloneResults(results)}
	if file, ok := w.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		t.terminal = file
		if _, err := io.WriteString(w, "\033[?25l"); err != nil {
			_, _ = io.WriteString(w, "\033[?25h")
			return nil, err
		}
	}
	t.mu.Lock()
	err := t.draw(false)
	t.mu.Unlock()
	if err != nil {
		if t.terminal != nil {
			_, _ = io.WriteString(w, "\033[?25h")
		}
		return nil, err
	}
	return t, nil
}

func (t *LiveTable) Update(index int, result model.ScanResult) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	if index < 0 || index >= len(t.rows) {
		return fmt.Errorf("live table row index %d out of range", index)
	}
	copy := result
	copy.Warnings = append([]string(nil), result.Warnings...)
	t.rows[index] = &copy
	return t.draw(false)
}

func (t *LiveTable) Finish() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.terminal != nil {
		defer io.WriteString(t.w, "\033[?25h")
	}
	if t.err != nil {
		return t.err
	}
	return t.draw(true)
}

func (t *LiveTable) draw(includeDiagnostics bool) error {
	content := renderTable(t.rows, t.borders, includeDiagnostics)
	if t.terminal != nil && !includeDiagnostics {
		width, height, err := term.GetSize(int(t.terminal.Fd()))
		if err != nil {
			t.err = err
			return err
		}
		// Reserve a column to avoid terminal autowrap and a row for the footer.
		content = liveViewport(content, width, height) + "\n"
	}
	if t.lines > 0 {
		if _, err := fmt.Fprintf(t.w, "\033[%dA\r\033[J", t.lines); err != nil {
			t.err = err
			return err
		}
	}
	if _, err := io.WriteString(t.w, content); err != nil {
		t.err = err
		return err
	}
	t.lines = strings.Count(content, "\n")
	return nil
}

// liveViewport bounds each frame so neither wrapping nor scrolling can move
// the header. The final complete table is printed on the normal screen.
func liveViewport(content string, width, height int) string {
	width = max(1, width-1)
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	visible := min(len(lines), max(1, height-1))
	for i := range visible {
		runes := []rune(lines[i])
		if len(runes) > width {
			// Preserve the rightmost STATUS column when horizontal space is tight.
			if width > 16 {
				lines[i] = string(runes[:width-13]) + "…" + string(runes[len(runes)-12:])
			} else {
				lines[i] = string(runes[:width])
			}
		}
	}
	footer := ""
	if visible < len(lines) {
		visible = max(1, height-2)
		footer = fmt.Sprintf("… %d more table lines; full results at completion", len(lines)-visible)
	}
	frame := strings.Join(lines[:visible], "\n")
	if footer != "" {
		footerRunes := []rune(footer)
		frame += "\n" + string(footerRunes[:min(width, len(footerRunes))])
	}
	return frame
}

func cloneResults(results []*model.ScanResult) []*model.ScanResult {
	clones := make([]*model.ScanResult, len(results))
	for i, result := range results {
		copy := *result
		copy.Warnings = append([]string(nil), result.Warnings...)
		clones[i] = &copy
	}
	return clones
}
