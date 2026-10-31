package tui

import (
	"image/color"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

// palette gives each part of the screen its colour. Themes keep the
// terminal's own background and colour only text, borders and highlights.
type palette struct {
	name string
	// accent colours the title and the table header.
	accent color.Color
	// text is the text of selected rows; selection their background.
	text, selection color.Color
	// muted colours labels, help descriptions and rules.
	muted color.Color
	// key colours help keys, pop-up titles and the input prompt.
	key color.Color
	// busy colours the spinner and the loading pop-up border.
	busy color.Color
	// warn borders the confirmation before creating pods.
	warn                    color.Color
	message, fail, finished color.Color
	// dialog borders the namespace picker and the options.
	dialog color.Color
}

var themes = []palette{
	{name: "dracula", accent: hex("#bd93f9"), text: hex("#f8f8f2"), selection: hex("#44475a"), muted: hex("#6272a4"),
		key: hex("#ff79c6"), busy: hex("#8be9fd"), warn: hex("#ffb86c"), message: hex("#f1fa8c"), fail: hex("#ff5555"),
		finished: hex("#50fa7b"), dialog: hex("#bd93f9")},
	{name: "catppuccin", accent: hex("#cba6f7"), text: hex("#cdd6f4"), selection: hex("#45475a"), muted: hex("#6c7086"),
		key: hex("#f5c2e7"), busy: hex("#89dceb"), warn: hex("#fab387"), message: hex("#f9e2af"), fail: hex("#f38ba8"),
		finished: hex("#a6e3a1"), dialog: hex("#cba6f7")},
	{name: "nord", accent: hex("#88c0d0"), text: hex("#d8dee9"), selection: hex("#434c5e"), muted: hex("#4c566a"),
		key: hex("#b48ead"), busy: hex("#8fbcbb"), warn: hex("#d08770"), message: hex("#ebcb8b"), fail: hex("#bf616a"),
		finished: hex("#a3be8c"), dialog: hex("#81a1c1")},
	{name: "gruvbox", accent: hex("#fabd2f"), text: hex("#ebdbb2"), selection: hex("#504945"), muted: hex("#928374"),
		key: hex("#fe8019"), busy: hex("#8ec07c"), warn: hex("#fe8019"), message: hex("#fabd2f"), fail: hex("#fb4934"),
		finished: hex("#b8bb26"), dialog: hex("#83a598")},
	{name: "tokyonight", accent: hex("#7aa2f7"), text: hex("#c0caf5"), selection: hex("#283457"), muted: hex("#565f89"),
		key: hex("#bb9af7"), busy: hex("#7dcfff"), warn: hex("#ff9e64"), message: hex("#e0af68"), fail: hex("#f7768e"),
		finished: hex("#9ece6a"), dialog: hex("#7aa2f7")},
}

// DefaultTheme is the theme used when none is chosen.
const DefaultTheme = "dracula"

func hex(s string) color.Color { return lipgloss.Color(s) }

// ThemeNames lists the themes in the order the options pop-up cycles them.
func ThemeNames() []string {
	names := make([]string, len(themes))
	for i, t := range themes {
		names[i] = t.name
	}
	return names
}

// themeByName returns the named theme, or the default one for an unknown
// name.
func themeByName(name string) palette {
	for _, t := range themes {
		if t.name == name {
			return t
		}
	}
	return themes[0]
}

// styles are the lipgloss styles of a palette.
type styles struct {
	title, label, message, fail, busy, popupTitle, selected, finished lipgloss.Style
	progressBox, confirmBox, dialogBox                                lipgloss.Style
}

func newStyles(p palette) styles {
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	return styles{
		title:      lipgloss.NewStyle().Bold(true).Foreground(p.accent),
		label:      lipgloss.NewStyle().Foreground(p.muted),
		message:    lipgloss.NewStyle().Foreground(p.message),
		fail:       lipgloss.NewStyle().Foreground(p.fail),
		busy:       lipgloss.NewStyle().Foreground(p.busy),
		popupTitle: lipgloss.NewStyle().Bold(true).Foreground(p.key),
		selected:   lipgloss.NewStyle().Foreground(p.text).Background(p.selection).Bold(true),
		finished:   lipgloss.NewStyle().Foreground(p.finished),
		// progressBox is the loading pop-up, confirmBox asks before creating
		// pods, and dialogBox holds the namespace picker and the options.
		progressBox: box.BorderForeground(p.busy),
		confirmBox:  box.BorderForeground(p.warn),
		dialogBox:   box.BorderForeground(p.dialog),
	}
}

// applyTheme colours every part of the TUI with the named theme.
func (m *Model) applyTheme(name string) {
	p := themeByName(name)
	m.theme, m.st = p.name, newStyles(p)
	m.table.SetStyles(tableStyles(p))
	m.help.Styles = helpStyles(p)
	m.input.SetStyles(inputStyles(p))
}

func tableStyles(p palette) table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.Foreground(p.accent).Bold(true).
		BorderStyle(lipgloss.NormalBorder()).BorderBottom(true).BorderForeground(p.muted)
	// Cells keep the terminal's text colour: a coloured cell ends with a
	// reset, which would cut the selected row's background after one column.
	s.Selected = lipgloss.NewStyle().Foreground(p.text).Background(p.selection).Bold(true)
	return s
}

func helpStyles(p palette) help.Styles {
	s := help.DefaultDarkStyles()
	s.ShortKey = s.ShortKey.Foreground(p.key)
	s.FullKey = s.FullKey.Foreground(p.key)
	s.ShortDesc = s.ShortDesc.Foreground(p.muted)
	s.FullDesc = s.FullDesc.Foreground(p.muted)
	s.ShortSeparator = s.ShortSeparator.Foreground(p.selection)
	s.FullSeparator = s.FullSeparator.Foreground(p.selection)
	s.Ellipsis = s.Ellipsis.Foreground(p.muted)
	return s
}

func inputStyles(p palette) textinput.Styles {
	s := textinput.DefaultDarkStyles()
	s.Focused.Prompt = s.Focused.Prompt.Foreground(p.key)
	s.Focused.Text = s.Focused.Text.Foreground(p.text)
	s.Focused.Placeholder = s.Focused.Placeholder.Foreground(p.muted)
	s.Cursor.Color = p.key
	return s
}
