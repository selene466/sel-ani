package config

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ParseTOML parses the subset of TOML used by the configuration file into a
// flat map keyed by dotted path, e.g. "theme.name". Supported syntax:
//
//	# comments
//	[table] and [dotted.table] headers
//	key = value and dotted.key = value (bare keys)
//	"basic strings" with escapes, 'literal strings'
//	integers, booleans
//	arrays of these values, which may span several lines
//
// Values are returned as string, int64, bool or []any. Multi-line strings,
// floats, dates, quoted keys, inline tables and arrays of tables are
// rejected with an error rather than misread.
func ParseTOML(r io.Reader) (map[string]any, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(b) {
		return nil, errors.New("file is not valid UTF-8")
	}
	p := &tomlParser{src: string(b), out: map[string]any{}, tables: map[string]bool{}}
	if err := p.parse(); err != nil {
		return nil, fmt.Errorf("line %d: %w", strings.Count(p.src[:p.pos], "\n")+1, err)
	}
	return p.out, nil
}

type tomlParser struct {
	src    string
	pos    int
	prefix string // current table followed by "."
	out    map[string]any
	tables map[string]bool
}

func (p *tomlParser) eof() bool { return p.pos >= len(p.src) }

// peek returns the current byte, or 0 at the end of the input.
func (p *tomlParser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

func (p *tomlParser) parse() error {
	for !p.eof() {
		p.skipBlank()
		var err error
		switch c := p.peek(); {
		case c == '[':
			err = p.tableHeader()
		case c != 0 && c != '#' && c != '\n' && c != '\r':
			err = p.keyValue()
		}
		if err == nil {
			err = p.lineEnd()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *tomlParser) skipBlank() {
	for c := p.peek(); c == ' ' || c == '\t'; c = p.peek() {
		p.pos++
	}
}

func (p *tomlParser) skipComment() {
	if p.peek() == '#' {
		for !p.eof() && p.peek() != '\n' {
			p.pos++
		}
	}
}

// lineEnd consumes trailing blanks, an optional comment and the newline.
func (p *tomlParser) lineEnd() error {
	p.skipBlank()
	p.skipComment()
	switch {
	case p.eof():
	case p.peek() == '\n':
		p.pos++
	case strings.HasPrefix(p.src[p.pos:], "\r\n"):
		p.pos += 2
	default:
		return fmt.Errorf("unexpected %q, expected end of line", p.peek())
	}
	return nil
}

func (p *tomlParser) tableHeader() error {
	p.pos++ // [
	if p.peek() == '[' {
		return errors.New("arrays of tables are not supported")
	}
	key, err := p.key()
	if err != nil {
		return err
	}
	if p.peek() != ']' {
		return errors.New("expected ']' after table name")
	}
	p.pos++
	name := strings.Join(key, ".")
	if p.tables[name] {
		return fmt.Errorf("table [%s] defined twice", name)
	}
	p.tables[name] = true
	p.prefix = name + "."
	return nil
}

func isBareKeyChar(c byte) bool {
	return c == '_' || c == '-' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// key parses a bare or dotted key and the blanks around it.
func (p *tomlParser) key() ([]string, error) {
	var parts []string
	for {
		p.skipBlank()
		start := p.pos
		for isBareKeyChar(p.peek()) {
			p.pos++
		}
		if start == p.pos {
			if c := p.peek(); c == '"' || c == '\'' {
				return nil, errors.New("quoted keys are not supported")
			}
			return nil, errors.New("expected a key")
		}
		parts = append(parts, p.src[start:p.pos])
		p.skipBlank()
		if p.peek() != '.' {
			return parts, nil
		}
		p.pos++
	}
}

func (p *tomlParser) keyValue() error {
	key, err := p.key()
	if err != nil {
		return err
	}
	if p.peek() != '=' {
		return errors.New("expected '=' after key")
	}
	p.pos++
	p.skipBlank()
	v, err := p.value()
	if err != nil {
		return err
	}
	full := p.prefix + strings.Join(key, ".")
	if _, dup := p.out[full]; dup {
		return fmt.Errorf("key %q defined twice", full)
	}
	p.out[full] = v
	return nil
}

func (p *tomlParser) value() (any, error) {
	rest := p.src[p.pos:]
	switch c := p.peek(); c {
	case '"':
		if strings.HasPrefix(rest, `"""`) {
			return nil, errors.New("multi-line strings are not supported")
		}
		return p.basicString()
	case '\'':
		if strings.HasPrefix(rest, "'''") {
			return nil, errors.New("multi-line strings are not supported")
		}
		return p.literalString()
	case '[':
		return p.array()
	case '{':
		return nil, errors.New("inline tables are not supported")
	case 0, '\n', '\r', '#', ',', ']':
		return nil, errors.New("missing value")
	}
	return p.scalar()
}

var tomlIntRe = regexp.MustCompile(`^[+-]?(?:0|[1-9](?:_?[0-9])*)$`)

// scalar parses a boolean or a decimal integer.
func (p *tomlParser) scalar() (any, error) {
	start := p.pos
	for c := p.peek(); c != 0 && !strings.ContainsRune(" \t\r\n#,]", rune(c)); c = p.peek() {
		p.pos++
	}
	tok := p.src[start:p.pos]
	switch {
	case tok == "true":
		return true, nil
	case tok == "false":
		return false, nil
	case tomlIntRe.MatchString(tok):
		n, err := strconv.ParseInt(strings.ReplaceAll(tok, "_", ""), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("integer %s out of range", tok)
		}
		return n, nil
	}
	return nil, fmt.Errorf("unsupported value %q (strings must be quoted)", tok)
}

func checkStringByte(c byte) error {
	if c == '\n' || c == '\r' {
		return errors.New("unterminated string")
	}
	if c < 0x20 && c != '\t' || c == 0x7f {
		return fmt.Errorf("control character %q in string", c)
	}
	return nil
}

func (p *tomlParser) basicString() (string, error) {
	p.pos++ // "
	var b strings.Builder
	for {
		if p.eof() {
			return "", errors.New("unterminated string")
		}
		c := p.src[p.pos]
		p.pos++
		switch c {
		case '"':
			return b.String(), nil
		case '\\':
			if err := p.escape(&b); err != nil {
				return "", err
			}
		default:
			if err := checkStringByte(c); err != nil {
				return "", err
			}
			b.WriteByte(c)
		}
	}
}

func (p *tomlParser) escape(b *strings.Builder) error {
	if p.eof() {
		return errors.New("unterminated string")
	}
	e := p.src[p.pos]
	p.pos++
	switch e {
	case 'b':
		b.WriteByte('\b')
	case 't':
		b.WriteByte('\t')
	case 'n':
		b.WriteByte('\n')
	case 'f':
		b.WriteByte('\f')
	case 'r':
		b.WriteByte('\r')
	case '"', '\\':
		b.WriteByte(e)
	case 'u', 'U':
		n := 4
		if e == 'U' {
			n = 8
		}
		if p.pos+n > len(p.src) {
			return fmt.Errorf("invalid escape \\%c", e)
		}
		code, err := strconv.ParseUint(p.src[p.pos:p.pos+n], 16, 32)
		if err != nil || !utf8.ValidRune(rune(code)) {
			return fmt.Errorf("invalid escape \\%c%s", e, p.src[p.pos:p.pos+n])
		}
		p.pos += n
		b.WriteRune(rune(code))
	default:
		return fmt.Errorf("invalid escape \\%c (use 'single quotes' for literal backslashes)", e)
	}
	return nil
}

func (p *tomlParser) literalString() (string, error) {
	p.pos++ // '
	start := p.pos
	for {
		if p.eof() {
			return "", errors.New("unterminated string")
		}
		c := p.src[p.pos]
		if c == '\'' {
			s := p.src[start:p.pos]
			p.pos++
			return s, nil
		}
		if err := checkStringByte(c); err != nil {
			return "", err
		}
		p.pos++
	}
}

// skipArraySpace skips blanks, newlines and comments inside an array.
func (p *tomlParser) skipArraySpace() {
	for {
		p.skipBlank()
		p.skipComment()
		switch {
		case p.peek() == '\n':
			p.pos++
		case strings.HasPrefix(p.src[p.pos:], "\r\n"):
			p.pos += 2
		default:
			return
		}
	}
}

func (p *tomlParser) array() ([]any, error) {
	p.pos++ // [
	out := []any{}
	for {
		p.skipArraySpace()
		if p.eof() {
			return nil, errors.New("unterminated array")
		}
		if p.peek() == ']' {
			p.pos++
			return out, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skipArraySpace()
		switch p.peek() {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return out, nil
		case 0:
			return nil, errors.New("unterminated array")
		default:
			return nil, fmt.Errorf("unexpected %q in array, expected ',' or ']'", p.peek())
		}
	}
}
