package anidb

import (
	"context"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Frieren: Beyond Journey's End": "frieren beyond journey s end",
		"  Sousou   no FRIEREN ":        "sousou no frieren",
		"葬送のフリーレン":                      "葬送のフリーレン",
		"Re:Zero − Starting Life":       "re zero starting life",
		"Mob Psycho 100 II":             "mob psycho 100 ii",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func loadedStore(t *testing.T) *Titles {
	db := newStore(t)
	srv, _, _ := titleServer(t)
	ti := &Titles{Store: db, URL: srv.URL, now: time.Now}
	if err := ti.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	return ti
}

func TestMatch(t *testing.T) {
	ti := loadedStore(t)
	tests := []struct {
		name  string
		q     Query
		sure  int // expected confident aid, 0 = not tracked
		first int // expected best candidate
	}{
		{"english alone is unique", Query{English: "Frieren: Beyond Journey's End"}, 17617, 17617},
		{"romaji and english agree", Query{Romaji: "Sousou no Frieren 2nd Season", English: "Frieren: Beyond Journey's End Season 2"}, 18290, 18290},
		{"same string twice", Query{Romaji: "One Piece", English: "One Piece"}, 69, 69},
		{"no exact title: not sure, best guess first", Query{English: "Frieren Season Two"}, 0, 18290},

		// romaji before english on our side
		{"romaji beats a conflicting english title", Query{Romaji: "Sousou no Frieren", English: "Frieren: Beyond Journey's End Season 2"}, 17617, 17617},
		{"falls back to english when romaji has no exact hit", Query{Romaji: "Frieren Mahou", English: "Frieren: Beyond Journey's End"}, 17617, 17617},

		// and on AniDB's side: an AniDB romaji title outranks an English one
		{"anidb romaji title beats anidb english title", Query{English: "Tsuki ga Kirei"}, 901, 901},

		// normalizing drops punctuation; the title as written decides
		{"K-On! is not its sequel K-On!!", Query{Romaji: "K-On!", English: "K-ON!"}, 6257, 6257},
		{"K-On!! is not K-On!", Query{Romaji: "K-On!!", English: "K-ON!!"}, 7307, 6257}, // candidates tie, by aid
		// then AniDB's main title beats a synonym
		{"main title beats a synonym", Query{Romaji: "Kanojo"}, 904, 904},
	}
	for _, tt := range tests {
		m, err := ti.Match(tt.q)
		if err != nil {
			t.Fatal(err)
		}
		if m.Sure != tt.sure {
			t.Errorf("%s: sure = %d, want %d (candidates %+v)", tt.name, m.Sure, tt.sure, m.Candidates)
		}
		if len(m.Candidates) == 0 || m.Candidates[0].AID != tt.first {
			t.Errorf("%s: candidates %+v, want %d first", tt.name, m.Candidates, tt.first)
		}
	}
	m, _ := ti.Match(Query{English: "Completely Unrelated Show"})
	if m.Sure != 0 || len(m.Candidates) != 0 {
		t.Errorf("unrelated: %+v", m)
	}
	// two main titles differing only in case: not sure
	m, _ = ti.Match(Query{Romaji: "hanako"})
	if m.Sure != 0 || len(m.Candidates) != 2 {
		t.Errorf("ambiguous romaji: %+v", m)
	}
}

// Candidates are labeled romaji first, then Japanese, then English.
func TestCandidateTitles(t *testing.T) {
	ti := loadedStore(t)
	m, _ := ti.Match(Query{English: "As the Moon, So Beautiful 2"})
	if len(m.Candidates) == 0 || m.Candidates[0].Title != "月がきれい2" {
		t.Errorf("no romaji title: want the Japanese one, got %+v", m.Candidates)
	}
	m, _ = ti.Match(Query{English: "Frieren: Beyond Journey's End"})
	if m.Candidates[0].Title != "Sousou no Frieren" {
		t.Errorf("want the romaji title, got %+v", m.Candidates[0])
	}
}
