package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"sel-ani/internal/config"
	"sel-ani/internal/domain"
)

type fakeSaver struct {
	applied  []config.Config
	applyErr error
	cfg      config.Config
	calls    []string
	err      error
	logins   []config.AniDBLogin
	loginErr error
	existing config.AniDBLogin
}

func (f *fakeSaver) save(key string, v any) (config.Config, error) {
	if f.err != nil {
		return config.Config{}, f.err
	}
	f.calls = append(f.calls, fmt.Sprintf("%s=%v", key, v))
	switch key {
	case "mode":
		f.cfg.Mode = v.(string)
	case "quality":
		f.cfg.Quality = v.(string)
	case "ui.cover":
		f.cfg.UI.Cover = v.(bool)
	case "theme.name":
		f.cfg.Theme, _ = config.LookupTheme(v.(string))
	case "player.command":
		f.cfg.Player.Command = v.(string)
	case "browser.enabled":
		f.cfg.Browser.Enabled = v.(bool)
	case "ui.font":
		f.cfg.UI.Font = v.(string)
	case "anidb.enabled":
		f.cfg.AniDB.Enabled = v.(bool)
	}
	return f.cfg, nil
}

func (f *fakeSaver) apply(c config.Config) error {
	f.applied = append(f.applied, c)
	return f.applyErr
}

func (f *fakeSaver) saveLogin(l config.AniDBLogin) (config.Config, error) {
	if f.loginErr != nil {
		return config.Config{}, f.loginErr
	}
	f.logins = append(f.logins, l)
	f.cfg.AniDB.Enabled = true
	return f.cfg, nil
}

func newPopupHarness(t *testing.T) (*harness, *fakeSaver) {
	t.Helper()
	fs := &fakeSaver{cfg: asciiConfig()}
	h := newHarnessOpts(t, false, func(o *Options) {
		o.Config, o.SaveConfig, o.SaveLogin, o.Apply = fs.cfg, fs.save, fs.saveLogin, fs.apply
		o.ReadLogin = func() config.AniDBLogin { return fs.existing }
		o.CoverSupported = true
	})
	h.m.goHome()
	h.key("j")
	h.key("j")
	h.key("j")
	h.key("enter") // Config
	if !h.m.pop.open {
		t.Fatal("the Config entry did not open the popup")
	}
	return h, fs
}

// pop moves the popup cursor to the item with key.
func (h *harness) pop(key string) {
	h.key("g")
	for _, it := range h.m.items() {
		if it.key == key {
			return
		}
		h.key("j")
	}
	panic("no item " + key)
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestMenuHasConfigAndPopupOpens(t *testing.T) {
	h, _ := newPopupHarness(t)
	scr := h.screen()
	if !strings.Contains(scr, "Config - every change is saved at once") || !strings.Contains(scr, "mode") {
		t.Errorf("popup not drawn:\n%s", scr)
	}
	lines := strings.Split(scr, "\n")
	if len(lines) != 30 {
		t.Fatalf("%d lines", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 120 {
			t.Fatalf("line %d is %d cells wide with the popup open: %q", i, w, l)
		}
	}
	if !strings.Contains(lines[0], "Main menu") || !strings.Contains(lines[29], "mouse") {
		t.Error("the screen must stay visible around the popup")
	}
}

func TestPopupSmallTerminalStillFits(t *testing.T) {
	h, _ := newPopupHarness(t)
	h.send(tea.WindowSizeMsg{Width: 60, Height: 14})
	for i, l := range strings.Split(h.screen(), "\n") {
		if w := ansi.StringWidth(l); w != 60 {
			t.Fatalf("line %d is %d cells wide", i, w)
		}
	}
}

func TestPopupKeysDoNotLeak(t *testing.T) {
	h, fs := newPopupHarness(t)
	h.pop("player.command")
	h.key("enter") // edit
	h.key("ctrl+u")
	h.typeText("q1/m j")
	if h.m.quit || !h.m.pop.open {
		t.Fatal("a typed key reached the global handlers")
	}
	if h.m.pop.buf != "q1/m j" {
		t.Errorf("buffer %q", h.m.pop.buf)
	}
	if h.m.typing || h.m.sec != secLatest {
		t.Error("/ or 1 acted while editing")
	}
	h.key("esc")
	if h.m.pop.editing || len(fs.calls) != 0 {
		t.Errorf("escape must cancel the edit without saving: %v", fs.calls)
	}
	h.key("m")
	h.key("tab")
	h.key("1")
	if h.m.cfg.Mode != "sub" || h.m.sec != secLatest || !h.m.home {
		t.Error("global keys must not act behind the popup")
	}
}

func TestToggleAndEnumSaveAtOnce(t *testing.T) {
	h, fs := newPopupHarness(t)
	h.pop("mode")
	h.key("enter")
	if h.m.opts.App.Mode != domain.ModeDub || len(fs.calls) != 1 || fs.calls[0] != "mode=dub" {
		t.Fatalf("calls %v, mode %v", fs.calls, h.m.opts.App.Mode)
	}
	if !strings.Contains(h.screen(), "saved") || strings.Contains(h.screen(), "restart") {
		t.Error("mode applies live")
	}
	h.key("h") // back
	if h.m.opts.App.Mode != domain.ModeSub {
		t.Error("h must step back")
	}
	h.pop("quality")
	h.key("l")
	if h.m.opts.App.Quality != "1080p" {
		t.Errorf("quality %q", h.m.opts.App.Quality)
	}
	bg := h.m.st.bg
	h.pop("theme.name")
	h.key("l")
	if h.m.st.bg == bg {
		t.Error("the theme must apply live")
	}
	h.pop("ui.cover")
	h.send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if h.m.cfg.UI.Cover {
		t.Error("cover toggle")
	}
	h.pop("browser.enabled")
	h.key("enter")
	if !h.m.cfg.Browser.Enabled || len(fs.applied) == 0 || !fs.applied[len(fs.applied)-1].Browser.Enabled {
		t.Error("a browser change must be applied at once, without a restart")
	}
}

func TestTextEditSavesOnEnter(t *testing.T) {
	h, fs := newPopupHarness(t)
	h.pop("player.command")
	h.key("enter")
	for range 10 {
		h.key("backspace")
	}
	h.typeText("vlc")
	h.key("enter")
	if len(fs.calls) != 1 || fs.calls[0] != "player.command=vlc" || h.m.pop.editing {
		t.Errorf("%v editing=%v", fs.calls, h.m.pop.editing)
	}
	if !strings.Contains(h.screen(), "vlc") {
		t.Error("new value not shown")
	}
}

func TestIntAndListEdits(t *testing.T) {
	h, fs := newPopupHarness(t)
	h.pop("anidb.local_port")
	h.key("enter")
	h.key("ctrl+u")
	h.typeText("12x")
	h.key("enter")
	if len(fs.calls) != 0 || !h.m.pop.editing || !strings.Contains(h.screen(), "not a number") {
		t.Errorf("a bad number must stay in the editor: %v", fs.calls)
	}
	h.key("esc")
	h.pop("provider.servers")
	h.key("enter")
	h.key("ctrl+u")
	h.typeText("sun, bee hop")
	h.key("enter")
	if len(fs.calls) != 1 || fs.calls[0] != "provider.servers=[sun bee hop]" {
		t.Errorf("%v", fs.calls)
	}
}

func TestSaveErrorIsShownAndKeepsConfig(t *testing.T) {
	h, fs := newPopupHarness(t)
	fs.err = errors.New("mode: must be sub or dub")
	h.pop("mode")
	h.key("enter")
	if h.m.cfg.Mode != "sub" || h.m.opts.App.Mode != domain.ModeSub {
		t.Error("a failed save must not change anything")
	}
	if s := h.screen(); !strings.Contains(s, "mode: must be sub or dub") {
		t.Errorf("error not shown:\n%s", s)
	}
}

func TestPasteIsSanitised(t *testing.T) {
	h, _ := newPopupHarness(t)
	h.pop("player.command")
	h.key("enter")
	h.key("ctrl+u")
	h.send(tea.PasteMsg{Content: "mp\nv\r\t\x1b"})
	if h.m.pop.buf != "mpv" {
		t.Errorf("%q", h.m.pop.buf)
	}
}

func TestPopupCursorIsRemembered(t *testing.T) {
	h, _ := newPopupHarness(t)
	h.pop("provider.base_url")
	want := h.m.pop.cursor
	h.key("esc")
	if h.m.pop.open {
		t.Fatal("esc must close")
	}
	h.m.homeCursor = 3
	h.key("enter")
	if h.m.pop.cursor != want {
		t.Errorf("cursor %d, want %d", h.m.pop.cursor, want)
	}
}

func TestPopupMouse(t *testing.T) {
	h, fs := newPopupHarness(t)
	x0, y0, bw, _ := h.m.popupGeom()
	h.screen()
	h.click(x0+5, y0+3) // second row: quality, selects
	if len(fs.calls) != 0 || h.m.pop.cursor != 1 {
		t.Fatalf("the first click only selects: %v %d", fs.calls, h.m.pop.cursor)
	}
	h.click(x0+5, y0+3) // second click activates
	if len(fs.calls) != 1 || fs.calls[0] != "quality=1080p" {
		t.Errorf("%v", fs.calls)
	}
	h.send(tea.MouseWheelMsg{X: x0 + 5, Y: y0 + 5, Button: tea.MouseWheelDown})
	if h.m.pop.cursor != 2 {
		t.Errorf("wheel: %d", h.m.pop.cursor)
	}
	h.click(x0+bw+2, y0+2) // outside
	if h.m.pop.open {
		t.Error("a click outside closes the popup")
	}
	h.m.homeCursor = 3
	h.key("enter")
	h.send(tea.MouseClickMsg{X: x0 + 5, Y: y0 + 3, Button: tea.MouseRight})
	if h.m.pop.open {
		t.Error("right click closes")
	}
}

func TestWizardWritesOnlyAtTheEnd(t *testing.T) {
	h, fs := newPopupHarness(t)
	fs.existing = config.AniDBLogin{Client: "selani", ClientVersion: 3, Username: "me", Password: "oldpw", APIKey: "oldkey"}
	h.pop("anidb.account")
	h.key("enter")
	w := h.m.pop.wiz
	if w == nil || w.vals[0] != "selani" || w.vals[1] != "3" || w.vals[2] != "me" || w.vals[3] != "" {
		t.Fatalf("prefill: %+v", w)
	}
	if !strings.Contains(h.screen(), "step 1 of 5") {
		t.Error("no progress shown")
	}
	h.key("enter") // client kept
	h.key("enter") // version kept
	h.key("enter") // username kept
	h.typeText("S3cretPW")
	scr := h.screen()
	if strings.Contains(scr, "S3cretPW") || !strings.Contains(scr, "********") {
		t.Errorf("the password must be masked:\n%s", scr)
	}
	h.key("enter")
	if len(fs.logins) != 0 {
		t.Fatal("saved before the last step")
	}
	h.key("enter") // API key kept (empty input keeps the saved one)
	if len(fs.logins) != 1 {
		t.Fatalf("logins %v", fs.logins)
	}
	got := fs.logins[0]
	if got.Client != "selani" || got.ClientVersion != 3 || got.Username != "me" || got.Password != "S3cretPW" || got.APIKey != "" {
		t.Errorf("%+v", got)
	}
	if s := h.screen(); !strings.Contains(s, "mode 0600") || !strings.Contains(s, "enabled") || strings.Contains(s, "S3cretPW") {
		t.Errorf("result screen:\n%s", s)
	}
	if !h.m.cfg.AniDB.Enabled {
		t.Error("the new config must be adopted")
	}
	h.key("x") // any key closes the result
	if h.m.pop.wiz != nil || !h.m.pop.open {
		t.Error("back to the list")
	}
	if len(fs.applied) == 0 || !fs.applied[len(fs.applied)-1].AniDB.Enabled {
		t.Error("the new login must be applied at once")
	}
}

func TestWizardValidationAndCancel(t *testing.T) {
	h, fs := newPopupHarness(t)
	h.pop("anidb.account")
	h.key("enter")
	h.key("enter") // empty client
	if h.m.pop.wiz.step != 0 || !strings.Contains(h.screen(), "required") {
		t.Error("an empty required field must stop")
	}
	h.typeText("selani")
	h.key("enter")
	h.typeText("abc")
	h.key("enter")
	if h.m.pop.wiz.step != 1 || !strings.Contains(h.screen(), "positive number") {
		t.Error("the version must be a number")
	}
	h.key("esc")
	if h.m.pop.wiz != nil || len(fs.logins) != 0 {
		t.Error("escape must cancel without writing")
	}
	// secrets are required when nothing is saved yet
	h.pop("anidb.account")
	h.key("enter")
	h.typeText("c")
	h.key("enter")
	h.typeText("1")
	h.key("enter")
	h.typeText("u")
	h.key("enter")
	h.key("enter")
	if h.m.pop.wiz.step != 3 || !strings.Contains(h.screen(), "Password is required") {
		t.Error("no saved password and none typed")
	}
}

func TestWizardSaveErrorStaysOnTheLastStep(t *testing.T) {
	h, fs := newPopupHarness(t)
	fs.loginErr = errors.New("client: bad name")
	h.pop("anidb.account")
	h.key("enter")
	for _, s := range []string{"c", "1", "u", "p", "k"} {
		h.typeText(s)
		h.key("enter")
	}
	w := h.m.pop.wiz
	if w == nil || w.done != nil || w.step != 4 || !strings.Contains(h.screen(), "client: bad name") {
		t.Errorf("%+v", w)
	}
}

func TestEveryItemSavesItsCurrentValue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range configItems() {
		if it.kind == kWizard {
			continue
		}
		cur := it.get(cfg)
		var v any = cur
		switch it.kind {
		case kBool:
			v, _ = strconv.ParseBool(cur)
		case kInt:
			v, _ = strconv.Atoi(cur)
		case kList:
			v = strings.Fields(strings.ReplaceAll(cur, ",", " "))
		}
		if _, err := config.Save(cfg.Source, it.key, v); err != nil {
			t.Errorf("%s = %v: %v", it.key, v, err)
		}
		n++
	}
	if n < 28 {
		t.Errorf("only %d items", n)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "sel-ani", "config.toml"))
	if _, err := config.Load(cfg.Source); err != nil {
		t.Errorf("the saved file does not load: %v\n%s", err, b)
	}
}

func TestItemsCoverEveryConfigKey(t *testing.T) {
	have := map[string]bool{}
	for _, it := range configItems() {
		have[it.key] = true
	}
	want := []string{"mode", "quality", "player.command", "player.args", "ui.cover", "theme.name",
		"provider.base_url", "provider.user_agent", "provider.timeout", "provider.servers",
		"browser.enabled", "browser.remote_url", "browser.bin", "browser.headless",
		"anidb.enabled", "anidb.server", "anidb.local_port", "anidb.account"}
	for _, c := range config.ThemeColors {
		want = append(want, "theme."+c)
	}
	for _, k := range want {
		if !have[k] {
			t.Errorf("no item for %s", k)
		}
	}
}

func TestOptionRowsShowArrows(t *testing.T) {
	h, _ := newPopupHarness(t)
	scr := h.screen()
	for _, want := range []string{"mode", "<< sub >>", "ui.font", "<< ASCII >>", "ui.cover", "<< on >>"} {
		if !strings.Contains(scr, want) {
			t.Errorf("rows lack %q:\n%s", want, scr)
		}
	}
	h.pop("browser.enabled")
	for _, l := range strings.Split(h.screen(), "\n") {
		if strings.Contains(l, "browser.enabled") {
			if !strings.Contains(l, "<< off >>") {
				t.Errorf("bool row: %q", l)
			}
			return
		}
	}
	t.Error("no browser.enabled row")
}

func TestArrowKeysSetTheValue(t *testing.T) {
	h, fs := newPopupHarness(t)
	h.pop("ui.font")
	h.send(tea.KeyPressMsg{Code: tea.KeyRight})
	if fs.cfg.UI.Font != "nerdfont" {
		t.Fatalf("right arrow: %q", fs.cfg.UI.Font)
	}
	h.send(tea.KeyPressMsg{Code: tea.KeyLeft})
	if fs.cfg.UI.Font != "ascii" {
		t.Fatalf("left arrow: %q", fs.cfg.UI.Font)
	}
	h.pop("mode")
	h.send(tea.KeyPressMsg{Code: tea.KeyLeft}) // wraps
	if fs.cfg.Mode != "dub" {
		t.Errorf("mode %q", fs.cfg.Mode)
	}
	h.pop("browser.headless")
	h.send(tea.KeyPressMsg{Code: tea.KeyRight})
	if len(fs.calls) < 4 || fs.calls[len(fs.calls)-1] != "browser.headless=false" {
		t.Errorf("a bool toggles with the arrows too: %v", fs.calls)
	}
}

func TestFontStyleSwitchesEveryGlyphLive(t *testing.T) {
	h, _ := newPopupHarness(t)
	ascii := h.screen()
	if !strings.Contains(ascii, "+") || !strings.Contains(ascii, "|") || strings.Contains(ascii, "│") || strings.Contains(ascii, "╭") {
		t.Errorf("ascii font:\n%s", ascii)
	}
	h.pop("ui.font")
	h.send(tea.KeyPressMsg{Code: tea.KeyRight}) // Nerdfont
	nerd := h.screen()
	for _, want := range []string{"╭", "╮", "╰", "╯", "│", "─", "\uf104 Nerdfont \uf105"} {
		if !strings.Contains(nerd, want) {
			t.Errorf("nerd font lacks %q:\n%s", want, nerd)
		}
	}
	if strings.Contains(nerd, "|") || strings.Contains(nerd, "+---") {
		t.Errorf("an ASCII border left over:\n%s", nerd)
	}
	for i, l := range strings.Split(nerd, "\n") {
		if w := ansi.StringWidth(l); w != 120 {
			t.Fatalf("line %d is %d cells wide", i, w)
		}
	}
	h.key("esc")
	h.m.homeCursor = 0
	h.key("enter")
	list := h.screen()
	for _, want := range []string{"│", "\uf005"} { // separator, in-list star
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}
	if strings.Contains(list, "|") {
		t.Errorf("ASCII symbols with the nerd font:\n%s", list)
	}
	h.key("enter") // open episodes
	if eps := h.screen(); !strings.Contains(eps, "\uf096") && !strings.Contains(eps, "\uf046") {
		t.Errorf("episode marks:\n%s", eps)
	}
}

func TestNerdFontIsTheDefault(t *testing.T) {
	if g := glyphsFor(""); g != nerdGlyphs {
		t.Error("an empty font must mean Nerd Font")
	}
	if glyphsFor("ascii") != asciiGlyphs || glyphsFor("nerdfont") != nerdGlyphs {
		t.Error("glyph sets")
	}
	m := New(Options{Config: config.Default()})
	if m.g != nerdGlyphs {
		t.Error("the default config must draw with Nerd Font glyphs")
	}
}

func TestEverySettingIsAppliedLive(t *testing.T) {
	h, fs := newPopupHarness(t)
	for _, k := range []string{"player.command", "provider.base_url", "browser.enabled", "anidb.server"} {
		fs.applied = nil
		h.pop(k)
		it := h.m.items()[h.m.pop.cursor]
		if it.kind == kBool {
			h.key("enter")
		} else {
			h.key("enter")
			h.key("ctrl+u")
			h.typeText("x:1")
			h.key("enter")
		}
		if len(fs.applied) != 1 {
			t.Errorf("%s: applied %d times", k, len(fs.applied))
		}
		if strings.Contains(h.screen(), "restart") {
			t.Errorf("%s: no restart is needed any more", k)
		}
	}
}

func TestApplyFailureIsShown(t *testing.T) {
	h, fs := newPopupHarness(t)
	fs.applyErr = errors.New("no such player")
	h.pop("player.command")
	h.key("enter")
	h.key("ctrl+u")
	h.typeText("vlc")
	h.key("enter")
	if s := h.screen(); !strings.Contains(s, "saved, but not applied: no such player") {
		t.Errorf("%s", s)
	}
}

func TestEnablingAniDBRefreshesTheList(t *testing.T) {
	h, fs := newPopupHarness(t)
	var synced int
	h.m.opts.App.Sync = countingSync{&synced}
	h.m.opts.App.Store = &fakeStore{}
	fs.cfg.AniDB.Enabled = false
	h.m.cfg.AniDB.Enabled = false
	h.pop("anidb.enabled")
	h.key("enter")
	_ = fs
	if synced == 0 {
		t.Error("enabling AniDB must refresh My List from it")
	}
}

type countingSync struct{ n *int }

func (c countingSync) MyListProgress(context.Context, int) (int, int, error) {
	*c.n++
	return 0, 0, nil
}
