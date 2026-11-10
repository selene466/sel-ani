// Package config loads the application configuration from a config.toml
// file.
//
// Precedence: built-in defaults < config file < command line flags.
// Flags are applied by the caller, which then calls Validate.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
)

// AppName is used for the user config and state directories.
const AppName = "sel-ani"

// FileName is the name of the configuration file.
const FileName = "config.toml"

// Config is the full program configuration.
type Config struct {
	Mode    string // "sub" | "dub"
	Quality string // "best" | "worst" | "1080p" ...

	Player   Player
	UI       UI
	Provider Provider
	Browser  Browser
	AniDB    AniDB
	Theme    Theme

	Source             string // config file that was loaded, "" when none
	Created            bool   // Source was just created from the template
	CredentialsCreated bool   // credentials.toml was just created from its template
	Warning            string // non fatal problem, e.g. the config file could not be created
}

type Player struct {
	Command string // "" = mpv
	Args    []string
}

// UI configures the terminal interface.
type UI struct {
	Cover bool   // show covers with the kitty graphics protocol
	Font  string // "nerdfont" | "ascii": the separators and symbols the UI draws
}

// Fonts lists the values of ui.font.
var Fonts = []string{"nerdfont", "ascii"}

type Provider struct {
	BaseURL   string
	UserAgent string
	Timeout   time.Duration
	Servers   []string // miruro stream servers, preferred first
}

// Browser configures the go-rod fallback used when the site serves a
// Cloudflare challenge. It is never used unless enabled.
type Browser struct {
	Enabled   bool
	RemoteURL string // DevTools url of an already running browser
	Bin       string // local browser binary; "" = rod's well-known paths
	Headless  bool
}

// AniDB configures watch tracking on AniDB. Everything the login uses
// comes from credentials.toml next to the config file; config.toml only
// switches it on and sets the connection.
type AniDB struct {
	Enabled       bool
	Client        string // credentials.toml: client name registered on anidb.net
	ClientVersion int    // credentials.toml
	Username      string // credentials.toml
	Password      string // credentials.toml
	APIKey        string // credentials.toml: UDP API key, enables encryption
	Server        string // host:port
	LocalPort     int    // fixed so the session survives between runs; 0 = any
}

// DBName is the database file, next to the config file.
const DBName = "db.sqlite"

// CredentialsName is the AniDB login file, next to the config file.
const CredentialsName = "credentials.toml"

const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// Default returns the built-in defaults. default.toml describes the same.
func Default() Config {
	t, _ := LookupTheme(DefaultTheme)
	return Config{
		Mode:    "sub",
		Quality: "best",
		Player:  Player{Command: "mpv"},
		UI:      UI{Cover: true, Font: "nerdfont"},
		Theme:   t,
		Provider: Provider{
			BaseURL:   "https://miruro.cx",
			UserAgent: DefaultUserAgent,
			Timeout:   20 * time.Second,
			Servers:   []string{"bun", "sun", "bee", "hop"},
		},
		Browser: Browser{Headless: true},
		AniDB:   AniDB{Server: "api.anidb.net:9000", LocalPort: 45678},
	}
}

// defaultTOML is written as config.toml when no configuration file exists.
// config.example.toml is a copy of it.
//
//go:embed default.toml
var defaultTOML string

// credentialsTOML is written as credentials.toml whenever that file is
// missing, like config.toml. credentials.example.toml is a copy of it.
//
//go:embed credentials.toml
var credentialsTOML string

// SearchPaths returns the config files looked up when no file is given, most
// preferred first. The first one is created when none of them exists.
//
//   - Linux, macOS, BSD: $XDG_CONFIG_HOME/sel-ani/config.toml, or
//     ~/.config/sel-ani/config.toml when XDG_CONFIG_HOME is unset
//   - macOS also: ~/Library/Application Support/sel-ani/config.toml
//   - Windows: %AppData%\sel-ani\config.toml, then
//     %USERPROFILE%\.config\sel-ani\config.toml
func SearchPaths() []string {
	return searchPaths(runtime.GOOS, os.Getenv)
}

func homeDir(goos string, getenv func(string) string) string {
	if goos == "windows" {
		return getenv("USERPROFILE")
	}
	return getenv("HOME")
}

// xdgDir returns $name when it is an absolute path; the XDG base directory
// spec says relative values are invalid and must be ignored.
func xdgDir(getenv func(string) string, name string) string {
	if v := getenv(name); strings.HasPrefix(v, "/") {
		return v
	}
	return ""
}

func searchPaths(goos string, getenv func(string) string) []string {
	home := homeDir(goos, getenv)
	var dotConfig string
	if home != "" {
		dotConfig = filepath.Join(home, ".config")
	}
	var dirs []string
	switch goos {
	case "windows":
		dirs = []string{getenv("AppData"), dotConfig}
	default:
		xdg := xdgDir(getenv, "XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = dotConfig
		}
		dirs = []string{xdg}
		if goos == "darwin" && home != "" {
			dirs = append(dirs, filepath.Join(home, "Library", "Application Support"))
		}
	}
	var paths []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if p := filepath.Join(dir, AppName, FileName); !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	return paths
}

// dir is the directory of the loaded config file, or where the config
// file would be. "" when there is no home directory.
func (c Config) dir() string {
	if c.Source != "" {
		return filepath.Dir(c.Source)
	}
	if paths := SearchPaths(); len(paths) > 0 {
		return filepath.Dir(paths[0])
	}
	return ""
}

func (c Config) sibling(name string) string {
	if d := c.dir(); d != "" {
		return filepath.Join(d, name)
	}
	return ""
}

// DBPath is db.sqlite next to the config file.
func (c Config) DBPath() string { return c.sibling(DBName) }

// CredentialsPath is credentials.toml next to the config file.
func (c Config) CredentialsPath() string { return c.sibling(CredentialsName) }

// Load builds the configuration from the defaults and a config.toml file:
// path when set, which must then exist, otherwise the first of SearchPaths
// that exists. When none exists, the first search path is created with the
// defaults.
func Load(path string) (Config, error) {
	var cfg Config
	var err error
	if path != "" {
		cfg, err = loadFile(path)
	} else {
		cfg, err = search(SearchPaths())
	}
	if err != nil {
		return cfg, err
	}
	return cfg, cfg.loadCredentials()
}

// search loads the first existing file of paths, or creates
// paths[0] with the defaults. Failing to create it is not fatal: the settings are used without
// being saved and Config.Warning says why.
func search(paths []string) (Config, error) {
	if len(paths) == 0 {
		cfg := Default()
		cfg.Warning = "no home directory found, using the default settings"
		return cfg, nil
	}
	for _, p := range paths {
		cfg, err := loadFile(p)
		if !notFound(err) {
			return cfg, err
		}
	}
	content := defaultTOML
	created, err := create(paths[0], content, 0o644)
	if err != nil {
		cfg, perr := parse(strings.NewReader(content))
		if perr != nil {
			return cfg, perr
		}
		cfg.Warning = fmt.Sprintf("settings not saved, %v", err)
		return cfg, nil
	}
	cfg, err := loadFile(paths[0])
	cfg.Created = created
	return cfg, err
}

// notFound reports whether err means the file does not exist. A path below
// a regular file is "not found" on Windows but ENOTDIR on Unix.
func notFound(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// create writes content to path with mode. It never replaces an existing
// file: created is false when path appeared in the meantime.
func create(path, content string, mode os.FileMode) (created bool, err error) {
	// XDG base directory spec: create missing directories with mode 0700
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("cannot create config: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot create config: %w", err)
	}
	_, err = f.WriteString(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return false, fmt.Errorf("cannot write config: %w", err)
	}
	return true, nil
}

func loadFile(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	defer file.Close()
	cfg, err := parse(file)
	cfg.Source = path
	if err != nil {
		return cfg, fmt.Errorf("config %s:\n%w", path, err)
	}
	return cfg, nil
}

// loadCredentials makes sure credentials.toml exists next to the config,
// created private from its template like config.toml, and reads the AniDB
// login from it while AniDB is enabled.
func (c *Config) loadCredentials() error {
	path := c.CredentialsPath()
	if path == "" {
		if c.AniDB.Enabled {
			return errors.New("AniDB is enabled but there is no config directory for " + CredentialsName)
		}
		return nil
	}
	created, err := create(path, credentialsTOML, 0o600)
	if err != nil {
		if c.AniDB.Enabled {
			return err
		}
		c.Warning = strings.TrimSpace(c.Warning + "\n" + err.Error())
		return nil
	}
	c.CredentialsCreated = created
	if !c.AniDB.Enabled {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("credentials: %w", err)
	}
	defer f.Close()
	if err := checkPrivate(f); err != nil {
		return fmt.Errorf("credentials %s: %w", path, err)
	}
	values, err := ParseTOML(f)
	if err != nil {
		return fmt.Errorf("credentials %s:\n%w", path, err)
	}
	if err := c.AniDB.applyCredentials(values); err != nil {
		return fmt.Errorf("credentials %s:\n%w", path, err)
	}
	return nil
}

// applyCredentials reads the [anidb] login keys. All are required: the
// API key too, since without it the password crosses the network as
// plain UDP.
func (a *AniDB) applyCredentials(values map[string]any) error {
	var errs []error
	fail := func(key, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{key}, args...)...))
	}
	str := func(key string, dst *string) {
		v, ok := values[key]
		delete(values, key)
		s, isStr := v.(string)
		switch {
		case ok && !isStr:
			fail(key, "expected a string, got %s", typeName(v))
		case strings.TrimSpace(s) == "":
			fail(key, "required when anidb.enabled = true")
		default:
			*dst = s
		}
	}
	str("anidb.client", &a.Client)
	if a.Client != "" && !clientNameRe.MatchString(a.Client) {
		fail("anidb.client", "expected the registered client name (letters, digits, - _ .), got %q", a.Client)
	}
	v, ok := values["anidb.client_version"]
	delete(values, "anidb.client_version")
	n, isInt := v.(int64)
	switch {
	case ok && !isInt:
		fail("anidb.client_version", "expected an integer, got %s", typeName(v))
	case n < 0 || n > 1<<31-1:
		fail("anidb.client_version", "expected the registered version number, got %d", n)
	case n == 0:
		fail("anidb.client_version", "required when anidb.enabled = true")
	default:
		a.ClientVersion = int(n)
	}
	str("anidb.username", &a.Username)
	str("anidb.password", &a.Password)
	str("anidb.api_key", &a.APIKey)

	unknown := make([]string, 0, len(values))
	for key := range values {
		unknown = append(unknown, key)
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		fail(key, "unknown key")
	}
	return errors.Join(errs...)
}

// checkPrivate refuses a file other users can read.
func checkPrivate(f *os.File) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("other users can read it (mode %v); run: chmod 600 %s", fi.Mode().Perm(), f.Name())
	}
	return nil
}

// parse builds the configuration from the defaults and TOML content.
func parse(r io.Reader) (Config, error) {
	values, err := ParseTOML(r)
	if err != nil {
		return Config{}, err
	}
	return build(values)
}

func typeName(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case int64:
		return "an integer"
	case bool:
		return "a boolean"
	case []any:
		return "an array"
	}
	return fmt.Sprintf("%T", v)
}

// build applies values on the defaults. Every problem is reported, not
// only the first, so a broken config is fixed in one go.
func build(values map[string]any) (Config, error) {
	cfg := Default()
	var errs []error
	used := map[string]bool{}
	fail := func(key, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{key}, args...)...))
	}
	lookup := func(key, want string) (any, bool) {
		v, ok := values[key]
		if !ok {
			return nil, false
		}
		used[key] = true
		if got := typeName(v); got != want {
			fail(key, "expected %s, got %s", want, got)
			return nil, false
		}
		return v, true
	}
	str := func(key string, dst *string) bool {
		v, ok := lookup(key, "a string")
		if ok {
			*dst = v.(string)
		}
		return ok
	}
	boolean := func(key string, dst *bool) {
		if v, ok := lookup(key, "a boolean"); ok {
			*dst = v.(bool)
		}
	}
	strList := func(key string, dst *[]string) {
		v, ok := lookup(key, "an array")
		if !ok {
			return
		}
		var out []string
		for _, item := range v.([]any) {
			s, isStr := item.(string)
			if !isStr {
				fail(key, "expected an array of strings, got %s in it", typeName(item))
				return
			}
			out = append(out, s)
		}
		*dst = out
	}
	duration := func(key string, dst *time.Duration) {
		var s string
		if !str(key, &s) {
			return
		}
		d, err := time.ParseDuration(s)
		if err != nil || d < time.Second {
			fail(key, "expected a duration of at least 1s such as \"20s\", got %q", s)
			return
		}
		*dst = d
	}

	str("mode", &cfg.Mode)
	str("quality", &cfg.Quality)

	if str("player.command", &cfg.Player.Command) && cfg.Player.Command != "" {
		if err := checkPlayer(cfg.Player.Command); err != nil {
			fail("player.command", "%w", err)
		}
	}
	strList("player.args", &cfg.Player.Args)
	boolean("ui.cover", &cfg.UI.Cover)
	if str("ui.font", &cfg.UI.Font) && !slices.Contains(Fonts, cfg.UI.Font) {
		fail("ui.font", "expected %s, got %q", strings.Join(Fonts, " or "), cfg.UI.Font)
	}

	var themeName string
	if str("theme.name", &themeName) && themeName != "" {
		t, found := LookupTheme(themeName)
		if !found {
			fail("theme.name", "unknown theme %q (available: %s)", themeName, strings.Join(ThemeNames(), ", "))
		} else {
			cfg.Theme = t
		}
	}
	for field := range cfg.Theme.fields() {
		var color string
		key := "theme." + field
		if str(key, &color) && color != "" {
			if err := cfg.Theme.Override(field, color); err != nil {
				fail(key, "%w", err)
			}
		}
	}

	str("provider.base_url", &cfg.Provider.BaseURL)
	str("provider.user_agent", &cfg.Provider.UserAgent)
	duration("provider.timeout", &cfg.Provider.Timeout)
	strList("provider.servers", &cfg.Provider.Servers)

	boolean("browser.enabled", &cfg.Browser.Enabled)
	if str("browser.remote_url", &cfg.Browser.RemoteURL) && cfg.Browser.RemoteURL != "" {
		u, err := url.Parse(cfg.Browser.RemoteURL)
		if err != nil || !slices.Contains([]string{"ws", "wss", "http", "https"}, u.Scheme) || u.Host == "" {
			fail("browser.remote_url", "expected a ws:// or http:// DevTools url, got %q", cfg.Browser.RemoteURL)
		}
	}
	if str("browser.bin", &cfg.Browser.Bin) && cfg.Browser.Bin != "" {
		cfg.Browser.Bin = expandHome(cfg.Browser.Bin)
		if err := checkBrowser(cfg.Browser.Bin); err != nil {
			fail("browser.bin", "%w", err)
		}
	}
	boolean("browser.headless", &cfg.Browser.Headless)

	intVal := func(key string, dst *int, lo, hi int) {
		v, ok := lookup(key, "an integer")
		if !ok {
			return
		}
		n := v.(int64)
		if n < int64(lo) || n > int64(hi) {
			fail(key, "expected an integer from %d to %d, got %d", lo, hi, n)
			return
		}
		*dst = int(n)
	}
	a := &cfg.AniDB
	boolean("anidb.enabled", &a.Enabled)
	for _, key := range []string{"anidb.client", "anidb.client_version", "anidb.username", "anidb.password", "anidb.api_key"} {
		if _, ok := values[key]; ok {
			used[key] = true
			fail(key, "moved to %s next to this file", CredentialsName)
		}
	}
	if str("anidb.server", &a.Server) {
		if _, port, err := net.SplitHostPort(a.Server); err != nil || port == "" {
			fail("anidb.server", "expected host:port such as \"api.anidb.net:9000\", got %q", a.Server)
		}
	}
	intVal("anidb.local_port", &a.LocalPort, 0, 65535)

	// settings of go-ani, which shared this config file: accepted and ignored
	for key := range values {
		if legacyKeys[key] || strings.HasPrefix(key, "menu.") {
			used[key] = true
		}
	}

	// the checks shared with command line flags
	errs = append(errs, cfg.validate()...)

	var unknown []string
	for key := range values {
		if !used[key] {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		fail(key, "unknown key")
	}
	return cfg, errors.Join(errs...)
}

// legacyKeys are the settings of the command line program this one replaces.
var legacyKeys = map[string]bool{
	"download_dir": true, "exact_match": true, "log_episodes": true,
	"player.no_detach": true, "player.exit_after_play": true, "player.skip_intro": true,
}

// Validate checks the values command line flags can change. Flags are the
// user's own choice, so the program allowlists do not apply to them.
func (c *Config) Validate() error {
	return errors.Join(c.validate()...)
}

func (c *Config) validate() []error {
	var errs []error
	if c.Mode != "sub" && c.Mode != "dub" {
		errs = append(errs, fmt.Errorf("mode: must be \"sub\" or \"dub\", got %q", c.Mode))
	}
	if !ValidQuality(c.Quality) {
		errs = append(errs, fmt.Errorf("quality: must be \"best\", \"worst\" or a resolution such as \"720p\", got %q", c.Quality))
	}
	u, err := url.Parse(c.Provider.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("provider.base_url: expected an http(s) url, got %q", c.Provider.BaseURL))
	}
	c.Provider.BaseURL = strings.TrimRight(c.Provider.BaseURL, "/")
	if c.Provider.Timeout <= 0 {
		errs = append(errs, errors.New("provider.timeout: must be positive"))
	}
	for _, name := range c.Provider.Servers {
		if !serverRe.MatchString(name) {
			errs = append(errs, fmt.Errorf("provider.servers: expected server names such as \"sun\", got %q", name))
			break
		}
	}
	return errs
}

var (
	resolutionRe = regexp.MustCompile(`^[0-9]{3,4}p?$`)
	serverRe     = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	clientNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// ValidQuality reports whether q is "best", "worst" or a resolution like
// "720p" or "720". Anything else would never match a stream and silently
// fall back to best on every episode.
func ValidQuality(q string) bool {
	return q == "best" || q == "worst" || resolutionRe.MatchString(q)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// FlatpakMPV is the pseudo-command for mpv installed with flatpak.
const FlatpakMPV = "flatpak:io.mpv.Mpv"

// Programs named in the config file are executed, and a config may be
// copied from an untrusted source. Only known programs are accepted, as a
// bare command name (looked up in PATH) or an absolute path, so a malicious
// config cannot run arbitrary programs.
var (
	playerBinaries = []string{"mpv", "vlc", "iina", "iina-cli", "syncplay", "catt", "celluloid", "haruna", "mplayer"}
	browserNames   = []string{
		"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome",
		"brave", "brave-browser", "microsoft-edge", "microsoft-edge-stable", "msedge", "helium",
		"google chrome", "brave browser", "microsoft edge", // macOS app binaries
	}
)

func binaryName(v string) (string, error) {
	if strings.ContainsAny(v, `/\`) && !filepath.IsAbs(v) {
		return "", fmt.Errorf("%q must be a command name or an absolute path", v)
	}
	return strings.TrimSuffix(strings.ToLower(filepath.Base(v)), ".exe"), nil
}

func allowed(kind, v string, names []string) error {
	name, err := binaryName(v)
	if err != nil {
		return err
	}
	if !slices.Contains(names, name) {
		return fmt.Errorf("unsupported %s %q (allowed: %s)", kind, v, strings.Join(names, ", "))
	}
	return nil
}

func checkPlayer(v string) error {
	if v == FlatpakMPV {
		return nil
	}
	if strings.HasPrefix(v, "flatpak:") {
		return fmt.Errorf("unsupported player %q (the only flatpak player is %s)", v, FlatpakMPV)
	}
	return allowed("player", v, playerBinaries)
}

func checkBrowser(v string) error {
	return allowed("browser", v, browserNames)
}
