package anidb

import (
	"context"
	"strings"
	"testing"
	"time"

	"sel-ani/internal/domain"
	"sel-ani/internal/infra/store"
)

func newTracker(t *testing.T, srv *fakeServer) (*Tracker, *store.DB) {
	db := newStore(t)
	ts, _, _ := titleServer(t)
	tr := &Tracker{Store: db, Titles: &Titles{Store: db, URL: ts.URL}}
	if srv != nil {
		u, _ := newClient(srv, memState{}, 0)
		u.State = db
		tr.UDP = u
	}
	return tr, db
}

var frieren = domain.Anime{ID: "frieren-beyond-journeys-end-481", Title: "Frieren: Beyond Journey's End", AltTitle: "Sousou no Frieren"}

func TestResolveSureMatch(t *testing.T) {
	tr, db := newTracker(t, nil)
	if err := tr.Resolve(context.Background(), frieren); err != nil {
		t.Fatal(err)
	}
	if aid, decided, _ := db.AID(frieren.ID); aid != 17617 || !decided {
		t.Errorf("aid %d %v", aid, decided)
	}
}

// Without one sure title match nothing is linked or asked: the anime is
// not tracked, and stays undecided for a later title list.
func TestResolveUnsureTracksNothing(t *testing.T) {
	tr, db := newTracker(t, nil)
	for _, a := range []domain.Anime{
		{ID: "frieren-two-9", Title: "Frieren Season Two"},    // fuzzy hits only
		{ID: "hanako-1", AltTitle: "hanako", Title: "Hanako"}, // two equal main titles
		{ID: "y-2", Title: "Completely Unrelated Show"},       // nothing at all
	} {
		if err := tr.Resolve(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		if aid, decided, _ := db.AID(a.ID); aid != 0 || decided {
			t.Errorf("%s: aid %d decided %v", a.ID, aid, decided)
		}
	}
}

// A decision made earlier, e.g. picked in the old menu, is kept.
func TestResolveKeepsEarlierDecision(t *testing.T) {
	tr, db := newTracker(t, nil)
	db.SetAID(frieren.ID, 18290, "user")
	tr.Resolve(context.Background(), frieren)
	if aid, _, _ := db.AID(frieren.ID); aid != 18290 {
		t.Errorf("aid %d", aid)
	}
}

func TestWatchedSyncsInBackground(t *testing.T) {
	srv := newFakeServer(t, "k3y")
	tr, db := newTracker(t, srv)
	ctx := context.Background()
	tr.Resolve(ctx, frieren)

	tr.Watched(ctx, frieren, "1")
	tr.Watched(ctx, frieren, "2")
	tr.Watched(ctx, frieren, "2")   // rewatch: no second packet
	tr.Watched(ctx, frieren, "5.5") // no AniDB equivalent
	summary := tr.Close(5 * time.Second)

	var adds []string
	for _, c := range srv.commands() {
		if strings.HasPrefix(c, "MYLISTADD") {
			adds = append(adds, c)
		}
	}
	if len(adds) != 2 {
		t.Errorf("MYLISTADD packets %q", adds)
	}
	if n, _ := db.PendingCount(); n != 0 {
		t.Errorf("pending %d", n)
	}
	if !strings.Contains(summary, "2 episode(s) marked watched") {
		t.Errorf("summary %q", summary)
	}
}

func TestWatchedWithoutLinkOrCredentialsSendsNothing(t *testing.T) {
	srv := newFakeServer(t, "")
	tr, db := newTracker(t, srv)
	ctx := context.Background()
	unlinked := domain.Anime{ID: "nolink-1", Title: "x"}
	tr.Watched(ctx, unlinked, "1")
	tr.Close(time.Second)
	if len(srv.commands()) != 0 {
		t.Errorf("sent for an unlinked anime: %q", srv.commands())
	}

	tr2, _ := newTracker(t, nil) // link-only mode
	tr2.Store = db
	tr2.Resolve(ctx, frieren)
	tr2.Watched(ctx, frieren, "1")
	tr2.Close(time.Second)
	if n, _ := db.PendingCount(); n != 0 {
		t.Errorf("link-only mode queued %d", n)
	}
}

func TestFlushKeepsQueueOnNoReply(t *testing.T) {
	srv := newFakeServer(t, "")
	srv.with(func() { srv.silent = true })
	tr, db := newTracker(t, srv)
	tr.UDP.Timeout = 100 * time.Millisecond
	tr.RetryPause = 10 * time.Millisecond
	ctx := context.Background()
	tr.Resolve(ctx, frieren)
	tr.Watched(ctx, frieren, "1")
	summary := tr.Close(5 * time.Second)
	if got := len(srv.commands()); got != noReplyTries {
		t.Errorf("sent %d packets, want %d tries: %q", got, noReplyTries, srv.commands())
	}
	if n, _ := db.PendingCount(); n != 1 {
		t.Errorf("pending %d", n)
	}
	if !strings.Contains(summary, "queued for next time") || !strings.Contains(summary, "no reply") {
		t.Errorf("summary %q", summary)
	}
	// backed off: not due right away
	if due, _ := db.DueOutbox(10); len(due) != 0 {
		t.Errorf("retried without backoff: %+v", due)
	}
}

// A lost reply is resent in the same run instead of parking the episode
// until a later run.
func TestFlushResendsAfterLostReply(t *testing.T) {
	srv := newFakeServer(t, "k3y")
	srv.with(func() { srv.lose["MYLISTADD"] = 1 })
	tr, db := newTracker(t, srv)
	tr.UDP.Timeout = 100 * time.Millisecond
	tr.RetryPause = 10 * time.Millisecond
	ctx := context.Background()
	tr.Resolve(ctx, frieren)
	tr.Watched(ctx, frieren, "1")
	summary := tr.Close(5 * time.Second)
	if n, _ := db.PendingCount(); n != 0 {
		t.Errorf("pending %d", n)
	}
	if !strings.Contains(summary, "1 episode(s) marked watched") {
		t.Errorf("summary %q", summary)
	}
	var adds []string
	for _, c := range srv.commands() {
		if strings.HasPrefix(c, "MYLISTADD") {
			adds = append(adds, c)
		}
	}
	// lost add, resend answered "0 added", generic edit
	if len(adds) != 3 || !strings.Contains(adds[2], "edit=1") {
		t.Errorf("MYLISTADD packets %q", adds)
	}
}

func adds(srv *fakeServer) []string {
	var out []string
	for _, c := range srv.commands() {
		if strings.HasPrefix(c, "MYLISTADD") {
			out = append(out, c)
		}
	}
	return out
}

// Finishing the last episode, with every earlier one in the local log,
// puts the earlier episodes AniDB lacks into MyList with their own watch
// time, so AniDB can mark the anime completed. Sent ones are not resent.
func TestLastEpisodeCompletesSeries(t *testing.T) {
	srv := newFakeServer(t, "")
	tr, db := newTracker(t, srv)
	ctx := context.Background()
	show := frieren
	show.Episodes = 4
	tr.Resolve(ctx, show)
	// 1 and 2 watched before tracking; 3 tracked and sent
	for ep, at := range map[string]int64{"1": 1_700_000_001, "2": 1_700_000_002, "3": 1_700_000_003} {
		db.Save(domain.HistoryEntry{AnimeID: show.ID, Title: show.Title, Episode: ep, WatchedAt: time.Unix(at, 0)})
	}
	tr.Watched(ctx, show, "3")
	tr.Close(5 * time.Second)
	before := len(adds(srv))

	tr2, _ := newTracker(t, srv)
	tr2.Store, tr2.UDP.State = db, db
	db.Save(domain.HistoryEntry{AnimeID: show.ID, Title: show.Title, Episode: "4"})
	tr2.Watched(ctx, show, "4")
	tr2.Close(5 * time.Second)

	sent := adds(srv)[before:]
	got := map[string]bool{}
	for _, c := range sent {
		for _, ep := range []string{"1", "2", "3", "4"} {
			if strings.Contains(c, "epno="+ep+"&") || strings.HasSuffix(c, "epno="+ep) {
				got[ep] = true
			}
		}
	}
	if len(sent) != 3 || !got["1"] || !got["2"] || !got["4"] || got["3"] {
		t.Errorf("sent %q", sent)
	}
	for _, c := range sent {
		if strings.Contains(c, "epno=1&") && !strings.Contains(c, "viewdate=1700000001") {
			t.Errorf("episode 1 not sent with its own watch time: %q", c)
		}
	}
}

// An episode missing from the log is never reported watched: only the
// last one goes out, and AniDB keeps the anime at watching.
func TestLastEpisodeWithGapSendsOnlyItself(t *testing.T) {
	srv := newFakeServer(t, "")
	tr, db := newTracker(t, srv)
	ctx := context.Background()
	show := frieren
	show.Episodes = 4
	tr.Resolve(ctx, show)
	for _, ep := range []string{"1", "3", "4"} { // 2 never watched
		db.Save(domain.HistoryEntry{AnimeID: show.ID, Title: show.Title, Episode: ep})
	}
	tr.Watched(ctx, show, "4")
	tr.Close(5 * time.Second)
	if a := adds(srv); len(a) != 1 || !strings.Contains(a[0], "epno=4") {
		t.Errorf("sent %q", a)
	}
}

// Not the last episode, or total unknown (airing): nothing extra.
func TestNoCompletionBeforeLastEpisode(t *testing.T) {
	srv := newFakeServer(t, "")
	tr, db := newTracker(t, srv)
	ctx := context.Background()
	airing := frieren // Episodes 0: still airing
	tr.Resolve(ctx, airing)
	for _, ep := range []string{"1", "2", "3"} {
		db.Save(domain.HistoryEntry{AnimeID: airing.ID, Title: airing.Title, Episode: ep})
	}
	tr.Watched(ctx, airing, "3")
	tr.Close(5 * time.Second)
	if a := adds(srv); len(a) != 1 {
		t.Errorf("sent %q", a)
	}
}

func TestOnlyOneProcessFlushes(t *testing.T) {
	srv := newFakeServer(t, "")
	tr, db := newTracker(t, srv)
	ctx := context.Background()
	tr.Resolve(ctx, frieren)
	// another process holds the lease
	db.Acquire(leaseName, "other-process", time.Minute)
	tr.Watched(ctx, frieren, "1")
	tr.Close(time.Second)
	if len(srv.commands()) != 0 {
		t.Errorf("sent while another process holds the lease: %q", srv.commands())
	}
	if n, _ := db.PendingCount(); n != 1 {
		t.Errorf("pending %d", n)
	}
}

func TestBackoff(t *testing.T) {
	if backoff(0) != 5*time.Minute || backoff(1) != 10*time.Minute || backoff(20) != 6*time.Hour {
		t.Errorf("%v %v %v", backoff(0), backoff(1), backoff(20))
	}
}
