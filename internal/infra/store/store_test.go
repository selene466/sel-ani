package store

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"sel-ani/internal/domain"
)

// clock is a settable time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock               { return &clock{t: time.Unix(1_800_000_000, 0)} }
func openTest(t *testing.T) (*DB, *clock) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "sub", "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := newClock()
	db.now = c.now
	return db, c
}

func TestOpenCreatesPrivateFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	path := filepath.Join(t.TempDir(), "cfg", "db.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("db mode = %v, %v", fi.Mode().Perm(), err)
	}
	// reopening runs no migration twice
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
}

func TestHistory(t *testing.T) {
	db, c := openTest(t)
	if got, err := db.Load(); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	save := func(ep, id, title string) {
		t.Helper()
		if err := db.Save(domain.HistoryEntry{Episode: ep, AnimeID: id, Title: title, AltTitle: title + " jp"}); err != nil {
			t.Fatal(err)
		}
		c.add(time.Minute)
	}
	save("1", "a-1", "A")
	save("1", "b-2", "B")
	save("2", "a-1", "A") // a-1 is the most recent again

	got, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.HistoryEntry{
		{Episode: "2", AnimeID: "a-1", Title: "A", AltTitle: "A jp", WatchedAt: time.Unix(1_800_000_120, 0)},
		{Episode: "1", AnimeID: "b-2", Title: "B", AltTitle: "B jp", WatchedAt: time.Unix(1_800_000_060, 0)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}

	// going back to an earlier episode keeps the higher progress
	save("1", "a-1", "A")
	got, _ = db.Load()
	if got[0].Episode != "2" || got[0].AnimeID != "a-1" {
		t.Errorf("rewatch: %+v", got[0])
	}
	eps, err := db.WatchedEpisodes("a-1")
	if err != nil || !reflect.DeepEqual(eps, []string{"1", "2"}) {
		t.Errorf("watched episodes %v %v", eps, err)
	}

	// the aid shows up in history once known
	if err := db.SetAID("a-1", 777, "auto"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.Load()
	if got[0].AID != 777 {
		t.Errorf("aid = %d", got[0].AID)
	}

	if err := db.Clear(); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Load(); len(got) != 0 {
		t.Errorf("after clear: %+v", got)
	}
	// the AniDB link survives a history clear: it cost an API hit or a question
	if aid, decided, _ := db.AID("a-1"); aid != 777 || !decided {
		t.Errorf("aid lost on clear: %d %v", aid, decided)
	}
}

func TestAID(t *testing.T) {
	db, _ := openTest(t)
	if _, decided, err := db.AID("x-1"); decided || err != nil {
		t.Errorf("unknown anime: %v %v", decided, err)
	}
	if err := db.RememberAnime(domain.Anime{ID: "x-1", Title: "X", AltTitle: "Ekkusu"}); err != nil {
		t.Fatal(err)
	}
	if _, decided, _ := db.AID("x-1"); decided {
		t.Error("remembered anime has no aid yet")
	}
	// 0 = decided that there is no AniDB entry, so never ask again
	db.SetAID("x-1", 0, "user")
	if aid, decided, _ := db.AID("x-1"); aid != 0 || !decided {
		t.Errorf("no-match not stored: %d %v", aid, decided)
	}
}

func TestCache(t *testing.T) {
	db, c := openTest(t)
	if _, ok, _ := db.CacheGet("k", time.Hour); ok {
		t.Error("empty cache hit")
	}
	db.CachePut("k", []byte("v1"))
	if v, ok, _ := db.CacheGet("k", time.Hour); !ok || string(v) != "v1" {
		t.Errorf("fresh: %q %v", v, ok)
	}
	c.add(2 * time.Hour)
	if _, ok, _ := db.CacheGet("k", time.Hour); ok {
		t.Error("stale entry returned")
	}
	if v, ok, _ := db.CacheGet("k", 3*time.Hour); !ok || string(v) != "v1" {
		t.Error("entry within a longer ttl must be returned")
	}
	db.CachePut("k", []byte("v2"))
	if v, _, _ := db.CacheGet("k", time.Hour); string(v) != "v2" {
		t.Errorf("overwrite: %q", v)
	}
}

func TestKVAndLease(t *testing.T) {
	db, c := openTest(t)
	if _, ok, _ := db.Get("a"); ok {
		t.Error("missing key found")
	}
	db.Set("a", "1")
	db.Set("a", "2")
	if v, ok, _ := db.Get("a"); !ok || v != "2" {
		t.Errorf("kv %q %v", v, ok)
	}

	if ok, _ := db.Acquire("flush", "p1", time.Minute); !ok {
		t.Fatal("first acquire failed")
	}
	if ok, _ := db.Acquire("flush", "p2", time.Minute); ok {
		t.Error("lease taken twice")
	}
	if ok, _ := db.Acquire("flush", "p1", time.Minute); !ok {
		t.Error("owner cannot renew")
	}
	c.add(2 * time.Minute)
	if ok, _ := db.Acquire("flush", "p2", time.Minute); !ok {
		t.Error("expired lease not taken over")
	}
	db.Release("flush", "p1") // not the owner any more: no effect
	if ok, _ := db.Acquire("flush", "p3", time.Minute); ok {
		t.Error("release by a non-owner freed the lease")
	}
	db.Release("flush", "p2")
	if ok, _ := db.Acquire("flush", "p3", time.Minute); !ok {
		t.Error("released lease not free")
	}
}

func TestTitles(t *testing.T) {
	db, _ := openTest(t)
	titles := []Title{
		{AID: 17617, Type: 1, Lang: "x-jat", Title: "Sousou no Frieren", Norm: "sousou no frieren"},
		{AID: 17617, Type: 4, Lang: "en", Title: "Frieren: Beyond Journey's End", Norm: "frieren beyond journey s end"},
		{AID: 18290, Type: 1, Lang: "x-jat", Title: "Sousou no Frieren (2025)", Norm: "sousou no frieren 2025"},
	}
	err := db.ReplaceTitles(func(yield func(Title) error) error {
		for _, x := range titles {
			if err := yield(x); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := db.TitleCount(); n != 3 {
		t.Errorf("count %d", n)
	}
	got, err := db.ExactTitles("sousou no frieren")
	if err != nil || len(got) != 1 || got[0].AID != 17617 {
		t.Errorf("exact %+v %v", got, err)
	}
	got, _ = db.TitlesLike("frieren", 10)
	if len(got) != 3 {
		t.Errorf("like %+v", got)
	}
	if m, _ := db.MainTitle(17617); m != "Sousou no Frieren" {
		t.Errorf("main title %q", m)
	}
	// replacing is all or nothing: a failing import keeps the old titles
	db.ReplaceTitles(func(yield func(Title) error) error {
		yield(Title{AID: 1, Type: 1, Title: "x", Norm: "x"})
		return os.ErrClosed
	})
	if n, _ := db.TitleCount(); n != 3 {
		t.Errorf("failed import changed titles: %d", n)
	}
}

func TestOutbox(t *testing.T) {
	db, c := openTest(t)
	added, err := db.Enqueue(17617, 1, c.now())
	if err != nil || !added {
		t.Fatalf("enqueue %v %v", added, err)
	}
	// the same episode twice is one API call, not two
	if added, _ := db.Enqueue(17617, 1, c.now()); added {
		t.Error("duplicate enqueued")
	}
	db.Enqueue(17617, 2, c.now())

	due, _ := db.DueOutbox(10)
	if len(due) != 2 || due[0].EpNo != 1 || due[1].EpNo != 2 {
		t.Fatalf("due %+v", due)
	}
	db.OutboxDone(due[0].ID)
	db.OutboxRetry(due[1].ID, "timeout", c.now().Add(10*time.Minute))
	if due, _ := db.DueOutbox(10); len(due) != 0 {
		t.Errorf("retry scheduled for later is due now: %+v", due)
	}
	c.add(11 * time.Minute)
	due, _ = db.DueOutbox(10)
	if len(due) != 1 || due[0].Attempts != 1 || due[0].LastError != "timeout" {
		t.Errorf("after delay %+v", due)
	}
	db.OutboxFailed(due[0].ID, "330 NO SUCH ANIME")
	if due, _ := db.DueOutbox(10); len(due) != 0 {
		t.Errorf("failed item still due: %+v", due)
	}
	// a done episode is never sent again, even when watched again
	if added, _ := db.Enqueue(17617, 1, c.now()); added {
		t.Error("done episode re-enqueued")
	}
	if n, _ := db.PendingCount(); n != 0 {
		t.Errorf("pending %d", n)
	}
}

func TestEpisodeTotals(t *testing.T) {
	db, _ := openTest(t)
	db.Save(domain.HistoryEntry{Episode: "3", AnimeID: "a-1", Title: "A"})
	if got, _ := db.Load(); got[0].Total != 0 {
		t.Errorf("unknown total = %d", got[0].Total)
	}
	db.RememberAnime(domain.Anime{ID: "a-1", Title: "A", Episodes: 12})
	if got, _ := db.Load(); got[0].Total != 12 {
		t.Errorf("total = %d", got[0].Total)
	}
	// a result without a total never erases a known one
	db.RememberAnime(domain.Anime{ID: "a-1", Title: "A"})
	db.Save(domain.HistoryEntry{Episode: "4", AnimeID: "a-1", Title: "A"})
	if got, _ := db.Load(); got[0].Total != 12 {
		t.Errorf("total erased: %d", got[0].Total)
	}
	if a, ok, _ := db.Anime("a-1"); !ok || a.Episodes != 12 {
		t.Errorf("Anime() = %+v %v", a, ok)
	}
}

// A database from before miruro.cx was the only source holds ids of other
// sites: it is refused, never upgraded or mixed in, and left as it was.
func TestRefusesOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE anime (provider_id TEXT PRIMARY KEY, title TEXT NOT NULL);
		INSERT INTO anime VALUES ('magicalexplorer-10494', 'Magical Explorer');
		PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	before, _ := os.ReadFile(path)

	if db, err := Open(path); err == nil || !strings.Contains(err.Error(), "older sel-ani") {
		if db != nil {
			db.Close()
		}
		t.Fatalf("old database opened: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("old database was modified")
	}
}

func TestReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Save(domain.HistoryEntry{Episode: "1", AnimeID: "wHWR9m4VQ8GLW4GP9_ZTblQXn9f0phB8", Title: "Frieren"})
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got, _ := db.Load(); len(got) != 1 {
		t.Errorf("history lost on reopen: %+v", got)
	}
}

func TestMyListAndAnimeMeta(t *testing.T) {
	d, _ := openTest(t)
	a := domain.Anime{ID: "x", Title: "Eng", AltTitle: "Romaji", Native: "JP", CoverURL: "http://c", Description: "d", Format: "TV", Status: "FINISHED", Episodes: 12}
	if err := d.RememberAnime(a); err != nil {
		t.Fatal(err)
	}
	// a later sparse save must not wipe the metadata
	if err := d.RememberAnime(domain.Anime{ID: "x", Title: "Eng"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.Anime("x")
	if err != nil || !ok || got.Native != "JP" || got.CoverURL != "http://c" || got.Episodes != 12 {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	if l, _ := d.MyList(); len(l) != 0 {
		t.Fatalf("unlinked anime listed: %+v", l)
	}
	d.SetAID("x", 42, "auto")
	d.Save(domain.HistoryEntry{AnimeID: "x", Title: "Eng", Episode: "3"})
	d.PutMyList(42, 3, 12)
	l, err := d.MyList()
	if err != nil || len(l) != 1 {
		t.Fatalf("%+v %v", l, err)
	}
	e := l[0]
	if e.AID != 42 || e.Watched != "3" || !e.Synced || e.AniDBWatched != 3 || e.AniDBTotal != 12 || e.Anime.Native != "JP" {
		t.Errorf("%+v", e)
	}
}

func TestImageLRUCap(t *testing.T) {
	d, clk := openTest(t)
	img := make([]byte, 100)
	for _, u := range []string{"a", "b", "c"} {
		if err := d.ImagePut(u, img, 250); err != nil {
			t.Fatal(err)
		}
		clk.t = clk.t.Add(time.Second)
	}
	if _, ok, _ := d.ImageGet("a"); ok {
		t.Error("oldest image survived the cap")
	}
	if _, ok, _ := d.ImageGet("b"); !ok {
		t.Fatal("b evicted")
	}
	clk.t = clk.t.Add(time.Second)
	d.ImagePut("d", img, 250) // b was read last, so c goes
	if _, ok, _ := d.ImageGet("c"); ok {
		t.Error("least recently used image kept")
	}
	if _, ok, _ := d.ImageGet("b"); !ok {
		t.Error("recently read image evicted")
	}
	// one image larger than the cap is still kept
	if err := d.ImagePut("big", make([]byte, 999), 250); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.ImageGet("big"); !ok {
		t.Error("the image just stored must stay")
	}
}

func TestCompact(t *testing.T) {
	d, _ := openTest(t)
	if err := d.Compact(); err != nil {
		t.Fatal(err)
	}
}
