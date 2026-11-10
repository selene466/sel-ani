package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/launcher"
)

// Integration test: needs a local chromium-based browser. Skipped with
// -short or when none is installed.
func TestRodPassesChallenge(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	bin, ok := launcher.LookPath()
	if !ok {
		t.Skip("no browser installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			// a toy challenge: real title appears only after JS runs and sets a cookie
			if c, err := r.Cookie("clearance"); err == nil && c.Value == "ok" {
				w.Write([]byte("<html><title>Home</title></html>"))
				return
			}
			w.Write([]byte(`<html><title>Just a moment...</title><script>
				setTimeout(() => { document.cookie = "clearance=ok; path=/"; location.reload(); }, 300);
			</script></html>`))
		case "/api":
			if c, err := r.Cookie("clearance"); err != nil || c.Value != "ok" {
				w.WriteHeader(403)
				w.Write([]byte("<html><title>Just a moment...</title></html>"))
				return
			}
			w.Write([]byte(`{"html":"ok"}`))
		}
	}))
	defer srv.Close()

	r := &Rod{Bin: bin, Headless: true, Timeout: 30 * time.Second}
	defer r.Close()

	// wired as main does it: plain http hits the challenge, rod solves it
	f := Fallback{Primary: NewHTTP("ua", time.Second), Secondary: r}
	b, err := f.Fetch(context.Background(), srv.URL+"/api", "")
	if err != nil || string(b) != `{"html":"ok"}` {
		t.Fatalf("fallback fetch: %q %v", b, err)
	}
	// second call reuses the solved tab
	if b, err = r.Fetch(context.Background(), srv.URL+"/api", ""); err != nil || string(b) != `{"html":"ok"}` {
		t.Fatalf("reuse: %q %v", b, err)
	}
}
