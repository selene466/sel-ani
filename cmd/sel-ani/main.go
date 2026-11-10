// sel-ani is a terminal anime browser and player.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"sel-ani/internal/app"
	"sel-ani/internal/config"
	"sel-ani/internal/domain"
	"sel-ani/internal/infra/anidb"
	"sel-ani/internal/infra/cache"
	"sel-ani/internal/infra/fetch"
	"sel-ani/internal/infra/image"
	"sel-ani/internal/infra/kitty"
	"sel-ani/internal/infra/miruro"
	"sel-ani/internal/infra/player"
	"sel-ani/internal/infra/store"
	"sel-ani/internal/ui"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sel-ani:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "", "load this config file instead of the default one")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("sel-ani", version)
		return nil
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	dbPath := cfg.DBPath()
	if dbPath == "" {
		dbPath = filepath.Join(os.TempDir(), config.AppName, config.DBName)
		cfg.Warning = "no home directory, the database goes to " + dbPath
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	// responses go stale within hours; covers have their own capped table
	_ = db.PruneCache(7 * 24 * time.Hour)
	_ = db.Compact()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Everything the config can change while running sits behind a setter, so
	// a saved setting applies at once (see apply below).
	httpFetch := fetch.NewHTTP(cfg.Provider.UserAgent, cfg.Provider.Timeout)
	rod := &fetch.Rod{}
	defer rod.Close()
	browser := &fetch.Switchable{Next: rod}
	fetcher := fetch.Fallback{Primary: httpFetch, Secondary: browser}
	site := miruro.New(fetcher, cfg.Provider.BaseURL, cfg.Provider.Servers)
	pl := &player.Exec{}
	tracker := &anidb.Switch{}
	defer func() {
		if s := tracker.Close(10 * time.Second); s != "" {
			fmt.Fprintln(os.Stderr, s)
		}
	}()

	a := &app.App{
		Provider: &cache.Provider{Next: site, Store: db},
		Store:    db,
		Player:   pl,
		Tracker:  tracker,
		Sync:     tracker,
		Mode:     domain.Mode(cfg.Mode),
		Quality:  cfg.Quality,
	}

	// apply puts a configuration into effect. It runs at startup and after
	// each change made in the Config popup, and may run while requests are
	// in flight: every setter it calls is safe for that.
	var applyMu sync.Mutex
	var anidbNow *config.AniDB // what the running tracker was built from
	apply := func(c config.Config) error {
		applyMu.Lock()
		defer applyMu.Unlock()
		httpFetch.Configure(c.Provider.UserAgent, c.Provider.Timeout)
		site.Configure(c.Provider.BaseURL, c.Provider.Servers)
		rod.Configure(c.Browser.RemoteURL, c.Browser.Bin, c.Browser.Headless, 3*c.Provider.Timeout)
		browser.Set(c.Browser.Enabled)
		extra := append([]string{}, c.Player.Args...)
		if player.KindOf(c.Player.Command) == player.MPV {
			// mpv must not read or draw on the terminal the UI owns
			extra = append([]string{"--no-terminal"}, extra...)
		}
		pl.Configure(c.Player.Command, extra)

		switch {
		case !c.AniDB.Enabled:
			if anidbNow != nil {
				tracker.Set(nil, 5*time.Second)
				anidbNow = nil
			}
		case anidbNow == nil || *anidbNow != c.AniDB:
			l := c.AniDB
			tracker.Set(newTracker(db, l), 5*time.Second)
			anidbNow = &l
		}
		return nil
	}
	if err := apply(cfg); err != nil {
		return err
	}

	supported := kitty.Supported(os.Getenv)
	cover := cfg.UI.Cover && supported
	notice := cfg.Warning
	if cfg.UI.Cover && !supported {
		notice = "covers need kitty or ghostty (not tmux); set ui.cover = false to hide this"
	}
	model := ui.New(ui.Options{
		App:     a,
		Images:  &image.Source{Fetcher: fetcher, Cache: db},
		Cover:   cover,
		Theme:   cfg.Theme,
		Notice:  notice,
		Context: ctx,

		Config:         cfg,
		CoverSupported: supported,
		SaveConfig: func(key string, v any) (config.Config, error) {
			if cfg.Source == "" {
				return config.Config{}, errors.New("there is no config file")
			}
			return config.Save(cfg.Source, key, v)
		},
		SaveLogin: func(l config.AniDBLogin) (config.Config, error) {
			if cfg.Source == "" {
				return config.Config{}, errors.New("there is no config file")
			}
			if err := config.SaveLogin(cfg, l); err != nil {
				return config.Config{}, err
			}
			return config.Save(cfg.Source, "anidb.enabled", true)
		},
		ReadLogin: func() config.AniDBLogin { return config.ReadLogin(cfg) },
		Apply:     apply,
	})
	_, err = tea.NewProgram(model, tea.WithContext(ctx)).Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// newTracker returns the AniDB tracker for the login and connection in c.
func newTracker(db *store.DB, c config.AniDB) *anidb.Tracker {
	return &anidb.Tracker{
		Store:  db,
		Titles: &anidb.Titles{Store: db, UserAgent: config.AppName + "/" + version},
		UDP: anidb.NewUDP(anidb.Config{
			Server: c.Server, LocalPort: c.LocalPort,
			Client: c.Client, ClientVersion: c.ClientVersion,
			Username: c.Username, Password: c.Password, APIKey: c.APIKey,
		}, db),
	}
}
