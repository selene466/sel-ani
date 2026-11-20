package app

import (
	"strconv"
	"strings"

	"sel-ani/internal/domain"
)

// SelectQuality picks a link by quality. links must be sorted best first.
// "best" (or empty) picks the first, "worst" the last, anything else the
// first link whose quality label contains q. If nothing matches, a
// resolution q like "360p" falls back to the closest offer: the best one
// not above it, else the lowest one above it. Anything else falls back to
// best. fallback=true reports that q was not offered.
func SelectQuality(links []domain.Link, q string) (l domain.Link, fallback bool) {
	if len(links) == 0 {
		return domain.Link{}, false
	}
	switch q {
	case "", "best":
		return links[0], false
	case "worst":
		return links[len(links)-1], false
	}
	for _, l := range links {
		if strings.Contains(l.Quality, q) {
			return l, false
		}
	}
	n, err := strconv.Atoi(strings.TrimSuffix(q, "p"))
	if err != nil {
		return links[0], true
	}
	var above domain.Link
	for _, l := range links {
		h, err := strconv.Atoi(strings.TrimSuffix(l.Quality, "p"))
		switch {
		case err != nil:
		case h <= n:
			return l, true
		default:
			above = l // sorted best first: the last one is the lowest above n
		}
	}
	if above.URL != "" {
		return above, true
	}
	return links[0], true
}
