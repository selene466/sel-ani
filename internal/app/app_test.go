package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"sel-ani/internal/domain"
)

type fakeProvider struct {
	latest   []domain.Anime
	pages    map[string]domain.Page
	episodes []domain.Episode
	info     map[string]domain.Anime
}

func (f *fakeProvider) Info(_ context.Context, id string) (domain.Anime, error) {
	a, ok := f.info[id]
	if !ok {
		return domain.Anime{}, errors.New("no such anime")
	}
	return a, nil
}

func (f *fakeProvider) Search(_ context.Context, q, cursor string) (domain.Page, error) {
	return f.pages[cursor], nil
}
func (f *fakeProvider) Latest(context.Context, domain.Mode) ([]domain.Anime, error) {
	return f.latest, nil
}
func (f *fakeProvider) Episodes(context.Context, string) ([]domain.Episode, error) {
	return f.episodes, nil
}
func (f *fakeProvider) Stream(_ context.Context, ep domain.Episode, _ domain.Mode) (domain.Stream, error) {
	return domain.Stream{Links: []domain.Link{{Quality: "1080p", URL: "u" + ep.Number}, {Quality: "720p", URL: "l" + ep.Number}}}, nil
}

type fakeStore struct {
	hist  []domain.HistoryEntry
	list  []domain.ListEntry
	saved []domain.HistoryEntry
	put   map[int]int
	kv    map[string]string
}

func (s *fakeStore) Get(k string) (string, bool, error) { v, ok := s.kv[k]; return v, ok, nil }
func (s *fakeStore) Set(k, v string) error {
	if s.kv == nil {
		s.kv = map[string]string{}
	}
	s.kv[k] = v
	return nil
}

func (s *fakeStore) Progress() (map[string]string, error) {
	out := map[string]string{}
	for _, h := range s.hist {
		out[h.AnimeID] = h.Episode
	}
	for _, e := range s.list {
		if _, ok := out[e.Anime.ID]; !ok {
			out[e.Anime.ID] = e.Watched
		}
	}
	return out, nil
}
func (s *fakeStore) Save(e domain.HistoryEntry) error { s.saved = append(s.saved, e); return nil }
func (s *fakeStore) WatchedEpisodes(string) ([]string, error) {
	var out []string
	for _, e := range s.saved {
		out = append(out, e.Episode)
	}
	return out, nil
}
func (s *fakeStore) RememberAnime(domain.Anime) error    { return nil }
func (s *fakeStore) MyList() ([]domain.ListEntry, error) { return s.list, nil }
func (s *fakeStore) PutMyList(aid, w, total int) error   { s.put[aid] = w; return nil }

type fakePlayer struct{ played []domain.Media }

func (p *fakePlayer) Play(_ context.Context, m domain.Media) error {
	p.played = append(p.played, m)
	return nil
}
func (p *fakePlayer) Wait(context.Context) error { return nil }
func (p *fakePlayer) Stop() error                { return nil }
func (p *fakePlayer) Interactive() bool          { return true }

func newApp() (*App, *fakeProvider, *fakeStore, *fakePlayer) {
	fp := &fakeProvider{pages: map[string]domain.Page{}}
	st := &fakeStore{put: map[int]int{}}
	pl := &fakePlayer{}
	return &App{Provider: fp, Store: st, Player: pl, Mode: domain.ModeSub, Quality: "best"}, fp, st, pl
}

func TestLatestIsOneFullListAndMarksNew(t *testing.T) {
	a, fp, st, _ := newApp()
	for i := 1; i <= 54; i++ {
		fp.latest = append(fp.latest, domain.Anime{ID: "a" + strconv.Itoa(i), Title: "T", Sub: 5, Latest: "5"})
	}
	st.hist = []domain.HistoryEntry{{AnimeID: "a2", Episode: "4"}, {AnimeID: "a3", Episode: "5"}}
	l, err := a.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 54 || l.Next != "" {
		t.Fatalf("%d entries, next %q: no paging any more", len(l.Entries), l.Next)
	}
	if !l.Entries[1].New || l.Entries[1].Counter != "[N] 5" {
		t.Errorf("a2 should be new: %+v", l.Entries[1])
	}
	if l.Entries[2].New || l.Entries[2].Counter != "5" {
		t.Errorf("a3 is up to date: %+v", l.Entries[2])
	}
	if l.Entries[0].New {
		t.Error("unlisted anime marked new")
	}
}

func TestSearchCursor(t *testing.T) {
	a, fp, _, _ := newApp()
	fp.pages[""] = domain.Page{Items: []domain.Anime{{ID: "x", Title: "X"}}, Next: "c2"}
	l, err := a.Search(context.Background(), "q", "")
	if err != nil || l.Next != "c2" || len(l.Entries) != 1 {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := a.Search(context.Background(), "  ", ""); !errors.Is(err, domain.ErrNoResults) {
		t.Errorf("blank query: %v", err)
	}
}

func TestMyListOrderAndNew(t *testing.T) {
	a, fp, st, _ := newApp()
	now := time.Now()
	fp.latest = []domain.Anime{{ID: "fin", Sub: 12}, {ID: "air", Sub: 6}}
	st.list = []domain.ListEntry{
		{Anime: domain.Anime{ID: "fin", Episodes: 12}, Watched: "12", WatchedAt: now},
		{Anime: domain.Anime{ID: "air", Episodes: 0}, Watched: "5", WatchedAt: now.Add(-time.Hour)},
		{Anime: domain.Anime{ID: "old", Episodes: 24}, Watched: "3", WatchedAt: now.Add(-48 * time.Hour)},
		{Anime: domain.Anime{ID: "sync", Episodes: 12}, Watched: "2", Synced: true, AniDBWatched: 12, AniDBTotal: 12},
	}
	l, err := a.MyList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range l.Entries {
		ids = append(ids, e.Anime.ID)
	}
	want := []string{"air", "old", "fin", "sync"} // unfinished first, then by recency
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order %v, want %v", ids, want)
		}
	}
	if !l.Entries[0].New || l.Entries[0].Counter != "[N] 5/?" {
		t.Errorf("air: %+v", l.Entries[0])
	}
	if l.Entries[2].New || l.Entries[2].Counter != "12/12" {
		t.Errorf("fin: %+v", l.Entries[2])
	}
	if l.Entries[3].Counter != "12/12" {
		t.Errorf("AniDB count wins when synced: %+v", l.Entries[3])
	}
}

func TestPlaySavesHistoryAndPicksQuality(t *testing.T) {
	a, _, st, pl := newApp()
	a.Quality = "720p"
	an := domain.Anime{ID: "x", Title: "Eng", AltTitle: "Romaji", Episodes: 12}
	if err := a.Play(context.Background(), an, domain.Episode{ID: "e3", Number: "3"}); err != nil {
		t.Fatal(err)
	}
	if len(pl.played) != 1 || pl.played[0].Video != "l3" || pl.played[0].Title != "Romaji Episode 3" {
		t.Errorf("%+v", pl.played)
	}
	if len(st.saved) != 1 || st.saved[0].Episode != "3" || st.saved[0].AltTitle != "Romaji" {
		t.Errorf("%+v", st.saved)
	}
}

type fakeTracker struct{ watched []string }

func (f *fakeTracker) Resolve(context.Context, domain.Anime) error { return nil }
func (f *fakeTracker) Watched(_ context.Context, _ domain.Anime, ep string) error {
	f.watched = append(f.watched, ep)
	return nil
}

func TestPlayTracks(t *testing.T) {
	a, _, _, _ := newApp()
	tr := &fakeTracker{}
	a.Tracker = tr
	a.Play(context.Background(), domain.Anime{ID: "x"}, domain.Episode{ID: "e1", Number: "1"})
	if len(tr.watched) != 1 || tr.watched[0] != "1" {
		t.Errorf("%v", tr.watched)
	}
}

type fakeSync struct{ calls int }

func (f *fakeSync) MyListProgress(_ context.Context, aid int) (int, int, error) {
	f.calls++
	return 7, 12, nil
}

func TestSyncMyList(t *testing.T) {
	a, _, st, _ := newApp()
	sy := &fakeSync{}
	a.Sync = sy
	st.list = []domain.ListEntry{{AID: 5, Anime: domain.Anime{ID: "x"}}}
	if err := a.SyncMyList(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sy.calls != 1 || st.put[5] != 7 {
		t.Errorf("calls %d put %v", sy.calls, st.put)
	}
}

func TestDetailMarksWatched(t *testing.T) {
	a, fp, st, _ := newApp()
	fp.episodes = []domain.Episode{{ID: "e1", Number: "1"}, {ID: "e2", Number: "2"}}
	st.saved = []domain.HistoryEntry{{Episode: "1"}}
	d, err := a.Detail(context.Background(), domain.Anime{ID: "x", Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Episodes) != 2 || !d.Watched["1"] || d.Watched["2"] || d.Next != "2" {
		t.Errorf("%+v", d)
	}
}

func TestMyListNeverShowsLessThanWatchedHere(t *testing.T) {
	a, _, st, _ := newApp()
	st.list = []domain.ListEntry{
		// AniDB has not received the queued episodes yet
		{Anime: domain.Anime{ID: "lag", Episodes: 12}, Watched: "8", Synced: true, AniDBWatched: 0},
		// not in MyList on AniDB at all, but finished here
		{Anime: domain.Anime{ID: "done", Episodes: 12}, Watched: "12", Synced: true},
	}
	l, _ := a.MyList(context.Background())
	got := map[string]Entry{}
	for _, e := range l.Entries {
		got[e.Anime.ID] = e
	}
	if got["lag"].Counter != "8/12" || got["done"].Counter != "12/12" {
		t.Errorf("%q %q", got["lag"].Counter, got["done"].Counter)
	}
	if l.Entries[0].Anime.ID != "lag" || l.Entries[1].Anime.ID != "done" {
		t.Errorf("finished must sort last: %v", []string{l.Entries[0].Anime.ID, l.Entries[1].Anime.ID})
	}
}

func TestHydrateReadsTheAnimePage(t *testing.T) {
	a, fp, _, _ := newApp()
	fp.info = map[string]domain.Anime{"x": {ID: "x", Title: "other", CoverURL: "http://cover", Native: "JP", Description: "d", Format: "TV", Episodes: 24}}
	got, err := a.Hydrate(context.Background(), domain.Anime{ID: "x", Title: "Eng", AltTitle: "Romaji", Episodes: 12})
	if err != nil {
		t.Fatal(err)
	}
	if got.CoverURL != "http://cover" || got.Native != "JP" || got.Title != "Eng" || got.AltTitle != "Romaji" || got.Episodes != 12 {
		t.Errorf("%+v", got)
	}
	if g, _ := a.Hydrate(context.Background(), domain.Anime{ID: "x", CoverURL: "have"}); g.CoverURL != "have" {
		t.Error("overwrote a known cover")
	}
	if g, err := a.Hydrate(context.Background(), domain.Anime{ID: "gone", Title: "T"}); err == nil || g.CoverURL != "" {
		t.Errorf("a failed lookup must report it and keep the anime: %+v %v", g, err)
	}
}

type offSync struct{ calls int }

func (o *offSync) MyListProgress(context.Context, int) (int, int, error) {
	o.calls++
	return 0, 0, domain.ErrDisabled
}

func TestSyncMyListStopsWhenAniDBIsOff(t *testing.T) {
	a, _, st, _ := newApp()
	off := &offSync{}
	a.Sync = off
	st.list = []domain.ListEntry{{AID: 1, Anime: domain.Anime{ID: "a"}}, {AID: 2, Anime: domain.Anime{ID: "b"}}}
	if err := a.SyncMyList(context.Background()); err != nil {
		t.Errorf("a disabled AniDB is not an error: %v", err)
	}
	if off.calls != 1 {
		t.Errorf("%d calls: it must stop at the first answer", off.calls)
	}
}

func syncHarness(n int) (*App, *fakeStore, *countSync) {
	a, _, st, _ := newApp()
	cs := &countSync{}
	a.Sync = cs
	for i := 1; i <= n; i++ {
		st.list = append(st.list, domain.ListEntry{AID: i, Anime: domain.Anime{ID: "a" + strconv.Itoa(i), Episodes: 24}, Watched: "3",
			WatchedAt: time.Now().Add(-time.Duration(i) * time.Hour)})
	}
	return a, st, cs
}

type countSync struct {
	aids []int
	fail map[int]bool
}

func (c *countSync) MyListProgress(_ context.Context, aid int) (int, int, error) {
	c.aids = append(c.aids, aid)
	if c.fail[aid] {
		return 0, 0, errors.New("no reply")
	}
	return 3, 24, nil
}

func TestSyncSkipsWhatCannotHaveChanged(t *testing.T) {
	a, st, cs := syncHarness(0)
	now := time.Now()
	st.list = []domain.ListEntry{
		{AID: 1, Anime: domain.Anime{ID: "done", Episodes: 12}, Watched: "12"},                                                                          // finished locally
		{AID: 2, Anime: domain.Anime{ID: "fin2", Episodes: 12}, Watched: "1", Synced: true, AniDBWatched: 12, SyncedAt: now.Add(-100 * 24 * time.Hour)}, // finished on AniDB
		{AID: 3, Anime: domain.Anime{ID: "fresh", Episodes: 24}, Watched: "3", Synced: true, AniDBWatched: 3, SyncedAt: now.Add(-24 * time.Hour)},       // asked yesterday
		{AID: 4, Anime: domain.Anime{ID: "stale", Episodes: 24}, Watched: "3", Synced: true, AniDBWatched: 3, SyncedAt: now.Add(-8 * 24 * time.Hour)},   // older than a week
		{AID: 5, Anime: domain.Anime{ID: "never", Episodes: 24}, Watched: "3"},                                                                          // never asked
	}
	if err := a.SyncMyList(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cs.aids) != 2 || !slices.Contains(cs.aids, 4) || !slices.Contains(cs.aids, 5) {
		t.Errorf("asked about %v, want only 4 and 5", cs.aids)
	}
}

func TestSyncBatchLimitPrefersRecentlyWatched(t *testing.T) {
	a, _, cs := syncHarness(40)
	if err := a.SyncMyList(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cs.aids) != SyncBatch {
		t.Fatalf("asked %d times, want %d", len(cs.aids), SyncBatch)
	}
	if cs.aids[0] != 1 {
		t.Errorf("the most recently watched goes first: %v", cs.aids[:3])
	}
}

func TestSyncBacksOffAfterAFailure(t *testing.T) {
	a, st, cs := syncHarness(2)
	cs.fail = map[int]bool{1: true}
	if err := a.SyncMyList(context.Background()); err == nil {
		t.Error("a failure must be reported")
	}
	if len(cs.aids) != 2 || st.put[2] != 3 {
		t.Fatalf("%v %v", cs.aids, st.put)
	}
	cs.aids, cs.fail = nil, nil
	a.SyncMyList(context.Background())
	if slices.Contains(cs.aids, 1) {
		t.Errorf("asked again right after a failure: %v", cs.aids)
	}
	st.kv[failKey(1)] = strconv.FormatInt(time.Now().Add(-13*time.Hour).Unix(), 10)
	a.SyncMyList(context.Background())
	if !slices.Contains(cs.aids, 1) {
		t.Error("a failure is forgotten after SyncRetry")
	}
}
