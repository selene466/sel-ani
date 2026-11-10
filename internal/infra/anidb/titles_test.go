package anidb

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"sel-ani/internal/infra/store"
)

const dump = `# created: Sat Oct 31 03:00:01 2026
# <aid>|<type>|<language>|<title>
# type: 1=primary title (one per anime), 2=synonyms (multiple per anime), 3=shorttitles (multiple per anime), 4=official title (one per language)
17617|1|x-jat|Sousou no Frieren
17617|4|en|Frieren: Beyond Journey's End
17617|4|ja|葬送のフリーレン
17617|3|en|Frieren
18290|1|x-jat|Sousou no Frieren (2026)
18290|4|en|Frieren: Beyond Journey's End Season 2
18290|2|x-jat|Sousou no Frieren 2nd Season
69|1|x-jat|One Piece
69|4|en|One Piece
900|2|en|Tsuki ga Kirei
901|1|x-jat|Tsuki ga Kirei
902|4|en|As the Moon, So Beautiful 2
902|4|ja|月がきれい2
904|1|x-jat|Kanojo
905|2|x-jat|Kanojo
906|1|x-jat|Hanako
907|1|x-jat|HANAKO
6257|1|x-jat|K-On!
6257|2|x-jat|K-ON
7307|1|x-jat|K-On!!
bad line
x|1|en|not a number
`

func gz(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

func newStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func titleServer(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	var full, notModified atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full.Add(1)
		w.Header().Set("ETag", `"v1"`)
		w.Write(gz(dump))
	}))
	t.Cleanup(srv.Close)
	return srv, &full, &notModified
}

func TestTitlesRefreshPolicy(t *testing.T) {
	db := newStore(t)
	srv, full, notMod := titleServer(t)
	now := time.Unix(1_800_000_000, 0)
	ti := &Titles{Store: db, URL: srv.URL, now: func() time.Time { return now }}
	ctx := context.Background()

	if err := ti.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.TitleCount(); n != 20 {
		t.Errorf("imported %d titles, want 20 (bad lines skipped)", n)
	}
	// within 24h: no request at all
	now = now.Add(23 * time.Hour)
	ti.Ensure(ctx)
	if full.Load() != 1 || notMod.Load() != 0 {
		t.Errorf("requests within a day: full=%d 304=%d", full.Load(), notMod.Load())
	}
	// after 24h: conditional request, 304, nothing downloaded
	now = now.Add(2 * time.Hour)
	if err := ti.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if full.Load() != 1 || notMod.Load() != 1 {
		t.Errorf("after a day: full=%d 304=%d", full.Load(), notMod.Load())
	}
	if n, _ := db.TitleCount(); n != 20 {
		t.Errorf("304 dropped titles: %d", n)
	}
}

func TestTitlesFailureBackoff(t *testing.T) {
	db := newStore(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	now := time.Unix(1_800_000_000, 0)
	ti := &Titles{Store: db, URL: srv.URL, now: func() time.Time { return now }}
	if err := ti.Ensure(context.Background()); err == nil {
		t.Fatal("503 not reported")
	}
	now = now.Add(30 * time.Minute)
	if err := ti.Ensure(context.Background()); err == nil {
		t.Error("no titles and no download must still be an error")
	}
	if hits.Load() != 1 {
		t.Errorf("retried within the hour: %d hits", hits.Load())
	}
	now = now.Add(31 * time.Minute)
	ti.Ensure(context.Background())
	if hits.Load() != 2 {
		t.Errorf("no retry after an hour: %d hits", hits.Load())
	}
}
