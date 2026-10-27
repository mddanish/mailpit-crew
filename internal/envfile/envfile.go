// Package envfile reads and writes KEY=value environment files (systemd
// EnvironmentFile / docker compose env_file style) while preserving comments,
// blank lines and the order of untouched lines.
package envfile

import (
	"fmt"
	"regexp"
	"strings"
)

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type line struct {
	raw string
	key string // empty for comments, blank and invalid lines
}

// File is a parsed environment file.
type File struct {
	lines []line
}

// InvalidLine describes a line that is neither a comment nor KEY=value.
type InvalidLine struct {
	Number int
	Text   string
}

// Parse parses an environment file. It never fails: lines it does not
// understand are kept as-is and reported by Invalid().
func Parse(data []byte) *File {
	f := &File{}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if text == "" {
		return f
	}
	parts := strings.Split(text, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for _, raw := range parts {
		f.lines = append(f.lines, line{raw: raw, key: parseKey(raw)})
	}
	return f
}

func parseKey(raw string) string {
	t := strings.TrimSpace(raw)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
		return ""
	}
	i := strings.Index(t, "=")
	if i < 1 {
		return ""
	}
	k := strings.TrimSpace(t[:i])
	if !keyRe.MatchString(k) {
		return ""
	}
	return k
}

// Invalid returns lines that are not comments, blank or valid assignments.
func (f *File) Invalid() []InvalidLine {
	var out []InvalidLine
	for i, l := range f.lines {
		if l.key != "" {
			continue
		}
		t := strings.TrimSpace(l.raw)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
			continue
		}
		out = append(out, InvalidLine{Number: i + 1, Text: l.raw})
	}
	return out
}

// Get returns the value of key. The last assignment wins, like systemd.
func (f *File) Get(key string) (string, bool) {
	for i := len(f.lines) - 1; i >= 0; i-- {
		if f.lines[i].key == key {
			return parseValue(f.lines[i].raw), true
		}
	}
	return "", false
}

// Keys returns all assigned keys in file order, without duplicates.
func (f *File) Keys() []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range f.lines {
		if l.key != "" && !seen[l.key] {
			seen[l.key] = true
			out = append(out, l.key)
		}
	}
	return out
}

// Map returns all key/value pairs.
func (f *File) Map() map[string]string {
	m := map[string]string{}
	for _, k := range f.Keys() {
		m[k], _ = f.Get(k)
	}
	return m
}

// Set assigns value to key. An existing line is replaced in place (other
// duplicates are removed); a new key is appended.
func (f *File) Set(key, value string) error {
	if !keyRe.MatchString(key) {
		return fmt.Errorf("invalid key %q", key)
	}
	if strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("value for %s must be a single line", key)
	}
	newRaw := key + "=" + formatValue(value)
	last := -1
	for i := len(f.lines) - 1; i >= 0; i-- {
		if f.lines[i].key == key {
			last = i
			break
		}
	}
	if last == -1 {
		f.lines = append(f.lines, line{raw: newRaw, key: key})
		return nil
	}
	if cur, _ := f.Get(key); cur != value {
		f.lines[last].raw = newRaw
	}
	out := f.lines[:0]
	for i, l := range f.lines {
		if l.key == key && i != last {
			continue
		}
		out = append(out, l)
	}
	f.lines = out
	return nil
}

// Delete removes every assignment of key.
func (f *File) Delete(key string) {
	out := f.lines[:0]
	for _, l := range f.lines {
		if l.key != key {
			out = append(out, l)
		}
	}
	f.lines = out
}

// Bytes renders the file. Untouched lines are returned byte for byte.
func (f *File) Bytes() []byte {
	var b strings.Builder
	for _, l := range f.lines {
		b.WriteString(l.raw)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func parseValue(raw string) string {
	t := strings.TrimSpace(raw)
	i := strings.Index(t, "=")
	v := strings.TrimSpace(t[i+1:])
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return unescape(v[1 : len(v)-1])
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1]
	}
	return v
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

var bareRe = regexp.MustCompile(`^[A-Za-z0-9_./:@,+\-=\[\]%]*$`)

func formatValue(v string) string {
	if bareRe.MatchString(v) {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(v) + `"`
}
