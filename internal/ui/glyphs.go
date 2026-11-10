package ui

// glyphs are the characters the UI draws besides text: the column
// separator, the popup border, list markers and the arrows of option rows.
// ui.font picks one set.
type glyphs struct {
	vsep                 string // column separator
	tl, tr, bl, br, h, v string // popup border
	inList               string // an anime in your list
	watched, unwatched   string // episode marks
	next                 string // the episode to watch next
	prev, nextPage       string // paging rows
	ellipsis             string // cut text
	left, right          string // arrows around an option value
}

var asciiGlyphs = glyphs{
	vsep: "|",
	tl:   "+", tr: "+", bl: "+", br: "+", h: "-", v: "|",
	inList:  "*",
	watched: "[x]", unwatched: "[ ]",
	next: ">",
	prev: "<< Prev", nextPage: ">> Next",
	ellipsis: "~",
	left:     "<<", right: ">>",
}

// nerdGlyphs use box drawing characters and Nerd Font (Font Awesome) glyphs.
var nerdGlyphs = glyphs{
	vsep: "│",
	tl:   "╭", tr: "╮", bl: "╰", br: "╯", h: "─", v: "│",
	inList:  "",                 // star
	watched: "", unwatched: "", // check square, empty square
	next: "",                          // chevron right
	prev: " Prev", nextPage: " Next", // angle double left, right
	ellipsis: "…",
	left:     "", right: "", // angle left, right
}

// glyphsFor returns the set of a ui.font value. Anything but "ascii" is
// the default, Nerd Font.
func glyphsFor(font string) glyphs {
	if font == "ascii" {
		return asciiGlyphs
	}
	return nerdGlyphs
}
