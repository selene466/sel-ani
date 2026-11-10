// Package app implements the use cases the interface calls: list latest
// releases, search, show the AniDB-backed my list, load an anime's detail
// and play an episode. It only talks to domain ports and imports no UI code.
package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"sel-ani/internal/domain"
)

// SearchLimit is the most results a search shows.
const SearchLimit = 50

// My list syncing keeps AniDB traffic low: its UDP API allows about one
// packet per 4 seconds and is not meant to mirror data. Local progress is
// always known and wins over AniDB's, so AniDB is only asked for what could
// have changed elsewhere.
const (
	SyncAge   = 7 * 24 * time.Hour // progress of an unfinished anime stays valid this long
	SyncRetry = 12 * time.Hour     // after a failed query, wait this long
	SyncBatch = 25                 // anime queried per run; the rest follow next run
)

// Store is the persistence the use cases need (implemented by store.DB).
type Store interface {
	Progress() (map[string]string, error)
	Save(domain.HistoryEntry) error
	WatchedEpisodes(animeID string) ([]string, error)
	RememberAnime(domain.Anime) error
	MyList() ([]domain.ListEntry, error)
	PutMyList(aid, watched, total int) error
	Get(key string) (string, bool, error)
	Set(key, value string) error
}

// Syncer reads an anime's progress from AniDB's MyList.
type Syncer interface {
	MyListProgress(ctx context.Context, aid int) (watched, total int, err error)
}

// App wires the ports together. Mode and Quality may change at runtime.
type App struct {
	Provider domain.Provider
	Store    Store
	Player   domain.Player
	Tracker  domain.Tracker // optional
	Sync     Syncer         // optional
	Mode     domain.Mode
	Quality  string
	Warn     func(string) // optional
}

// Entry is one row of a list.
type Entry struct {
	Anime   domain.Anime
	Watched string // last episode watched, "" = none
	InList  bool   // tracked locally or on AniDB
	New     bool   // an episode is out that was not watched yet
	Counter string // episode counter, "[N] " first when New
}

// Listing is a list of entries. For a search it is one page of results.
type Listing struct {
	Entries []Entry
	Next    string // search: cursor of the next page, "" = last
}

func num(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func (a *App) warn(s string) {
	if a.Warn != nil {
		a.Warn(s)
	}
}

// isNew reports whether more episodes are out than were watched. Unknown
// counts never mark.
func isNew(avail int, watched string) bool {
	w, ok := num(watched)
	return avail > 0 && ok && float64(avail) > w
}

func tag(isNew bool, counter string) string {
	if isNew {
		return "[N] " + counter
	}
	return counter
}

func total(n int) string {
	if n > 0 {
		return strconv.Itoa(n)
	}
	return "?"
}

func (a *App) entries(items []domain.Anime, prog map[string]string, latest bool) []Entry {
	out := make([]Entry, len(items))
	for i, it := range items {
		w, inList := prog[it.ID]
		e := Entry{Anime: it, Watched: w, InList: inList}
		avail := it.Available(a.Mode)
		e.New = inList && isNew(avail, w)
		base := total(avail) + "/" + total(it.Episodes)
		if latest {
			base = it.Latest
			if base == "" {
				base = total(avail)
			}
		}
		e.Counter = tag(e.New, base)
		out[i] = e
	}
	return out
}

// Latest returns the newest releases, all of them: the site serves one
// list and it is short.
func (a *App) Latest(ctx context.Context) (Listing, error) {
	all, err := a.Provider.Latest(ctx, a.Mode)
	if err != nil {
		return Listing{}, err
	}
	prog, err := a.Store.Progress()
	if err != nil {
		return Listing{}, err
	}
	return Listing{Entries: a.entries(all, prog, true)}, nil
}

// Search returns the page of results at cursor ("" = first).
func (a *App) Search(ctx context.Context, query, cursor string) (Listing, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Listing{}, domain.ErrNoResults
	}
	pg, err := a.Provider.Search(ctx, query, cursor)
	if err != nil {
		return Listing{}, err
	}
	if len(pg.Items) == 0 {
		return Listing{}, fmt.Errorf("%w for %q", domain.ErrNoResults, query)
	}
	prog, err := a.Store.Progress()
	if err != nil {
		return Listing{}, err
	}
	return Listing{Entries: a.entries(pg.Items, prog, false), Next: pg.Next}, nil
}

// finished reports whether every episode of e is watched.
func finished(e domain.ListEntry) bool {
	w, _ := num(e.Watched)
	if e.Synced {
		w = max(w, float64(e.AniDBWatched))
	}
	n := max(e.Anime.Episodes, e.AniDBTotal)
	return n > 0 && w >= float64(n)
}

// sortList puts unfinished anime first, each group most recently watched first.
func sortList(list []domain.ListEntry) {
	sort.SliceStable(list, func(i, j int) bool {
		fi, fj := finished(list[i]), finished(list[j])
		if fi != fj {
			return !fi
		}
		return list[i].WatchedAt.After(list[j].WatchedAt)
	})
}

// MyList returns the anime linked to AniDB, unfinished ones first, each
// group most recently watched first.
func (a *App) MyList(ctx context.Context) (Listing, error) {
	list, err := a.Store.MyList()
	if err != nil {
		return Listing{}, err
	}
	// how many episodes are out: only known for anime on the latest list,
	// which is cached; a failure just means no [N] marks
	avail := map[string]int{}
	if latest, err := a.Provider.Latest(ctx, a.Mode); err == nil {
		for _, it := range latest {
			avail[it.ID] = it.Available(a.Mode)
		}
	}
	sortList(list)
	out := Listing{Entries: make([]Entry, len(list))}
	for i, le := range list {
		// AniDB lags the local log (its queue is rate limited): never show less
		// than what was watched here
		watched, tot := le.Watched, le.Anime.Episodes
		if le.Synced {
			if lw, _ := num(watched); float64(le.AniDBWatched) > lw {
				watched = strconv.Itoa(le.AniDBWatched)
			}
			tot = max(le.AniDBTotal, tot)
		}
		if watched == "" {
			watched = "0"
		}
		e := Entry{Anime: le.Anime, Watched: le.Watched, InList: true}
		e.New = isNew(avail[le.Anime.ID], watched)
		e.Counter = tag(e.New, watched+"/"+total(tot))
		out.Entries[i] = e
	}
	return out, nil
}

// SyncMyList refreshes AniDB's progress of listed anime, as little as it can:
// finished anime are never asked again, the others only when the stored
// answer is older than SyncAge, a failed query waits SyncRetry, and one run
// asks about SyncBatch anime at most (unfinished, most recently watched
// first). The syncer rate limits, so this may take a while: run it in the
// background.
func (a *App) SyncMyList(ctx context.Context) error {
	if a.Sync == nil {
		return nil
	}
	list, err := a.Store.MyList()
	if err != nil {
		return err
	}
	sortList(list)
	var errs []error
	asked := 0
	for _, e := range list {
		if finished(e) || (e.Synced && time.Since(e.SyncedAt) < SyncAge) || a.failedRecently(e.AID) {
			continue
		}
		if asked == SyncBatch {
			break
		}
		asked++
		w, t, err := a.Sync.MyListProgress(ctx, e.AID)
		if errors.Is(err, domain.ErrDisabled) {
			return nil // AniDB is off: nothing to refresh
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			_ = a.Store.Set(failKey(e.AID), strconv.FormatInt(time.Now().Unix(), 10))
			errs = append(errs, fmt.Errorf("%s: %w", e.Anime.Name(), err))
			continue
		}
		if err := a.Store.PutMyList(e.AID, w, t); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

func failKey(aid int) string { return "mylist_fail:" + strconv.Itoa(aid) }

// failedRecently reports whether asking AniDB about aid failed lately.
func (a *App) failedRecently(aid int) bool {
	v, ok, err := a.Store.Get(failKey(aid))
	if err != nil || !ok {
		return false
	}
	at, err := strconv.ParseInt(v, 10, 64)
	return err == nil && time.Since(time.Unix(at, 0)) < SyncRetry
}

// Hydrate fills in the cover, Japanese title, description, format and status
// of an anime that was stored without them, e.g. by an older version. It
// reads the anime's own miruro.cx page by id (cached) and stores the result,
// so it costs a request only once per anime.
func (a *App) Hydrate(ctx context.Context, an domain.Anime) (domain.Anime, error) {
	if an.CoverURL != "" {
		return an, nil
	}
	info, err := a.Provider.Info(ctx, an.ID)
	if err != nil {
		return an, err
	}
	an.CoverURL, an.Description = info.CoverURL, info.Description
	an.Native = cmp.Or(an.Native, info.Native)
	an.Format, an.Status = cmp.Or(an.Format, info.Format), cmp.Or(an.Status, info.Status)
	if an.Episodes == 0 {
		an.Episodes = info.Episodes
	}
	_ = a.Store.RememberAnime(an)
	return an, nil
}

// Detail is everything the detail view shows.
type Detail struct {
	Anime    domain.Anime
	Episodes []domain.Episode
	Watched  map[string]bool
	Next     string // episode after the last watched one, "" = none
}

// Detail loads the episode list of an anime and its watch state.
func (a *App) Detail(ctx context.Context, an domain.Anime) (Detail, error) {
	eps, err := a.Provider.Episodes(ctx, an.ID)
	if err != nil {
		return Detail{}, err
	}
	seen, err := a.Store.WatchedEpisodes(an.ID)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Anime: an, Episodes: eps, Watched: map[string]bool{}}
	last := ""
	for _, s := range seen {
		d.Watched[s] = true
		if f, ok := num(s); ok {
			if lf, lok := num(last); !lok || f > lf {
				last = s
			}
		}
	}
	if last == "" {
		return d, nil
	}
	d.Next, _ = Next(numbers(eps), last)
	return d, nil
}

// Play resolves the stream of ep, hands it to the player, and records the
// watch locally and on AniDB.
func (a *App) Play(ctx context.Context, an domain.Anime, ep domain.Episode) error {
	if a.Tracker != nil {
		if err := a.Tracker.Resolve(ctx, an); err != nil {
			a.warn("tracking: " + err.Error())
		}
	}
	st, err := a.Provider.Stream(ctx, ep, a.Mode)
	if err != nil {
		return fmt.Errorf("episode %s (%s): %w", ep.Number, a.Mode, err)
	}
	l, fallback := SelectQuality(st.Links, a.Quality)
	if fallback {
		a.warn(a.Quality + " not found, using best")
	}
	if l.URL == "" {
		return domain.ErrNoSources
	}
	m := domain.Media{
		Title:   fmt.Sprintf("%s Episode %s", an.Name(), ep.Number),
		AnimeID: an.ID,
		Episode: ep.Number,
		Video:   l.URL,
		Stream:  st,
	}
	if err := a.Player.Stop(); err != nil {
		a.warn("stopping previous player: " + err.Error())
	}
	if err := a.Player.Play(ctx, m); err != nil {
		var ee *domain.ExitError
		if !errors.As(err, &ee) {
			return err
		}
	}
	_ = a.Store.RememberAnime(an)
	if err := a.Store.Save(domain.HistoryEntry{Episode: ep.Number, AnimeID: an.ID, Title: an.Title, AltTitle: an.AltTitle, Total: an.Episodes}); err != nil {
		a.warn("history: " + err.Error())
	}
	if a.Tracker != nil {
		if err := a.Tracker.Watched(ctx, an, ep.Number); err != nil {
			a.warn("tracking: " + err.Error())
		}
	}
	return nil
}
