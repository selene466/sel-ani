package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Theme is a color palette expressed as hex colors.
type Theme struct {
	Name       string
	Background string // terminal background
	Surface    string // modals, highlighted rows
	Overlay    string // selection background
	Foreground string
	Muted      string
	Border     string
	Accent     string // focused borders, titles
	Secondary  string
	Success    string
	Warning    string
	Error      string
	Info       string
}

// DefaultTheme is the theme used when none is configured.
const DefaultTheme = "tokyonight"

var themes = map[string]Theme{
	"tokyonight": {
		Background: "#0e1419", Surface: "#16161e", Overlay: "#283457",
		Foreground: "#c0caf5", Muted: "#565f89", Border: "#3b4261",
		Accent: "#7aa2f7", Secondary: "#bb9af7",
		Success: "#9ece6a", Warning: "#e0af68", Error: "#f7768e", Info: "#7dcfff",
	},
	"tokyonight-storm": {
		Background: "#24283b", Surface: "#1f2335", Overlay: "#2e3c64",
		Foreground: "#c0caf5", Muted: "#565f89", Border: "#3b4261",
		Accent: "#7aa2f7", Secondary: "#bb9af7",
		Success: "#9ece6a", Warning: "#e0af68", Error: "#f7768e", Info: "#7dcfff",
	},
	"catppuccin": {
		Background: "#1e1e2e", Surface: "#181825", Overlay: "#313244",
		Foreground: "#cdd6f4", Muted: "#6c7086", Border: "#45475a",
		Accent: "#89b4fa", Secondary: "#cba6f7",
		Success: "#a6e3a1", Warning: "#f9e2af", Error: "#f38ba8", Info: "#89dceb",
	},
	"dracula": {
		Background: "#282a36", Surface: "#21222c", Overlay: "#44475a",
		Foreground: "#f8f8f2", Muted: "#6272a4", Border: "#44475a",
		Accent: "#bd93f9", Secondary: "#ff79c6",
		Success: "#50fa7b", Warning: "#f1fa8c", Error: "#ff5555", Info: "#8be9fd",
	},
	"gruvbox": {
		Background: "#282828", Surface: "#1d2021", Overlay: "#3c3836",
		Foreground: "#ebdbb2", Muted: "#928374", Border: "#504945",
		Accent: "#83a598", Secondary: "#d3869b",
		Success: "#b8bb26", Warning: "#fabd2f", Error: "#fb4934", Info: "#8ec07c",
	},
	"nord": {
		Background: "#2e3440", Surface: "#3b4252", Overlay: "#434c5e",
		Foreground: "#eceff4", Muted: "#4c566a", Border: "#4c566a",
		Accent: "#88c0d0", Secondary: "#b48ead",
		Success: "#a3be8c", Warning: "#ebcb8b", Error: "#bf616a", Info: "#81a1c1",
	},
}

// ThemeNames lists the built-in themes.
func ThemeNames() []string {
	names := make([]string, 0, len(themes))
	for n := range themes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// LookupTheme returns a built-in theme by name.
func LookupTheme(name string) (Theme, bool) {
	t, ok := themes[strings.ToLower(strings.TrimSpace(name))]
	if ok {
		t.Name = strings.ToLower(strings.TrimSpace(name))
	}
	return t, ok
}

var hexColorRe = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// ValidColor reports whether s is a #rgb or #rrggbb color.
func ValidColor(s string) bool { return hexColorRe.MatchString(s) }

// fields maps the color keys of the [theme] table to theme fields.
func (t *Theme) fields() map[string]*string {
	return map[string]*string{
		"background": &t.Background, "surface": &t.Surface,
		"overlay": &t.Overlay, "foreground": &t.Foreground,
		"muted": &t.Muted, "border": &t.Border,
		"accent": &t.Accent, "secondary": &t.Secondary,
		"success": &t.Success, "warning": &t.Warning,
		"error": &t.Error, "info": &t.Info,
	}
}

// Override sets a single color by its key (background, accent, ...).
func (t *Theme) Override(field, color string) error {
	ptr, ok := t.fields()[strings.ToLower(field)]
	if !ok {
		return fmt.Errorf("unknown theme color %q", field)
	}
	if !ValidColor(color) {
		return fmt.Errorf("invalid color %q for %s, expected #rrggbb", color, field)
	}
	*ptr = color
	return nil
}

// ThemeColors lists the color keys of the [theme] table, in display order.
var ThemeColors = []string{
	"background", "foreground", "surface", "overlay", "muted", "border",
	"accent", "secondary", "success", "warning", "error", "info",
}

// Color returns the color of a [theme] key, "" for an unknown key.
func (t Theme) Color(key string) string {
	if p, ok := t.fields()[key]; ok {
		return *p
	}
	return ""
}
