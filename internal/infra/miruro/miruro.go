// Package miruro implements domain.Provider for miruro.cx.
//
// The site is a SvelteKit app; every page's data is served as JSON at
// <route>/__data.json, so nothing here parses HTML:
//
//	/search/__data.json?q=        search results
//	/__data.json                  home page, latest episodes
//	/watch/<id>/__data.json       redirects to /watch/<id>/<slug>?ep=N
//	/watch/<id>/<slug>/__data.json?ep=N
//	                              episode list and per-server streams
//
// Servers ("sun", "bee", ...) give HLS urls plus the headers the stream
// host wants. Servers that only offer an iframe embed are skipped.
package miruro

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"sel-ani/internal/domain"
	"sel-ani/internal/infra/fetch"
	"sel-ani/internal/infra/hls"
)

// DefaultServers is the server preference when none is configured.
// bun only offers an embedded player so far: it is skipped, at no cost,
// until it serves direct streams. hop is last: it serves audio as a
// separate stream, which mpv plays with stutter and a jumping duration.
var DefaultServers = []string{"bun", "sun", "bee", "hop"}

// Provider is the miruro.cx client.
type Provider struct {
	f       fetch.Fetcher
	mu      sync.RWMutex // guards base and servers, which Configure replaces
	base    string
	servers []string

	// SearchGap is the pause between a search request and the next page of
	// it, so paging through results stays polite. Pages served from the
	// cache never reach this provider and cost no wait.
	SearchGap time.Duration
	paceMu    sync.Mutex
	lastPage  time.Time
}

// pace claims the next search slot. A next page waits until SearchGap has
// passed since the last search request; concurrent searches queue.
func (p *Provider) pace(ctx context.Context, nextPage bool) error {
	p.paceMu.Lock()
	defer p.paceMu.Unlock()
	if d := time.Until(p.lastPage.Add(p.SearchGap)); nextPage && d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	p.lastPage = time.Now()
	return nil
}

// Configure changes the site and the server preference of later requests.
func (p *Provider) Configure(base string, servers []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.base, p.servers = base, servers
}

func (p *Provider) baseURL() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.base
}

func (p *Provider) serverOrder() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.servers
}

// New returns a provider using f for all requests. base has no trailing
// slash. servers is the preference order; empty uses the site's order.
func New(f fetch.Fetcher, base string, servers []string) *Provider {
	return &Provider{f: f, base: base, servers: servers, SearchGap: time.Second}
}

func (p *Provider) data(ctx context.Context, path string, query url.Values) (page, error) {
	base := p.baseURL()
	u := base + strings.TrimSuffix(path, "/") + "/__data.json"
	if path == "/" {
		u = base + "/__data.json"
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	b, err := p.f.Fetch(ctx, u, "")
	if err != nil {
		return page{}, err
	}
	return decodePage(b)
}

type title struct {
	Romaji  string `json:"romaji"`
	English string `json:"english"`
	Native  string `json:"native"`
}

type counts struct {
	Sub int `json:"sub"`
	Dub int `json:"dub"`
}

func anime(id string, t title, total int, c counts) domain.Anime {
	a := domain.Anime{ID: id, Title: t.English, AltTitle: t.Romaji, Native: t.Native, Episodes: total, Sub: c.Sub, Dub: c.Dub}
	if a.Title == "" {
		a.Title = a.AltTitle
	}
	return a
}

func (p *Provider) Search(ctx context.Context, query, cursor string) (domain.Page, error) {
	q := url.Values{"q": {query}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if err := p.pace(ctx, cursor != ""); err != nil {
		return domain.Page{}, err
	}
	pg, err := p.data(ctx, "/search", q)
	if err != nil {
		return domain.Page{}, err
	}
	v, err := into[struct {
		Items []struct {
			ID          string `json:"id"`
			Title       title  `json:"title"`
			Total       int    `json:"episode_count"`
			Counts      counts `json:"episode_counts"`
			Cover       string `json:"cover_url"`
			Description string `json:"description"`
			Format      string `json:"format"`
			Status      string `json:"status"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
		HasMore    bool   `json:"hasMore"`
		Error      any    `json:"error"`
	}](pg.last())
	if err != nil {
		return domain.Page{}, fmt.Errorf("search results: %w", err)
	}
	if v.Error != nil {
		return domain.Page{}, fmt.Errorf("search failed: %v", v.Error)
	}
	var out domain.Page
	for _, it := range v.Items {
		if it.ID == "" {
			continue
		}
		a := anime(it.ID, it.Title, it.Total, it.Counts)
		a.CoverURL, a.Description, a.Format, a.Status = it.Cover, it.Description, it.Format, it.Status
		out.Items = append(out.Items, a)
	}
	if v.HasMore {
		out.Next = v.NextCursor
	}
	return out, nil
}

// Latest returns the home page's newest releases for mode, most recent
// first. The site keeps one rail per audio track: "raw" carries every new
// release with its sub count, "dub" only dubbed ones; there is no sub rail.
func (p *Provider) Latest(ctx context.Context, mode domain.Mode) ([]domain.Anime, error) {
	pg, err := p.data(ctx, "/", nil)
	if err != nil {
		return nil, err
	}
	type item struct {
		ID       string      `json:"animeId"`
		Title    title       `json:"title"`
		Total    int         `json:"episodeCount"`
		Counts   counts      `json:"episodeCounts"`
		AiringAt int64       `json:"airingAt"`
		Cover    string      `json:"coverUrl"`
		Format   string      `json:"format"`
		Episode  json.Number `json:"episode"`
	}
	type rail struct {
		Latest []item `json:"latest"`
	}
	v, err := into[struct {
		Rails struct {
			Raw rail `json:"raw"`
			Dub rail `json:"dub"`
		} `json:"rails"`
	}](pg.last())
	if err != nil {
		return nil, fmt.Errorf("latest list: %w", err)
	}
	items := v.Rails.Raw.Latest
	if mode == domain.ModeDub {
		items = v.Rails.Dub.Latest
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: no latest list on the home page (did the site change?)", domain.ErrNoResults)
	}
	// newest first, whatever order the page uses
	slices.SortStableFunc(items, func(a, b item) int { return cmp.Compare(b.AiringAt, a.AiringAt) })
	var out []domain.Anime
	for _, it := range items {
		if it.ID != "" {
			a := anime(it.ID, it.Title, it.Total, it.Counts)
			a.CoverURL, a.Format, a.Latest = it.Cover, it.Format, it.Episode.String()
			out = append(out, a)
		}
	}
	return out, nil
}

// watch loads an episode's watch page, following the site's redirects
// from /watch/<id> to /watch/<id>/<slug>?ep=N. It returns the page and its
// slugged path. ep "" lets the site pick the first episode.
func (p *Provider) watch(ctx context.Context, animeID, ep string) (page, string, error) {
	if animeID == "" || strings.ContainsAny(animeID, "/?#") {
		return page{}, "", fmt.Errorf("unexpected anime id %q", animeID)
	}
	path := "/watch/" + url.PathEscape(animeID)
	q := url.Values{}
	if ep != "" {
		q.Set("ep", ep)
	}
	for range 4 {
		pg, err := p.data(ctx, path, q)
		if err != nil || pg.Redirect == "" {
			return pg, path, err
		}
		u, err := url.Parse(pg.Redirect)
		if err != nil || !strings.HasPrefix(u.Path, "/watch/"+animeID+"/") {
			return pg, path, fmt.Errorf("anime %q: unexpected redirect %q", animeID, pg.Redirect)
		}
		path, q = u.Path, u.Query()
	}
	return page{}, path, fmt.Errorf("anime %q: too many redirects", animeID)
}

// Info reads the anime's own page: the details miruro.cx shows for it.
func (p *Provider) Info(ctx context.Context, animeID string) (domain.Anime, error) {
	pg, _, err := p.watch(ctx, animeID, "")
	if err != nil {
		return domain.Anime{}, err
	}
	v, err := into[struct {
		Media struct {
			ID          string `json:"id"`
			Title       title  `json:"title"`
			Format      string `json:"format"`
			Status      string `json:"status"`
			Episodes    int    `json:"episodes"`
			Description string `json:"description"`
			CoverImage  struct {
				ExtraLarge string `json:"extraLarge"`
				Large      string `json:"large"`
				Medium     string `json:"medium"`
			} `json:"coverImage"`
			Counts counts `json:"episodeCounts"`
		} `json:"media"`
	}](pg.last())
	if err != nil {
		return domain.Anime{}, fmt.Errorf("anime info: %w", err)
	}
	m := v.Media
	if m.ID == "" {
		return domain.Anime{}, fmt.Errorf("anime %q: no details on its page", animeID)
	}
	a := anime(m.ID, m.Title, m.Episodes, m.Counts)
	a.Description, a.Format, a.Status = m.Description, m.Format, m.Status
	a.CoverURL = cmp.Or(m.CoverImage.ExtraLarge, m.CoverImage.Large, m.CoverImage.Medium)
	return a, nil
}

func (p *Provider) Episodes(ctx context.Context, animeID string) ([]domain.Episode, error) {
	pg, path, err := p.watch(ctx, animeID, "")
	if err != nil {
		return nil, err
	}
	v, err := into[struct {
		EpisodesData struct {
			Episodes []struct {
				Number json.Number `json:"number"`
			} `json:"episodes"`
		} `json:"episodesData"`
	}](pg.last())
	if err != nil {
		return nil, fmt.Errorf("episode list: %w", err)
	}
	var out []domain.Episode
	for _, e := range v.EpisodesData.Episodes {
		n := e.Number.String()
		if n == "" {
			continue
		}
		out = append(out, domain.Episode{ID: path + "?ep=" + n, Number: n})
	}
	return out, nil
}

type sources struct {
	ExternalIDs struct {
		MAL []string `json:"mal"`
	} `json:"externalIds"`
	SourcesData struct {
		ProviderOrder []string     `json:"providerOrder"`
		Tracks        []trackOffer `json:"tracks"`
	} `json:"sourcesData"`
}

type trackOffer struct {
	Track     string    `json:"track"`
	Providers []offered `json:"providers"`
}

type offered struct {
	Provider string `json:"provider"`
	Servers  []struct {
		Server  string            `json:"server"`
		Headers map[string]string `json:"headers"`
		Streams []struct {
			URL    string `json:"url"`
			Format string `json:"format"`
		} `json:"streams"`
	} `json:"servers"`
	Subtitles []struct {
		Language string `json:"language"`
		Label    string `json:"label"`
		File     string `json:"file"`
		Default  bool   `json:"default"`
	} `json:"subtitles"`
}

// tracks lists the site's track names for mode, preferred first: "sub"
// has the subtitles burned in, "ssub" ships them as a separate file.
func tracks(mode domain.Mode) []string {
	if mode == domain.ModeDub {
		return []string{"dub"}
	}
	return []string{"sub", "ssub"}
}

func (p *Provider) Stream(ctx context.Context, ep domain.Episode, mode domain.Mode) (domain.Stream, error) {
	var st domain.Stream
	path, query, ok := strings.Cut(ep.ID, "?")
	q, err := url.ParseQuery(query)
	if !ok || err != nil || !strings.HasPrefix(path, "/watch/") || q.Get("ep") == "" {
		return st, fmt.Errorf("unexpected episode id %q", ep.ID)
	}
	pg, err := p.data(ctx, path, url.Values{"ep": {q.Get("ep")}})
	if err != nil {
		return st, err
	}
	src, err := into[sources](pg.last())
	if err != nil {
		return st, fmt.Errorf("episode sources: %w", err)
	}
	if len(src.ExternalIDs.MAL) > 0 {
		st.MalID = src.ExternalIDs.MAL[0]
	}
	order := p.serverOrder()
	if len(order) == 0 {
		order = src.SourcesData.ProviderOrder
	}
	// hard sub on every server before soft sub on any: soft-sub servers
	// tend to split audio into its own stream, which plays choppily
	var tried []string
	for _, c := range candidates(src.SourcesData.Tracks, tracks(mode), order) {
		s, err := p.resolve(ctx, c.offered, c.track)
		if err == nil {
			s.MalID = st.MalID
			return s, nil
		}
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		tried = append(tried, fmt.Sprintf("%s/%s: %v", c.Provider, c.track, err))
	}
	if len(tried) == 0 {
		return st, fmt.Errorf("%w for %s on servers %s", domain.ErrNoSources, mode, strings.Join(order, ", "))
	}
	return st, fmt.Errorf("%w for %s (%s)", domain.ErrNoSources, mode, strings.Join(tried, "; "))
}

type candidate struct {
	offered
	track string
}

// candidates lists the offers to try: by track preference, then server order.
func candidates(have []trackOffer, wanted, order []string) []candidate {
	var out []candidate
	for _, track := range wanted {
		for _, name := range order {
			for _, t := range have {
				if t.Track != track {
					continue
				}
				for _, o := range t.Providers {
					if o.Provider == name {
						out = append(out, candidate{o, track})
					}
				}
			}
		}
	}
	return out
}

// resolve returns the first playable server of o.
func (p *Provider) resolve(ctx context.Context, o offered, track string) (domain.Stream, error) {
	var last error = errors.New("embed only")
	for _, s := range o.Servers {
		for _, stream := range s.Streams {
			if stream.URL == "" {
				continue
			}
			ref, origin := s.Headers["Referer"], s.Headers["Origin"]
			st := domain.Stream{Referer: ref, Origin: origin}
			if stream.Format == "hls" || strings.Contains(stream.URL, ".m3u8") {
				pl, err := p.f.Fetch(fetch.WithOrigin(ctx, origin), stream.URL, ref)
				if err != nil {
					last = err
					continue
				}
				switch st.Links, st.Audio, err = hls.ParseMaster(pl, stream.URL); {
				case err == nil:
				case bytes.Contains(pl, []byte("#EXTINF")): // a media playlist, one quality
					st.Links = []domain.Link{{Quality: "unknown", URL: stream.URL}}
				default:
					last = err
					continue
				}
			} else {
				st.Links = []domain.Link{{Quality: "unknown", URL: stream.URL}}
			}
			if track == "ssub" {
				st.Subtitle = subtitle(o)
			}
			return st, nil
		}
	}
	return domain.Stream{}, last
}

// subtitle picks the first English track, else the default one. Some
// servers mark another language as default.
func subtitle(o offered) string {
	for _, s := range o.Subtitles {
		if s.File != "" && isEnglish(s.Language, s.Label) {
			return s.File
		}
	}
	for _, s := range o.Subtitles {
		if s.Default && s.File != "" {
			return s.File
		}
	}
	return ""
}

func isEnglish(lang, label string) bool {
	switch strings.ToLower(lang) {
	case "en", "eng", "english":
		return true
	}
	return strings.HasPrefix(strings.ToLower(label), "english")
}

var _ domain.Provider = (*Provider)(nil)
