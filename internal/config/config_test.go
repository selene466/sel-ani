package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultTOMLMatchesDefaults(t *testing.T) {
	cfg, err := parse(strings.NewReader(defaultTOML))
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	if cfg.Mode != d.Mode || cfg.Player.Command != "mpv" || !cfg.UI.Cover || cfg.UI.Font != "nerdfont" || cfg.Theme != d.Theme ||
		cfg.Provider.BaseURL != d.Provider.BaseURL || cfg.AniDB.Enabled {
		t.Errorf("template differs from defaults: %+v", cfg)
	}
}

func TestLoadCreatesFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "sel-ani", "config.toml")
	if !cfg.Created || cfg.Source != want {
		t.Errorf("created=%v source=%q", cfg.Created, cfg.Source)
	}
	if cfg.DBPath() != filepath.Join(dir, "sel-ani", "db.sqlite") {
		t.Errorf("db path %q", cfg.DBPath())
	}
	fi, err := os.Stat(cfg.CredentialsPath())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials: %v %v", fi, err)
	}
}

func TestBuildErrors(t *testing.T) {
	for name, in := range map[string]string{
		"unknown key":   "nope = 1",
		"bad theme":     "[theme]\nname = \"x\"",
		"bad color":     "[theme]\naccent = \"red\"",
		"bad player":    "[player]\ncommand = \"rm\"",
		"bad mode":      "mode = \"raw\"",
		"bad font":      "[ui]\nfont = \"emoji\"",
		"anidb secrets": "[anidb]\npassword = \"x\"",
	} {
		if _, err := parse(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestThemeOverride(t *testing.T) {
	cfg, err := parse(strings.NewReader("[theme]\nname = \"nord\"\naccent = \"#ff0000\""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme.Accent != "#ff0000" || cfg.Theme.Background != "#2e3440" {
		t.Errorf("theme %+v", cfg.Theme)
	}
}

func TestCredentialsMustBePrivate(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte("[anidb]\nenabled = true\n"), 0o644)
	creds := "[anidb]\nclient = \"c\"\nclient_version = 1\nusername = \"u\"\npassword = \"p\"\napi_key = \"k\"\n"
	os.WriteFile(filepath.Join(dir, CredentialsName), []byte(creds), 0o644)
	if _, err := Load(cfgPath); err == nil {
		t.Fatal("world readable credentials accepted")
	}
	os.Chmod(filepath.Join(dir, CredentialsName), 0o600)
	cfg, err := Load(cfgPath)
	if err != nil || cfg.AniDB.Username != "u" {
		t.Fatalf("%+v %v", cfg.AniDB, err)
	}
}

func TestGoAniConfigStillLoads(t *testing.T) {
	f, err := os.Open("testdata/go-ani.toml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := parse(f)
	if err != nil {
		t.Fatalf("an existing go-ani config must keep working: %v", err)
	}
	if cfg.Warning != "" {
		t.Errorf("go-ani's settings are ignored quietly, got a warning: %q", cfg.Warning)
	}
	if cfg.UI.Font != "nerdfont" {
		t.Errorf("an old config gets the default font, not %q", cfg.UI.Font)
	}
	if cfg.Player.Command != "" && cfg.Player.Command != "mpv" {
		t.Errorf("player %q", cfg.Player.Command)
	}
}
