package app

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"sel-ani/internal/domain"
)

var epToken = regexp.MustCompile(`^(-1|[0-9]+(\.[0-9]+)?)$`)

// ResolveEpisodes turns an episode spec into the matching episode numbers
// from list. Accepted forms:
//
//	"5"     single episode
//	"-1"    last episode
//	"2-4"   inclusive range, also "2 4"
//	"0-3"   0 as start means the first episode
//	"4--1"  -1 as end means the last episode
func ResolveEpisodes(list []string, spec string) ([]string, error) {
	start, end, isRange, err := parseSpec(spec)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, domain.ErrInvalidEp
	}
	start = resolveAlias(list, start)
	if !isRange {
		if !slices.Contains(list, start) {
			return nil, fmt.Errorf("%w: %s", domain.ErrInvalidEp, start)
		}
		return []string{start}, nil
	}
	end = resolveAlias(list, end)
	i, j := slices.Index(list, start), slices.Index(list, end)
	if i < 0 || j < 0 || j < i {
		return nil, fmt.Errorf("%w: %s", domain.ErrInvalidRng, strings.TrimSpace(spec))
	}
	return slices.Clone(list[i : j+1]), nil
}

func parseSpec(spec string) (start, end string, isRange bool, err error) {
	bad := fmt.Errorf("%w: %q", domain.ErrInvalidEp, spec)
	f := strings.Fields(spec)
	switch {
	case len(f) == 2:
		start, end = f[0], f[1]
	case len(f) == 1:
		s := f[0]
		if epToken.MatchString(s) {
			return s, "", false, nil
		}
		// first '-' after position 0 separates the bounds, so "4--1" works
		i := strings.Index(s[1:], "-")
		if i < 0 {
			return "", "", false, bad
		}
		start, end = s[:i+1], s[i+2:]
	default:
		return "", "", false, bad
	}
	if !epToken.MatchString(start) || !epToken.MatchString(end) {
		return "", "", false, bad
	}
	if start == end {
		return start, "", false, nil
	}
	return start, end, true, nil
}

func resolveAlias(list []string, n string) string {
	switch n {
	case "-1":
		return list[len(list)-1]
	case "0":
		if !slices.Contains(list, "0") {
			return list[0]
		}
	}
	return n
}

// Next returns the episode after cur.
func Next(list []string, cur string) (string, bool) {
	i := slices.Index(list, cur)
	if i < 0 || i+1 >= len(list) {
		return "", false
	}
	return list[i+1], true
}

// Prev returns the episode before cur.
func Prev(list []string, cur string) (string, bool) {
	i := slices.Index(list, cur)
	if i <= 0 {
		return "", false
	}
	return list[i-1], true
}

func numbers(eps []domain.Episode) []string {
	out := make([]string, len(eps))
	for i, e := range eps {
		out[i] = e.Number
	}
	return out
}
