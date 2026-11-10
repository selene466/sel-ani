package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// changedLines returns the lines of b that differ from a, same length only.
func changedLines(t *testing.T, a, b string) []string {
	t.Helper()
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	if len(al) != len(bl) {
		t.Fatalf("line count %d -> %d", len(al), len(bl))
	}
	var out []string
	for i := range al {
		if al[i] != bl[i] {
			out = append(out, bl[i])
		}
	}
	return out
}

func readTestdata(t *testing.T) string {
	b, err := os.ReadFile("testdata/go-ani.toml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEditReplacesOnlyTheKeyInItsTable(t *testing.T) {
	src := readTestdata(t)
	// "enabled" is in both [browser] and [anidb], "args" in [player] and [menu]
	out, err := Edit(src, "anidb.enabled", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := changedLines(t, src, out); len(got) != 1 || got[0] != "enabled = true" {
		t.Fatalf("changed %q", got)
	}
	if strings.Index(out, "enabled = true") < strings.Index(out, "[anidb]") {
		t.Error("changed the [browser] line")
	}
	out, err = Edit(src, "player.args", []string{"--fs", "--volume=50"})
	if err != nil {
		t.Fatal(err)
	}
	if got := changedLines(t, src, out); len(got) != 1 || got[0] != `args = ["--fs", "--volume=50"]` {
		t.Fatalf("changed %q", got)
	}
	player, menu := strings.Index(out, "[player]"), strings.Index(out, "[menu]")
	if i := strings.Index(out, `args = ["--fs"`); i < player || i > menu {
		t.Error("[menu] args changed instead of [player] args")
	}
	for _, keep := range []string{"[menu]", "program = \"fzf\"", "# Headless browsers are often detected"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q", keep)
		}
	}
}

func TestEditUncommentsAnExample(t *testing.T) {
	src := readTestdata(t)
	out, err := Edit(src, "browser.remote_url", "http://127.0.0.1:9222")
	if err != nil {
		t.Fatal(err)
	}
	if got := changedLines(t, src, out); len(got) != 1 || got[0] != `remote_url = "http://127.0.0.1:9222"` {
		t.Fatalf("changed %q", got)
	}
}

func TestEditCreatesMissingTables(t *testing.T) {
	src := readTestdata(t) // has no [ui] and no [theme]
	out, err := Edit(src, "ui.cover", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "\n\n[ui]\ncover = false\n") || !strings.HasPrefix(out, src[:len(src)-1]) {
		t.Errorf("tail %q", out[len(out)-60:])
	}
	out, err = Edit(out, "theme.accent", "#ff0000")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "[theme]\naccent = \"#ff0000\"\n") {
		t.Errorf("tail %q", out[len(out)-60:])
	}
	cfg, err := parse(strings.NewReader(out))
	if err != nil {
		t.Fatalf("edited file does not load: %v", err)
	}
	if cfg.UI.Cover || cfg.Theme.Accent != "#ff0000" {
		t.Errorf("%+v %+v", cfg.UI, cfg.Theme)
	}
}

func TestEditAppendsToTheEndOfItsTable(t *testing.T) {
	src := "mode = \"sub\"\n\n[player]\ncommand = \"mpv\"\n\n[browser]\nheadless = true\n"
	out, err := Edit(src, "player.args", []string{"--fs"})
	if err != nil {
		t.Fatal(err)
	}
	want := "mode = \"sub\"\n\n[player]\ncommand = \"mpv\"\nargs = [\"--fs\"]\n\n[browser]\nheadless = true\n"
	if out != want {
		t.Errorf("%q", out)
	}
	// a new top-level key goes before the first table
	out, err = Edit(src, "quality", "720p")
	if err != nil {
		t.Fatal(err)
	}
	if want := "mode = \"sub\"\nquality = \"720p\"\n\n[player]"; !strings.HasPrefix(out, want) {
		t.Errorf("%q", out)
	}
}

func TestEditKeepsCRLF(t *testing.T) {
	out, err := Edit("mode = \"sub\"\r\n", "mode", "dub")
	if err != nil || out != "mode = \"dub\"\r\n" {
		t.Errorf("%q %v", out, err)
	}
}

func TestEditStringsRoundTrip(t *testing.T) {
	for _, s := range []string{`p"a\s#s ü`, "tab\there", "line\nbreak", `C:\dir\x`, "", "日本語", "\x01\x7f"} {
		out, err := Edit("[anidb]\n", "anidb.password", s)
		if err != nil {
			t.Fatal(err)
		}
		values, err := ParseTOML(strings.NewReader(out))
		if err != nil {
			t.Fatalf("%q: %v\n%s", s, err, out)
		}
		if got := values["anidb.password"]; got != s {
			t.Errorf("%q came back as %q", s, got)
		}
	}
}

func TestEditRejectsUnknownTypes(t *testing.T) {
	if _, err := Edit("", "x", 1.5); err == nil {
		t.Error("float accepted")
	}
}

func newConfigDir(t *testing.T, content string) Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestSave(t *testing.T) {
	cfg := newConfigDir(t, readTestdata(t))
	got, err := Save(cfg.Source, "mode", "dub")
	if err != nil || got.Mode != "dub" {
		t.Fatalf("%v %+v", err, got.Mode)
	}
	b, _ := os.ReadFile(cfg.Source)
	if !strings.Contains(string(b), `mode = "dub"`) {
		t.Error("not written")
	}
	fi, _ := os.Stat(cfg.Source)
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode changed to %v", fi.Mode().Perm())
	}
	// no temp files left behind
	entries, _ := os.ReadDir(filepath.Dir(cfg.Source))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

func TestSaveRejectsBadValuesAndChangesNothing(t *testing.T) {
	cfg := newConfigDir(t, readTestdata(t))
	before, _ := os.ReadFile(cfg.Source)
	for key, v := range map[string]any{
		"mode":             "raw",
		"quality":          "huge",
		"player.command":   "rm",
		"theme.name":       "nope",
		"theme.accent":     "red",
		"provider.timeout": "soon",
		"anidb.local_port": 70000,
		"browser.bin":      "/bin/sh",
	} {
		if _, err := Save(cfg.Source, key, v); err == nil {
			t.Errorf("%s = %v accepted", key, v)
		}
	}
	after, _ := os.ReadFile(cfg.Source)
	if string(before) != string(after) {
		t.Error("a rejected value changed the file")
	}
}

func TestSaveEnablingAniDBNeedsALoginAndRollsBack(t *testing.T) {
	cfg := newConfigDir(t, readTestdata(t))
	before, _ := os.ReadFile(cfg.Source)
	if _, err := Save(cfg.Source, "anidb.enabled", true); err == nil {
		t.Fatal("enabled without a login")
	}
	after, _ := os.ReadFile(cfg.Source)
	if string(before) != string(after) {
		t.Error("the file was not restored")
	}
	login := AniDBLogin{Client: "selani", ClientVersion: 1, Username: "u", Password: "p", APIKey: "k"}
	if err := SaveLogin(cfg, login); err != nil {
		t.Fatal(err)
	}
	got, err := Save(cfg.Source, "anidb.enabled", true)
	if err != nil || !got.AniDB.Enabled || got.AniDB.Username != "u" {
		t.Fatalf("%v %+v", err, got.AniDB)
	}
}

func TestSaveLoginPermissionsAndMerge(t *testing.T) {
	cfg := newConfigDir(t, readTestdata(t))
	path := cfg.CredentialsPath()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveLogin(cfg, AniDBLogin{Client: "selani", ClientVersion: 2, Username: "me", Password: `p"a\s#s ü`, APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials are %v, want 0600", fi.Mode().Perm())
	}
	got := ReadLogin(cfg)
	if got.Password != `p"a\s#s ü` || got.Client != "selani" || got.ClientVersion != 2 {
		t.Errorf("round trip: %+v", got)
	}
	// empty fields keep the stored values
	if err := SaveLogin(cfg, AniDBLogin{Username: "other"}); err != nil {
		t.Fatal(err)
	}
	got = ReadLogin(cfg)
	if got.Username != "other" || got.Password != `p"a\s#s ü` || got.APIKey != "key" {
		t.Errorf("merge: %+v", got)
	}
	// and the file loads with AniDB enabled
	if _, err := Save(cfg.Source, "anidb.enabled", true); err != nil {
		t.Fatal(err)
	}
}

func TestSaveLoginIncompleteAndNoLeaks(t *testing.T) {
	cfg := newConfigDir(t, readTestdata(t))
	os.Remove(cfg.CredentialsPath())
	err := SaveLogin(cfg, AniDBLogin{Client: "bad client!", ClientVersion: 1, Username: "u", Password: "SECRETpass", APIKey: "SECRETkey"})
	if err == nil {
		t.Fatal("invalid client name accepted")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error leaks a secret: %v", err)
	}
	if _, serr := os.Stat(cfg.CredentialsPath()); serr == nil {
		b, _ := os.ReadFile(cfg.CredentialsPath())
		if strings.Contains(string(b), "SECRET") {
			t.Error("a rejected login was written")
		}
	}
	err = SaveLogin(cfg, AniDBLogin{Username: "u"})
	if err == nil {
		t.Error("an incomplete login must not be saved")
	}
}
