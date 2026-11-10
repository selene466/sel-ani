package player

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"sel-ani/internal/domain"
	"sel-ani/internal/infra/fetch"
)

// Downloader saves episodes with yt-dlp, or ffmpeg when a separate audio
// track has to be muxed in (or yt-dlp is missing).
type Downloader struct {
	Dir     string
	Extra   []string
	Fetcher fetch.Fetcher // for subtitles

	look func(string) (string, error)
	run  func(context.Context, []string) error
}

func (d *Downloader) Interactive() bool          { return false }
func (d *Downloader) Wait(context.Context) error { return nil }
func (d *Downloader) Stop() error                { return nil }

func (d *Downloader) Play(ctx context.Context, m domain.Media) error {
	look, run := d.look, d.run
	if look == nil {
		look = exec.LookPath
	}
	if run == nil {
		run = runAttached
	}
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return err
	}
	name := Sanitize(m.Title)
	if m.Stream.Subtitle != "" && d.Fetcher != nil {
		b, err := d.Fetcher.Fetch(fetch.WithOrigin(ctx, m.Stream.Origin), m.Stream.Subtitle, m.Stream.Referer)
		if err == nil {
			err = os.WriteFile(filepath.Join(d.Dir, name+".vtt"), b, 0o644)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "subtitle download failed:", err)
		}
	}
	_, ytErr := look("yt-dlp")
	argv := d.argv(m, ytErr == nil)
	if _, err := look(argv[0]); err != nil {
		return fmt.Errorf("%s not found: install yt-dlp or ffmpeg", argv[0])
	}
	return run(ctx, argv)
}

func (d *Downloader) argv(m domain.Media, haveYtdlp bool) []string {
	out := filepath.Join(d.Dir, Sanitize(m.Title)+".mp4")
	ref := m.Stream.Referer
	var a []string
	if m.Stream.Audio == "" && haveYtdlp {
		a = []string{"yt-dlp", "--referer", ref}
		if o := m.Stream.Origin; o != "" {
			a = append(a, "--add-headers", "Origin:"+o)
		}
		a = append(a, m.Video, "--no-skip-unavailable-fragments", "--fragment-retries", "infinite", "-N", "16", "-o", out)
	} else {
		// ffmpeg's -headers needs CRLF-terminated lines
		in := []string{"-extension_picky", "0", "-referer", ref}
		if o := m.Stream.Origin; o != "" {
			in = append(in, "-headers", "Origin: "+o+"\r\n")
		}
		a = append([]string{"ffmpeg"}, in...)
		a = append(a, "-loglevel", "error", "-stats", "-i", m.Video)
		if m.Stream.Audio != "" {
			a = append(a, in...)
			a = append(a, "-i", m.Stream.Audio)
		}
		a = append(a, "-c", "copy")
		a = append(a, d.Extra...)
		return append(a, out)
	}
	return append(a, d.Extra...)
}

func runAttached(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
