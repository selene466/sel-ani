// Package cache wraps a domain.Provider with a persistent response cache,
// so repeated searches and episode lists cost no requests.
package cache

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"sel-ani/internal/domain"
)

// Store is the persistence the cache needs (implemented by store.DB).
type Store interface {
	CacheGet(key string, maxAge time.Duration) ([]byte, bool, error)
	CachePut(key string, value []byte) error
}

// Default lifetimes. Search results barely change, so they last a day; episode lists grow
// when a new episode airs, so they expire sooner.
const (
	SearchTTL   = 24 * time.Hour
	EpisodesTTL = time.Hour
	LatestTTL   = 10 * time.Minute
)

type refreshKey struct{}

// WithRefresh returns a context whose Latest call skips the cached read.
func WithRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, refreshKey{}, true)
}

// Provider caches Search and Episodes. Stream is passed through: stream
// urls carry expiring tokens.
type Provider struct {
	Next        domain.Provider
	Store       Store
	SearchTTL   time.Duration // 0 = SearchTTL
	EpisodesTTL time.Duration // 0 = EpisodesTTL
	Refresh     bool          // skip cached reads (--no-cache), still write

	mu    sync.Mutex
	known map[string]int // episodes out per anime, as Latest reported them
}

func (p *Provider) Search(ctx context.Context, query, cursor string) (domain.Page, error) {
	key := "search:" + strings.Join(strings.Fields(strings.ToLower(query)), " ") + "|" + cursor
	v, err := cached(p, false, key, or(p.SearchTTL, SearchTTL), nil, func() ([]domain.Page, error) {
		pg, err := p.Next.Search(ctx, query, cursor)
		if len(pg.Items) == 0 {
			return nil, err
		}
		return []domain.Page{pg}, err
	})
	if len(v) == 0 {
		return domain.Page{}, err
	}
	return v[0], err
}

// Latest is cached briefly. It also remembers how many episodes each
// listed anime has out, so Episodes can tell a cached list is behind.
func (p *Provider) Latest(ctx context.Context, mode domain.Mode) ([]domain.Anime, error) {
	skip, _ := ctx.Value(refreshKey{}).(bool)
	v, err := cached(p, skip, "latest:"+string(mode), LatestTTL, nil, func() ([]domain.Anime, error) {
		return p.Next.Latest(ctx, mode)
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.known == nil {
		p.known = map[string]int{}
	}
	for _, a := range v {
		p.known[a.ID] = max(a.Sub, a.Dub)
	}
	return v, err
}

func (p *Provider) Episodes(ctx context.Context, animeID string) ([]domain.Episode, error) {
	p.mu.Lock()
	want := p.known[animeID]
	p.mu.Unlock()
	stale := func(v []domain.Episode) bool { return len(v) < want }
	return cached(p, false, "episodes:"+animeID, or(p.EpisodesTTL, EpisodesTTL), stale, func() ([]domain.Episode, error) {
		return p.Next.Episodes(ctx, animeID)
	})
}

// InfoTTL: a cover and a description hardly ever change.
const InfoTTL = 7 * 24 * time.Hour

func (p *Provider) Info(ctx context.Context, animeID string) (domain.Anime, error) {
	v, err := cached(p, false, "info:"+animeID, InfoTTL, nil, func() ([]domain.Anime, error) {
		a, err := p.Next.Info(ctx, animeID)
		if err != nil {
			return nil, err
		}
		return []domain.Anime{a}, nil
	})
	if len(v) == 0 {
		return domain.Anime{}, err
	}
	return v[0], err
}

func (p *Provider) Stream(ctx context.Context, ep domain.Episode, mode domain.Mode) (domain.Stream, error) {
	return p.Next.Stream(ctx, ep, mode)
}

// cached returns the stored value of key or fetches and stores it. Cache
// failures never fail the request: the cache is an optimization.
// Errors and empty results are not cached, so a hiccup is not remembered.
// A cached value that stale (optional) rejects is fetched again.
func cached[T any](p *Provider, skip bool, key string, ttl time.Duration, stale func([]T) bool, fetch func() ([]T, error)) ([]T, error) {
	if !p.Refresh && !skip {
		if b, ok, err := p.Store.CacheGet(key, ttl); err == nil && ok {
			var v []T
			if json.Unmarshal(b, &v) == nil && (stale == nil || !stale(v)) {
				return v, nil
			}
		}
	}
	v, err := fetch()
	if err != nil || len(v) == 0 {
		return v, err
	}
	if b, err := json.Marshal(v); err == nil {
		_ = p.Store.CachePut(key, b)
	}
	return v, nil
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}
