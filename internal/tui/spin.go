package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// The frames come from the Bubbles spinner presets, but one clock drives every
// animation on screen instead of one spinner model per row.

// tickInterval is fast enough for the quickest spinner in use.
const tickInterval = 50 * time.Millisecond

type animation struct {
	frames   []string
	interval time.Duration
}

func newAnimation(s spinner.Spinner) animation {
	frames := make([]string, len(s.Frames))
	for i, frame := range s.Frames {
		// Some presets pad their frames for standalone use; cells add their own.
		frames[i] = strings.TrimRight(frame, " ")
	}
	// Despite its name, FPS is the time each frame stays on screen.
	return animation{frames: frames, interval: s.FPS}
}

// frame is the frame shown after elapsed time of animation.
func (a animation) frame(elapsed time.Duration) string {
	if len(a.frames) == 0 || a.interval <= 0 {
		return ""
	}
	return a.frames[int(elapsed/a.interval)%len(a.frames)]
}

// busy is the one animation for everything in progress; the status text
// tells the stages apart.
var busy = newAnimation(spinner.Dot)

type tickMsg time.Time

// animating reports whether anything on screen moves.
func (m *Model) animating() bool { return m.running > 0 || m.loading || m.nsLoading }

// startTicking starts the animation clock unless it already runs; the clock
// stops by itself once nothing animates, so an idle TUI does not redraw.
func (m *Model) startTicking() tea.Cmd {
	if m.ticking || !m.animating() {
		return nil
	}
	m.ticking = true
	return tick()
}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) onTick(t time.Time) tea.Cmd {
	m.now = t
	if !m.animating() {
		m.ticking = false
		return nil
	}
	m.refresh()
	return tick()
}

func (m *Model) elapsed() time.Duration { return m.now.Sub(m.started) }
