package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseTOML(t *testing.T) {
	in := `
# comment
engine = "podman"   # trailing comment
compose = ["podman", "compose"]
quoted = "hello # not a comment"
literal = 'C:\stacks\$HOME'
escaped = "a\nb\t\"c\" \u00e9"
empty = ""
hex = "#0e1419"
count = 1_000
negative = -3
enabled = true
dirs = [
  "~/projects", # first
  '/srv/stacks',
]
nested = [[1, 2], []]
dotted.key = "x"

[theme]
name = "dracula"

[ theme.extra ]
accent = "#123456"
`
	got, err := ParseTOML(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"engine":             "podman",
		"compose":            []any{"podman", "compose"},
		"quoted":             "hello # not a comment",
		"literal":            `C:\stacks\$HOME`,
		"escaped":            "a\nb\t\"c\" é",
		"empty":              "",
		"hex":                "#0e1419",
		"count":              int64(1000),
		"negative":           int64(-3),
		"enabled":            true,
		"dirs":               []any{"~/projects", "/srv/stacks"},
		"nested":             []any{[]any{int64(1), int64(2)}, []any{}},
		"dotted.key":         "x",
		"theme.name":         "dracula",
		"theme.extra.accent": "#123456",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

func TestParseTOMLCRLF(t *testing.T) {
	got, err := ParseTOML(strings.NewReader("a = 1\r\nb = [\r\n  \"x\",\r\n]\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]any{"a": int64(1), "b": []any{"x"}}) {
		t.Errorf("got %#v", got)
	}
}

func TestParseTOMLErrors(t *testing.T) {
	for _, in := range []string{
		"novalue",
		"key =",
		"key = value",
		`key = "unterminated`,
		"key = 'unterminated",
		`key = "bad \q escape"`,
		`key = "a" "b"`,
		"key = [1, 2",
		"key = [1 2]",
		"key = [1,,2]",
		"key = 01",
		"key = 1.5",
		"key = 0x10",
		"key = 1979-05-27",
		"key = {a = 1}",
		`key = """multi"""`,
		`"quoted" = 1`,
		"a = 1\na = 2",
		"[t]\n[t]",
		"[[tables]]",
		"[t",
		"key = \"a\x01b\"",
		"key = \"\xff\"",
	} {
		if _, err := ParseTOML(strings.NewReader(in)); err == nil {
			t.Errorf("ParseTOML(%q) expected error", in)
		}
	}
}

func TestParseTOMLErrorLine(t *testing.T) {
	_, err := ParseTOML(strings.NewReader("a = 1\n\n# c\nb = nope\n"))
	if err == nil || !strings.HasPrefix(err.Error(), "line 4:") {
		t.Errorf("err = %v, want line 4", err)
	}
}
