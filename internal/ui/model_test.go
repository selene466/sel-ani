package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"sel-ani/internal/app"
	"sel-ani/internal/config"
	"sel-ani/internal/domain"
	"sel-ani/internal/infra/kitty"
)

type fakeProvider struct {
	latest        []domain.Anime
	searchCursors []string
}

func (f *fakeProvider) Search(_ context.Context, q, cursor string) (domain.Page, error) {
	f.searchCursors = append(f.searchCursors, cursor)
	page := map[string]int{"": 0, "c2": 1, "c3": 2}[cursor]
	n, next := 24, map[int]string{0: "c2", 1: "c3"}[page]
	if page == 2 {
		n = 12 // 60 results in all, the screen keeps 50
	}
	var items []domain.Anime
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("s%d", page*24+i+1)
		items = append(items, domain.Anime{ID: id, Title: "Found " + q + " " + id, AltTitle: "Rom " + id, Native: "NAT" + id})
	}
	return domain.Page{Items: items, Next: next}, nil
}
func (f *fakeProvider) Info(_ context.Context, id string) (domain.Anime, error) {
	return domain.Anime{ID: id, Title: "Listed", CoverURL: "http://c/" + id, Native: "JP"}, nil
}
func (f *fakeProvider) Latest(context.Context, domain.Mode) ([]domain.Anime, error) {
	return f.latest, nil
}
func (f *fakeProvider) Episodes(context.Context, string) ([]domain.Episode, error) {
	return []domain.Episode{{ID: "e1", Number: "1"}, {ID: "e2", Number: "2"}, {ID: "e3", Number: "3"}}, nil
}
func (f *fakeProvider) Stream(_ context.Context, ep domain.Episode, _ domain.Mode) (domain.Stream, error) {
	return domain.Stream{Links: []domain.Link{{Quality: "720p", URL: "v" + ep.Number}}}, nil
}

type fakeStore struct{ saved []domain.HistoryEntry }

func (s *fakeStore) Progress() (map[string]string, error) {
	return map[string]string{"a2": "1", "l1": "3"}, nil
}
func (s *fakeStore) Save(e domain.HistoryEntry) error { s.saved = append(s.saved, e); return nil }
func (s *fakeStore) WatchedEpisodes(string) ([]string, error) {
	var out []string
	for _, e := range s.saved {
		out = append(out, e.Episode)
	}
	return out, nil
}
func (s *fakeStore) RememberAnime(domain.Anime) error { return nil }
func (s *fakeStore) MyList() ([]domain.ListEntry, error) {
	return []domain.ListEntry{{AID: 1, Anime: domain.Anime{ID: "l1", Title: "Listed", Episodes: 12}, Watched: "3"}}, nil
}
func (s *fakeStore) PutMyList(int, int, int) error    { return nil }
func (s *fakeStore) Get(string) (string, bool, error) { return "", false, nil }
func (s *fakeStore) Set(string, string) error         { return nil }

type fakePlayer struct{ played []domain.Media }

func (p *fakePlayer) Play(_ context.Context, m domain.Media) error {
	p.played = append(p.played, m)
	return nil
}
func (p *fakePlayer) Wait(context.Context) error { return nil }
func (p *fakePlayer) Stop() error                { return nil }
func (p *fakePlayer) Interactive() bool          { return true }

type fakeImages struct{ calls int }

func (f *fakeImages) PNG(context.Context, string) ([]byte, error) {
	f.calls++
	return nil, fmt.Errorf("no image")
}

type harness struct {
	m  *Model
	st *fakeStore
	pl *fakePlayer
}

func newHarness(t *testing.T, cover bool) *harness {
	t.Helper()
	return newHarnessOpts(t, cover, nil)
}

func newHarnessOpts(t *testing.T, cover bool, mod func(*Options)) *harness {
	t.Helper()
	fp := &fakeProvider{}
	for i := 1; i <= 25; i++ {
		fp.latest = append(fp.latest, domain.Anime{ID: fmt.Sprintf("a%d", i), Title: fmt.Sprintf("Anime %d", i), AltTitle: fmt.Sprintf("Romaji %d", i), Sub: 5, Latest: "5", CoverURL: "http://c/" + fmt.Sprint(i)})
	}
	st, pl := &fakeStore{}, &fakePlayer{}
	a := &app.App{Provider: fp, Store: st, Player: pl, Mode: domain.ModeSub, Quality: "best"}
	opts := Options{App: a, Theme: config.Default().Theme, Cover: cover, Config: asciiConfig()}
	if cover {
		opts.Images = &fakeImages{}
	}
	if mod != nil {
		mod(&opts)
	}
	h := &harness{m: New(opts), st: st, pl: pl}
	h.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.run(h.m.Init())
	h.key("1") // out of the main menu into Latest
	return h
}

// run executes cmd and every message it yields, feeding them back.
func (h *harness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var got tea.Msg
	select {
	case got = <-ch:
	case <-time.After(300 * time.Millisecond):
		return // a timer such as the status clear: not waited for
	}
	switch msg := got.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
	case tea.QuitMsg:
	default:
		if strings.Contains(fmt.Sprintf("%T", msg), "sequenceMsg") {
			return // raw terminal output, nothing to assert
		}
		_, c := h.m.Update(msg)
		h.run(c)
	}
}

func (h *harness) send(msg tea.Msg) {
	_, c := h.m.Update(msg)
	h.run(c)
}

func (h *harness) key(s string) {
	h.send(keyMsg(s))
}

func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func (h *harness) screen() string { return ansi.Strip(h.m.render()) }

func TestOpenEpisodesAndPlay(t *testing.T) {
	h := newHarness(t, false)
	h.key("enter")
	if h.m.level != levelEpisodes || h.m.detail == nil || len(h.m.detail.Episodes) != 3 {
		t.Fatalf("detail not loaded: %+v", h.m.detail)
	}
	h.key("j")
	h.key("enter")
	if len(h.pl.played) != 1 || h.pl.played[0].Episode != "2" || h.pl.played[0].Title != "Romaji 1 Episode 2" {
		t.Fatalf("played %+v", h.pl.played)
	}
	if len(h.st.saved) != 1 {
		t.Error("history not saved")
	}
	h.key("h")
	if h.m.level != levelList {
		t.Error("h goes back")
	}
}

func TestEpisodeCursorStartsAtNext(t *testing.T) {
	h := newHarness(t, false)
	h.key("j") // a2: episode 1 watched in the fake history
	h.st.saved = []domain.HistoryEntry{{Episode: "1"}}
	h.key("enter")
	if h.m.epCursor != 1 {
		t.Errorf("cursor %d, want the episode after the last watched", h.m.epCursor)
	}
}

func TestModeToggle(t *testing.T) {
	h := newHarness(t, false)
	h.key("m")
	if h.m.opts.App.Mode != domain.ModeDub {
		t.Error("mode")
	}
}

func TestQuit(t *testing.T) {
	h := newHarness(t, false)
	_, cmd := h.m.Update(keyMsg("q"))
	if cmd == nil {
		t.Fatal("no quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q must quit")
	}
}

func TestSmallTerminal(t *testing.T) {
	h := newHarness(t, false)
	h.send(tea.WindowSizeMsg{Width: 30, Height: 8})
	if !strings.Contains(h.screen(), "too small") {
		t.Error("no hint")
	}
}

func TestMissingCoverDoesNotBreakTheScreen(t *testing.T) {
	h := newHarness(t, true)
	h.key("j")
	if !strings.Contains(h.screen(), "Romaji 2") {
		t.Error("list broken by a cover failure")
	}
}

func TestCoverPlaceholdersFitTheScreen(t *testing.T) {
	h := newHarness(t, true)
	e, _ := h.m.selected()
	cs := coverState{png: []byte("x"), w: 460, h: 650}
	h.m.pngs[e.Anime.CoverURL] = cs
	if h.m.screenHasCover() {
		t.Fatal("cover drawn before it reached the terminal")
	}
	cols, rows := h.m.coverCells(cs)
	h.m.sent[kitty.ID(fmt.Sprintf("%s@%dx%d", e.Anime.CoverURL, cols, rows))] = true
	if !h.m.screenHasCover() {
		t.Fatal("no placeholder cells")
	}
	for i, l := range strings.Split(h.m.render(), "\n") {
		if w := ansi.StringWidth(l); w != 120 {
			t.Fatalf("line %d is %d cells wide", i, w)
		}
	}
	if cols < 1 || rows < 4 || rows > 17 {
		t.Errorf("cover %dx%d", cols, rows)
	}
}

func (m *Model) screenHasCover() bool { return strings.ContainsRune(m.render(), '\U0010EEEE') }

func TestResizeFreesImages(t *testing.T) {
	h := newHarness(t, true)
	h.m.sent[7] = true
	_, cmd := h.m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if cmd == nil || len(h.m.sent) != 0 {
		t.Errorf("sent images survived a resize: %v", h.m.sent)
	}
}

func newHomeHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, false)
	h.m.goHome()
	return h
}

func (h *harness) click(x, y int) {
	h.send(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
}

func TestStartsAtMainMenuThenMovesLeft(t *testing.T) {
	fp := &fakeProvider{latest: []domain.Anime{{ID: "a1", Title: "A", AltTitle: "Romaji A", Sub: 1}}}
	a := &app.App{Provider: fp, Store: &fakeStore{}, Player: &fakePlayer{}, Mode: domain.ModeSub, Quality: "best"}
	h := &harness{m: New(Options{App: a, Theme: config.Default().Theme, Config: asciiConfig()})}
	h.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.run(h.m.Init())
	lw, mw, _ := h.m.widths()
	home := strings.Split(h.screen(), "\n")
	if !strings.Contains(home[0], "Main menu") {
		t.Errorf("top bar: %q", home[0])
	}
	for i, n := range homeItems {
		row := []rune(home[1+i])
		if strings.TrimSpace(string(row[:lw])) != "" {
			t.Errorf("the left column is empty on the main menu: %q", string(row[:lw]))
		}
		if !strings.Contains(string(row[lw:lw+mw]), n) {
			t.Errorf("row %d of the middle column lacks %q: %q", i, n, string(row[lw:lw+mw]))
		}
	}
	h.key("enter") // Latest is highlighted first
	if h.m.home || h.m.sec != secLatest || len(h.m.list.Entries) != 1 {
		t.Fatalf("home=%v sec=%d entries=%d", h.m.home, h.m.sec, len(h.m.list.Entries))
	}
	lines := strings.Split(h.screen(), "\n")
	if !strings.HasPrefix(lines[1], " Latest") || !strings.HasPrefix(lines[2], " Search") || !strings.HasPrefix(lines[3], " My List") {
		t.Errorf("the menu must have moved to the left column: %q %q", lines[1], lines[2])
	}
	if !strings.Contains(lines[1][lw:], "Romaji A") || strings.Contains(lines[0], "Main menu") {
		t.Errorf("list not in the middle column: %q", lines[1])
	}
	h.key("esc")
	if !h.m.home || h.m.homeCursor != int(secLatest) {
		t.Error("escape must return to the main menu")
	}
}

func TestMouseWheelScrollsOneRowPerNotch(t *testing.T) {
	h := newHarness(t, false)
	for want := 1; want <= 3; want++ {
		h.send(tea.MouseWheelMsg{X: 40, Y: 10, Button: tea.MouseWheelDown})
		if h.m.cursor != want {
			t.Fatalf("notch %d: cursor %d", want, h.m.cursor)
		}
	}
	h.send(tea.MouseWheelMsg{X: 40, Y: 10, Button: tea.MouseWheelUp})
	if h.m.cursor != 2 {
		t.Errorf("cursor %d", h.m.cursor)
	}
	for range 5 {
		h.send(tea.MouseWheelMsg{X: 40, Y: 10, Button: tea.MouseWheelUp})
	}
	if h.m.cursor != 0 {
		t.Errorf("cursor must stop at the top: %d", h.m.cursor)
	}
}

func TestViewEnablesMouse(t *testing.T) {
	h := newHarness(t, false)
	if h.m.View().MouseMode != tea.MouseModeCellMotion {
		t.Error("mouse not enabled")
	}
}

func TestMyListHydratesMissingCover(t *testing.T) {
	h := newHarness(t, true)
	h.key("esc")
	h.key("3") // the stored anime has no cover url
	e, ok := h.m.selected()
	if !ok || e.Anime.CoverURL != "http://c/l1" || e.Anime.Native != "JP" {
		t.Fatalf("not hydrated: %+v", e.Anime)
	}
	if _, asked := h.m.pngs["http://c/l1"]; !asked {
		t.Error("the cover was not requested after hydrating")
	}
}

// asciiConfig is the default configuration drawing with plain ASCII, which
// most tests assert on.
func asciiConfig() config.Config {
	c := config.Default()
	c.UI.Font = "ascii"
	return c
}

func TestMyListHasNoInListMarker(t *testing.T) {
	h := newHarness(t, false)
	lw, mw, _ := h.m.widths()
	if !strings.Contains(h.screen(), "*Romaji 2") {
		t.Fatal("Latest keeps the marker on anime in your list")
	}
	h.key("3") // My List
	rows := strings.Split(h.screen(), "\n")
	mid := string([]rune(rows[1])[lw : lw+mw])
	if strings.Contains(mid, "*") {
		t.Errorf("marker in My List: %q", mid)
	}
	if !strings.HasPrefix(mid, "|Listed") {
		t.Errorf("the title must start right after the separator, the column is removed: %q", mid)
	}
	if w := ansi.StringWidth(rows[1]); w != 120 {
		t.Errorf("row is %d cells wide", w)
	}
	nerd := New(Options{Config: config.Default()})
	if nerd.g.inList == "" {
		t.Error("glyph set")
	}
}

func TestLatestIsOneFullList(t *testing.T) {
	h := newHarness(t, false)
	if len(h.m.shown) != 25 || h.m.rowCount() != 25 {
		t.Fatalf("%d rows: all of the latest list, no pages", len(h.m.shown))
	}
	s := h.screen()
	if !strings.Contains(s, "Romaji 1") || !strings.Contains(s, "Romaji 25") || !strings.Contains(s, "[N] 5") {
		t.Errorf("list:\n%s", s)
	}
	if strings.Contains(s, "Next") || strings.Contains(s, "Prev") || strings.Contains(s, "page") {
		t.Errorf("paging is gone:\n%s", s)
	}
	if !strings.Contains(strings.Split(s, "\n")[0], "Latest - 25") {
		t.Errorf("top bar: %q", strings.Split(s, "\n")[0])
	}
	for i, l := range strings.Split(s, "\n") {
		if w := ansi.StringWidth(l); w != 120 {
			t.Fatalf("line %d is %d cells wide", i, w)
		}
	}
	h.key("G")
	if h.m.cursor != 24 {
		t.Errorf("G goes to the last anime: %d", h.m.cursor)
	}
	h.key("j")
	if h.m.cursor != 24 {
		t.Error("cursor must stop at the end")
	}
}

func TestNavigationBounds(t *testing.T) {
	h := newHarness(t, false)
	h.key("j")
	h.key("j")
	h.key("k")
	h.key("k")
	h.key("k")
	if h.m.cursor != 0 {
		t.Errorf("cursor %d", h.m.cursor)
	}
}

func TestFilterNarrowsTheListAsYouType(t *testing.T) {
	h := newHarness(t, false)
	h.key("/")
	if !h.m.typing {
		t.Fatal("/ opens the filter prompt")
	}
	h.typeText("2") // a2, a12, a20..a25
	if got := len(h.m.shown); got != 8 {
		t.Errorf("%d anime match", got)
	}
	s := h.screen()
	if !strings.Contains(strings.Split(s, "\n")[0], `filter "2" 8/25`) {
		t.Errorf("top bar: %q", strings.Split(s, "\n")[0])
	}
	if !strings.Contains(strings.Split(s, "\n")[29], "/2_") {
		t.Errorf("prompt: %q", strings.Split(s, "\n")[29])
	}
	if strings.Contains(s, "Romaji 3 ") || strings.Contains(s, "Romaji 19") {
		t.Errorf("a non matching anime is shown:\n%s", s)
	}
	h.key("enter") // keep
	if h.m.typing || h.m.filter != "2" {
		t.Error("enter keeps the filter")
	}
	h.key("j")
	if e, _ := h.m.selected(); e.Anime.ID != "a12" {
		t.Errorf("the cursor works on the filtered list: %s", e.Anime.ID)
	}
	h.key("enter") // opens the filtered anime, not the one at that index of the full list
	if h.m.detail == nil || h.m.detail.Anime.ID != "a12" {
		t.Errorf("%+v", h.m.detail)
	}
}

func TestFilterMatchesEveryTitleAndShowsNoMatch(t *testing.T) {
	h := newHarness(t, false)
	h.key("/")
	h.typeText("anime 25") // words: the English title of a25
	if len(h.m.shown) != 1 || h.m.shown[0].Anime.ID != "a25" {
		t.Errorf("english title: %d", len(h.m.shown))
	}
	h.key("ctrl+u")
	if len(h.m.shown) != 25 {
		t.Errorf("clearing the text shows everything: %d", len(h.m.shown))
	}
	h.typeText("zzz")
	if !strings.Contains(h.screen(), "no match") {
		t.Error("an empty result must say so")
	}
	if _, ok := h.m.selected(); ok {
		t.Error("nothing is selected in an empty list")
	}
}

func TestFilterEscapeAndBack(t *testing.T) {
	h := newHarness(t, false)
	h.key("/")
	h.typeText("romaji 2")
	h.key("esc") // while typing: drops the filter
	if h.m.typing || h.m.filter != "" || len(h.m.shown) != 25 {
		t.Fatalf("typing=%v filter=%q shown=%d", h.m.typing, h.m.filter, len(h.m.shown))
	}
	h.key("/")
	h.typeText("romaji 2")
	h.key("enter")
	h.key("esc") // back: the filter first
	if h.m.home || h.m.filter != "" {
		t.Errorf("home=%v filter=%q: back must drop the filter before leaving the list", h.m.home, h.m.filter)
	}
	h.key("esc")
	if !h.m.home {
		t.Error("then the main menu")
	}
	// a left column click is back as well
	h.key("1")
	h.key("/")
	h.typeText("romaji 3")
	h.key("enter")
	h.click(1, 5)
	if h.m.home || h.m.filter != "" {
		t.Error("clicking the left column drops the filter first")
	}
}

func TestFilterIsClearedBySwitchingSections(t *testing.T) {
	h := newHarness(t, false)
	h.key("/")
	h.typeText("romaji 2")
	h.key("enter")
	h.key("3") // My List
	if h.m.filter != "" || len(h.m.shown) != 1 {
		t.Errorf("filter %q, %d shown", h.m.filter, len(h.m.shown))
	}
	h.key("/")
	h.typeText("listed")
	if len(h.m.shown) != 1 {
		t.Error("the filter works on My List")
	}
}

func TestFilterIgnoredInEpisodes(t *testing.T) {
	h := newHarness(t, false)
	h.key("enter")
	h.key("/")
	if h.m.typing {
		t.Error("/ filters lists, not the episodes")
	}
}

func TestFilterPaste(t *testing.T) {
	h := newHarness(t, false)
	h.key("/")
	h.send(tea.PasteMsg{Content: "romaji\n 2"})
	if h.m.input != "romaji 2" || len(h.m.shown) != 8 { // 2, 12, 20..25
		t.Errorf("%q %d", h.m.input, len(h.m.shown))
	}
}

func TestSearchOnTheMainMenuAsksForAKeyword(t *testing.T) {
	h := newHomeHarness(t)
	if scr := h.screen(); !strings.Contains(scr, "Search") {
		t.Fatalf("the menu has a Search entry:\n%s", scr)
	}
	h.key("j")
	if !strings.Contains(h.screen(), "find an anime") {
		t.Error("the hint of the highlighted entry")
	}
	h.key("enter") // Search
	if !h.m.sdlg || !h.m.home {
		t.Fatal("Search opens the keyword popup, over the menu")
	}
	scr := h.screen()
	if !strings.Contains(scr, "Search keyword") || !strings.Contains(scr, "enter searches") {
		t.Errorf("popup:\n%s", scr)
	}
	for i, l := range strings.Split(scr, "\n") {
		if w := ansi.StringWidth(l); w != 120 {
			t.Fatalf("line %d is %d cells wide with the popup", i, w)
		}
	}
	h.key("enter") // empty keyword: nothing happens
	if !h.m.sdlg {
		t.Error("an empty keyword must not close the popup")
	}
	h.typeText("frieren")
	if !strings.Contains(h.screen(), " frieren_") {
		t.Error("typed text not shown")
	}
	h.key("enter")
	if h.m.sdlg || h.m.home || h.m.sec != secSearch || h.m.query != "frieren" {
		t.Fatalf("sdlg=%v home=%v sec=%d query=%q", h.m.sdlg, h.m.home, h.m.sec, h.m.query)
	}
	if !strings.Contains(h.screen(), `Search "frieren"`) {
		t.Error("top bar")
	}
}

func TestSearchLoadsPagesUpToFifty(t *testing.T) {
	h := newHomeHarness(t)
	h.key("2") // Search
	h.typeText("x")
	h.key("enter")
	if len(h.m.list.Entries) != app.SearchLimit {
		t.Fatalf("%d results, want exactly %d of 60", len(h.m.list.Entries), app.SearchLimit)
	}
	fp := h.m.opts.App.Provider.(*fakeProvider)
	if got := strings.Join(fp.searchCursors, ","); got != ",c2,c3" {
		t.Errorf("pages asked for: %q", got)
	}
	if h.m.more || h.m.list.Entries[49].Anime.ID != "s50" {
		t.Errorf("more=%v last=%s", h.m.more, h.m.list.Entries[49].Anime.ID)
	}
	h.key("G")
	if h.m.cursor != 49 {
		t.Errorf("one long list: %d", h.m.cursor)
	}
	if strings.Contains(h.screen(), "Next") {
		t.Error("no paging rows")
	}
}

func TestSearchShowsTheFirstPageBeforeTheRest(t *testing.T) {
	h := newHomeHarness(t)
	h.key("2")
	h.typeText("x")
	_, cmd := h.m.Update(keyMsg("enter"))
	msg := cmd // batch of load + cover cmds
	var first tea.Msg
	var find func(c tea.Cmd)
	find = func(c tea.Cmd) {
		if c == nil || first != nil {
			return
		}
		switch r := c().(type) {
		case tea.BatchMsg:
			for _, cc := range r {
				find(cc)
			}
		case listMsg:
			first = r
		}
	}
	find(msg)
	_, next := h.m.Update(first)
	if len(h.m.list.Entries) != 24 || !h.m.more || !strings.Contains(h.screen(), "loading more") {
		t.Fatalf("%d entries after the first page, more=%v", len(h.m.list.Entries), h.m.more)
	}
	if next == nil {
		t.Fatal("the next page must be requested")
	}
}

func TestSearchAgainDropsTheOldLoad(t *testing.T) {
	h := newHomeHarness(t)
	h.key("2")
	h.typeText("a")
	h.key("enter")
	old := h.m.seq
	h.m.goHome()
	h.key("2")
	h.key("ctrl+u")
	h.typeText("b")
	h.key("enter")
	if h.m.seq == old || h.m.query != "b" || len(h.m.list.Entries) != app.SearchLimit {
		t.Errorf("seq %d query %q entries %d", h.m.seq, h.m.query, len(h.m.list.Entries))
	}
	before := len(h.m.list.Entries)
	h.send(moreMsg{seq: old, l: app.Listing{Entries: []app.Entry{{Anime: domain.Anime{ID: "stale"}}}}})
	if len(h.m.list.Entries) != before {
		t.Error("a stale page must be dropped")
	}
}

func TestSearchPopupCancelAndPrefill(t *testing.T) {
	h := newHomeHarness(t)
	h.key("2")
	h.typeText("abc")
	h.key("esc")
	if h.m.sdlg || !h.m.home || h.m.query != "" {
		t.Errorf("esc cancels: sdlg=%v home=%v query=%q", h.m.sdlg, h.m.home, h.m.query)
	}
	h.key("2")
	h.typeText("abc")
	h.key("enter")
	h.key("tab") // My List
	h.key("tab") // wraps to Latest
	h.key("tab") // Search: asks again
	if !h.m.sdlg || h.m.sbuf != "abc" || h.m.sec != secLatest {
		t.Errorf("sdlg=%v prefill=%q sec=%d", h.m.sdlg, h.m.sbuf, h.m.sec)
	}
	h.key("esc")
	if h.m.sec != secLatest || h.m.home {
		t.Error("cancelling keeps the view you were in")
	}
}

func TestSearchPopupKeysAndPaste(t *testing.T) {
	h := newHomeHarness(t)
	h.key("/") // on the main menu, / is a shortcut to the same popup
	if !h.m.sdlg {
		t.Fatal("/ on the main menu")
	}
	h.typeText("q1/m j")
	h.send(tea.PasteMsg{Content: "\nxy\r"})
	if h.m.sbuf != "q1/m jxy" || h.m.quit || h.m.sec != secLatest {
		t.Errorf("%q", h.m.sbuf)
	}
	h.key("backspace")
	h.key("ctrl+u")
	if h.m.sbuf != "" {
		t.Errorf("%q", h.m.sbuf)
	}
}

func TestSearchPopupMouse(t *testing.T) {
	h := newHomeHarness(t)
	h.key("2")
	h.click(1, 1) // outside the popup
	if h.m.sdlg {
		t.Error("a click outside cancels")
	}
	h.key("2")
	x0, y0, w, _ := h.m.searchGeom()
	h.click(x0+w/2, y0+2) // inside: nothing
	if !h.m.sdlg {
		t.Error("a click inside keeps the popup")
	}
	h.send(tea.MouseWheelMsg{X: x0 + 3, Y: y0 + 2, Button: tea.MouseWheelDown})
	h.send(tea.MouseClickMsg{X: x0 + 3, Y: y0 + 2, Button: tea.MouseRight})
	if h.m.sdlg {
		t.Error("right click cancels")
	}
}

func TestSlashInSearchAsksForANewKeyword(t *testing.T) {
	h := newHomeHarness(t)
	h.key("2")
	h.typeText("first")
	h.key("enter")
	h.key("/")
	if !h.m.sdlg || h.m.typing || h.m.sbuf != "first" {
		t.Fatalf("sdlg=%v typing=%v prefill=%q: / in the results opens the keyword popup", h.m.sdlg, h.m.typing, h.m.sbuf)
	}
	h.key("ctrl+u")
	h.typeText("second")
	h.key("enter")
	if h.m.query != "second" || h.m.sec != secSearch || len(h.m.list.Entries) != app.SearchLimit {
		t.Errorf("query %q, %d results", h.m.query, len(h.m.list.Entries))
	}
	if !strings.Contains(h.screen(), `Search "second"`) {
		t.Error("top bar")
	}
	h.key("/")
	h.key("esc") // cancel: the results stay
	if h.m.query != "second" || len(h.m.list.Entries) != app.SearchLimit || h.m.sdlg {
		t.Error("cancelling keeps the results")
	}
	h.key("enter") // open an anime, then / does nothing there
	h.key("/")
	if h.m.sdlg || h.m.typing {
		t.Error("/ is for lists, not episodes")
	}
}

func TestSectionsAndMyList(t *testing.T) {
	h := newHarness(t, false)
	h.key("3")
	if h.m.sec != secMyList || len(h.m.list.Entries) != 1 {
		t.Fatalf("sec %d entries %d", h.m.sec, len(h.m.list.Entries))
	}
	if s := h.screen(); !strings.Contains(s, "3/12") {
		t.Errorf("counter missing:\n%s", s)
	}
	h.key("tab")
	if h.m.sec != secLatest {
		t.Errorf("tab wraps to latest: %d", h.m.sec)
	}
	h.key("shift+tab") // My List again
	if h.m.sec != secMyList {
		t.Errorf("shift+tab: %d", h.m.sec)
	}
}

func TestMainMenuKeys(t *testing.T) {
	h := newHomeHarness(t)
	h.key("j")
	h.key("j")
	h.key("enter") // My List
	if h.m.sec != secMyList || h.m.home {
		t.Errorf("sec %d home %v", h.m.sec, h.m.home)
	}
	h.m.goHome()
	if h.m.homeCursor != 2 {
		t.Errorf("the cursor returns to the section left: %d", h.m.homeCursor)
	}
	h.key("G")
	if homeItems[h.m.homeCursor] != "Quit" {
		t.Errorf("G went to %q", homeItems[h.m.homeCursor])
	}
	h.key("j") // wraps
	if homeItems[h.m.homeCursor] != "Latest" {
		t.Errorf("wrap went to %q", homeItems[h.m.homeCursor])
	}
	h.key("3")
	if h.m.sec != secMyList {
		t.Error("3 is My List")
	}
	h.m.goHome()
	h.key("G")
	h.key("enter")
	if !h.m.quit {
		t.Error("Quit item")
	}
}

func TestMouseOnMainMenu(t *testing.T) {
	h := newHomeHarness(t)
	lw, mw, _ := h.m.widths()
	h.m.homeCursor = 0
	h.click(lw+3, 1+2) // My List: first click selects
	if !h.m.home || h.m.homeCursor != 2 {
		t.Fatalf("home=%v cursor=%d", h.m.home, h.m.homeCursor)
	}
	h.click(lw+3, 1+2) // second click enters
	if h.m.home || h.m.sec != secMyList {
		t.Errorf("home=%v sec=%d", h.m.home, h.m.sec)
	}
	h.m.goHome()
	h.m.homeCursor = 0
	h.click(lw+mw+3, 5) // right column enters the selected item
	if h.m.home || h.m.sec != secLatest {
		t.Errorf("right column: home=%v sec=%d", h.m.home, h.m.sec)
	}
	h.key("esc")
	h.m.goHome()
	h.m.homeCursor = 1
	h.click(lw+mw+3, 5) // Search through the right column
	if !h.m.sdlg {
		t.Error("Search must ask for a keyword")
	}
	h.key("esc")
	h.m.homeCursor = 0
	h.send(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
	if h.m.homeCursor != 1 {
		t.Errorf("wheel moved the cursor to %d", h.m.homeCursor)
	}
	h.click(1, 5) // left column from the main menu: exit
	if !h.m.quit {
		t.Error("left column on the main menu must exit")
	}
}

func TestClickColumns(t *testing.T) {
	h := newHarness(t, false)
	lw, mw, _ := h.m.widths()
	h.screen()
	right := lw + mw + 3

	// right column: open the selected anime, then play the selected episode
	h.click(right, 5)
	if h.m.level != levelEpisodes || h.m.detail == nil {
		t.Fatal("a click on the right column must open the anime")
	}
	h.click(right, 5)
	if len(h.pl.played) != 1 {
		t.Fatalf("a click on the right column must play the episode: %d", len(h.pl.played))
	}
	// left column: back, back again to the main menu
	h.click(1, 10)
	if h.m.level != levelList || h.m.home {
		t.Fatalf("level %d home %v", h.m.level, h.m.home)
	}
	h.click(1, 10)
	if !h.m.home {
		t.Fatal("a click on the left column from a list must reach the main menu")
	}
	// the middle column only selects
	h.key("1")
	h.click(lw+3, 1+2)
	if h.m.cursor != 2 || h.m.level != levelList {
		t.Errorf("cursor %d level %d", h.m.cursor, h.m.level)
	}
}

func TestLeftClickDoesNotSwitchSections(t *testing.T) {
	h := newHarness(t, false)
	h.click(2, 3)
	if !h.m.home {
		t.Error("the left column is back, not a section switch")
	}
}

func TestCursorIsRemembered(t *testing.T) {
	h := newHarness(t, false)
	for range 5 {
		h.key("j")
	}
	h.key("3") // another section
	h.key("1") // and back
	if h.m.cursor != 5 {
		t.Errorf("section switch lost the cursor: %d", h.m.cursor)
	}
	h.key("esc")
	h.key("enter") // via the main menu
	if h.m.cursor != 5 {
		t.Errorf("main menu round trip lost the cursor: %d", h.m.cursor)
	}
	h.key("enter") // open
	h.key("j")
	h.key("j")
	h.key("h")
	h.key("enter")
	if h.m.epCursor != 2 {
		t.Errorf("episode cursor not remembered: %d", h.m.epCursor)
	}
}

func TestMouseSectionsListAndBack(t *testing.T) {
	h := newHarness(t, false)
	lw, _, _ := h.m.widths()
	h.screen()
	h.click(lw+3, 1+4) // fifth row
	if h.m.cursor != 4 || h.m.level != levelList {
		t.Errorf("first click only selects: cursor %d level %d", h.m.cursor, h.m.level)
	}
	h.click(lw+3, 1+4) // again: open
	if h.m.level != levelEpisodes || h.m.detail == nil {
		t.Fatal("second click must open the episodes")
	}
	h.screen()
	h.click(lw+3, 1+1)
	if h.m.epCursor != 1 || len(h.pl.played) != 0 {
		t.Errorf("episode click only selects: %d %d", h.m.epCursor, len(h.pl.played))
	}
	h.click(lw+3, 1+1)
	if len(h.pl.played) != 1 || h.pl.played[0].Episode != "2" {
		t.Errorf("played %+v", h.pl.played)
	}
	h.send(tea.MouseClickMsg{X: 50, Y: 10, Button: tea.MouseRight})
	if h.m.level != levelList || h.m.home {
		t.Error("right click goes back one level")
	}
	h.send(tea.MouseClickMsg{X: 50, Y: 10, Button: tea.MouseRight})
	if !h.m.home {
		t.Error("right click again returns to the main menu")
	}
}

// A reload that reorders the list (watching moves an anime to the top) must
// leave the cursor on the same anime.
func TestReloadKeepsCursorOnAnime(t *testing.T) {
	h := newHarness(t, false)
	e := func(id string) app.Entry { return app.Entry{Anime: domain.Anime{ID: id, Title: id}} }
	h.m.list = app.Listing{Entries: []app.Entry{e("a"), e("b"), e("c")}}
	h.m.refilter()
	h.m.cursor = 2 // c
	h.m.seq = 7
	h.send(listMsg{seq: 7, l: app.Listing{Entries: []app.Entry{e("c"), e("a"), e("b")}}})
	if got, _ := h.m.selected(); got.Anime.ID != "c" {
		t.Errorf("cursor on %q after reorder, want c", got.Anime.ID)
	}
}
