package anidb

import (
	"context"
	"sync"
	"time"

	"sel-ani/internal/domain"
)

// Switch is a tracker that can be replaced, or turned off, while the program
// runs: enabling AniDB in the config takes effect at once. While off it does
// nothing and reports domain.ErrDisabled for progress queries.
type Switch struct {
	mu  sync.RWMutex
	cur *Tracker
}

// Set makes t the active tracker, nil turns tracking off. The previous one is
// closed, waiting up to wait for it to finish sending. Queued updates of an
// enabled tracker are sent right away.
func (s *Switch) Set(t *Tracker, wait time.Duration) string {
	s.mu.Lock()
	old := s.cur
	s.cur = t
	s.mu.Unlock()
	// the old tracker goes first: it holds the send lease, and a flush of
	// the new one started meanwhile would give up on it
	var sum string
	if old != nil {
		sum = old.Close(wait)
	}
	if t != nil {
		t.Flush()
	}
	return sum
}

// Close closes the active tracker and returns its summary.
func (s *Switch) Close(wait time.Duration) string { return s.Set(nil, wait) }

func (s *Switch) active() *Tracker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

func (s *Switch) Resolve(ctx context.Context, a domain.Anime) error {
	if t := s.active(); t != nil {
		return t.Resolve(ctx, a)
	}
	return nil
}

func (s *Switch) Watched(ctx context.Context, a domain.Anime, episode string) error {
	if t := s.active(); t != nil {
		return t.Watched(ctx, a, episode)
	}
	return nil
}

// MyListProgress reads an anime's progress from AniDB.
func (s *Switch) MyListProgress(ctx context.Context, aid int) (watched, total int, err error) {
	t := s.active()
	if t == nil || t.UDP == nil {
		return 0, 0, domain.ErrDisabled
	}
	return t.UDP.MyListProgress(ctx, aid)
}

var _ domain.Tracker = (*Switch)(nil)
