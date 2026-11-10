package domain

import "testing"

func TestName(t *testing.T) {
	for _, c := range []struct{ romaji, english, want string }{
		{"Sousou no Frieren", "Frieren", "Sousou no Frieren"},
		{"", "Frieren", "Frieren"},
		{"Ranma ½ (2024) 3rd Season", "Ranma ½ (2024) Season 3", "Ranma ½ (2024) 3rd Season"},
	} {
		if got := (Anime{AltTitle: c.romaji, Title: c.english}).Name(); got != c.want {
			t.Errorf("%q/%q: got %q", c.romaji, c.english, got)
		}
		if got := (HistoryEntry{AltTitle: c.romaji, Title: c.english}).Name(); got != c.want {
			t.Errorf("history %q/%q: got %q", c.romaji, c.english, got)
		}
	}
}
