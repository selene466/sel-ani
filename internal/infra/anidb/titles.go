// Package anidb links anime to AniDB and reports watched episodes to the
// user's AniDB MyList.
//
// AniDB bans clients that hammer it, so everything here is built to send
// as little as possible:
//   - titles come from the official daily dump, fetched at most once a day
//     with a conditional request, and are matched locally
//   - an anime is matched once; the answer is stored for good
//   - UDP packets are spaced at least 4 seconds apart, across runs
//   - the session is reused across runs instead of logging in each time
//   - an episode is reported once, even when watched again
package anidb

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sel-ani/internal/infra/store"
)

// TitlesURL is AniDB's daily title dump.
const TitlesURL = "https://anidb.net/api/anime-titles.dat.gz"

// AniDB regenerates the dump daily and asks not to fetch it more often.
const (
	titlesMaxAge     = 24 * time.Hour
	titlesRetryAfter = time.Hour
	titlesMaxBytes   = 64 << 20 // the dump is ~1.5MB gzipped
)

const (
	kvTitlesChecked = "anidb_titles_checked" // last successful check
	kvTitlesTried   = "anidb_titles_tried"   // last attempt, success or not
	kvTitlesETag    = "anidb_titles_etag"
	kvTitlesLastMod = "anidb_titles_last_modified"
)

// Titles keeps the local AniDB title index fresh.
type Titles struct {
	Store     *store.DB
	URL       string // "" = TitlesURL
	UserAgent string
	Client    *http.Client
	now       func() time.Time
}

func (t *Titles) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

func (t *Titles) unixKV(key string) time.Time {
	v, ok, _ := t.Store.Get(key)
	if !ok {
		return time.Time{}
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return time.Unix(n, 0)
}

// Ensure makes sure the title index exists and is at most a day old. It
// returns an error only when there is no usable index at all.
func (t *Titles) Ensure(ctx context.Context) error {
	n, err := t.Store.TitleCount()
	if err != nil {
		return err
	}
	now := t.clock()
	if n > 0 && now.Sub(t.unixKV(kvTitlesChecked)) < titlesMaxAge {
		return nil
	}
	if now.Sub(t.unixKV(kvTitlesTried)) < titlesRetryAfter {
		if n > 0 {
			return nil
		}
		return errors.New("AniDB title list unavailable, retrying later")
	}
	t.Store.Set(kvTitlesTried, strconv.FormatInt(now.Unix(), 10))
	err = t.download(ctx, n > 0)
	if err != nil && n > 0 {
		return nil // a stale index beats none; try again in an hour
	}
	return err
}

func (t *Titles) download(ctx context.Context, conditional bool) error {
	url := t.URL
	if url == "" {
		url = TitlesURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if t.UserAgent != "" {
		req.Header.Set("User-Agent", t.UserAgent)
	}
	if conditional {
		if v, ok, _ := t.Store.Get(kvTitlesETag); ok {
			req.Header.Set("If-None-Match", v)
		}
		if v, ok, _ := t.Store.Get(kvTitlesLastMod); ok {
			req.Header.Set("If-Modified-Since", v)
		}
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("AniDB titles: %w", err)
	}
	defer resp.Body.Close()
	now := strconv.FormatInt(t.clock().Unix(), 10)
	switch {
	case resp.StatusCode == http.StatusNotModified && conditional:
		return t.Store.Set(kvTitlesChecked, now)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("AniDB titles: HTTP %d", resp.StatusCode)
	}
	zr, err := gzip.NewReader(io.LimitReader(resp.Body, titlesMaxBytes))
	if err != nil {
		return fmt.Errorf("AniDB titles: %w", err)
	}
	if err := t.Store.ReplaceTitles(func(yield func(store.Title) error) error {
		return parseTitles(zr, yield)
	}); err != nil {
		return fmt.Errorf("AniDB titles: %w", err)
	}
	if v := resp.Header.Get("ETag"); v != "" {
		t.Store.Set(kvTitlesETag, v)
	}
	if v := resp.Header.Get("Last-Modified"); v != "" {
		t.Store.Set(kvTitlesLastMod, v)
	}
	return t.Store.Set(kvTitlesChecked, now)
}

// parseTitles reads "aid|type|language|title" lines; # starts a comment.
func parseTitles(r io.Reader, yield func(store.Title) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	count := 0
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		f := strings.SplitN(line, "|", 4)
		if len(f) != 4 {
			continue
		}
		aid, err1 := strconv.Atoi(f[0])
		typ, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil || aid <= 0 || f[3] == "" {
			continue
		}
		if err := yield(store.Title{AID: aid, Type: typ, Lang: f[2], Title: f[3], Norm: Normalize(f[3])}); err != nil {
			return err
		}
		count++
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if count == 0 {
		return errors.New("empty title list")
	}
	return nil
}
