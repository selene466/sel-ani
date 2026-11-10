package app

import (
	"testing"

	"sel-ani/internal/domain"
)

func TestSelectQuality(t *testing.T) {
	links := []domain.Link{
		{Quality: "1080p", URL: "u1080"},
		{Quality: "720p", URL: "u720"},
		{Quality: "360p", URL: "u360"},
	}
	tests := []struct {
		q, want  string
		fallback bool
	}{
		{"best", "u1080", false},
		{"", "u1080", false},
		{"worst", "u360", false},
		{"720p", "u720", false},
		{"720", "u720", false},
		{"480p", "u1080", true},
	}
	for _, tt := range tests {
		got, fb := SelectQuality(links, tt.q)
		if got.URL != tt.want || fb != tt.fallback {
			t.Errorf("SelectQuality(%q) = %q,%v want %q,%v", tt.q, got.URL, fb, tt.want, tt.fallback)
		}
	}
	if got, _ := SelectQuality(nil, "best"); got.URL != "" {
		t.Errorf("empty links should give empty link, got %v", got)
	}
}
