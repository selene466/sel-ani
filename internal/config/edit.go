package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Edit sets one key of a config file's text and returns the new text. Only
// that key's line changes, so comments and unrelated settings stay as they
// are. key is "name" for a top-level key or "table.name". The key's line is
// replaced when it is set; else a commented example line of it in the same
// table is uncommented; else the key is added at the end of its table, which
// is created when missing. value is a string, bool, int or []string.
func Edit(src, key string, value any) (string, error) {
	table, name := "", key
	if i := strings.LastIndex(key, "."); i >= 0 {
		table, name = key[:i], key[i+1:]
	}
	lit, err := literal(value)
	if err != nil {
		return "", err
	}
	newLine := name + " = " + lit
	setRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(name) + `\s*=`)
	exampleRe := regexp.MustCompile(`^\s*#\s*` + regexp.QuoteMeta(name) + `\s*=`)

	lines := strings.Split(src, "\n")
	cur, found := "", table == ""
	start, end := 0, len(lines) // lines of the table, header excluded
	set, example := -1, -1
	for i, l := range lines {
		if h, ok := tableHeader(l); ok {
			if found && cur == table {
				end = i
				break
			}
			cur = h
			if h == table {
				found, start = true, i+1
			}
			continue
		}
		if cur != table || !found {
			continue
		}
		switch {
		case setRe.MatchString(l):
			if set < 0 {
				set = i
			}
		case exampleRe.MatchString(l):
			if example < 0 {
				example = i
			}
		}
	}
	replace := func(i int) string {
		eol := ""
		if strings.HasSuffix(lines[i], "\r") {
			eol = "\r"
		}
		last := i
		if lines[i] != "" && setRe.MatchString(lines[i]) {
			last = valueEnd(lines, i)
		}
		lines = append(lines[:i], append([]string{newLine + eol}, lines[last+1:]...)...)
		return strings.Join(lines, "\n")
	}
	switch {
	case set >= 0:
		return replace(set), nil
	case example >= 0:
		return replace(example), nil
	case !found:
		out := strings.TrimRight(src, "\r\n")
		if out != "" {
			out += "\n\n"
		}
		return out + "[" + table + "]\n" + newLine + "\n", nil
	}
	// after the last non-blank line of the table
	at := end
	for at > start && strings.TrimSpace(lines[at-1]) == "" {
		at--
	}
	lines = append(lines[:at], append([]string{newLine}, lines[at:]...)...)
	return strings.Join(lines, "\n"), nil
}

// valueEnd returns the last line of the value that starts on line i: an
// array may span several lines, and replacing only the first would leave the
// rest behind as garbage.
func valueEnd(lines []string, i int) int {
	depth := 0
	for j := i; j < len(lines); j++ {
		l := lines[j]
		if j == i {
			l = l[strings.Index(l, "=")+1:]
		}
		inStr := false
		for k := 0; k < len(l); k++ {
			switch c := l[k]; {
			case inStr && c == '\\':
				k++
			case c == '"':
				inStr = !inStr
			case inStr:
			case c == '#':
				k = len(l)
			case c == '[':
				depth++
			case c == ']':
				depth--
			}
		}
		if depth <= 0 {
			return j
		}
	}
	return len(lines) - 1
}

// tableHeader returns the name of a "[table]" line.
func tableHeader(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") || strings.HasPrefix(t, "[[") {
		return "", false
	}
	end := strings.Index(t, "]")
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(t[1:end]), true
}

func literal(v any) (string, error) {
	switch v := v.(type) {
	case string:
		return tomlString(v), nil
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case []string:
		parts := make([]string, len(v))
		for i, s := range v {
			parts[i] = tomlString(s)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	}
	return "", fmt.Errorf("cannot write a %T", v)
}

// tomlString quotes s as a TOML basic string, with only the escapes the
// parser in toml.go understands.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\' || r == '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// writeFile replaces path atomically: the content goes to a private temp
// file in the same directory, which is then renamed over path.
func writeFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Save sets one key of the config file at path and returns the configuration
// that results. The new text is checked with the same rules as a load first,
// so a bad value changes nothing; so does a result that does not load, e.g.
// enabling AniDB without a login in credentials.toml.
func Save(path, key string, value any) (Config, error) {
	old, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	text, err := Edit(string(old), key, value)
	if err != nil {
		return Config{}, err
	}
	if _, err := parse(strings.NewReader(text)); err != nil {
		return Config{}, oneLine(err)
	}
	if err := writeFile(path, []byte(text), mode); err != nil {
		return Config{}, err
	}
	cfg, err := Load(path)
	if err != nil {
		if rerr := writeFile(path, old, mode); rerr != nil {
			return Config{}, fmt.Errorf("%w (and the old file could not be restored: %v)", oneLine(err), rerr)
		}
		return Config{}, oneLine(err)
	}
	return cfg, nil
}

// oneLine joins a multi-line error for a status line.
func oneLine(err error) error {
	return errors.New(strings.Join(strings.Fields(strings.ReplaceAll(err.Error(), "\n", "; ")), " "))
}

// AniDBLogin is the content of credentials.toml.
type AniDBLogin struct {
	Client        string
	ClientVersion int
	Username      string
	Password      string
	APIKey        string
}

// ReadLogin returns the login stored in credentials.toml, as far as it can be
// read. A missing or broken file gives the zero value.
func ReadLogin(c Config) AniDBLogin {
	var l AniDBLogin
	path := c.CredentialsPath()
	if path == "" {
		return l
	}
	f, err := os.Open(path)
	if err != nil {
		return l
	}
	defer f.Close()
	values, err := ParseTOML(f)
	if err != nil {
		return l
	}
	l.Client, _ = values["anidb.client"].(string)
	if n, ok := values["anidb.client_version"].(int64); ok {
		l.ClientVersion = int(n)
	}
	l.Username, _ = values["anidb.username"].(string)
	l.Password, _ = values["anidb.password"].(string)
	l.APIKey, _ = values["anidb.api_key"].(string)
	return l
}

// SaveLogin writes the AniDB login to credentials.toml next to the config
// file. Empty fields keep what the file has. The result must be a complete
// login, and the file ends up private (mode 0600) whatever it was before.
// Errors never contain the password or the API key.
func SaveLogin(c Config, in AniDBLogin) error {
	path := c.CredentialsPath()
	if path == "" {
		return errors.New("no config directory for " + CredentialsName)
	}
	text := credentialsTOML
	switch b, err := os.ReadFile(path); {
	case err == nil:
		text = string(b)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	set := func(key string, v any, skip bool) error {
		if skip {
			return nil
		}
		var err error
		text, err = Edit(text, key, v)
		return err
	}
	for _, e := range []struct {
		key  string
		v    any
		skip bool
	}{
		{"anidb.client", in.Client, in.Client == ""},
		{"anidb.client_version", in.ClientVersion, in.ClientVersion == 0},
		{"anidb.username", in.Username, in.Username == ""},
		{"anidb.password", in.Password, in.Password == ""},
		{"anidb.api_key", in.APIKey, in.APIKey == ""},
	} {
		if err := set(e.key, e.v, e.skip); err != nil {
			return err
		}
	}
	values, err := ParseTOML(strings.NewReader(text))
	if err != nil {
		return errors.New("credentials: " + err.Error())
	}
	var check AniDB
	if err := check.applyCredentials(values); err != nil {
		return oneLine(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := writeFile(path, []byte(text), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
