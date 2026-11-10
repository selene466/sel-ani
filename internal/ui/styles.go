package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"sel-ani/internal/config"
)

type styles struct {
	bg, fg                                   color.Color
	text, muted, accent, secondary           lipgloss.Style
	warn, err, ok, info, title, topbar, line lipgloss.Style
	selected, panel                          lipgloss.Style
}

func newStyles(t config.Theme) styles {
	c := func(s string) color.Color { return lipgloss.Color(s) }
	fg := func(s string) lipgloss.Style { return lipgloss.NewStyle().Foreground(c(s)) }
	return styles{
		bg: c(t.Background), fg: c(t.Foreground),
		text:      fg(t.Foreground),
		muted:     fg(t.Muted),
		accent:    fg(t.Accent),
		secondary: fg(t.Secondary),
		warn:      fg(t.Warning),
		err:       fg(t.Error),
		ok:        fg(t.Success),
		info:      fg(t.Info),
		title:     fg(t.Accent).Bold(true),
		topbar:    lipgloss.NewStyle().Foreground(c(t.Foreground)).Background(c(t.Surface)),
		line:      fg(t.Border),
		panel:     lipgloss.NewStyle().Foreground(c(t.Foreground)).Background(c(t.Surface)),
		selected:  lipgloss.NewStyle().Foreground(c(t.Foreground)).Background(c(t.Overlay)).Bold(true),
	}
}
