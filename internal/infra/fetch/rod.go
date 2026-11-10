package fetch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Rod fetches through a real browser so Cloudflare's JS challenge can be
// solved. It either attaches to a running browser (RemoteURL) or launches
// a local binary. It never downloads a browser: rod does that silently
// when no binary is given, and fetching executables at runtime is not
// something a CLI tool should do behind your back.
type Rod struct {
	RemoteURL string // ws://... or http://host:port of a browser's DevTools
	Bin       string // local browser; empty = rod's well-known paths
	Headless  bool
	Timeout   time.Duration

	mu      sync.Mutex
	browser *rod.Browser
	launch  *launcher.Launcher
	pages   map[string]*rod.Page // one tab per origin, challenge solved once
}

func (r *Rod) connect() (*rod.Browser, error) {
	if r.browser != nil {
		return r.browser, nil
	}
	var control string
	var err error
	if r.RemoteURL != "" {
		if control, err = launcher.ResolveURL(r.RemoteURL); err != nil {
			return nil, fmt.Errorf("browser.remote_url: %w", err)
		}
	} else {
		bin := r.Bin
		if bin == "" {
			var ok bool
			if bin, ok = launcher.LookPath(); !ok {
				return nil, errors.New("no chromium-based browser found; set browser.bin or browser.remote_url")
			}
		}
		r.launch = launcher.New().Bin(bin).Headless(r.Headless).Leakless(false)
		if control, err = r.launch.Launch(); err != nil {
			return nil, fmt.Errorf("launching %s: %w", bin, err)
		}
	}
	b := rod.New().ControlURL(control)
	if err := b.Connect(); err != nil {
		return nil, fmt.Errorf("connecting to browser: %w", err)
	}
	r.browser = b
	return b, nil
}

// page returns a tab sitting on origin with the challenge passed.
func (r *Rod) page(ctx context.Context, origin string) (*rod.Page, error) {
	if p, ok := r.pages[origin]; ok {
		return p, nil
	}
	b, err := r.connect()
	if err != nil {
		return nil, err
	}
	p, err := b.Context(ctx).Page(proto.TargetCreateTarget{URL: origin + "/"})
	if err != nil {
		return nil, err
	}
	p = p.Context(ctx)
	if err := p.WaitLoad(); err != nil {
		return nil, err
	}
	// the challenge page reloads itself into the real one when solved
	for {
		res, err := p.Eval(`() => document.title`)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(res.Value.Str(), "Just a moment") {
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("browser could not pass the challenge: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	if r.pages == nil {
		r.pages = map[string]*rod.Page{}
	}
	r.pages[origin] = p
	return p, nil
}

// Fetch issues a same-origin fetch() from inside the page, so the request
// carries the clearance cookies. The referer argument cannot be forged
// from page JS and is ignored; streams fetched this way use the page's own.
func (r *Rod) Fetch(ctx context.Context, rawURL, _ string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("refusing to fetch %q", rawURL)
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.page(ctx, u.Scheme+"://"+u.Host)
	if err != nil {
		return nil, err
	}
	res, err := p.Context(ctx).Eval(`async (u) => {
		const r = await fetch(u, {credentials: "include"});
		if (!r.ok) throw new Error("HTTP " + r.status);
		return await r.text();
	}`, rawURL)
	if err != nil {
		return nil, fmt.Errorf("browser fetch %s: %w", rawURL, err)
	}
	return []byte(res.Value.Str()), nil
}

// Configure applies new settings. The browser is shut down, so the next fetch
// starts it with them.
func (r *Rod) Configure(remoteURL, bin string, headless bool, timeout time.Duration) {
	_ = r.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.RemoteURL, r.Bin, r.Headless, r.Timeout = remoteURL, bin, headless, timeout
}

// Close shuts down a launched browser; an attached one is left running.
func (r *Rod) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.pages {
		_ = p.Close()
	}
	r.pages = nil
	if r.launch != nil && r.browser != nil {
		_ = r.browser.Close()
		r.launch.Cleanup()
	}
	r.browser, r.launch = nil, nil
	return nil
}
