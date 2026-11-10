// Package kitty draws images with the kitty graphics protocol, in a way that
// survives a full-screen UI: the image is transmitted once with a virtual
// placement, and the UI prints Unicode placeholder cells where it should
// show, so it lays out like text.
package kitty

import (
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/charmbracelet/x/ansi/kitty"
)

const chunk = 4096 // base64 bytes per escape, the protocol limit

// ID returns a stable image id below 2^24 for key, so it fits the 24-bit
// foreground color that names the image in placeholder cells.
func ID(key string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(key))
	return h.Sum32()&0xffffff | 1
}

// Transmit returns the escape sequences that store a PNG as image id with a
// virtual placement of cols x rows cells. It draws nothing by itself.
func Transmit(id uint32, png []byte, cols, rows int) string {
	enc := base64.StdEncoding.EncodeToString(png)
	var b strings.Builder
	for i := 0; i < len(enc); i += chunk {
		end := min(i+chunk, len(enc))
		more := 1
		if end == len(enc) {
			more = 0
		}
		if i == 0 {
			fmt.Fprintf(&b, "\x1b_Ga=T,U=1,f=100,q=2,i=%d,c=%d,r=%d,m=%d;", id, cols, rows, more)
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d;", more)
		}
		b.WriteString(enc[i:end])
		b.WriteString("\x1b\\")
	}
	return b.String()
}

// Delete returns the sequence that frees image id.
func Delete(id uint32) string {
	return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
}

// Placeholder returns rows lines of cols placeholder cells showing image id.
func Placeholder(id uint32, cols, rows int) string {
	fg := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", id>>16&0xff, id>>8&0xff, id&0xff)
	lines := make([]string, rows)
	for r := range rows {
		var b strings.Builder
		b.WriteString(fg)
		for c := range cols {
			b.WriteRune('\U0010EEEE')
			b.WriteRune(kitty.Diacritic(r))
			b.WriteRune(kitty.Diacritic(c))
		}
		b.WriteString("\x1b[39m")
		lines[r] = b.String()
	}
	return strings.Join(lines, "\n")
}

// cellAspect is the height of a terminal cell over its width.
const cellAspect = 2.0

// Fit returns the cell size of an image of w x h pixels that fits in
// maxCols x maxRows cells, keeping its aspect ratio. 0, 0 when w or h is
// unknown.
func Fit(w, h, maxCols, maxRows int) (cols, rows int) {
	if w <= 0 || h <= 0 || maxCols <= 0 || maxRows <= 0 {
		return 0, 0
	}
	// cells needed per image row at full width: cols * cellAspect * h / w
	rows = maxRows
	cols = int(float64(rows)*cellAspect*float64(w)/float64(h) + 0.5)
	if cols > maxCols {
		cols = maxCols
		rows = int(float64(cols)*float64(h)/(cellAspect*float64(w)) + 0.5)
	}
	return max(cols, 1), max(rows, 1)
}

// Supported reports whether the terminal described by getenv speaks the
// kitty graphics protocol with Unicode placeholders: kitty and Ghostty do.
// tmux and screen swallow the escapes, so they never count.
func Supported(getenv func(string) string) bool {
	if getenv("TMUX") != "" || strings.HasPrefix(getenv("TERM"), "screen") {
		return false
	}
	switch {
	case getenv("KITTY_WINDOW_ID") != "", getenv("TERM") == "xterm-kitty", getenv("TERM") == "xterm-ghostty":
		return true
	}
	return getenv("TERM_PROGRAM") == "ghostty"
}
