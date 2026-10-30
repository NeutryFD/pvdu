package render

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/neutry/pvdu/internal/model"
	"golang.org/x/term"
)

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
		defer func() { _, _ = io.WriteString(t.w, "\033[?25h") }()
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
