// Package hls reads HLS master playlists.
package hls

import (
	"cmp"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"sel-ani/internal/domain"
)

var (
	resRe = regexp.MustCompile(`RESOLUTION=\d+x(\d+)`)
	uriRe = regexp.MustCompile(`URI="([^"]*)"`)
)

// ParseMaster extracts quality variants (best first) and the default
// audio rendition from an HLS master playlist. Relative uris are resolved
// against masterURL.
func ParseMaster(b []byte, masterURL string) ([]domain.Link, string, error) {
	base, err := url.Parse(masterURL)
	if err != nil {
		return nil, "", err
	}
	abs := func(s string) string {
		r, err := base.Parse(s)
		if err != nil {
			return s
		}
		return r.String()
	}
	var links []domain.Link
	var audio string
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(l, "#EXT-X-MEDIA:") && strings.Contains(l, "TYPE=AUDIO") && strings.Contains(l, "DEFAULT=YES") && audio == "":
			if m := uriRe.FindStringSubmatch(l); m != nil {
				audio = abs(m[1])
			}
		case strings.HasPrefix(l, "#EXT-X-STREAM-INF:"):
			q := "unknown"
			if m := resRe.FindStringSubmatch(l); m != nil {
				q = m[1] + "p"
			}
			for i+1 < len(lines) {
				i++
				next := strings.TrimSpace(lines[i])
				if next != "" && !strings.HasPrefix(next, "#") {
					links = append(links, domain.Link{Quality: q, URL: abs(next)})
					break
				}
			}
		}
	}
	if len(links) == 0 {
		return nil, "", fmt.Errorf("%w: empty playlist", domain.ErrNoSources)
	}
	slices.SortStableFunc(links, func(a, b domain.Link) int {
		return cmp.Compare(qualityNum(b.Quality), qualityNum(a.Quality))
	})
	return links, audio, nil
}

func qualityNum(q string) int {
	n, _ := strconv.Atoi(strings.TrimSuffix(q, "p"))
	return n
}
