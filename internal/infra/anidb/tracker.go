package anidb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"sel-ani/internal/domain"
	"sel-ani/internal/infra/store"
)

const (
	leaseName = "anidb"
	leaseTTL  = 2 * time.Minute
	batch     = 10
	// noReplyTries: sends per item and run when replies get lost. UDP
	// drops packets and AniDB is sometimes slow; giving up after one
	// silence parked the episode for at least 5 minutes, and nothing
	// retried it before the next run.
	noReplyTries = 3
)

// Tracker implements domain.Tracker with AniDB.
type Tracker struct {
	Store  *store.DB
	Titles *Titles
	UDP    *UDP // nil: link anime only, never send anything
	Info   func(string)
	// RetryPause is the wait before resending after a lost reply; 0 = 30s.
	RetryPause time.Duration

	owner  string
	mu     sync.Mutex
	done   chan struct{} // open while a flush runs
	cancel context.CancelFunc
	report []string
	synced int
}

func (t *Tracker) info(s string) {
	if t.Info != nil {
		t.Info(s)
	}
}

// Resolve links a to an AniDB anime id by its titles, once, without
// asking anything.
func (t *Tracker) Resolve(ctx context.Context, a domain.Anime) error {
	if err := t.Store.RememberAnime(a); err != nil {
		return err
	}
	if _, decided, err := t.Store.AID(a.ID); err != nil || decided {
		return err
	}
	if err := t.Titles.Ensure(ctx); err != nil {
		return err
	}
	m, err := t.Titles.Match(Query{Romaji: a.AltTitle, English: a.Title})
	if err != nil {
		return err
	}
	if m.Sure > 0 {
		return t.Store.SetAID(a.ID, m.Sure, "auto")
	}
	// no title names exactly one anime: stay undecided and don't track.
	// Guessing would mark the wrong show watched, and asking adds a menu
	// to every play. A later title list may settle it; checking again
	// costs no API request.
	return nil
}

// Watched queues the episode for AniDB and starts sending in the
// background. Episodes like "12.5" have no AniDB counterpart and are skipped.
//
// AniDB sets an anime's list state itself; the UDP API cannot. "Watching"
// follows from any episode marked watched in the last 3 months.
// "Completed" needs every episode in MyList and watched, so finishing the
// last episode also queues earlier ones the local log confirms watched but
// AniDB may lack, e.g. from before tracking was on.
func (t *Tracker) Watched(_ context.Context, a domain.Anime, episode string) error {
	aid, _, err := t.Store.AID(a.ID)
	if err != nil || aid <= 0 || t.UDP == nil {
		return err
	}
	epno, err := strconv.Atoi(episode)
	if err != nil || epno <= 0 {
		return nil
	}
	if _, err := t.Store.Enqueue(aid, epno, time.Now()); err != nil {
		return err
	}
	if a.Episodes > 0 && epno == a.Episodes {
		if err := t.completeSeries(a, aid); err != nil {
			return err
		}
	}
	t.Flush()
	return nil
}

// completeSeries queues episodes 1..total-1 with their own watch time,
// but only when the log has all of them: an episode never watched must
// not be reported watched. Queued or sent episodes are left alone.
func (t *Tracker) completeSeries(a domain.Anime, aid int) error {
	seen, err := t.Store.WatchTimes(a.ID)
	if err != nil {
		return err
	}
	at := make([]time.Time, a.Episodes)
	for ep := 1; ep < a.Episodes; ep++ {
		when, ok := seen[strconv.Itoa(ep)]
		if !ok {
			return nil // not confirmed: AniDB keeps "watching"
		}
		at[ep] = when
	}
	for ep := 1; ep < a.Episodes; ep++ {
		if _, err := t.Store.Enqueue(aid, ep, at[ep]); err != nil {
			return err
		}
	}
	return nil
}

// Flush starts sending queued updates in the background, unless a flush
// is already running here or in another sel-ani process.
func (t *Tracker) Flush() {
	if t.UDP == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done != nil {
		select {
		case <-t.done:
		default:
			return // still running; it picks up the new item
		}
	}
	if t.owner == "" {
		var b [4]byte
		rand.Read(b[:])
		t.owner = fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(b[:]))
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.done = make(chan struct{})
	go t.run(ctx, t.done)
}

func (t *Tracker) note(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.report = append(t.report, s)
}

func (t *Tracker) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	if ok, err := t.Store.Acquire(leaseName, t.owner, leaseTTL); err != nil || !ok {
		return
	}
	defer t.Store.Release(leaseName, t.owner)
	for ctx.Err() == nil {
		items, err := t.Store.DueOutbox(batch)
		if err != nil || len(items) == 0 {
			return
		}
		for _, it := range items {
			if ok, _ := t.Store.Acquire(leaseName, t.owner, leaseTTL); !ok {
				return
			}
			err := t.send(ctx, it)
			var pe *PermanentError
			switch {
			case err == nil:
				t.Store.OutboxDone(it.ID)
				t.mu.Lock()
				t.synced++
				t.mu.Unlock()
			case errors.As(err, &pe) && pe.Code != codeLoginFailed && pe.Code != codeClientOutdated && pe.Code != codeClientBanned:
				t.Store.OutboxFailed(it.ID, err.Error())
				t.note(fmt.Sprintf("AniDB rejected anime %d episode %d: %v", it.AID, it.EpNo, err))
			case ctx.Err() != nil:
				return // interrupted: the item stays queued as is
			default:
				// ban, refused login, no reply, server trouble: keep the
				// item, back off, and stop for this run
				t.Store.OutboxRetry(it.ID, err.Error(), time.Now().Add(backoff(it.Attempts)))
				t.note(err.Error())
				return
			}
		}
	}
}

// send marks one item watched, resending after lost replies. A resend of
// an add that did arrive is harmless: AniDB answers "already listed" and
// the entry is edited instead.
func (t *Tracker) send(ctx context.Context, it store.OutboxItem) error {
	pause := t.RetryPause
	if pause <= 0 {
		pause = 30 * time.Second
	}
	for try := 1; ; try++ {
		err := t.UDP.MarkWatched(ctx, it.AID, it.EpNo, it.ViewedAt)
		if !errors.Is(err, ErrNoReply) || try >= noReplyTries {
			return err
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if ok, _ := t.Store.Acquire(leaseName, t.owner, leaseTTL); !ok {
			return err
		}
	}
}

// backoff grows from 5 minutes to at most 6 hours.
func backoff(attempts int) time.Duration {
	d := 5 * time.Minute
	for i := 0; i < attempts && d < 6*time.Hour; i++ {
		d *= 2
	}
	return min(d, 6*time.Hour)
}

// Close waits up to wait for a running flush, then stops it (queued items
// are sent next time) and returns a one-line summary, "" when there is
// nothing to say.
func (t *Tracker) Close(wait time.Duration) string {
	t.mu.Lock()
	done, cancel := t.done, t.cancel
	t.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		default:
			t.info("syncing with AniDB...")
			select {
			case <-done:
			case <-time.After(wait):
				cancel()
				<-done
			}
		}
	}
	if t.UDP != nil {
		t.UDP.Close()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var parts []string
	if t.synced > 0 {
		parts = append(parts, fmt.Sprintf("AniDB: %d episode(s) marked watched", t.synced))
	}
	if n, _ := t.Store.PendingCount(); n > 0 {
		parts = append(parts, fmt.Sprintf("AniDB: %d update(s) queued for next time", n))
	}
	parts = append(parts, dedupe(t.report)...)
	return strings.Join(parts, "\n")
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
