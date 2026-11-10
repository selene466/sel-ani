// Package domain holds the core types and the ports (interfaces) the
// application layer depends on. It has no I/O and no external dependencies.
package domain

import (
	"context"
	"errors"
	"time"
)

// Mode selects subtitled or dubbed audio.
type Mode string

const (
	ModeSub Mode = "sub"
	ModeDub Mode = "dub"
)

// ParseMode validates a mode string.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeSub, ModeDub:
		return Mode(s), nil
	}
	return "", errors.New("mode must be \"sub\" or \"dub\", got " + `"` + s + `"`)
}

// Anime is a search result.
type Anime struct {
	ID       string // miruro.cx anime id, e.g. "wHWR9m4VQ8GLW4GP9_ZTblQXn9f0phB8"
	Title    string // English title, e.g. "Frieren: Beyond Journey's End"
	AltTitle string // romanized Japanese title when known, e.g. "Sousou no Frieren"
	Episodes int    // total episodes, 0 = unknown (still airing)
	Sub      int    // subbed episodes out so far, 0 = unknown
	Dub      int    // dubbed episodes out so far, 0 = unknown

	Native      string // Japanese title, optional
	CoverURL    string
	Description string
	Format      string // "TV", "MOVIE", ...
	Status      string // "FINISHED", "RELEASING", ...
	Latest      string // newest episode number when listed by Latest, optional
}

// Page is one page of search results. Next is an opaque cursor, "" = last.
type Page struct {
	Items []Anime
	Next  string
}

// Available is the number of episodes out in mode, 0 = unknown.
func (a Anime) Available(m Mode) int {
	if m == ModeDub {
		return a.Dub
	}
	return a.Sub
}

// Name is the title the UI shows: romaji first, English when the
// provider gave no romaji.
func (a Anime) Name() string { return displayName(a.AltTitle, a.Title) }

// Episode is one entry of an anime's episode list.
type Episode struct {
	ID     string // provider episode id
	Number string // display number, may be fractional ("12.5")
}

// Link is one quality variant of a stream.
type Link struct {
	Quality string // "1080p", "720p", ...
	URL     string
}

// Stream is everything a player needs to play one episode.
type Stream struct {
	Links    []Link // sorted best first
	Audio    string // separate audio track, optional
	Subtitle string // subtitle url, optional
	Referer  string
	Origin   string // Origin header the stream host checks, optional
	MalID    string
}

// Media is a resolved, quality-selected stream ready for a player.
type Media struct {
	Title   string // "<anime> Episode <n>"
	AnimeID string
	Episode string
	Video   string
	Stream  Stream
}

// ListEntry is an anime linked to AniDB, with its local watch progress.
type ListEntry struct {
	Anime     Anime
	AID       int
	Watched   string    // last episode watched locally, "" = none
	WatchedAt time.Time // zero = never
	// AniDB's own count, valid when Synced is true.
	Synced       bool
	AniDBWatched int
	AniDBTotal   int
	SyncedAt     time.Time // when AniDB was asked, zero = never
}

// HistoryEntry records the last watched episode of an anime.
type HistoryEntry struct {
	Episode   string
	AnimeID   string
	Title     string
	AltTitle  string
	AID       int       // AniDB anime id, 0 = unknown
	Total     int       // total episodes, 0 = unknown (still airing)
	WatchedAt time.Time // zero = now, when saving
}

// Name is the title the UI shows, as Anime.Name.
func (e HistoryEntry) Name() string { return displayName(e.AltTitle, e.Title) }

func displayName(romaji, english string) string {
	if romaji != "" {
		return romaji
	}
	return english
}

// ScheduleInfo describes the airing state of an anime.
type ScheduleInfo struct {
	English string
	Romaji  string
	Status  string
	NextSub *Release
	NextDub *Release
}

// Release is an upcoming episode.
type Release struct {
	Episode string
	In      string // human readable countdown, e.g. "2d 23h 14m"
}

// Provider is a streaming source.
type Provider interface {
	Search(ctx context.Context, query, cursor string) (Page, error)
	// Latest returns recently updated anime for mode, most recent first.
	Latest(ctx context.Context, mode Mode) ([]Anime, error)
	// Info returns the details of one anime by id: titles, cover, description.
	Info(ctx context.Context, animeID string) (Anime, error)
	Episodes(ctx context.Context, animeID string) ([]Episode, error)
	Stream(ctx context.Context, ep Episode, mode Mode) (Stream, error)
}

// History persists watch progress.
type History interface {
	// Load returns the last watched episode per anime, most recent first.
	Load() ([]HistoryEntry, error)
	Save(HistoryEntry) error
	Clear() error
}

// Player plays or otherwise consumes media.
type Player interface {
	Play(ctx context.Context, m Media) error
	// Wait blocks until a previously started, detached player exits or
	// ctx is done. It must never block past ctx.
	Wait(ctx context.Context) error
	// Stop terminates the running detached player, if any. It is bounded
	// in time even if the player ignores the polite signal.
	Stop() error
	// Interactive reports whether the post-play menu makes sense
	// (false for download and debug).
	Interactive() bool
}

// Tracker syncs watch progress to an external service (AniDB).
// Implementations must keep slow network work off the caller's path.
type Tracker interface {
	// Resolve links the anime to the service's id, asking the user once
	// when the match is ambiguous. Called before playback.
	Resolve(ctx context.Context, a Anime) error
	// Watched records an episode as watched.
	Watched(ctx context.Context, a Anime, episode string) error
}

// Schedule looks up release times.
type Schedule interface {
	Lookup(ctx context.Context, query string) ([]ScheduleInfo, error)
}

// Sentinel errors.
var (
	ErrNoResults   = errors.New("no results found")
	ErrNoSources   = errors.New("no sources found")
	ErrDisabled    = errors.New("disabled in the config")
	ErrCancelled   = errors.New("cancelled")
	ErrChallenge   = errors.New("blocked by cloudflare challenge")
	ErrInvalidEp   = errors.New("invalid episode")
	ErrInvalidRng  = errors.New("invalid range")
	ErrOutOfRange  = errors.New("out of range")
	ErrNoUnwatched = errors.New("no unwatched series in history")
	ErrNoHistory   = errors.New("history is empty")
)

// ExitError carries a player's exit code up to main (--exit-after-play).
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return "player exited" }
