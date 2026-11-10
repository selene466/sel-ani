package anidb

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"sel-ani/internal/infra/store"
)

// Normalize lowercases s and turns everything but letters and digits into
// single spaces, so "Frieren: Beyond Journey's End" and
// "frieren beyond journey s end" compare equal.
func Normalize(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
		} else {
			space = true
		}
	}
	return b.String()
}

// Candidate is a possible AniDB entry for an anime.
type Candidate struct {
	AID   int
	Title string // display title: romaji, else Japanese, else English
	Score float64
	rank  int // priority of the title that gave Score, lower is better
}

// Match is the result of matching titles against the AniDB index.
type Match struct {
	Sure       int // aid when the highest priority title names exactly one anime
	Candidates []Candidate
}

// Query holds the titles of an anime, tried romaji first, then English.
// Empty fields are skipped.
type Query struct {
	Romaji  string
	English string
}

func (q Query) titles() []string { return []string{q.Romaji, q.English} }

// AniDB title languages in the same priority: romaji (x-jat), English,
// anything else.
const langCount = 3

func langRank(lang string) int {
	switch lang {
	case "x-jat":
		return 0
	case "en":
		return 1
	}
	return 2
}

const (
	maxCandidates = 8
	minScore      = 0.3
	likeLimit     = 2000
)

// Match looks up an anime by its titles.
//
// The highest priority title of q that has any exact hit decides: among
// its hits, those on an AniDB romaji title win over English ones, and if
// that leaves a single anime it is a sure match. Every exact and fuzzy hit
// becomes a candidate, best score first, then best title priority.
func (t *Titles) Match(q Query) (Match, error) {
	var m Match
	sc := scores{}
	decided := false
	for qi, title := range q.titles() {
		norm := Normalize(title)
		if norm == "" {
			continue
		}
		hits, err := t.Store.ExactTitles(norm)
		if err != nil {
			return m, err
		}
		for _, h := range hits {
			sc.note(h.AID, 1, qi*langCount+langRank(h.Lang))
		}
		if !decided && len(hits) > 0 {
			decided = true
			m.Sure = sure(hits, title)
		}

		words := strings.Fields(norm)
		word := longest(words)
		if utf8.RuneCountInString(word) < 3 {
			continue
		}
		rows, err := t.Store.TitlesLike(word, likeLimit)
		if err != nil {
			return m, err
		}
		for _, r := range rows {
			if s := similarity(words, strings.Fields(r.Norm)); s >= minScore {
				sc.note(r.AID, s, qi*langCount+langRank(r.Lang))
			}
		}
	}
	for aid, e := range sc {
		title, err := t.Store.MainTitle(aid)
		if err != nil {
			return m, err
		}
		m.Candidates = append(m.Candidates, Candidate{AID: aid, Title: title, Score: e.score, rank: e.rank})
	}
	slices.SortFunc(m.Candidates, func(a, b Candidate) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		if c := cmp.Compare(a.rank, b.rank); c != 0 {
			return c
		}
		return cmp.Compare(a.AID, b.AID)
	})
	if len(m.Candidates) > maxCandidates {
		m.Candidates = m.Candidates[:maxCandidates]
	}
	return m, nil
}

// sure returns the anime the exact hits of one query title name, using
// only hits in the best AniDB title language; 0 when that is ambiguous.
// Normalizing drops punctuation, so "K-On!" hits both "K-On!" and its
// sequel "K-On!!": ties go to the title as written (ignoring case), then
// to AniDB's main title.
func sure(hits []store.Title, query string) int {
	best := langCount
	for _, h := range hits {
		best = min(best, langRank(h.Lang))
	}
	var top []store.Title
	for _, h := range hits {
		if langRank(h.Lang) == best {
			top = append(top, h)
		}
	}
	query = strings.TrimSpace(query)
	for _, keep := range []func(store.Title) bool{
		func(store.Title) bool { return true },
		func(h store.Title) bool { return strings.EqualFold(strings.TrimSpace(h.Title), query) },
		func(h store.Title) bool { return h.Type == titleMain },
	} {
		var next []store.Title
		aids := map[int]bool{}
		for _, h := range top {
			if keep(h) {
				next = append(next, h)
				aids[h.AID] = true
			}
		}
		switch len(aids) {
		case 0:
			continue // the rule matches nothing: ignore it
		case 1:
			return next[0].AID
		}
		top = next
	}
	return 0
}

// titleMain is the AniDB title type of an anime's main title.
const titleMain = 1

// scores keeps each anime's best score and the rank of the title that
// gave it, so a weak fuzzy romaji hit does not lift an anime that was
// found by an exact English title.
type scores map[int]scored

type scored struct {
	score float64
	rank  int
}

func (sc scores) note(aid int, s float64, rank int) {
	old, seen := sc[aid]
	if !seen || s > old.score || (s == old.score && rank < old.rank) {
		sc[aid] = scored{s, rank}
	}
}

func longest(words []string) string {
	var w string
	for _, x := range words {
		if utf8.RuneCountInString(x) > utf8.RuneCountInString(w) {
			w = x
		}
	}
	return w
}

// similarity weighs how much of the query a title covers over plain
// overlap, so "frieren season two" prefers "sousou no frieren 2nd season"
// to the short title "frieren".
func similarity(query, title []string) float64 {
	qs := map[string]bool{}
	for _, w := range query {
		qs[w] = true
	}
	ts := map[string]bool{}
	for _, w := range title {
		ts[w] = true
	}
	inter := 0
	for w := range qs {
		if ts[w] {
			inter++
		}
	}
	if inter == 0 {
		return 0
	}
	union := len(qs) + len(ts) - inter
	coverage := float64(inter) / float64(len(qs))
	jaccard := float64(inter) / float64(union)
	return 0.7*coverage + 0.3*jaccard
}
