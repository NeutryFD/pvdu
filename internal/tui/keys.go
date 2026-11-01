package tui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
)

type keyMap struct {
	Up, Down, Open, Back               key.Binding
	Scan, Force, ScanAll, ForceAll     key.Binding
	Mark                               key.Binding
	Cancel, Filter, Namespace, Refresh key.Binding
	Options                            key.Binding
	Logs, Help, Quit                   key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Open:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "details")),
		Back:      key.NewBinding(key.WithKeys("esc", "backspace"), key.WithHelp("esc", "back")),
		Scan:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "scan")),
		Force:     key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "force scan")),
		Mark:      key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "mark for s/f")),
		ScanAll:   key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "scan all")),
		ForceAll:  key.NewBinding(key.WithKeys("F"), key.WithHelp("F", "force scan all")),
		Cancel:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "cancel scan")),
		Filter:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Namespace: key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "namespace")),
		Options:   key.NewBinding(key.WithKeys("O"), key.WithHelp("O", "options")),
		Refresh:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Logs:      key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "logs")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Scan, k.Force, k.ScanAll, k.ForceAll, k.Mark, k.Open, k.Filter, k.Namespace, k.Options, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Open, k.Back},
		{k.Scan, k.Force, k.ScanAll, k.ForceAll, k.Mark, k.Cancel},
		{k.Filter, k.Namespace, k.Options, k.Refresh, k.Logs},
		{k.Help, k.Quit},
	}
}

// tableKeyMap keeps the table's navigation keys but frees space for marking
// and f for a forced scan.
func tableKeyMap() table.KeyMap {
	km := table.DefaultKeyMap()
	km.PageDown.SetKeys("pgdown")
	return km
}
