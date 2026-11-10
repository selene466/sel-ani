package ui

import (
	"fmt"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"sel-ani/internal/app"
	"sel-ani/internal/domain"
	"sel-ani/internal/infra/kitty"
)

const (
	minWidth  = 56
	minHeight = 10
)

// View implements tea.Model.
func (m *Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.BackgroundColor = m.st.bg
	v.ForegroundColor = m.st.fg
	v.WindowTitle = "sel-ani"
	v.MouseMode = tea.MouseModeCellMotion
	if m.quit || m.width == 0 || m.height == 0 {
		return v
	}
	v.SetContent(m.render())
	return v
}

// column widths, 1:4:3
func (m *Model) widths() (left, mid, right int) {
	u := m.width / 8
	return u, 4 * u, m.width - 5*u
}

func (m *Model) bodyHeight() int { return max(m.height-2, 1) }

// pad cuts s to w cells, marking the cut, and pads it with spaces to exactly w.
func (m *Model) pad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, m.g.ellipsis)
	if d := w - ansi.StringWidth(s); d > 0 {
		s += strings.Repeat(" ", d)
	}
	return s
}

func (m *Model) render() string {
	w, h := m.width, m.height
	if w < minWidth || h < minHeight {
		return m.st.text.Render(fmt.Sprintf("terminal too small: %dx%d, need %dx%d", w, h, minWidth, minHeight))
	}
	lw, mw, rw := m.widths()
	bh := m.bodyHeight()
	left, mid, right := m.renderSections(lw, bh), m.renderMiddle(mw, bh), m.renderPreview(rw, bh)
	var b strings.Builder
	b.WriteString(m.renderTop(w))
	for i := range bh {
		b.WriteByte('\n')
		b.WriteString(left[i] + mid[i] + right[i])
	}
	b.WriteByte('\n')
	b.WriteString(m.renderBottom(w))
	if !m.pop.open && !m.sdlg {
		return b.String()
	}
	rows := strings.Split(b.String(), "\n")
	if m.pop.open {
		x0, y0, _, _ := m.popupGeom()
		overlay(rows, m.popupBox(), x0, y0)
	}
	if m.sdlg {
		x0, y0, _, _ := m.searchGeom()
		overlay(rows, m.searchBox(), x0, y0)
	}
	return strings.Join(rows, "\n")
}

func (m *Model) renderTop(w int) string {
	if m.home {
		return m.st.topbar.Render(m.pad(" sel-ani  Main menu", w))
	}
	where := sectionNames[m.sec]
	switch m.sec {
	case secSearch:
		where += fmt.Sprintf(" %q", m.query)
	}
	where += fmt.Sprintf(" - %d", len(m.list.Entries))
	if m.filter != "" {
		where = fmt.Sprintf("%s filter %q %d/%d", sectionNames[m.sec], m.filter, len(m.shown), len(m.list.Entries))
	}
	if m.level == levelEpisodes {
		if e, ok := m.selected(); ok {
			where += " / " + e.Anime.Name()
		}
	}
	s := " sel-ani  " + where
	if m.loading {
		s += "  loading..."
	} else if m.more {
		s += "  loading more..."
	}
	return m.st.topbar.Render(m.pad(s, w))
}

func (m *Model) renderBottom(w int) string {
	switch {
	case m.typing:
		return m.st.accent.Render(m.pad("/"+m.input+"_", w))
	case m.status != "":
		if m.statErr {
			return m.st.err.Render(m.pad(" "+m.status, w))
		}
		return m.st.info.Render(m.pad(" "+m.status, w))
	}
	if m.home {
		return m.st.muted.Render(m.pad(" j/k move  enter select  1 2 section  / search  m sub/dub  q quit  (mouse: left column exit, right column enter)", w))
	}
	help := " j/k move  l open  h back  / filter  tab section  s sort  r refresh  m sub/dub  q quit  (mouse: left column back, right column open)"
	return m.st.muted.Render(m.pad(help, w))
}

func (m *Model) renderSections(w, h int) []string {
	lines := make([]string, h)
	for i := range lines {
		lines[i] = m.pad("", w)
	}
	if m.home {
		return lines // the menu is in the middle until a section is entered
	}
	for i, sec := range menuSections {
		n := sectionNames[sec]
		if i >= h {
			break
		}
		if sec == m.sec {
			lines[i] = m.st.selected.Render(m.pad(" "+n, w))
		} else {
			lines[i] = m.st.text.Render(m.pad(" "+n, w))
		}
	}
	if h > 4 {
		lines[4] = m.st.muted.Render(m.pad(" "+string(m.opts.App.Mode), w))
	}
	return lines
}

// window keeps cursor inside the n visible rows starting at *off.
func window(cursor, rows int, off *int) {
	if cursor < *off {
		*off = cursor
	}
	if cursor >= *off+rows {
		*off = cursor - rows + 1
	}
	*off = max(*off, 0)
}

func (m *Model) renderMiddle(w, h int) []string {
	sep := m.st.line.Render(m.g.vsep)
	iw := w - 1
	var rows []string
	switch {
	case m.home:
		for i, n := range homeItems {
			st := m.st.text
			if i == m.homeCursor {
				st = m.st.selected
			}
			rows = append(rows, st.Render(m.pad(" "+n, iw)))
		}
	case m.level == levelEpisodes:
		rows = m.episodeRows(iw, h)
	case len(m.shown) == 0:
		rows = []string{m.st.muted.Render(m.pad(m.emptyText(), iw))}
	default:
		window(m.cursor, h, &m.offset)
		for i := m.offset; i < len(m.shown) && len(rows) < h; i++ {
			rows = append(rows, m.entryRow(m.shown[i], iw, i == m.cursor))
		}
	}
	out := make([]string, h)
	for i := range out {
		r := m.pad("", iw)
		if i < len(rows) {
			r = rows[i]
		}
		out[i] = sep + r
	}
	return out
}

func (m *Model) emptyText() string {
	switch {
	case m.loading:
		return " loading..."
	case m.filter != "":
		return " no match"
	case m.sec == secSearch && m.query == "":
		return " press / to search"
	case m.sec == secMyList:
		return " nothing here yet: watched anime matched to AniDB show up"
	}
	return " no results"
}

// entryRow draws "title        [N] 5/12" in w cells.
func (m *Model) entryRow(e app.Entry, w int, selected bool) string {
	base := m.st.text
	if selected {
		base = m.st.selected
	}
	// the marker column says "in your list": in My List that is every row,
	// so there the column is left out, not blanked
	marker, markerW := "", 0
	if m.sec != secMyList {
		marker, markerW = " ", 1
		if e.InList {
			marker = m.g.inList
		}
	}
	counter := e.Counter
	cw := ansi.StringWidth(counter)
	nameW := max(w-markerW-cw-1, 1)
	name := m.pad(e.Anime.Name(), nameW)
	var c string
	if rest, ok := strings.CutPrefix(counter, "[N] "); ok {
		warn := m.st.warn
		if selected {
			warn = warn.Background(m.st.selected.GetBackground())
		}
		c = warn.Render("[N]") + base.Render(" "+rest)
	} else {
		c = base.Render(counter)
	}
	return base.Render(marker+name+" ") + c
}

func (m *Model) episodeRows(w, h int) []string {
	if m.detail == nil {
		return []string{m.st.muted.Render(m.pad(" loading episodes...", w))}
	}
	eps := m.detail.Episodes
	if len(eps) == 0 {
		return []string{m.st.muted.Render(m.pad(" no episodes", w))}
	}
	window(m.epCursor, h, &m.epOff)
	var rows []string
	for i := m.epOff; i < len(eps) && len(rows) < h; i++ {
		e := eps[i]
		mark := m.g.unwatched
		if m.detail.Watched[e.Number] {
			mark = m.g.watched
		}
		next := " "
		if e.Number == m.detail.Next {
			next = m.g.next
		}
		line := m.pad(fmt.Sprintf(" %s %s Episode %s", next, mark, e.Number), w)
		st := m.st.text
		if i == m.epCursor {
			st = m.st.selected
		} else if m.detail.Watched[e.Number] {
			st = m.st.muted
		}
		rows = append(rows, st.Render(line))
	}
	return rows
}

var (
	reBR  = regexp.MustCompile(`(?i)<br\s*/?>`)
	reTag = regexp.MustCompile(`<[^>]*>`)
)

func cleanText(s string) string {
	s = reBR.ReplaceAllString(s, "\n")
	return strings.TrimSpace(reTag.ReplaceAllString(s, ""))
}

// coverCells is the size the cover of cs takes in the preview pane.
func (m *Model) coverCells(cs coverState) (cols, rows int) {
	_, _, rw := m.widths()
	maxRows := max(m.bodyHeight()*55/100, 4)
	return kitty.Fit(cs.w, cs.h, rw-3, maxRows)
}

func (m *Model) renderPreview(w, h int) []string {
	sep := m.st.line.Render(m.g.vsep)
	iw := w - 2 // separator and a gutter
	var lines []string
	line := func(st lipgloss.Style, s string) {
		for _, l := range strings.Split(ansi.Wrap(s, iw, ""), "\n") {
			lines = append(lines, " "+st.Render(m.pad(l, iw)))
		}
	}
	if m.home {
		line(m.st.title, homeItems[m.homeCursor])
		line(m.st.muted, homeHints[m.homeCursor])
	} else if e, ok := m.selected(); ok {
		lines = append(lines, m.coverLines(e.Anime, iw)...)
		a := e.Anime
		line(m.st.title, a.Name())
		if a.Title != "" && a.Title != a.Name() {
			line(m.st.text, "English  "+a.Title)
		}
		if a.AltTitle != "" && a.AltTitle != a.Name() {
			line(m.st.text, "Romaji   "+a.AltTitle)
		}
		if a.Native != "" {
			line(m.st.text, "Japanese "+a.Native)
		}
		line(m.st.muted, m.facts(e))
		if d := cleanText(a.Description); d != "" {
			lines = append(lines, " "+m.pad("", iw))
			line(m.st.text, d)
		}
	}
	out := make([]string, h)
	for i := range out {
		l := m.pad("", iw+1)
		if i < len(lines) {
			l = lines[i]
		}
		out[i] = sep + l
	}
	return out
}

func (m *Model) facts(e app.Entry) string {
	a := e.Anime
	var p []string
	if a.Format != "" {
		p = append(p, a.Format)
	}
	if a.Status != "" {
		p = append(p, strings.ToLower(strings.ReplaceAll(a.Status, "_", " ")))
	}
	if a.Episodes > 0 {
		p = append(p, fmt.Sprintf("%d eps", a.Episodes))
	}
	if n := a.Available(m.opts.App.Mode); n > 0 {
		p = append(p, fmt.Sprintf("%d %s out", n, m.opts.App.Mode))
	}
	if e.Watched != "" {
		p = append(p, "watched "+e.Watched)
	}
	return strings.Join(p, " - ")
}

// coverLines is the cover as placeholder cells, or nothing while it is not
// in the terminal yet.
func (m *Model) coverLines(a domain.Anime, iw int) []string {
	if !m.opts.Cover || a.CoverURL == "" {
		return nil
	}
	cs, ok := m.pngs[a.CoverURL]
	if !ok || cs.png == nil {
		return nil
	}
	cols, rows := m.coverCells(cs)
	if cols == 0 {
		return nil
	}
	id := kitty.ID(fmt.Sprintf("%s@%dx%d", a.CoverURL, cols, rows))
	if !m.sent[id] {
		return nil
	}
	var out []string
	for _, l := range strings.Split(kitty.Placeholder(id, cols, rows), "\n") {
		out = append(out, " "+l+strings.Repeat(" ", max(iw-cols, 0)))
	}
	return append(out, " "+m.pad("", iw))
}

var homeHints = []string{"newest releases", "find an anime", "your AniDB list", "settings and AniDB account", "leave sel-ani"}
