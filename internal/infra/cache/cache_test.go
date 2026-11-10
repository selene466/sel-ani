package cache

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"sel-ani/internal/domain"
)

type memStore struct {
	m   map[string][]byte
	age map[string]time.Duration // pretend age of each entry
}

func (s *memStore) CacheGet(k string, maxAge time.Duration) ([]byte, bool, error) {
	v, ok := s.m[k]
	if !ok || s.age[k] > maxAge {
		return nil, false, nil
	}
	return v, true, nil
}
func (s *memStore) CachePut(k string, v []byte) error {
	s.m[k] = v
	s.age[k] = 0
	return nil
}

type countingProvider struct {
	search, episodes, stream, latest, info int
	fail                                   bool
}

func (p *countingProvider) Latest(context.Context, domain.Mode) ([]domain.Anime, error) {
	p.latest++
	return []domain.Anime{{ID: "a-1", Title: "A", Sub: 3}}, nil
}

func (p *countingProvider) Search(_ context.Context, q, cursor string) (domain.Page, error) {
	p.search++
	if p.fail {
		return domain.Page{}, errors.New("down")
	}
	return domain.Page{Items: []domain.Anime{{ID: "a-1", Title: "A " + q, AltTitle: "Ei"}}, Next: "c2"}, nil
}
func (p *countingProvider) Info(_ context.Context, id string) (domain.Anime, error) {
	p.info++
	return domain.Anime{ID: id, CoverURL: "c"}, nil
}
func (p *countingProvider) Episodes(_ context.Context, id string) ([]domain.Episode, error) {
	p.episodes++
	return []domain.Episode{{ID: "1", Number: "1"}, {ID: "2", Number: "2"}}, nil
}
func (p *countingProvider) Stream(_ context.Context, ep domain.Episode, m domain.Mode) (domain.Stream, error) {
	p.stream++
	return domain.Stream{Referer: "r"}, nil
}

func TestProviderCaches(t *testing.T) {
	st := &memStore{m: map[string][]byte{}, age: map[string]time.Duration{}}
	next := &countingProvider{}
	p := &Provider{Next: next, Store: st}
	ctx := context.Background()

	r1, err := p.Search(ctx, "Frieren", "")
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := p.Search(ctx, "  frieren ", "") // same query, normalized
	if _, _ = p.Search(ctx, "frieren", "c2"); next.search != 2 {
		t.Errorf("cursor must be part of the key: %d calls", next.search)
	}
	next.search = 1
	if next.search != 1 || !reflect.DeepEqual(r1, r2) || r2.Items[0].AltTitle != "Ei" {
		t.Errorf("search calls %d, %+v %+v", next.search, r1, r2)
	}
	p.Episodes(ctx, "a-1")
	p.Episodes(ctx, "a-1")
	if next.episodes != 1 {
		t.Errorf("episodes calls %d", next.episodes)
	}
	p.Stream(ctx, domain.Episode{ID: "1"}, domain.ModeSub)
	p.Stream(ctx, domain.Episode{ID: "1"}, domain.ModeSub)
	if next.stream != 2 {
		t.Errorf("stream links expire and must never be cached: %d calls", next.stream)
	}

	// expiry
	st.age["episodes:a-1"] = 2 * time.Hour
	p.Episodes(ctx, "a-1")
	if next.episodes != 2 {
		t.Errorf("stale episode list served: %d calls", next.episodes)
	}

	// --no-cache: always fetch, but still refresh the cache
	p.Refresh = true
	p.Search(ctx, "frieren", "")
	if next.search != 2 {
		t.Errorf("refresh ignored: %d", next.search)
	}
}

func TestProviderDoesNotCacheErrorsOrEmpty(t *testing.T) {
	st := &memStore{m: map[string][]byte{}, age: map[string]time.Duration{}}
	next := &countingProvider{fail: true}
	p := &Provider{Next: next, Store: st}
	if _, err := p.Search(context.Background(), "x", ""); err == nil {
		t.Fatal("error swallowed")
	}
	if len(st.m) != 0 {
		t.Error("error cached")
	}
}

func TestLatestInvalidatesShortEpisodeList(t *testing.T) {
	st := &memStore{m: map[string][]byte{}, age: map[string]time.Duration{}}
	next := &countingProvider{}
	p := &Provider{Next: next, Store: st}
	ctx := context.Background()

	p.Episodes(ctx, "a-1") // cached with 2 episodes
	p.Latest(ctx, domain.ModeSub)
	p.Latest(ctx, domain.ModeSub)
	if next.latest != 1 {
		t.Errorf("latest calls %d", next.latest)
	}
	p.Latest(ctx, domain.ModeDub) // its own list
	if next.latest != 2 {
		t.Errorf("dub list served from the sub cache: %d calls", next.latest)
	}
	// Latest says 3 are out: the cached list of 2 is behind
	p.Episodes(ctx, "a-1")
	if next.episodes != 2 {
		t.Errorf("short cached list served: %d calls", next.episodes)
	}
	p.Episodes(ctx, "b-1")
	p.Episodes(ctx, "b-1")
	if next.episodes != 3 {
		t.Errorf("unrelated anime refetched: %d calls", next.episodes)
	}
}

func TestInfoIsCachedAndErrorsAreNot(t *testing.T) {
	st := &memStore{m: map[string][]byte{}, age: map[string]time.Duration{}}
	next := &countingProvider{}
	p := &Provider{Next: next, Store: st}
	for range 3 {
		a, err := p.Info(context.Background(), "x")
		if err != nil || a.CoverURL != "c" {
			t.Fatalf("%+v %v", a, err)
		}
	}
	if next.info != 1 {
		t.Errorf("%d requests for one anime", next.info)
	}
}
