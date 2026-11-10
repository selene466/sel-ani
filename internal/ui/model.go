// Package ui is the Bubble Tea interface: a yazi style three column layout
// (sections, list, preview) over the use cases of package app.
package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"sel-ani/internal/app"
	"sel-ani/internal/config"
	"sel-ani/internal/domain"
	"sel-ani/internal/infra/cache"
	"sel-ani/internal/infra/image"
	"sel-ani/internal/infra/kitty"
)

// Images loads cover images as PNG.
type Images interface {
	PNG(ctx context.Context, url string) ([]byte, error)
}

// Options configure the model.
type Options struct {
	App     *app.App
	Images  Images // nil: no covers
	Cover   bool   // draw covers (the terminal must speak the kitty protocol)
	Theme   config.Theme
	Notice  string
	Context context.Context // cancelled on exit; nil = background

	// the config popup
	Config         config.Config
	CoverSupported bool // the terminal can show covers, whatever ui.cover says
	// SaveConfig sets one key of config.toml and returns the new configuration.
	SaveConfig func(key string, value any) (config.Config, error)
	// SaveLogin writes credentials.toml and enables AniDB.
	SaveLogin func(config.AniDBLogin) (config.Config, error)
	ReadLogin func() config.AniDBLogin
	// Apply puts a saved configuration into effect: the player, provider,
	// browser and AniDB. It runs off the UI thread.
	Apply func(config.Config) error
}

type section int

const (
	secLatest section = iota
	secSearch
	secMyList
)

var sectionNames = []string{"Latest", "Search", "My List"}

type level int

const (
	levelList level = iota
	levelEpisodes
)

// Model is the whole UI state.
type Model struct {
	opts Options
	cfg  config.Config
	pop  popup
	g    glyphs
	st   styles
	ctx  context.Context

	width, height int

	home       bool // the main menu is showing, full screen
	homeCursor int
	sec        section
	list       app.Listing
	cursor     int
	loading    bool
	seq        int // list requests; older answers are dropped
	query      string
	filter     string      // keeps the entries of the list whose titles contain it
	shown      []app.Entry // the entries that pass the filter
	typing     bool        // the filter prompt is open
	input      string
	sdlg       bool   // the "Search keyword" popup is open
	sbuf       string // its text
	more       bool   // more search results are being loaded
	level      level
	detail     *app.Detail
	detSeq     int
	epCursor   int

	pngs     map[string]coverState // by url
	sent     map[uint32]bool       // images transmitted to the terminal
	status   string
	statErr  bool
	statSeq  int             // identifies the status the clear timer belongs to
	keep     int             // cursor to restore after the next list load, -1 = none
	hydrated map[string]bool // anime already looked up for a missing cover
	mem      map[string]int  // last cursor per list view
	epMem    map[string]int  // last episode cursor per anime
	offset   int             // first visible row of the list
	epOff    int
	quit     bool
	alpha    map[section]bool // sections sorted by title instead of list order
}

type coverState struct {
	png  []byte
	w, h int
	err  error
}

// New returns the model.
func New(o Options) *Model {
	ctx := o.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return &Model{
		opts: o, cfg: o.Config, st: newStyles(o.Theme), g: glyphsFor(o.Config.UI.Font), ctx: ctx,
		pngs: map[string]coverState{}, sent: map[uint32]bool{}, hydrated: map[string]bool{}, mem: map[string]int{}, alpha: map[section]bool{}, epMem: map[string]int{},
		home: true, status: o.Notice, keep: -1,
	}
}

// messages
type (
	listMsg struct {
		seq int
		l   app.Listing
		err error
	}
	detailMsg struct {
		seq int
		d   app.Detail
		err error
	}
	coverMsg struct {
		url string
		st  coverState
	}
	sentMsg   struct{ id uint32 }
	playedMsg struct {
		title string
		err   error
	}
	syncedMsg  struct{ err error }
	appliedMsg struct {
		err  error
		sync bool // AniDB was just enabled: refresh the list from it
	}
	moreMsg struct { // a further page of search results
		seq int
		l   app.Listing
		err error
	}
	hydrateMsg struct {
		a   domain.Anime
		err error
	}
	clearMsg struct{ seq int }
)

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return m.sync() }

func (m *Model) load() tea.Cmd { return m.loadWith(false) }

// loadWith loads the section; refresh skips the cached Latest.
func (m *Model) loadWith(refresh bool) tea.Cmd {
	m.seq++
	m.loading, m.more = true, false
	seq, sec, q := m.seq, m.sec, m.query
	a, ctx := m.opts.App, m.ctx
	if refresh {
		ctx = cache.WithRefresh(ctx)
	}
	return func() tea.Msg {
		var l app.Listing
		var err error
		switch sec {
		case secLatest:
			l, err = a.Latest(ctx)
		case secSearch:
			if q == "" {
				return listMsg{seq: seq}
			}
			l, err = a.Search(ctx, q, "")
		case secMyList:
			l, err = a.MyList(ctx)
		}
		return listMsg{seq: seq, l: l, err: err}
	}
}

// searchMore loads the page of search results at cursor. The provider waits
// a second between pages that really hit the network.
func (m *Model) searchMore(cursor string) tea.Cmd {
	m.more = true
	seq, q, a, ctx := m.seq, m.query, m.opts.App, m.ctx
	return func() tea.Msg {
		l, err := a.Search(ctx, q, cursor)
		return moreMsg{seq: seq, l: l, err: err}
	}
}

// sync refreshes AniDB's progress in the background.
func (m *Model) sync() tea.Cmd {
	a, ctx := m.opts.App, m.ctx
	if a.Sync == nil {
		return nil
	}
	return func() tea.Msg { return syncedMsg{err: a.SyncMyList(ctx)} }
}

func (m *Model) loadDetail(an domain.Anime) tea.Cmd {
	m.detSeq++
	seq, a, ctx := m.detSeq, m.opts.App, m.ctx
	return func() tea.Msg {
		d, err := a.Detail(ctx, an)
		return detailMsg{seq: seq, d: d, err: err}
	}
}

func (m *Model) fetchCover(url string) tea.Cmd {
	if m.opts.Images == nil || !m.opts.Cover || url == "" {
		return nil
	}
	if _, ok := m.pngs[url]; ok {
		return nil
	}
	m.pngs[url] = coverState{} // in flight
	img, ctx := m.opts.Images, m.ctx
	return func() tea.Msg {
		b, err := img.PNG(ctx, url)
		w, h := image.Size(b)
		return coverMsg{url: url, st: coverState{png: b, w: w, h: h, err: err}}
	}
}

func (m *Model) play(an domain.Anime, ep domain.Episode) tea.Cmd {
	a, ctx := m.opts.App, m.ctx
	title := an.Name() + " " + ep.Number
	return func() tea.Msg { return playedMsg{title: title, err: a.Play(ctx, an, ep)} }
}

func (m *Model) say(s string, isErr bool) tea.Cmd {
	m.status, m.statErr = s, isErr
	m.statSeq++
	seq := m.statSeq
	return tea.Tick(6*time.Second, func(time.Time) tea.Msg { return clearMsg{seq: seq} })
}

// selected returns the highlighted anime.
func (m *Model) selected() (app.Entry, bool) {
	if m.cursor < 0 || m.cursor >= len(m.shown) {
		return app.Entry{}, false
	}
	return m.shown[m.cursor], true
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.remember()
	if m.quit {
		return m, tea.Quit
	}
	return m, tea.Batch(cmd, m.coverCmds())
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width != m.width || msg.Height != m.height {
			m.width, m.height = msg.Width, msg.Height
			return m.resetImages()
		}
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.PasteMsg:
		m.paste(msg.Content)
	case tea.MouseClickMsg:
		if m.sdlg {
			return m.searchClick(tea.Mouse(msg))
		}
		if m.pop.open {
			return m.popupClick(tea.Mouse(msg))
		}
		return m.click(tea.Mouse(msg))
	case tea.MouseWheelMsg:
		if m.sdlg {
			return nil
		}
		if m.pop.open {
			switch tea.Mouse(msg).Button {
			case tea.MouseWheelDown:
				m.popupWheel(1)
			case tea.MouseWheelUp:
				m.popupWheel(-1)
			}
			return nil
		}
		return m.wheel(tea.Mouse(msg))
	case listMsg:
		if msg.seq != m.seq {
			return nil
		}
		m.loading = false
		if msg.err != nil {
			m.list, m.cursor, m.keep = app.Listing{}, 0, -1
			m.refilter()
			return m.say(msg.err.Error(), true)
		}
		// a reload may reorder the list (watching moves an anime up, syncing
		// moves finished ones down): the cursor follows its anime, not its row
		prev := ""
		if e, ok := m.selected(); ok {
			prev = e.Anime.ID
		}
		m.list = msg.l
		if len(m.list.Entries) > app.SearchLimit && m.sec == secSearch {
			m.list.Entries = m.list.Entries[:app.SearchLimit]
		}
		m.refilter()
		m.cursor = 0
		switch {
		case m.keep >= 0:
			m.cursor = min(m.keep, max(m.rowCount()-1, 0))
			m.keep = -1
		case m.filter == "":
			if c, ok := m.mem[m.memKey()]; ok {
				m.cursor = min(c, max(m.rowCount()-1, 0))
			}
		}
		for i, e := range m.shown {
			if prev != "" && e.Anime.ID == prev {
				m.cursor = i
				break
			}
		}
		m.status = ""
		if m.sec == secSearch && msg.l.Next != "" && len(m.list.Entries) < app.SearchLimit {
			return m.searchMore(msg.l.Next)
		}
	case moreMsg:
		if msg.seq != m.seq {
			return nil
		}
		m.more = false
		if msg.err != nil {
			return m.say("more results: "+msg.err.Error(), true)
		}
		have := make(map[string]bool, len(m.list.Entries))
		for _, e := range m.list.Entries {
			have[e.Anime.ID] = true
		}
		for _, e := range msg.l.Entries {
			if !have[e.Anime.ID] && len(m.list.Entries) < app.SearchLimit {
				m.list.Entries = append(m.list.Entries, e)
			}
		}
		m.list.Next = msg.l.Next
		m.refilter()
		if msg.l.Next != "" && len(m.list.Entries) < app.SearchLimit {
			return m.searchMore(msg.l.Next)
		}
	case detailMsg:
		if msg.seq != m.detSeq {
			return nil
		}
		if msg.err != nil {
			m.level = levelList
			return m.say(msg.err.Error(), true)
		}
		m.detail = &msg.d
		m.epCursor = 0
		for i, e := range msg.d.Episodes {
			if e.Number == msg.d.Next {
				m.epCursor = i
			}
		}
		if c, ok := m.epMem[msg.d.Anime.ID]; ok {
			m.epCursor = min(c, max(len(msg.d.Episodes)-1, 0))
		}
	case hydrateMsg:
		if msg.err == nil {
			m.updateAnime(msg.a)
		}
	case coverMsg:
		m.pngs[msg.url] = msg.st
	case sentMsg:
		m.sent[msg.id] = true
	case playedMsg:
		if msg.err != nil {
			return m.say(msg.err.Error(), true)
		}
		// counters and [N] marks changed: reload without touching the cursor
		m.keep = m.cursor
		cmds := []tea.Cmd{m.load(), m.say("playing "+msg.title, false)}
		if e, ok := m.selected(); ok && m.level == levelEpisodes {
			cmds = append(cmds, m.loadDetail(e.Anime)) // [x] and > marks moved
		}
		return tea.Batch(cmds...)
	case appliedMsg:
		if msg.err != nil {
			m.pop.status, m.pop.statusErr = "saved, but not applied: "+msg.err.Error(), true
		} else if msg.sync {
			return m.sync()
		}
	case syncedMsg:
		if msg.err != nil {
			return m.say("anidb: "+msg.err.Error(), true)
		}
		if m.sec == secMyList {
			return m.load()
		}
	case clearMsg:
		if msg.seq == m.statSeq {
			m.status = ""
		}
	}
	return nil
}

// coverCmds fetches the cover of the highlighted anime and transmits it to
// the terminal once it is there, at the size the preview pane shows it.
func (m *Model) coverCmds() tea.Cmd {
	e, ok := m.selected()
	if !ok {
		return nil
	}
	if e.Anime.CoverURL == "" && !m.hydrated[e.Anime.ID] {
		// stored by an older version without a cover: find it again
		m.hydrated[e.Anime.ID] = true
		a, ctx, an := m.opts.App, m.ctx, e.Anime
		return func() tea.Msg {
			h, err := a.Hydrate(ctx, an)
			return hydrateMsg{a: h, err: err}
		}
	}
	if !m.opts.Cover {
		return nil
	}
	url := e.Anime.CoverURL
	cmds := []tea.Cmd{m.fetchCover(url)}
	cs, have := m.pngs[url]
	if !have || cs.png == nil {
		return tea.Batch(cmds...)
	}
	cols, rows := m.coverCells(cs)
	if cols == 0 {
		return tea.Batch(cmds...)
	}
	id := kitty.ID(fmt.Sprintf("%s@%dx%d", url, cols, rows))
	if _, done := m.sent[id]; !done {
		m.sent[id] = false // transmitting
		seq := kitty.Transmit(id, cs.png, cols, rows)
		cmds = append(cmds, tea.Sequence(tea.Raw(seq), func() tea.Msg { return sentMsg{id: id} }))
	}
	return tea.Batch(cmds...)
}

// resetImages frees every transmitted image, e.g. after a resize changed the
// cell size the images were placed with.
func (m *Model) resetImages() tea.Cmd {
	if len(m.sent) == 0 {
		return nil
	}
	var b strings.Builder
	for id := range m.sent {
		b.WriteString(kitty.Delete(id))
	}
	m.sent = map[uint32]bool{}
	return tea.Raw(b.String())
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	if m.pop.open {
		return m.popupKey(k)
	}
	if m.sdlg {
		return m.searchKey(k)
	}
	if m.typing {
		return m.typingKey(k)
	}
	s := k.String()
	if m.home {
		return m.homeKey(s)
	}
	switch s {
	case "ctrl+c", "q":
		m.quit = true
		return nil
	case "tab":
		return m.switchSection(menuSections[(menuIndex(m.sec)+1)%len(menuSections)])
	case "shift+tab":
		return m.switchSection(menuSections[(max(menuIndex(m.sec), 0)+len(menuSections)-1)%len(menuSections)])
	case "1", "2", "3":
		return m.switchSection(menuSections[s[0]-'1'])
	case "/":
		switch {
		case m.level != levelList:
		case m.sec == secSearch:
			m.askSearch() // in the results, / asks for a new keyword
		default:
			m.typing, m.input = true, m.filter
		}
		return nil
	case "m":
		return m.toggleMode()
	}
	if m.level == levelEpisodes {
		return m.episodeKey(s)
	}
	switch s {
	case "s":
		if m.sec == secLatest || m.sec == secMyList {
			prev := ""
			if e, ok := m.selected(); ok {
				prev = e.Anime.ID
			}
			m.alpha[m.sec] = !m.alpha[m.sec]
			m.refilter()
			m.cursor, m.offset = 0, 0
			for i, e := range m.shown {
				if e.Anime.ID == prev {
					m.cursor = i
					break
				}
			}
		}
	case "r":
		if m.sec == secLatest && !m.loading {
			m.keep = m.cursor
			return m.loadWith(true)
		}
	case "h", "left", "esc", "backspace":
		return m.back()
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(m.rowCount()-1, 0)
	case "ctrl+d", "pgdown":
		m.move(m.bodyHeight() / 2)
	case "ctrl+u", "pgup":
		m.move(-m.bodyHeight() / 2)
	case "l", "right", "enter":
		return m.open()
	}
	return nil
}

func (m *Model) episodeKey(s string) tea.Cmd {
	n := 0
	if m.detail != nil {
		n = len(m.detail.Episodes)
	}
	switch s {
	case "h", "left", "esc", "backspace":
		return m.back()
	case "j", "down":
		m.epCursor = min(m.epCursor+1, max(n-1, 0))
	case "k", "up":
		m.epCursor = max(m.epCursor-1, 0)
	case "g", "home":
		m.epCursor = 0
	case "G", "end":
		m.epCursor = max(n-1, 0)
	case "ctrl+d", "pgdown":
		m.epCursor = min(m.epCursor+m.bodyHeight()/2, max(n-1, 0))
	case "ctrl+u", "pgup":
		m.epCursor = max(m.epCursor-m.bodyHeight()/2, 0)
	case "l", "right", "enter":
		return m.playSelected()
	}
	return nil
}

// typingKey edits the filter while its prompt is open. The list narrows as
// you type; enter keeps the filter, escape drops it.
func (m *Model) typingKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "ctrl+c":
		m.typing = false
		m.setFilter("")
	case "enter":
		m.typing = false
	case "backspace":
		if r := []rune(m.input); len(r) > 0 {
			m.setFilter(string(r[:len(r)-1]))
		}
	case "ctrl+u":
		m.setFilter("")
	default:
		if t := printable(k.Text); t != "" {
			m.setFilter(m.input + t)
		}
	}
	return nil
}

// setFilter narrows the list to the entries matching text, cursor on top.
func (m *Model) setFilter(text string) {
	m.filter, m.input = text, text
	m.refilter()
	m.cursor, m.offset = 0, 0
}

// refilter recomputes the shown entries from the list and the filter. Every
// word of the filter must appear in one of the anime's titles.
func (m *Model) refilter() {
	m.filterShown()
	if m.alpha[m.sec] {
		m.shown = append([]app.Entry(nil), m.shown...)
		sort.SliceStable(m.shown, func(i, j int) bool {
			return strings.ToLower(m.shown[i].Anime.Name()) < strings.ToLower(m.shown[j].Anime.Name())
		})
	}
}

func (m *Model) filterShown() {
	words := strings.Fields(strings.ToLower(m.filter))
	if len(words) == 0 {
		m.shown = m.list.Entries
		return
	}
	m.shown = nil
	for _, e := range m.list.Entries {
		a := e.Anime
		titles := strings.ToLower(a.Name() + "\n" + a.Title + "\n" + a.AltTitle + "\n" + a.Native)
		ok := true
		for _, w := range words {
			if !strings.Contains(titles, w) {
				ok = false
				break
			}
		}
		if ok {
			m.shown = append(m.shown, e)
		}
	}
}

// clearList empties the list and the filter, for a section about to load.
func (m *Model) clearList() {
	m.list, m.filter, m.input = app.Listing{}, "", ""
	m.cursor, m.offset, m.keep = 0, 0, -1
	m.refilter()
}

// askSearch opens the "Search keyword" popup, with the last keyword in it.
func (m *Model) askSearch() {
	m.sdlg, m.sbuf = true, m.query
}

func (m *Model) searchKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "ctrl+c":
		m.quit = true
	case "esc":
		m.sdlg = false
	case "enter":
		if q := strings.TrimSpace(m.sbuf); q != "" {
			m.sdlg = false
			return m.submitSearch(q)
		}
	case "backspace":
		if r := []rune(m.sbuf); len(r) > 0 {
			m.sbuf = string(r[:len(r)-1])
		}
	case "ctrl+u":
		m.sbuf = ""
	default:
		m.sbuf += printable(k.Text)
	}
	return nil
}

// submitSearch shows the results of q, a first page at once and the rest,
// up to app.SearchLimit, as they arrive.
func (m *Model) submitSearch(q string) tea.Cmd {
	m.query = q
	m.home, m.level, m.detail, m.sec = false, levelList, nil, secSearch
	m.clearList()
	return m.load()
}

func (m *Model) searchClick(ms tea.Mouse) tea.Cmd {
	x0, y0, w, h := m.searchGeom()
	if ms.Button == tea.MouseRight || ms.X < x0 || ms.X >= x0+w || ms.Y < y0 || ms.Y >= y0+h {
		m.sdlg = false
	}
	return nil
}

func (m *Model) switchSection(s section) tea.Cmd {
	if s == secSearch {
		m.askSearch()
		return nil
	}
	m.level, m.detail = levelList, nil
	if s == m.sec {
		return nil
	}
	m.sec = s
	m.clearList()
	return m.load()
}

// rowCount is the number of selectable rows: the entries that pass the filter.
func (m *Model) rowCount() int { return len(m.shown) }

func (m *Model) move(d int) {
	m.cursor = min(max(m.cursor+d, 0), max(m.rowCount()-1, 0))
}

// memKey names the list view the cursor belongs to.
func (m *Model) memKey() string {
	q := ""
	if m.sec == secSearch {
		q = m.query
	}
	return fmt.Sprintf("%d|%s|%s|%t", m.sec, m.opts.App.Mode, q, m.alpha[m.sec])
}

// remember stores the cursors, so coming back to a view finds them again.
func (m *Model) remember() {
	if !m.home && !m.loading && m.filter == "" && len(m.list.Entries) > 0 {
		m.mem[m.memKey()] = m.cursor
	}
	if m.level == levelEpisodes && m.detail != nil {
		m.epMem[m.detail.Anime.ID] = m.epCursor
	}
}

// menuSections are the sections the main menu lists.
var menuSections = []section{secLatest, secSearch, secMyList}

// homeItems are the entries of the main menu: its sections, then config and quit.
var homeItems = []string{"Latest", "Search", "My List", "Config", "Quit"}

// menuIndex is the position of s in the menu, -1 when it is not in it.
func menuIndex(s section) int {
	for i, ms := range menuSections {
		if ms == s {
			return i
		}
	}
	return -1
}

func (m *Model) goHome() {
	m.home, m.homeCursor, m.level, m.detail, m.typing = true, max(menuIndex(m.sec), 0), levelList, nil, false
}

// enter opens a section from the main menu, which moves to the left column.
// Search first asks for its keyword.
func (m *Model) enter(s section) tea.Cmd {
	if s == secSearch {
		m.askSearch()
		return nil
	}
	m.home, m.level, m.detail = false, levelList, nil
	m.sec = s
	m.clearList()
	return m.load()
}

// activateHome runs the main menu entry i.
func (m *Model) activateHome(i int) tea.Cmd {
	switch {
	case i < len(menuSections):
		return m.enter(menuSections[i])
	case i == len(menuSections):
		m.openConfig()
	default:
		m.quit = true
	}
	return nil
}

func (m *Model) homeKey(s string) tea.Cmd {
	switch s {
	case "ctrl+c", "q":
		m.quit = true
	case "j", "down", "tab":
		m.homeCursor = (m.homeCursor + 1) % len(homeItems)
	case "k", "up", "shift+tab":
		m.homeCursor = (m.homeCursor + len(homeItems) - 1) % len(homeItems)
	case "g", "home":
		m.homeCursor = 0
	case "G", "end":
		m.homeCursor = len(homeItems) - 1
	case "1", "2", "3":
		return m.enter(menuSections[s[0]-'1'])
	case "/":
		m.askSearch()
	case "m":
		return m.toggleMode()
	case "enter", "l", "right":
		return m.activateHome(m.homeCursor)
	}
	return nil
}

func (m *Model) toggleMode() tea.Cmd {
	if m.opts.App.Mode == domain.ModeSub {
		m.opts.App.SetMode(domain.ModeDub)
	} else {
		m.opts.App.SetMode(domain.ModeSub)
	}
	cmds := []tea.Cmd{m.say("mode: "+string(m.opts.App.Mode), false)}
	if !m.home {
		cmds = append(cmds, m.load())
	}
	return tea.Batch(cmds...)
}

// click handles a mouse press, by column like the 1:4:3 layout reads:
// a click in the left column goes back (to the main menu from a list), in
// the right column it opens or plays the selected row, and in the middle it
// selects a row (a second click does what the right column does, a paging
// row pages at once). A right click anywhere goes back too.
func (m *Model) click(ms tea.Mouse) tea.Cmd {
	if m.typing || m.width < minWidth || m.height < minHeight {
		return nil
	}
	if ms.Button == tea.MouseRight {
		return m.back()
	}
	if ms.Button != tea.MouseLeft {
		return nil
	}
	lw, mw, _ := m.widths()
	row := ms.Y - 1 // the top bar is row 0
	if row < 0 || row >= m.bodyHeight() {
		return nil
	}
	if m.home {
		switch {
		case ms.X < lw:
			m.quit = true // back from the main menu is exit
		case ms.X >= lw+mw:
			return m.activateHome(m.homeCursor)
		case row < len(homeItems):
			if row == m.homeCursor {
				return m.activateHome(row)
			}
			m.homeCursor = row
		}
		return nil
	}
	switch {
	case ms.X < lw:
		return m.back() // the left column is the negative side: back, finally exit
	case ms.X >= lw+mw:
		return m.activate() // the right column is the positive side: open, play
	default:
		if m.level == levelEpisodes {
			return m.clickEpisode(row)
		}
		window(m.cursor, m.bodyHeight(), &m.offset)
		i := m.offset + row
		if i >= m.rowCount() {
			return nil
		}
		if i == m.cursor {
			return m.open()
		}
		m.cursor = i
	}
	return nil
}

// activate opens the selected anime or plays the selected episode.
func (m *Model) activate() tea.Cmd {
	if m.level == levelEpisodes {
		return m.playSelected()
	}
	return m.open()
}

func (m *Model) clickEpisode(row int) tea.Cmd {
	if m.detail == nil {
		return nil
	}
	window(m.epCursor, m.bodyHeight(), &m.epOff)
	i := m.epOff + row
	if i >= len(m.detail.Episodes) {
		return nil
	}
	if i == m.epCursor {
		return m.playSelected()
	}
	m.epCursor = i
	return nil
}

// back is h / escape / right click: one level up, ending at the main menu.
func (m *Model) back() tea.Cmd {
	switch {
	case m.home:
		m.quit = true
	case m.level == levelEpisodes:
		m.level, m.detail = levelList, nil
	case m.filter != "":
		m.setFilter("") // the filter goes first, then the list
	default:
		m.goHome()
	}
	return nil
}

func (m *Model) wheel(ms tea.Mouse) tea.Cmd {
	d := 0
	switch ms.Button {
	case tea.MouseWheelDown:
		d = 1
	case tea.MouseWheelUp:
		d = -1
	default:
		return nil
	}
	switch {
	case m.typing:
	case m.home:
		m.homeCursor = min(max(m.homeCursor+d, 0), len(homeItems)-1)
	case m.level == levelEpisodes && m.detail != nil:
		m.epCursor = min(max(m.epCursor+d, 0), max(len(m.detail.Episodes)-1, 0))
	case m.level == levelList:
		m.move(d)
	}
	return nil
}

// open shows the episodes of the highlighted anime.
func (m *Model) open() tea.Cmd {
	e, ok := m.selected()
	if !ok {
		return nil
	}
	m.level, m.detail = levelEpisodes, nil
	return m.loadDetail(e.Anime)
}

// playSelected plays the highlighted episode.
func (m *Model) playSelected() tea.Cmd {
	e, ok := m.selected()
	if !ok || m.detail == nil || len(m.detail.Episodes) == 0 {
		return nil
	}
	ep := m.detail.Episodes[m.epCursor]
	return tea.Batch(m.play(e.Anime, ep), m.say("starting episode "+ep.Number+"...", false))
}

// updateAnime replaces the listed copy of an anime with a fuller one.
func (m *Model) updateAnime(an domain.Anime) {
	for i := range m.list.Entries {
		if m.list.Entries[i].Anime.ID == an.ID {
			m.list.Entries[i].Anime = an
		}
	}
	m.refilter()
	if m.detail != nil && m.detail.Anime.ID == an.ID {
		m.detail.Anime = an
	}
}
