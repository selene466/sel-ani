// Package fetch retrieves remote resources. The default implementation is
// plain net/http; a go-rod browser can be chained in as a fallback for
// Cloudflare challenges.
package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"sel-ani/internal/domain"
)

// Fetcher returns the body of a GET request.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL, referer string) ([]byte, error)
}

// HTTP is a net/http Fetcher.
type HTTP struct {
	Client    *http.Client
	UserAgent string
	MaxBytes  int64
	Retries   int // extra attempts on network errors/timeouts, not on HTTP errors

	mu sync.RWMutex // guards Client and UserAgent, which Configure replaces
}

// Configure changes the User-Agent and the request timeout of later requests.
func (h *HTTP) Configure(userAgent string, timeout time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.UserAgent = userAgent
	h.Client = &http.Client{Timeout: timeout}
}

func (h *HTTP) snapshot() (*http.Client, string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.Client, h.UserAgent
}

// NewHTTP returns an HTTP fetcher with a per-request timeout.
func NewHTTP(userAgent string, timeout time.Duration) *HTTP {
	return &HTTP{
		Client:    &http.Client{Timeout: timeout},
		UserAgent: userAgent,
		MaxBytes:  32 << 20, // pages and playlists are KB; 32MB is generous
		Retries:   1,        // the stream hosts are slow and drop requests
	}
}

type originKey struct{}

// WithOrigin makes HTTP send an Origin header on requests made with ctx.
// Some stream hosts refuse requests without the embedding site's origin.
func WithOrigin(ctx context.Context, origin string) context.Context {
	return context.WithValue(ctx, originKey{}, origin)
}

// OriginFrom returns the origin set by WithOrigin, or "".
func OriginFrom(ctx context.Context) string {
	o, _ := ctx.Value(originKey{}).(string)
	return o
}

var (
	challengeMarker = []byte("<title>Just a moment")
	// a WAF block, not a challenge, but a real browser gets through it too
	blockMarker = []byte("cf-error-details")
)

// challenged reports whether a response is Cloudflare refusing a client
// it takes for a bot.
func challenged(status int, body []byte) bool {
	return bytes.Contains(body, challengeMarker) ||
		(status == http.StatusForbidden && bytes.Contains(body, blockMarker))
}

func (h *HTTP) Fetch(ctx context.Context, rawURL, referer string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("refusing to fetch %q: not http(s)", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	client, ua := h.snapshot()
	req.Header.Set("User-Agent", ua)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if o := OriginFrom(ctx); o != "" {
		req.Header.Set("Origin", o)
	}
	resp, err := client.Do(req)
	for i := 0; err != nil && i < h.Retries && ctx.Err() == nil; i++ {
		resp, err = client.Do(req)
	}
	if err != nil {
		return nil, fmt.Errorf("connection error: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, h.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rawURL, err)
	}
	if int64(len(body)) > h.MaxBytes {
		return nil, fmt.Errorf("%s: response larger than %d bytes", rawURL, h.MaxBytes)
	}
	if challenged(resp.StatusCode, body) {
		return nil, fmt.Errorf("%s: %w", rawURL, domain.ErrChallenge)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("request failed: HTTP %d from %s", resp.StatusCode, rawURL)
	}
	return body, nil
}

// Fallback retries with Secondary only when Primary hits a challenge.
type Fallback struct {
	Primary   Fetcher
	Secondary Fetcher // nil = no fallback
}

func (f Fallback) Fetch(ctx context.Context, rawURL, referer string) ([]byte, error) {
	b, err := f.Primary.Fetch(ctx, rawURL, referer)
	if err == nil || f.Secondary == nil || !errors.Is(err, domain.ErrChallenge) {
		return b, err
	}
	return f.Secondary.Fetch(ctx, rawURL, referer)
}

// Switchable hands requests to Next only while turned on, so a fallback such
// as the browser can be enabled and disabled while running. While off it
// fails like a fallback that was never configured.
type Switchable struct {
	Next Fetcher
	on   atomic.Bool
}

// Set turns the fetcher on or off.
func (s *Switchable) Set(on bool) { s.on.Store(on) }

func (s *Switchable) Fetch(ctx context.Context, rawURL, referer string) ([]byte, error) {
	if !s.on.Load() {
		return nil, fmt.Errorf("%w: turn on [browser] in the config to solve it with a real browser", domain.ErrChallenge)
	}
	return s.Next.Fetch(ctx, rawURL, referer)
}
