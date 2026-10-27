package web

import (
	"regexp"
	"strings"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/catalog"
)

// secretPlaceholder replaces secret values in the full editor. Saving it
// keeps the value that is on disk.
const secretPlaceholder = "__CREW_SECRET_UNCHANGED__"

var yamlSecretRe = regexp.MustCompile(`^(\s*)(password|secret)(\s*:\s*)(.*)$`)

// secretLine reports whether a line holds a secret, and returns the prefix
// up to the value.
func secretLine(kind apply.Kind, line string) (string, bool) {
	switch kind {
	case apply.KindEnv:
		t := strings.TrimLeft(line, " \t")
		i := strings.Index(t, "=")
		if i <= 0 || strings.HasPrefix(t, "#") {
			return "", false
		}
		if k, ok := catalog.Lookup(strings.TrimSpace(t[:i])); ok && k.Sensitive {
			return line[:len(line)-len(t)+i+1], true
		}
	case apply.KindRelay, apply.KindForward:
		if m := yamlSecretRe.FindStringSubmatch(line); m != nil && m[1] == "" {
			return m[1] + m[2] + m[3], true
		}
	}
	return "", false
}

// maskSecrets replaces secret values with the placeholder.
func maskSecrets(kind apply.Kind, data string) string {
	lines := strings.Split(data, "\n")
	for i, l := range lines {
		if p, ok := secretLine(kind, l); ok && strings.TrimSpace(l[len(p):]) != "" {
			lines[i] = p + secretPlaceholder
		}
	}
	return strings.Join(lines, "\n")
}

// unmaskSecrets puts the values from current back where the edited text
// still has the placeholder. A placeholder for a key that has no value on
// disk is removed.
func unmaskSecrets(kind apply.Kind, edited, current string) string {
	orig := map[string]string{}
	for _, l := range strings.Split(current, "\n") {
		if p, ok := secretLine(kind, l); ok {
			orig[strings.TrimSpace(p)] = l
		}
	}
	lines := strings.Split(edited, "\n")
	out := lines[:0]
	for _, l := range lines {
		if p, ok := secretLine(kind, l); ok && strings.TrimSpace(l[len(p):]) == secretPlaceholder {
			if o, found := orig[strings.TrimSpace(p)]; found {
				out = append(out, o)
			}
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// normalize makes editor text a clean file: LF line ends, trailing newline.
func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}
