package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"sel-ani/internal/app"
	"sel-ani/internal/config"
	"sel-ani/internal/infra/image"
)

const menuLimit = 20

var lineClean = strings.NewReplacer("\n", " ", "\r", " ", "\x00", "")

// item is one menu line. icon is a cover file, rofi only.
type item struct {
	text string
	icon string
}

// menu runs dmenu or rofi.
type menu struct {
	rofi bool
}

// pick shows items under the prompt and returns the index of the choice.
// ok is false when the menu was cancelled.
func (m menu) pick(ctx context.Context, prompt string, items []item) (idx int, ok bool, err error) {
	argv := []string{"dmenu", "-l", fmt.Sprint(menuLimit), "-p", prompt}
	if m.rofi {
		argv = []string{"rofi", "-dmenu", "-show-icons", "-format", "i", "-l", fmt.Sprint(menuLimit), "-p", prompt}
	}
	var in strings.Builder
	byText := map[string]int{}
	for i, it := range items {
		line := lineClean.Replace(it.text)
		if _, dup := byText[line]; !dup {
			byText[line] = i
		}
		if it.icon != "" {
			line += "\x00icon\x1f" + it.icon
		}
		in.WriteString(line + "\n")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(in.String())
	cmd.Stderr = os.Stderr
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) { // escape exits 1
			return 0, false, nil
		}
		return 0, false, err
	}
	sel := strings.TrimRight(out.String(), "\r\n")
	if sel == "" {
		return 0, false, nil
	}
	if m.rofi { // -format i prints the index
		var i int
		if _, err := fmt.Sscan(sel, &i); err != nil || i < 0 || i >= len(items) {
			return 0, false, fmt.Errorf("unknown selection %q", sel)
		}
		return i, true, nil
	}
	i, found := byText[sel]
	if !found {
		return 0, false, fmt.Errorf("unknown selection %q", sel)
	}
	return i, true, nil
}

// ask prompts for free text. ok is false when cancelled or empty.
func (m menu) ask(ctx context.Context, prompt string) (string, bool, error) {
	argv := []string{"dmenu", "-p", prompt}
	if m.rofi {
		argv = []string{"rofi", "-dmenu", "-p", prompt, "-l", "0"}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader("")
	cmd.Stderr = os.Stderr
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", false, nil
		}
		return "", false, err
	}
	q := strings.TrimSpace(out.String())
	return q, q != "", nil
}

// shorten cuts a title to 13 characters plus "...".
func shorten(s string) string {
	r := []rune(s)
	if len(r) <= 13 {
		return s
	}
	return string(r[:13]) + "..."
}

// covers downloads the cover of each entry to the cache and returns the paths.
func covers(ctx context.Context, imgs *image.Source, entries []app.Entry) []string {
	icons := make([]string, len(entries))
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, config.AppName, "rofi")
	if os.MkdirAll(dir, 0o700) != nil {
		return icons
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			b, err := imgs.PNG(ctx, e.Anime.CoverURL)
			if err != nil {
				return
			}
			sum := sha1.Sum([]byte(e.Anime.CoverURL))
			path := filepath.Join(dir, hex.EncodeToString(sum[:])+".png")
			if os.WriteFile(path, b, 0o600) == nil {
				icons[i] = path
			}
		}()
	}
	wg.Wait()
	return icons
}

// paged shows n entries, menuLimit lines at most. Longer lists get a Next row
// first and a Prev row second, then the page. build returns the items of
// entries lo..hi. start is an entry index whose page opens first.
func (m menu) paged(ctx context.Context, prompt string, n, start int, build func(lo, hi int) []item) (int, bool, error) {
	size := menuLimit
	if n > menuLimit {
		size = menuLimit - 2
	}
	page := start / size
	for {
		lo := page * size
		hi := min(lo+size, n)
		var items []item
		hasNext, hasPrev := hi < n, page > 0
		if hasNext {
			items = append(items, item{text: ">> Next"})
		}
		if hasPrev {
			items = append(items, item{text: "<< Prev"})
		}
		nav := len(items)
		items = append(items, build(lo, hi)...)
		i, ok, err := m.pick(ctx, prompt, items)
		if err != nil || !ok {
			return 0, false, err
		}
		switch {
		case i >= nav:
			return lo + i - nav, true, nil
		case hasNext && i == 0:
			page++
		default:
			page--
		}
	}
}

// runMenu is the dmenu or rofi flow: Latest, My List or Search, then title,
// then episode, then the player. The episode menu comes back after each
// play; escape quits.
func runMenu(ctx context.Context, a *app.App, imgs *image.Source, rofi bool) error {
	m := menu{rofi: rofi}
	i, ok, err := m.pick(ctx, "sel-ani", []item{{text: "Latest"}, {text: "My List"}, {text: "Search"}})
	if err != nil || !ok {
		return err
	}
	var l app.Listing
	prompt := "Latest"
	switch i {
	case 0:
		l, err = a.Latest(ctx)
	case 2:
		q, ok, aerr := m.ask(ctx, "Search")
		if aerr != nil || !ok {
			return aerr
		}
		prompt = "Search"
		l, err = a.Search(ctx, q, "")
	default:
		prompt = "My List"
		l, err = a.MyList(ctx) // most recently watched first
	}
	if err != nil {
		return err
	}
	entries := l.Entries
	if len(entries) == 0 {
		return errors.New(prompt + " is empty")
	}
	// indicator, episode counter, then the title, so a long title runs free
	i, ok, err = m.paged(ctx, prompt, len(entries), 0, func(lo, hi int) []item {
		var icons []string
		if rofi {
			icons = covers(ctx, imgs, entries[lo:hi])
		}
		items := make([]item, hi-lo)
		for j, e := range entries[lo:hi] {
			// [watched/total], "[N] " first when an episode is new
			counter := e.Counter
			if prompt != "My List" {
				w, tot := e.Watched, "?"
				if w == "" {
					w = "0"
				}
				if e.Anime.Episodes > 0 {
					tot = strconv.Itoa(e.Anime.Episodes)
				}
				counter = w + "/" + tot
				if e.New {
					counter = "[N] " + counter
				}
			}
			counter, isNew := strings.CutPrefix(counter, "[N] ")
			counter = "[" + counter + "]"
			if isNew {
				counter = "[N] " + counter
			}
			text := counter + " " + e.Anime.Name()
			if prompt != "My List" {
				marker := " "
				if e.InList {
					marker = "*"
				}
				text = marker + " " + text
			}
			items[j] = item{text: text}
			if icons != nil {
				items[j].icon = icons[j]
			}
		}
		return items
	})
	if err != nil || !ok {
		return err
	}
	an := entries[i].Anime
	start := 0
	for {
		d, err := a.Detail(ctx, an)
		if err != nil {
			return err
		}
		if len(d.Episodes) == 0 {
			return errors.New("no episodes")
		}
		for j, e := range d.Episodes {
			if e.Number == d.Next {
				start = j
			}
		}
		i, ok, err := m.paged(ctx, shorten(an.Name()), len(d.Episodes), start, func(lo, hi int) []item {
			items := make([]item, hi-lo)
			for j, e := range d.Episodes[lo:hi] {
				mark := "[ ]"
				if d.Watched[e.Number] {
					mark = "[x]"
				}
				next := " "
				if e.Number == d.Next {
					next = ">"
				}
				items[j] = item{text: fmt.Sprintf(" %s %s Episode %s", next, mark, e.Number)}
			}
			return items
		})
		if err != nil || !ok {
			return err
		}
		start = i
		if err := a.Play(ctx, an, d.Episodes[i]); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintln(os.Stderr, "sel-ani:", err)
		}
	}
}
