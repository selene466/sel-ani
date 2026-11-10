package hls

import (
	"os"
	"reflect"
	"testing"

	"sel-ani/internal/domain"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const masterURL = "https://hls.dramahot.top/p/scxqicy/55ejtphmn9/e38a115ezb/dnfinmtr3wdbax/master.m3u8"

func TestParseMaster(t *testing.T) {
	links, audio, err := ParseMaster(fixture(t, "master.m3u8"), masterURL)
	if err != nil {
		t.Fatal(err)
	}
	base := "https://hls.dramahot.top/p/scxqicy/55ejtphmn9/e38a115ezb/dnfinmtr3wdbax/"
	want := []domain.Link{
		{Quality: "1080p", URL: base + "1080/index.m3u8"},
		{Quality: "720p", URL: base + "720/index.m3u8"},
		{Quality: "360p", URL: base + "360/index.m3u8"},
	}
	if !reflect.DeepEqual(links, want) || audio != "" {
		t.Errorf("links %+v audio %q", links, audio)
	}

	src := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="jp",DEFAULT=NO,URI="jp.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="en",DEFAULT=YES,URI="audio/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=1280x720,CODECS="avc1"
https://cdn.example/720.m3u8
#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=1,RESOLUTION=1280x720,URI="iframe.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=2,RESOLUTION=1920x1080
1080.m3u8
`
	links, audio, err = ParseMaster([]byte(src), "https://h.example/x/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	want = []domain.Link{
		{Quality: "1080p", URL: "https://h.example/x/1080.m3u8"},
		{Quality: "720p", URL: "https://cdn.example/720.m3u8"},
	}
	if !reflect.DeepEqual(links, want) || audio != "https://h.example/x/audio/en.m3u8" {
		t.Errorf("links %+v audio %q", links, audio)
	}
}
