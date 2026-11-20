package app

import (
	"context"
	"testing"

	"sel-ani/internal/domain"
	"sel-ani/internal/infra/hls"
)

const master = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360
360/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1400000,RESOLUTION=854x480
480/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2800000,RESOLUTION=1280x720
720/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
1080/index.m3u8
`

// playlist -> parser -> App.Play -> the url handed to the player
func TestPlayPicksConfiguredQuality(t *testing.T) {
	links, _, err := hls.ParseMaster([]byte(master), "https://cdn.example/a/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		"360p": "https://cdn.example/a/360/index.m3u8",
		"480p": "https://cdn.example/a/480/index.m3u8",
		"720p": "https://cdn.example/a/720/index.m3u8",
		"best": "https://cdn.example/a/1080/index.m3u8",
	} {
		a, fp, _, pl := newApp()
		a.Provider = streamProvider{fp, domain.Stream{Links: links}}
		a.Quality = q
		if err := a.Play(context.Background(), domain.Anime{ID: "x", Title: "T"}, domain.Episode{Number: "1"}); err != nil {
			t.Fatal(err)
		}
		if got := pl.played[0].Video; got != want {
			t.Errorf("%s: player got %s, want %s", q, got, want)
		}
	}
}

type streamProvider struct {
	*fakeProvider
	st domain.Stream
}

func (s streamProvider) Stream(context.Context, domain.Episode, domain.Mode) (domain.Stream, error) {
	return s.st, nil
}

func TestPlayReportsQualityFallback(t *testing.T) {
	a, fp, _, pl := newApp()
	a.Provider = streamProvider{fp, domain.Stream{Links: []domain.Link{{Quality: "1080p", URL: "u1080"}, {Quality: "480p", URL: "u480"}}}}
	a.Quality = "720p"
	var note string
	ctx := WithWarn(context.Background(), func(s string) { note = s })
	if err := a.Play(ctx, domain.Anime{ID: "x"}, domain.Episode{Number: "1"}); err != nil {
		t.Fatal(err)
	}
	if pl.played[0].Video != "u480" || note != "720p not offered, using 480p" {
		t.Errorf("%q %q", pl.played[0].Video, note)
	}
}
