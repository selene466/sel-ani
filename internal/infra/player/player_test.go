package player

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"sel-ani/internal/domain"
)

var media = domain.Media{
	Title:   "Frieren Episode 1",
	AnimeID: "frieren-1",
	Episode: "1",
	Video:   "https://v/1080.m3u8",
	Stream: domain.Stream{
		Links:    []domain.Link{{Quality: "1080p", URL: "https://v/1080.m3u8"}},
		Referer:  "https://ref/",
		Subtitle: "https://s/en.vtt",
		Audio:    "https://a/en.m3u8",
		MalID:    "52991",
	},
}

func TestKindOf(t *testing.T) {
	for cmd, want := range map[string]Kind{
		"mpv":                MPV,
		"/usr/bin/mpv":       MPV,
		"mpv.exe":            MPV,
		"flatpak:io.mpv.Mpv": FlatpakMPV,
		"vlc":                VLC,
		"/Applications/IINA.app/Contents/MacOS/iina-cli": IINA,
		"iina":      IINA,
		"syncplay":  Syncplay,
		"catt":      Catt,
		"celluloid": Generic,
	} {
		if got := KindOf(cmd); got != want {
			t.Errorf("KindOf(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestArgv(t *testing.T) {
	extra := []string{"--fs"}
	skip := []string{"--script-opts=skip"}
	tests := []struct {
		cmd  string
		iina bool
		want []string
	}{
		{"mpv", false, []string{"mpv", "--referrer=https://ref/", "--audio-file=https://a/en.m3u8", "--sub-file=https://s/en.vtt", "--script-opts=skip", "--fs", "--force-media-title=Frieren Episode 1", "https://v/1080.m3u8"}},
		{"flatpak:io.mpv.Mpv", false, []string{"flatpak", "run", "io.mpv.Mpv", "--referrer=https://ref/", "--audio-file=https://a/en.m3u8", "--sub-file=https://s/en.vtt", "--script-opts=skip", "--fs", "--force-media-title=Frieren Episode 1", "https://v/1080.m3u8"}},
		{"vlc", false, []string{"vlc", "--http-referrer=https://ref/", "--fs", "--play-and-exit", "--meta-title=Frieren Episode 1", "https://v/1080.m3u8", ":input-slave=https://a/en.m3u8#https://s/en.vtt"}},
		{"iina", false, []string{"iina", "--mpv-referrer=https://ref/", `--mpv-audio-files=https\://a/en.m3u8`, `--mpv-sub-files=https\://s/en.vtt`, "--fs", "--no-stdin", "--keep-running", "--mpv-force-media-title=Frieren Episode 1", "https://v/1080.m3u8"}},
		{"iina", true, []string{"iina", "--mpv-referrer=https://ref/", `--mpv-audio-files=https\://a/en.m3u8`, `--mpv-sub-files=https\://s/en.vtt`, "--fs", "--no-stdin", "--mpv-force-media-title=Frieren Episode 1", "https://v/1080.m3u8"}},
		{"syncplay", false, []string{"syncplay", "--fs", "https://v/1080.m3u8", "--", "--script-opts=skip", "--referrer=https://ref/", "--audio-file=https://a/en.m3u8", "--sub-file=https://s/en.vtt", "--force-media-title=Frieren Episode 1"}},
		{"catt", false, []string{"catt", "cast", "--fs", "https://v/1080.m3u8"}},
		{"celluloid", false, []string{"celluloid", "--fs", "https://v/1080.m3u8"}},
	}
	for _, tt := range tests {
		got := Argv(tt.cmd, extra, skip, media, tt.iina)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s:\n got %q\nwant %q", tt.cmd, got, tt.want)
		}
	}

	bare := media
	bare.Stream.Audio, bare.Stream.Subtitle = "", ""
	got := Argv("mpv", nil, nil, bare, false)
	want := []string{"mpv", "--referrer=https://ref/", "--force-media-title=Frieren Episode 1", "https://v/1080.m3u8"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bare mpv: %q", got)
	}
	if got := Argv("vlc", nil, nil, bare, false); strings.Contains(strings.Join(got, " "), "input-slave") {
		t.Errorf("vlc without extras: %q", got)
	}
}

func TestDetect(t *testing.T) {
	have := map[string]bool{"vlc": true}
	look := func(s string) (string, error) {
		if have[s] {
			return "/usr/bin/" + s, nil
		}
		return "", errors.New("nope")
	}
	exists := func(string) bool { return false }
	if got, err := Detect("linux", look, exists, "/home/u"); err != nil || got != "vlc" {
		t.Errorf("linux vlc: %q %v", got, err)
	}
	have["mpv"] = true
	if got, _ := Detect("linux", look, exists, "/home/u"); got != "mpv" {
		t.Errorf("linux prefers mpv: %q", got)
	}
	have = map[string]bool{}
	exists = func(p string) bool { return p == "/var/lib/flatpak/app/io.mpv.Mpv" }
	if got, _ := Detect("linux", look, exists, "/home/u"); got != "flatpak:io.mpv.Mpv" {
		t.Errorf("flatpak: %q", got)
	}
	exists = func(p string) bool { return p == "/Applications/IINA.app/Contents/MacOS/iina-cli" }
	if got, _ := Detect("darwin", look, exists, "/home/u"); got != "/Applications/IINA.app/Contents/MacOS/iina-cli" {
		t.Errorf("darwin iina app: %q", got)
	}
	exists = func(string) bool { return false }
	if _, err := Detect("linux", look, exists, "/home/u"); err == nil {
		t.Error("expected no player error")
	}
}

func TestDebugPlayer(t *testing.T) {
	var buf bytes.Buffer
	d := &Debug{Out: &buf}
	if err := d.Play(context.Background(), media); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"1080p https://v/1080.m3u8", "Selected link:\nhttps://v/1080.m3u8", "https://s/en.vtt", "https://a/en.m3u8"} {
		if !strings.Contains(buf.String(), s) {
			t.Errorf("debug output missing %q:\n%s", s, buf.String())
		}
	}
	if d.Interactive() {
		t.Error("debug is not interactive")
	}
}

func TestSanitize(t *testing.T) {
	if got := Sanitize(`a<b>c:d"e/f\g|h?i*j`); got != "a_b_c_d_e_f_g_h_i_j" {
		t.Errorf("got %q", got)
	}
}

func TestDownloaderArgv(t *testing.T) {
	d := &Downloader{Dir: "/dl", Extra: []string{"-x"}}
	m := media
	m.Stream.Audio = ""
	got := d.argv(m, true)
	want := []string{"yt-dlp", "--referer", "https://ref/", "https://v/1080.m3u8", "--no-skip-unavailable-fragments", "--fragment-retries", "infinite", "-N", "16", "-o", "/dl/Frieren Episode 1.mp4", "-x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("yt-dlp:\n got %q\nwant %q", got, want)
	}
	got = d.argv(media, true) // audio track forces ffmpeg
	want = []string{"ffmpeg", "-extension_picky", "0", "-referer", "https://ref/", "-loglevel", "error", "-stats", "-i", "https://v/1080.m3u8", "-extension_picky", "0", "-referer", "https://ref/", "-i", "https://a/en.m3u8", "-c", "copy", "/dl/Frieren Episode 1.mp4", "-x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ffmpeg:\n got %q\nwant %q", got, want)
	}
}

type subFetcher struct{}

func (subFetcher) Fetch(context.Context, string, string) ([]byte, error) {
	return []byte("WEBVTT\n"), nil
}

func TestDownloaderRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	var ran []string
	d := &Downloader{
		Dir:     dir,
		Fetcher: subFetcher{},
		look:    func(s string) (string, error) { return "/bin/" + s, nil },
		run: func(_ context.Context, argv []string) error {
			ran = argv
			return nil
		},
	}
	m := media
	m.Stream.Audio = ""
	if err := d.Play(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if ran[0] != "yt-dlp" {
		t.Errorf("ran %q", ran)
	}
	b, err := os.ReadFile(filepath.Join(dir, "Frieren Episode 1.vtt"))
	if err != nil || string(b) != "WEBVTT\n" {
		t.Errorf("subtitle: %q %v", b, err)
	}
}

func TestExecPlayerForeground(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fakeplayer")
	out := filepath.Join(dir, "args")
	os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+out+"\nexit 4\n"), 0o700)

	p := &Exec{Command: script, NoDetach: true}
	err := p.Play(context.Background(), media)
	var ee *domain.ExitError
	if !errors.As(err, &ee) || ee.Code != 4 {
		t.Fatalf("err = %v", err)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), "https://v/1080.m3u8") {
		t.Errorf("args = %q", b)
	}

	// detached: returns immediately, Wait collects it
	p = &Exec{Command: script}
	if err := p.Play(context.Background(), media); err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(context.Background()); err != nil {
		t.Errorf("wait on detached player should swallow exit codes: %v", err)
	}
	if !p.Interactive() {
		t.Error("exec player is interactive")
	}
}

func TestExecStopWithoutPlayer(t *testing.T) {
	if err := (&Exec{}).Stop(); err != nil {
		t.Error(err)
	}
	if err := (&Exec{}).Wait(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestOriginHeader(t *testing.T) {
	m := media
	m.Stream.Origin = "https://o.example"
	m.Stream.Audio, m.Stream.Subtitle = "", ""
	for cmd, want := range map[string]string{
		"mpv":      "--http-header-fields=Origin: https://o.example",
		"syncplay": "--http-header-fields=Origin: https://o.example",
		"iina":     "--mpv-http-header-fields=Origin: https://o.example",
	} {
		if a := Argv(cmd, nil, nil, m, false); !slices.Contains(a, want) {
			t.Errorf("%s: %q lacks %q", cmd, a, want)
		}
	}
	d := &Downloader{Dir: "/d"}
	if a := d.argv(m, true); !slices.Contains(a, "Origin:https://o.example") {
		t.Errorf("yt-dlp: %q", a)
	}
	m.Stream.Audio = "https://a/en.m3u8"
	a := d.argv(m, true)
	if n := strings.Count(strings.Join(a, " "), "Origin: https://o.example\r\n"); n != 2 {
		t.Errorf("ffmpeg needs the header on both inputs, got %d: %q", n, a)
	}
}

func TestConfigureWhilePlaying(t *testing.T) {
	p := &Exec{Command: "/bin/true", NoDetach: true}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() { defer wg.Done(); p.Play(context.Background(), domain.Media{Video: "v"}) }()
		go func() {
			defer wg.Done()
			p.Configure("/bin/true", []string{"--n" + strconv.Itoa(i)})
		}()
	}
	wg.Wait()
	p.Configure("mpv", []string{"--fs"})
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Command != "mpv" || len(p.Extra) != 1 {
		t.Errorf("%q %v", p.Command, p.Extra)
	}
}
