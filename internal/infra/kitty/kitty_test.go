package kitty

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTransmitChunks(t *testing.T) {
	png := make([]byte, 10000)
	s := Transmit(0x010203, png, 20, 10)
	if !strings.HasPrefix(s, "\x1b_Ga=T,U=1,f=100,q=2,i=66051,c=20,r=10,m=1;") {
		t.Fatalf("first chunk: %q", s[:60])
	}
	n := strings.Count(s, "\x1b_G")
	if n != 4 { // 10000 bytes = 13336 base64 chars = 4 chunks of <= 4096
		t.Errorf("%d chunks", n)
	}
	if !strings.Contains(s, "\x1b_Gm=0;") {
		t.Error("no final chunk marker")
	}
	if strings.Count(s, "m=1")+strings.Count(s, "m=0") != n {
		t.Error("every chunk needs m")
	}
}

func TestPlaceholderWidth(t *testing.T) {
	out := Placeholder(0xabcdef, 7, 3)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 7 {
			t.Errorf("line %d width %d", i, w)
		}
		if !strings.Contains(l, "\x1b[38;2;171;205;239m") {
			t.Errorf("line %d: id color missing: %q", i, l)
		}
	}
}

func TestFit(t *testing.T) {
	// a 2:3 cover in a wide box is limited by the height
	c, r := Fit(460, 650, 80, 20)
	if r != 20 || c < 26 || c > 30 {
		t.Errorf("%dx%d", c, r)
	}
	// and by the width in a narrow one
	c, r = Fit(460, 650, 10, 40)
	if c != 10 || r < 6 || r > 8 {
		t.Errorf("%dx%d", c, r)
	}
	if c, r = Fit(0, 0, 10, 10); c != 0 || r != 0 {
		t.Error("unknown size")
	}
}

func TestID(t *testing.T) {
	if ID("a") == ID("b") || ID("a") == 0 || ID("a") > 0xffffff {
		t.Error("bad ids")
	}
}

func TestSupported(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	for name, c := range map[string]struct {
		env  func(string) string
		want bool
	}{
		"kitty":   {env("TERM", "xterm-kitty"), true},
		"window":  {env("KITTY_WINDOW_ID", "3"), true},
		"ghostty": {env("TERM_PROGRAM", "ghostty"), true},
		"tmux":    {env("TERM", "xterm-kitty", "TMUX", "/tmp/x"), false},
		"xterm":   {env("TERM", "xterm-256color"), false},
	} {
		if got := Supported(c.env); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}
