package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"sel-ani/internal/config"
	"sel-ani/internal/domain"
)

type itemKind int

const (
	kBool itemKind = iota
	kEnum
	kText
	kInt
	kList
	kWizard
)

// item is one setting of the config popup. key is the config.toml key.
type item struct {
	key  string
	hint string
	kind itemKind
	opts []string
	get  func(config.Config) string
}

func configItems() []item {
	str := func(f func(c config.Config) string) func(config.Config) string { return f }
	items := []item{
		{"mode", "sub or dub audio", kEnum, []string{"sub", "dub"}, str(func(c config.Config) string { return c.Mode })},
		{"quality", "stream quality: best, worst or a resolution", kEnum, []string{"best", "1080p", "720p", "480p", "360p", "worst"}, str(func(c config.Config) string { return c.Quality })},
		{"player.command", "player program, e.g. mpv", kText, nil, str(func(c config.Config) string { return c.Player.Command })},
		{"player.args", "extra player arguments, separated by spaces", kList, nil, str(func(c config.Config) string { return strings.Join(c.Player.Args, " ") })},
		{"ui.cover", "show covers in the preview (kitty or ghostty)", kBool, nil, str(func(c config.Config) string { return strconv.FormatBool(c.UI.Cover) })},
		{"ui.font", "font style: Nerd Font glyphs, or plain ASCII if your font has none", kEnum, config.Fonts, str(func(c config.Config) string { return c.UI.Font })},
		{"theme.name", "color theme", kEnum, config.ThemeNames(), str(func(c config.Config) string { return c.Theme.Name })},
	}
	for _, col := range config.ThemeColors {
		items = append(items, item{"theme." + col, "#rrggbb override of the theme's " + col + ", empty = the theme's own", kText, nil,
			str(func(c config.Config) string { return c.Theme.Color(col) })})
	}
	items = append(items,
		item{"provider.base_url", "miruro.cx or a mirror", kText, nil, str(func(c config.Config) string { return c.Provider.BaseURL })},
		item{"provider.user_agent", "User-Agent sent with every request", kText, nil, str(func(c config.Config) string { return c.Provider.UserAgent })},
		item{"provider.timeout", "per request, e.g. 20s", kText, nil, str(func(c config.Config) string { return c.Provider.Timeout.String() })},
		item{"provider.servers", "stream servers, preferred first, separated by commas", kList, nil, str(func(c config.Config) string { return strings.Join(c.Provider.Servers, ", ") })},
		item{"browser.enabled", "solve Cloudflare challenges with a real browser", kBool, nil, str(func(c config.Config) string { return strconv.FormatBool(c.Browser.Enabled) })},
		item{"browser.remote_url", "DevTools url of a running browser", kText, nil, str(func(c config.Config) string { return c.Browser.RemoteURL })},
		item{"browser.bin", "browser program to launch, empty = auto", kText, nil, str(func(c config.Config) string { return c.Browser.Bin })},
		item{"browser.headless", "run the browser without a window", kBool, nil, str(func(c config.Config) string { return strconv.FormatBool(c.Browser.Headless) })},
		item{"anidb.account", "set up your AniDB login: credentials.toml, mode 0600", kWizard, nil, str(func(config.Config) string { return "press enter to start" })},
		item{"anidb.enabled", "track watched episodes on AniDB; needs the login", kBool, nil, str(func(c config.Config) string { return strconv.FormatBool(c.AniDB.Enabled) })},
		item{"anidb.server", "host:port of the UDP API", kText, nil, str(func(c config.Config) string { return c.AniDB.Server })},
		item{"anidb.local_port", "fixed local UDP port, 0 = any", kInt, nil, str(func(c config.Config) string { return strconv.Itoa(c.AniDB.LocalPort) })},
	)
	return items
}

// popup is the config popup of the main menu.
type popup struct {
	open      bool
	cursor    int
	off       int
	editing   bool
	buf       string
	status    string
	statusErr bool
	wiz       *wizard
}

type wizStep struct {
	label, help string
	secret      bool
}

var wizSteps = []wizStep{
	{"Client name", "the client name you registered at anidb.net/software/add", false},
	{"Client version", "the version number you registered", false},
	{"Username", "your AniDB username", false},
	{"Password", "your AniDB password; enter keeps the saved one", true},
	{"UDP API key", "set in your AniDB profile settings, it encrypts the connection; enter keeps the saved one", true},
}

type wizard struct {
	step     int
	vals     [5]string
	existing config.AniDBLogin
	err      string
	done     []string // result lines once saved
}

func (m *Model) items() []item { return configItems() }

func (m *Model) openConfig() {
	m.pop.open, m.pop.editing, m.pop.status = true, false, ""
}

func (m *Model) closeConfig() {
	m.pop.open, m.pop.editing, m.pop.wiz = false, false, nil
}

// popupGeom is the box: top-left corner, width and height.
func (m *Model) popupGeom() (x0, y0, bw, bh int) {
	bw = min(m.width-4, 84)
	bh = min(m.height-2, 28)
	return (m.width - bw) / 2, (m.height - bh) / 2, bw, bh
}

// listRows is how many items the popup shows at once.
func (m *Model) listRows() int {
	_, _, _, bh := m.popupGeom()
	return max(bh-2-3, 1) // border, then title, hint and status
}

func (m *Model) popupKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "ctrl+c" {
		m.quit = true
		return nil
	}
	switch {
	case m.pop.wiz != nil:
		return m.wizardKey(k)
	case m.pop.editing:
		return m.editKey(k)
	}
	items := m.items()
	switch s {
	case "esc", "q":
		m.closeConfig()
	case "j", "down":
		m.pop.cursor = min(m.pop.cursor+1, len(items)-1)
	case "k", "up":
		m.pop.cursor = max(m.pop.cursor-1, 0)
	case "g", "home":
		m.pop.cursor = 0
	case "G", "end":
		m.pop.cursor = len(items) - 1
	case "enter", "space", " ", "l", "right":
		return m.activateItem(items[m.pop.cursor], 1)
	case "h", "left":
		return m.activateItem(items[m.pop.cursor], -1)
	}
	return nil
}

// activateItem changes the selected setting: bools toggle and enums step
// (dir = -1 steps back) and are saved at once, text starts an edit, the
// account entry starts the wizard.
func (m *Model) activateItem(it item, dir int) tea.Cmd {
	cur := it.get(m.cfg)
	switch it.kind {
	case kBool:
		b, _ := strconv.ParseBool(cur)
		return m.saveItem(it, !b)
	case kEnum:
		i := -1
		for j, o := range it.opts {
			if o == cur {
				i = j
			}
		}
		n := len(it.opts)
		if i < 0 {
			i = 0
			if dir < 0 {
				i = n - 1
			}
		} else {
			i = (i + dir + n) % n
		}
		return m.saveItem(it, it.opts[i])
	case kWizard:
		if dir > 0 {
			m.startWizard()
		}
	default:
		if dir > 0 {
			m.pop.editing, m.pop.buf, m.pop.status = true, cur, ""
		}
	}
	return nil
}

func (m *Model) editKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.pop.editing = false
	case "enter":
		it := m.items()[m.pop.cursor]
		text := strings.TrimSpace(m.pop.buf)
		var v any = text
		switch it.kind {
		case kInt:
			n, err := strconv.Atoi(text)
			if err != nil {
				m.pop.status, m.pop.statusErr = "not a number: "+text, true
				return nil
			}
			v = n
		case kList:
			v = strings.Fields(strings.ReplaceAll(text, ",", " "))
		}
		m.pop.editing = false
		return m.saveItem(it, v)
	case "backspace":
		if r := []rune(m.pop.buf); len(r) > 0 {
			m.pop.buf = string(r[:len(r)-1])
		}
	case "ctrl+u":
		m.pop.buf = ""
	default:
		m.pop.buf += printable(k.Text)
	}
	return nil
}

// printable drops control characters and line breaks from typed or pasted text.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// paste adds pasted text to the field being edited, or to the search prompt.
func (m *Model) paste(s string) {
	s = printable(s)
	switch {
	case m.pop.open && m.pop.wiz != nil && m.pop.wiz.done == nil:
		w := m.pop.wiz
		w.vals[w.step] += s
	case m.pop.open && m.pop.editing:
		m.pop.buf += s
	case m.sdlg:
		m.sbuf += s
	case m.typing:
		m.setFilter(m.input + s)
	}
}

// saveItem writes one setting through the injected saver, at once.
func (m *Model) saveItem(it item, v any) tea.Cmd {
	if m.opts.SaveConfig == nil {
		m.pop.status, m.pop.statusErr = "cannot save: there is no config file", true
		return nil
	}
	cfg, err := m.opts.SaveConfig(it.key, v)
	if err != nil {
		m.pop.status, m.pop.statusErr = it.key+": "+err.Error(), true
		return nil
	}
	cmd := m.applyConfig(cfg)
	m.pop.status, m.pop.statusErr = "saved", false
	return cmd
}

// applyConfig takes a saved configuration into use: what the UI itself shows
// at once, the rest (player, provider, browser, AniDB) through opts.Apply.
func (m *Model) applyConfig(cfg config.Config) tea.Cmd {
	prev := m.cfg
	m.cfg = cfg
	m.opts.App.SetMode(domain.Mode(cfg.Mode))
	m.opts.App.SetQuality(cfg.Quality)
	m.st = newStyles(cfg.Theme)
	m.g = glyphsFor(cfg.UI.Font)
	want := cfg.UI.Cover && m.opts.CoverSupported
	var cmds []tea.Cmd
	if m.opts.Cover && !want {
		cmds = append(cmds, m.resetImages())
	}
	m.opts.Cover = want
	if apply := m.opts.Apply; apply != nil {
		syncAfter := cfg.AniDB.Enabled && !prev.AniDB.Enabled
		cmds = append(cmds, func() tea.Msg { return appliedMsg{err: apply(cfg), sync: syncAfter} })
	}
	return tea.Batch(cmds...)
}

func (m *Model) startWizard() {
	ex := config.AniDBLogin{}
	if m.opts.ReadLogin != nil {
		ex = m.opts.ReadLogin()
	}
	w := &wizard{existing: ex}
	w.vals[0], w.vals[2] = ex.Client, ex.Username
	if ex.ClientVersion > 0 {
		w.vals[1] = strconv.Itoa(ex.ClientVersion)
	}
	m.pop.wiz, m.pop.status = w, ""
}

func (m *Model) wizardKey(k tea.KeyPressMsg) tea.Cmd {
	w := m.pop.wiz
	if w.done != nil {
		m.pop.wiz = nil // any key closes the result
		return nil
	}
	switch k.String() {
	case "esc":
		m.pop.wiz = nil // nothing was written
	case "up":
		w.step, w.err = max(w.step-1, 0), ""
	case "backspace":
		if r := []rune(w.vals[w.step]); len(r) > 0 {
			w.vals[w.step] = string(r[:len(r)-1])
		}
	case "ctrl+u":
		w.vals[w.step] = ""
	case "enter":
		return m.wizardNext()
	default:
		w.vals[w.step] += printable(k.Text)
	}
	return nil
}

func (m *Model) wizardNext() tea.Cmd {
	w := m.pop.wiz
	v := strings.TrimSpace(w.vals[w.step])
	w.vals[w.step] = v
	have := map[int]string{3: w.existing.Password, 4: w.existing.APIKey}[w.step]
	switch {
	case v == "" && have == "":
		w.err = wizSteps[w.step].label + " is required"
		return nil
	case w.step == 1:
		if n, err := strconv.Atoi(v); err != nil || n <= 0 {
			w.err = "the version is a positive number"
			return nil
		}
	}
	w.err = ""
	if w.step < len(wizSteps)-1 {
		w.step++
		return nil
	}
	if m.opts.SaveLogin == nil {
		w.err = "cannot save: there is no config file"
		return nil
	}
	n, _ := strconv.Atoi(w.vals[1])
	cfg, err := m.opts.SaveLogin(config.AniDBLogin{
		Client: w.vals[0], ClientVersion: n, Username: w.vals[2], Password: w.vals[3], APIKey: w.vals[4],
	})
	if err != nil {
		w.err = err.Error()
		return nil
	}
	w.done = []string{"credentials.toml saved, only you can read it (mode 0600)", "AniDB tracking is enabled"}
	return m.applyConfig(cfg)
}

// popupClick handles a mouse press while the popup is open.
func (m *Model) popupClick(ms tea.Mouse) tea.Cmd {
	x0, y0, bw, bh := m.popupGeom()
	inside := ms.X >= x0 && ms.X < x0+bw && ms.Y >= y0 && ms.Y < y0+bh
	switch {
	case m.pop.wiz != nil || m.pop.editing:
		return nil
	case ms.Button == tea.MouseRight, !inside:
		m.closeConfig()
		return nil
	case ms.Button != tea.MouseLeft:
		return nil
	}
	row := ms.Y - (y0 + 2) // border and title
	if row < 0 || row >= m.listRows() {
		return nil
	}
	items := m.items()
	i := m.pop.off + row
	if i >= len(items) {
		return nil
	}
	if i == m.pop.cursor {
		return m.activateItem(items[i], 1)
	}
	m.pop.cursor = i
	return nil
}

func (m *Model) popupWheel(d int) {
	if m.pop.wiz != nil || m.pop.editing {
		return
	}
	m.pop.cursor = min(max(m.pop.cursor+d, 0), len(m.items())-1)
}

// popupBox returns the popup as screen rows, all bw cells wide.
func (m *Model) popupBox() []string {
	_, _, bw, bh := m.popupGeom()
	iw := bw - 2
	border := m.st.line
	row := func(s string) string { return border.Render(m.g.v) + s + border.Render(m.g.v) }
	var inner []string
	bg := m.st.panel.GetBackground()
	line := func(st lipgloss.Style, s string) {
		if _, none := st.GetBackground().(lipgloss.NoColor); none {
			st = st.Background(bg)
		}
		inner = append(inner, st.Render(m.pad(s, iw)))
	}
	hint, status, statusErr := "", m.pop.status, m.pop.statusErr
	switch w := m.pop.wiz; {
	case w != nil && w.done != nil:
		line(m.st.title, " AniDB account setup - done")
		line(m.st.panel, "")
		for _, l := range w.done {
			line(m.st.ok, " "+l)
		}
		hint = "any key closes"
	case w != nil:
		line(m.st.title, fmt.Sprintf(" AniDB account setup - step %d of %d", w.step+1, len(wizSteps)))
		for i, st := range wizSteps {
			v := w.vals[i]
			if st.secret {
				v = strings.Repeat("*", len([]rune(v)))
				if v == "" && (i == 3 && w.existing.Password != "" || i == 4 && w.existing.APIKey != "") {
					v = "(saved)"
				}
			}
			style := m.st.panel
			if i == w.step {
				style = m.st.selected
				v += "_"
			}
			line(style, fmt.Sprintf(" %-16s %s", st.label, v))
		}
		hint = wizSteps[w.step].help
		status, statusErr = w.err, w.err != ""
		if status == "" {
			status = "enter next, up back, esc cancels (nothing is written until the last step)"
		}
	default:
		line(m.st.title, " Config - every change is saved at once")
		items := m.items()
		window(m.pop.cursor, m.listRows(), &m.pop.off)
		for i := m.pop.off; i < len(items) && i < m.pop.off+m.listRows(); i++ {
			it := items[i]
			v := it.get(m.cfg)
			style := m.st.panel
			if i == m.pop.cursor {
				style = m.st.selected
				if m.pop.editing {
					v = m.pop.buf + "_"
				}
			}
			switch {
			case i == m.pop.cursor && m.pop.editing:
			case it.kind == kBool || it.kind == kEnum:
				v = m.g.left + " " + optionName(it, v) + " " + m.g.right
			case v == "":
				v = "(empty)"
			}
			line(style, fmt.Sprintf(" %-22s %s", it.key, v))
		}
		hint = items[m.pop.cursor].hint
	}
	for len(inner) < bh-2-2 {
		line(m.st.panel, "")
	}
	line(m.st.muted, " "+hint)
	if statusErr {
		line(m.st.err, " "+status)
	} else {
		line(m.st.info, " "+status)
	}
	out := make([]string, 0, bh)
	out = append(out, border.Render(m.g.tl+strings.Repeat(m.g.h, iw)+m.g.tr))
	for _, l := range inner[:min(len(inner), bh-2)] {
		out = append(out, row(l))
	}
	out = append(out, border.Render(m.g.bl+strings.Repeat(m.g.h, iw)+m.g.br))
	return out
}

// overlay draws box over the screen rows at (x0, y0).
func overlay(screen []string, box []string, x0, y0 int) {
	for i, b := range box {
		y := y0 + i
		if y < 0 || y >= len(screen) {
			continue
		}
		w := ansi.StringWidth(b)
		screen[y] = ansi.Truncate(screen[y], x0, "") + "\x1b[m" + b + "\x1b[m" + ansi.TruncateLeft(screen[y], x0+w, "")
	}
}

// optionName is how a bool or enum value reads in its row.
func optionName(it item, v string) string {
	switch {
	case it.kind == kBool && v == "true":
		return "on"
	case it.kind == kBool:
		return "off"
	case it.key == "ui.font" && v == "nerdfont":
		return "Nerdfont"
	case it.key == "ui.font" && v == "ascii":
		return "ASCII"
	}
	return v
}

// searchGeom is the "Search keyword" popup: top-left corner, width, height.
func (m *Model) searchGeom() (x0, y0, w, h int) {
	w, h = min(m.width-4, 60), 5
	return (m.width - w) / 2, (m.height - h) / 2, w, h
}

// searchBox returns the "Search keyword" popup as screen rows.
func (m *Model) searchBox() []string {
	_, _, w, _ := m.searchGeom()
	iw := w - 2
	border := m.st.line
	row := func(s string) string { return border.Render(m.g.v) + s + border.Render(m.g.v) }
	return []string{
		border.Render(m.g.tl + strings.Repeat(m.g.h, iw) + m.g.tr),
		row(m.st.title.Background(m.st.panel.GetBackground()).Render(m.pad(" Search keyword", iw))),
		row(m.st.selected.Render(m.pad(" "+m.sbuf+"_", iw))),
		row(m.st.muted.Background(m.st.panel.GetBackground()).Render(m.pad(" enter searches, esc cancels", iw))),
		border.Render(m.g.bl + strings.Repeat(m.g.h, iw) + m.g.br),
	}
}
