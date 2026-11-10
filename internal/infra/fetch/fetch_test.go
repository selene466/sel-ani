package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sel-ani/internal/domain"
)

func TestHTTPFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte(r.Header.Get("User-Agent") + "|" + r.Header.Get("Referer")))
		case "/cf":
			w.WriteHeader(403)
			w.Write([]byte("<html><title>Just a moment...</title></html>"))
		case "/cf200":
			w.Write([]byte("<html><title>Just a moment...</title></html>"))
		case "/404":
			http.NotFound(w, r)
		case "/big":
			w.Write([]byte(strings.Repeat("x", 2048)))
		case "/slow":
			time.Sleep(200 * time.Millisecond)
		}
	}))
	defer srv.Close()

	h := NewHTTP("ua/1", time.Second)
	ctx := context.Background()

	b, err := h.Fetch(ctx, srv.URL+"/ok", "https://ref/")
	if err != nil || string(b) != "ua/1|https://ref/" {
		t.Errorf("ok: %q %v", b, err)
	}
	for _, p := range []string{"/cf", "/cf200"} {
		if _, err := h.Fetch(ctx, srv.URL+p, ""); !errors.Is(err, domain.ErrChallenge) {
			t.Errorf("%s: err = %v", p, err)
		}
	}
	if _, err := h.Fetch(ctx, srv.URL+"/404", ""); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: err = %v", err)
	}
	h.MaxBytes = 1024
	if _, err := h.Fetch(ctx, srv.URL+"/big", ""); err == nil {
		t.Error("oversized body should fail")
	}
	h = NewHTTP("ua", 50*time.Millisecond)
	if _, err := h.Fetch(ctx, srv.URL+"/slow", ""); err == nil {
		t.Error("timeout expected")
	}
	if _, err := h.Fetch(ctx, "file:///etc/passwd", ""); err == nil {
		t.Error("non-http scheme must be rejected")
	}
}

func TestHTTPRetry(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			time.Sleep(150 * time.Millisecond) // first attempt times out
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	h := NewHTTP("ua", 100*time.Millisecond)
	b, err := h.Fetch(context.Background(), srv.URL, "")
	if err != nil || string(b) != "ok" || n.Load() != 2 {
		t.Errorf("retry: %q %v attempts=%d", b, err, n.Load())
	}
	// HTTP errors are answers, not flakiness: no retry
	n.Store(10)
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.NotFound(w, r)
	}))
	defer srv404.Close()
	h.Fetch(context.Background(), srv404.URL, "")
	if n.Load() != 11 {
		t.Errorf("404 was retried: attempts=%d", n.Load()-10)
	}
}

type stub struct {
	body  string
	err   error
	calls int
}

func (s *stub) Fetch(context.Context, string, string) ([]byte, error) {
	s.calls++
	return []byte(s.body), s.err
}

func TestFallback(t *testing.T) {
	ctx := context.Background()
	p, s := &stub{err: domain.ErrChallenge}, &stub{body: "rod"}
	b, err := Fallback{Primary: p, Secondary: s}.Fetch(ctx, "u", "")
	if err != nil || string(b) != "rod" {
		t.Errorf("challenge should fall back: %q %v", b, err)
	}
	p, s = &stub{err: errors.New("boom")}, &stub{body: "rod"}
	if _, err := (Fallback{Primary: p, Secondary: s}).Fetch(ctx, "u", ""); err == nil || s.calls != 0 {
		t.Error("other errors must not fall back")
	}
	p = &stub{err: domain.ErrChallenge}
	if _, err := (Fallback{Primary: p}).Fetch(ctx, "u", ""); !errors.Is(err, domain.ErrChallenge) {
		t.Errorf("nil secondary: %v", err)
	}
}

func TestChallenged(t *testing.T) {
	block := []byte(`<title>Attention Required! | Cloudflare</title><div id="cf-error-details">`)
	for _, c := range []struct {
		status int
		body   []byte
		want   bool
	}{
		{503, []byte("<html><title>Just a moment...</title>"), true},
		{403, block, true},
		{200, block, false}, // a page that merely mentions it
		{403, []byte("forbidden"), false},
		{200, []byte("ok"), false},
	} {
		if got := challenged(c.status, c.body); got != c.want {
			t.Errorf("challenged(%d, %q) = %v", c.status, c.body, got)
		}
	}
}

func TestHTTPFetchOrigin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Header.Get("Origin")))
	}))
	defer srv.Close()
	h := NewHTTP("ua", time.Second)
	b, err := h.Fetch(WithOrigin(context.Background(), "https://o.example"), srv.URL, "")
	if err != nil || string(b) != "https://o.example" {
		t.Errorf("with origin: %q %v", b, err)
	}
	if b, _ := h.Fetch(context.Background(), srv.URL, ""); len(b) != 0 {
		t.Errorf("origin sent without WithOrigin: %q", b)
	}
}

func TestHTTPConfigure(t *testing.T) {
	var ua atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua.Store(r.Header.Get("User-Agent"))
		if r.URL.Path == "/slow" {
			time.Sleep(300 * time.Millisecond)
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	h := NewHTTP("first", 5*time.Second)
	if _, err := h.Fetch(context.Background(), srv.URL, ""); err != nil || ua.Load() != "first" {
		t.Fatalf("%v %v", err, ua.Load())
	}
	var wg sync.WaitGroup
	for range 4 { // requests in flight while the settings change
		wg.Add(1)
		go func() { defer wg.Done(); h.Fetch(context.Background(), srv.URL, "") }()
	}
	h.Configure("second", 50*time.Millisecond)
	wg.Wait()
	if _, err := h.Fetch(context.Background(), srv.URL, ""); err != nil || ua.Load() != "second" {
		t.Fatalf("%v %v", err, ua.Load())
	}
	h.Retries = 0
	if _, err := h.Fetch(context.Background(), srv.URL+"/slow", ""); err == nil {
		t.Error("the new timeout is not in effect")
	}
}

func TestSwitchable(t *testing.T) {
	next := &countFetch{}
	s := &Switchable{Next: next}
	if _, err := s.Fetch(context.Background(), "http://x", ""); !errors.Is(err, domain.ErrChallenge) || next.n != 0 {
		t.Fatalf("off: %v %d", err, next.n)
	}
	s.Set(true)
	if b, err := s.Fetch(context.Background(), "http://x", ""); err != nil || string(b) != "browser" || next.n != 1 {
		t.Fatalf("on: %v %q", err, b)
	}
	s.Set(false)
	if _, err := s.Fetch(context.Background(), "http://x", ""); err == nil {
		t.Error("off again")
	}
	// as the fallback of a challenged primary
	f := Fallback{Primary: failing{domain.ErrChallenge}, Secondary: s}
	if _, err := f.Fetch(context.Background(), "http://x", ""); !errors.Is(err, domain.ErrChallenge) {
		t.Errorf("a challenge with the browser off must stay a challenge: %v", err)
	}
	s.Set(true)
	if b, err := f.Fetch(context.Background(), "http://x", ""); err != nil || string(b) != "browser" {
		t.Errorf("%v %q", err, b)
	}
}

type countFetch struct{ n int }

func (c *countFetch) Fetch(context.Context, string, string) ([]byte, error) {
	c.n++
	return []byte("browser"), nil
}

type failing struct{ err error }

func (f failing) Fetch(context.Context, string, string) ([]byte, error) { return nil, f.err }
