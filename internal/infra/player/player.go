// Package player implements domain.Player for external media players,
// downloads (yt-dlp/ffmpeg) and a debug printer.
package player

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sel-ani/internal/domain"
)

// Kind decides how the argv is built.
type Kind int

const (
	Generic Kind = iota
	MPV
	FlatpakMPV
	VLC
	IINA
	Syncplay
	Catt
)

// FlatpakMPV is selected with this pseudo-command.
const flatpakMPV = "flatpak:io.mpv.Mpv"

// KindOf classifies a player command by its base name.
func KindOf(cmd string) Kind {
	if cmd == flatpakMPV {
		return FlatpakMPV
	}
	base := strings.ToLower(filepath.Base(cmd))
	switch {
	case strings.Contains(base, "mpv"):
		return MPV
	case strings.Contains(base, "iina"):
		return IINA
	case strings.Contains(base, "vlc"):
		return VLC
	case strings.Contains(base, "syncplay"):
		return Syncplay
	case base == "catt":
		return Catt
	}
	return Generic
}

// Argv builds the command line for cmd. skip holds ani-skip flags (mpv
// family only); iinaRunning drops --keep-running, which hangs when an IINA
// instance already exists.
func Argv(cmd string, extra, skip []string, m domain.Media, iinaRunning bool) []string {
	st := m.Stream
	opt := func(prefix, v string) []string {
		if v == "" {
			return nil
		}
		return []string{prefix + v}
	}
	mpvOpts := func() []string {
		a := []string{"--referrer=" + st.Referer}
		a = append(a, opt("--http-header-fields=Origin: ", st.Origin)...)
		a = append(a, opt("--audio-file=", st.Audio)...)
		a = append(a, opt("--sub-file=", st.Subtitle)...)
		return append(a, skip...)
	}
	var a []string
	switch KindOf(cmd) {
	case MPV, FlatpakMPV:
		if cmd == flatpakMPV {
			a = []string{"flatpak", "run", "io.mpv.Mpv"}
		} else {
			a = []string{cmd}
		}
		a = append(a, mpvOpts()...)
		a = append(a, extra...)
		a = append(a, "--force-media-title="+m.Title, m.Video)
	case IINA:
		// mpv splits file lists on ':', so urls need escaping
		esc := func(s string) string { return strings.ReplaceAll(s, ":", `\:`) }
		a = []string{cmd, "--mpv-referrer=" + st.Referer}
		a = append(a, opt("--mpv-http-header-fields=Origin: ", st.Origin)...)
		if st.Audio != "" {
			a = append(a, "--mpv-audio-files="+esc(st.Audio))
		}
		if st.Subtitle != "" {
			a = append(a, "--mpv-sub-files="+esc(st.Subtitle))
		}
		a = append(a, extra...)
		a = append(a, "--no-stdin")
		if !iinaRunning {
			a = append(a, "--keep-running")
		}
		a = append(a, "--mpv-force-media-title="+m.Title, m.Video)
	case VLC:
		a = []string{cmd, "--http-referrer=" + st.Referer}
		a = append(a, extra...)
		a = append(a, "--play-and-exit", "--meta-title="+m.Title, m.Video)
		var slaves []string
		for _, s := range []string{st.Audio, st.Subtitle} {
			if s != "" {
				slaves = append(slaves, s)
			}
		}
		if len(slaves) > 0 {
			a = append(a, ":input-slave="+strings.Join(slaves, "#"))
		}
	case Syncplay:
		a = []string{cmd}
		a = append(a, extra...)
		a = append(a, m.Video, "--")
		a = append(a, skip...)
		a = append(a, "--referrer="+st.Referer)
		a = append(a, opt("--http-header-fields=Origin: ", st.Origin)...)
		a = append(a, opt("--audio-file=", st.Audio)...)
		a = append(a, opt("--sub-file=", st.Subtitle)...)
		a = append(a, "--force-media-title="+m.Title)
	case Catt:
		a = append([]string{cmd, "cast"}, extra...)
		a = append(a, m.Video)
	default:
		a = append([]string{cmd}, extra...)
		a = append(a, m.Video)
	}
	return a
}

// Detect picks the first available player for goos.
func Detect(goos string, look func(string) (string, error), exists func(string) bool, home string) (string, error) {
	type cand struct{ name, path string } // path: checked with exists instead of PATH
	var cands []cand
	switch goos {
	case "darwin":
		cands = []cand{{name: "iina"}, {name: "/Applications/IINA.app/Contents/MacOS/iina-cli", path: "/Applications/IINA.app/Contents/MacOS/iina-cli"}, {name: "mpv"}, {name: "vlc"}}
	case "windows":
		cands = []cand{{name: "mpv"}, {name: "vlc"}}
	default:
		cands = []cand{
			{name: "mpv"},
			{name: flatpakMPV, path: filepath.Join(home, ".local/share/flatpak/app/io.mpv.Mpv")},
			{name: flatpakMPV, path: "/var/lib/flatpak/app/io.mpv.Mpv"},
			{name: "vlc"},
		}
	}
	var names []string
	for _, c := range cands {
		names = append(names, c.name)
		if c.path != "" {
			if exists(c.path) {
				return c.name, nil
			}
			continue
		}
		if _, err := look(c.name); err == nil {
			return c.name, nil
		}
	}
	return "", fmt.Errorf("no player found, looked for %s", strings.Join(names, ", "))
}

// Exec runs an external player.
type Exec struct {
	Command   string
	Extra     []string
	NoDetach  bool
	SkipIntro bool   // run ani-skip for mpv/syncplay
	LogTag    string // non-empty: log "title" via logger(1) with this tag
	Stderr    io.Writer

	mu        sync.Mutex // guards Command and Extra, which Configure replaces
	running   *proc
	killGrace time.Duration // SIGTERM -> SIGKILL delay in Stop; 0 = 3s
}

// proc is a detached player. A goroutine reaps it and closes done, so
// waiting can always be abandoned on ctx and never leaves zombies.
type proc struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // valid after done is closed
}

func start(cmd *exec.Cmd) (*proc, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &proc{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

func (p *Exec) Interactive() bool { return true }

// Configure changes the player command and its extra arguments of later plays.
func (p *Exec) Configure(command string, extra []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Command, p.Extra = command, extra
}

func (p *Exec) Play(ctx context.Context, m domain.Media) error {
	p.mu.Lock()
	command, extra := p.Command, p.Extra
	p.mu.Unlock()
	kind := KindOf(command)
	var skip []string
	if p.SkipIntro && m.Stream.MalID != "" && (kind == MPV || kind == FlatpakMPV || kind == Syncplay) {
		out, err := exec.CommandContext(ctx, "ani-skip", "-i", m.Stream.MalID, "-e", m.Episode).Output()
		if err != nil {
			p.warn("ani-skip: " + err.Error())
		} else {
			skip = strings.Fields(string(out))
		}
	}
	iinaRunning := kind == IINA && exec.Command("pgrep", "-f", "IINA").Run() == nil
	argv := Argv(command, extra, skip, m, iinaRunning)
	p.log(m.Title)

	if p.NoDetach {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err := cmd.Run()
		var xe *exec.ExitError
		if errors.As(err, &xe) {
			return &domain.ExitError{Code: xe.ExitCode()}
		}
		return err
	}
	// detached: survives sel-ani exiting, no output on our terminal.
	// Not tied to ctx on purpose: Ctrl-C in the menu must not kill playback.
	cmd := exec.Command(argv[0], argv[1:]...)
	detach(cmd)
	pr, err := start(cmd)
	if err != nil {
		return fmt.Errorf("starting %s: %w", argv[0], err)
	}
	p.running = pr
	return nil
}

// Wait blocks until the detached player exits or ctx is done. Cancelling
// leaves the player running. Its exit status is not an error for us.
func (p *Exec) Wait(ctx context.Context) error {
	if p.running == nil {
		return nil
	}
	select {
	case <-p.running.done:
		p.running = nil
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop ends the detached player: SIGTERM to its process group, SIGKILL if
// it is still alive after the grace period. Always returns in bounded time.
func (p *Exec) Stop() error {
	pr := p.running
	if pr == nil {
		return nil
	}
	p.running = nil
	select {
	case <-pr.done:
		return nil // already exited
	default:
	}
	grace := p.killGrace
	if grace <= 0 {
		grace = 3 * time.Second
	}
	if err := terminate(pr.cmd, false); err != nil {
		return err
	}
	select {
	case <-pr.done:
		return nil
	case <-time.After(grace):
	}
	if err := terminate(pr.cmd, true); err != nil {
		return err
	}
	select {
	case <-pr.done:
		return nil
	case <-time.After(grace):
		return fmt.Errorf("player pid %d did not exit after SIGKILL", pr.cmd.Process.Pid)
	}
}

func (p *Exec) log(title string) {
	if p.LogTag == "" {
		return
	}
	if _, err := exec.LookPath("logger"); err != nil {
		return
	}
	_ = exec.Command("logger", "-t", p.LogTag, title).Run()
}

func (p *Exec) warn(s string) {
	if p.Stderr != nil {
		fmt.Fprintln(p.Stderr, s)
	}
}

// Debug prints what would be played.
type Debug struct{ Out io.Writer }

func (d *Debug) Interactive() bool          { return false }
func (d *Debug) Wait(context.Context) error { return nil }
func (d *Debug) Stop() error                { return nil }
func (d *Debug) Play(_ context.Context, m domain.Media) error {
	var b strings.Builder
	b.WriteString("All links:\n")
	for _, l := range m.Stream.Links {
		fmt.Fprintf(&b, "%s %s\n", l.Quality, l.URL)
	}
	if m.Stream.Origin != "" {
		fmt.Fprintf(&b, "Origin:\n%s\n", m.Stream.Origin)
	}
	fmt.Fprintf(&b, "Selected link:\n%s\nReferer:\n%s\nAudio:\n%s\nSubtitles:\n%s\n", m.Video, m.Stream.Referer, m.Stream.Audio, m.Stream.Subtitle)
	_, err := io.WriteString(d.Out, b.String())
	return err
}

// Sanitize makes a title safe as a file name on all common filesystems.
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, s)
}
