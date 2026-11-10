package app

import (
	"strings"

	"sel-ani/internal/domain"
)

// SelectQuality picks a link by quality. links must be sorted best first.
// "best" (or empty) picks the first, "worst" the last, anything else the
// first link whose quality label contains q. If nothing matches it falls
// back to best and reports fallback=true.
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
	return links[0], true
}
