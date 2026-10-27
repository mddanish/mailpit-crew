// Package verify checks Mailpit's configuration files the way Mailpit reads
// them, and reports problems with line numbers for the editors.
package verify

import (
	"bytes"
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mddanish/mailpit-crew/internal/catalog"
	"github.com/mddanish/mailpit-crew/internal/envfile"
	"github.com/mddanish/mailpit-crew/internal/htpasswd"
	"gopkg.in/yaml.v3"
)

// Issue is one problem. Line is 1-based, 0 when not tied to a line.
type Issue struct {
	Line int    `json:"line"`
	Msg  string `json:"msg"`
	Warn bool   `json:"warn,omitempty"`
}

func (i Issue) String() string {
	s := i.Msg
	if i.Line > 0 {
		s = fmt.Sprintf("line %d: %s", i.Line, s)
	}
	if i.Warn {
		s = "warning: " + s
	}
	return s
}

// Errors returns only the issues that are not warnings.
func Errors(l []Issue) []Issue {
	var out []Issue
	for _, i := range l {
		if !i.Warn {
			out = append(out, i)
		}
	}
	return out
}

func sortIssues(l []Issue) []Issue {
	sort.SliceStable(l, func(a, b int) bool { return l[a].Line < l[b].Line })
	return l
}

var yamlLineRe = regexp.MustCompile(`line (\d+)`)

func yamlError(err error) Issue {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	line := 0
	if m := yamlLineRe.FindStringSubmatch(msg); m != nil {
		line, _ = strconv.Atoi(m[1])
		msg = strings.TrimPrefix(msg, m[0]+": ")
	}
	return Issue{Line: line, Msg: "YAML: " + msg}
}

// EnvOptions are the checks for mailpit.ini that depend on Crew.
type EnvOptions struct {
	Locked  map[string]string      // key -> required value ("" = must not be set)
	Pending func(path string) bool // files that will exist (treated as present)
	Extra   func(map[string]string) []catalog.FieldError
}

// Env checks a Mailpit env file.
func Env(data []byte, o EnvOptions) []Issue {
	f := envfile.Parse(data)
	var out []Issue
	for _, l := range f.Invalid() {
		out = append(out, Issue{Line: l.Number, Msg: "not a KEY=value line: " + l.Text})
	}
	m := f.Map()
	for _, e := range catalog.ValidateEnvWith(m, o.Pending) {
		out = append(out, Issue{Line: f.Line(e.Key), Msg: e.Error()})
	}
	if o.Extra != nil {
		for _, e := range o.Extra(m) {
			out = append(out, Issue{Line: f.Line(e.Key), Msg: e.Error()})
		}
	}
	keys := make([]string, 0, len(o.Locked))
	for k := range o.Locked {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		want := o.Locked[k]
		have, set := m[k]
		switch {
		case want == "" && set:
			out = append(out, Issue{Line: f.Line(k), Msg: k + " is managed by Crew and must not be set"})
		case want != "" && have != want:
			out = append(out, Issue{Line: f.Line(k), Msg: k + " is managed by Crew and must be " + want})
		}
	}
	for _, k := range f.Keys() {
		if _, ok := catalog.Lookup(k); !ok {
			msg := k + " is not a Mailpit setting Crew knows"
			if !strings.HasPrefix(k, "MP_") {
				msg = k + " does not start with MP_, Mailpit ignores it"
			}
			out = append(out, Issue{Line: f.Line(k), Msg: msg, Warn: true})
		}
	}
	return sortIssues(out)
}

// Auth checks a password file. serviceUser must be present when set.
func Auth(data []byte, serviceUser string) []Issue {
	f := htpasswd.Parse(data)
	var out []Issue
	for _, n := range f.Invalid() {
		out = append(out, Issue{Line: n, Msg: "not a user:hash line"})
	}
	if len(f.Entries()) == 0 {
		out = append(out, Issue{Msg: "the file has no accounts; Mailpit needs at least one"})
	}
	if serviceUser != "" {
		if _, ok := f.Get(serviceUser); !ok {
			out = append(out, Issue{Msg: "Crew service account " + serviceUser + " is missing (Crew would lose API access)"})
		}
	}
	return out
}

// Tags checks a tag filters file (MP_TAGS_CONFIG).
func Tags(data []byte) []Issue {
	if len(bytes.TrimSpace(data)) == 0 {
		return []Issue{{Msg: "the file is empty; Mailpit needs a filters list (use filters: [] for none)"}}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return []Issue{yamlError(err)}
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return []Issue{{Line: 1, Msg: "top level must be a mapping with a filters list"}}
	}
	root := doc.Content[0]
	var out []Issue
	var filters *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Value == "filters" {
			filters = v
			continue
		}
		out = append(out, Issue{Line: k.Line, Msg: "unknown key " + k.Value + " (Mailpit only reads filters)", Warn: true})
	}
	switch {
	case filters == nil || (filters.Kind == yaml.ScalarNode && filters.Tag == "!!null"):
		out = append(out, Issue{Line: 1, Msg: "missing filters list (Mailpit fails with \"missing tag: array\")"})
	case filters.Kind != yaml.SequenceNode:
		out = append(out, Issue{Line: filters.Line, Msg: "filters must be a list"})
	default:
		for n, it := range filters.Content {
			if it.Kind != yaml.MappingNode {
				out = append(out, Issue{Line: it.Line, Msg: fmt.Sprintf("filter %d must have match and tags", n+1)})
				continue
			}
			seen := map[string]bool{}
			for i := 0; i+1 < len(it.Content); i += 2 {
				k, v := it.Content[i], it.Content[i+1]
				switch k.Value {
				case "match", "tags":
					seen[k.Value] = true
					if v.Kind != yaml.ScalarNode {
						out = append(out, Issue{Line: v.Line, Msg: k.Value + " must be text (tags are comma separated, not a list)"})
					} else if strings.TrimSpace(v.Value) == "" {
						out = append(out, Issue{Line: v.Line, Msg: k.Value + " must not be empty"})
					}
				default:
					out = append(out, Issue{Line: k.Line, Msg: "unknown key " + k.Value + " in a filter", Warn: true})
				}
			}
			for _, need := range []string{"match", "tags"} {
				if !seen[need] {
					out = append(out, Issue{Line: it.Line, Msg: fmt.Sprintf("filter %d has no %s", n+1, need)})
				}
			}
		}
	}
	return sortIssues(out)
}

// FieldKind is the value type of a relay or forward field.
type FieldKind string

// Field kinds.
const (
	FString FieldKind = "string"
	FBool   FieldKind = "bool"
	FInt    FieldKind = "int"
	FEnum   FieldKind = "enum"
	FRegex  FieldKind = "regex"
	FEmail  FieldKind = "email"
	FEmails FieldKind = "emails"
)

// Field describes one key of a relay or forward file.
type Field struct {
	Key     string
	Kind    FieldKind
	Desc    string
	Example string
	Secret  bool
	Options []string
}

var authOptions = []string{"none", "plain", "login", "cram-md5"}

// RelayFields are the keys of the SMTP relay file (MP_SMTP_RELAY_CONFIG).
var RelayFields = []Field{
	{Key: "host", Kind: FString, Desc: "SMTP server to relay through (required)", Example: "smtp.example.com"},
	{Key: "port", Kind: FInt, Desc: "SMTP port (default 25)", Example: "587"},
	{Key: "starttls", Kind: FBool, Desc: "Use STARTTLS", Example: "true"},
	{Key: "tls", Kind: FBool, Desc: "Use SSL/TLS (not together with starttls)", Example: "false"},
	{Key: "allow-insecure", Kind: FBool, Desc: "Allow insecure authentication and skip TLS checks", Example: "false"},
	{Key: "auth", Kind: FEnum, Options: authOptions, Desc: "Authentication: none, plain, login or cram-md5", Example: "plain"},
	{Key: "username", Kind: FString, Desc: "Username for plain, login and cram-md5", Example: "relay@example.com"},
	{Key: "password", Kind: FString, Secret: true, Desc: "Password for plain and login", Example: "app-password"},
	{Key: "secret", Kind: FString, Secret: true, Desc: "Secret for cram-md5", Example: "secret"},
	{Key: "return-path", Kind: FString, Desc: "Override the bounce address", Example: "bounces@example.com"},
	{Key: "override-from", Kind: FEmail, Desc: "Send all relayed mail from this address", Example: "mailpit@example.com"},
	{Key: "subject-prefix", Kind: FString, Desc: "Text added before the subject (newer Mailpit only)", Example: "[TEST] "},
	{Key: "allowed-recipients", Kind: FRegex, Desc: "Only relay to recipients matching this regular expression", Example: `@example\.com$`},
	{Key: "blocked-recipients", Kind: FRegex, Desc: "Never relay to recipients matching this regular expression", Example: `@(gmail|yahoo)\.com$`},
	{Key: "preserve-message-ids", Kind: FBool, Desc: "Keep the original Message-ID when relaying", Example: "false"},
	{Key: "forward-smtp-errors", Kind: FBool, Desc: "Return relay SMTP errors to the sending client", Example: "false"},
}

// ForwardFields are the keys of the SMTP forward file (MP_SMTP_FORWARD_CONFIG).
var ForwardFields = []Field{
	{Key: "to", Kind: FEmails, Desc: "Forward every message to these addresses, comma separated (required)", Example: "dev@example.com, qa@example.com"},
	{Key: "host", Kind: FString, Desc: "SMTP server to forward through (required)", Example: "smtp.example.com"},
	{Key: "port", Kind: FInt, Desc: "SMTP port (default 25)", Example: "587"},
	{Key: "starttls", Kind: FBool, Desc: "Use STARTTLS", Example: "true"},
	{Key: "tls", Kind: FBool, Desc: "Use SSL/TLS (not together with starttls)", Example: "false"},
	{Key: "allow-insecure", Kind: FBool, Desc: "Allow insecure authentication and skip TLS checks", Example: "false"},
	{Key: "auth", Kind: FEnum, Options: authOptions, Desc: "Authentication: none, plain, login or cram-md5", Example: "plain"},
	{Key: "username", Kind: FString, Desc: "Username for plain, login and cram-md5", Example: "forward@example.com"},
	{Key: "password", Kind: FString, Secret: true, Desc: "Password for plain and login", Example: "app-password"},
	{Key: "secret", Kind: FString, Secret: true, Desc: "Secret for cram-md5", Example: "secret"},
	{Key: "return-path", Kind: FString, Desc: "Override the bounce address", Example: "bounces@example.com"},
	{Key: "override-from", Kind: FEmail, Desc: "Send all forwarded mail from this address", Example: "mailpit@example.com"},
	{Key: "subject-prefix", Kind: FString, Desc: "Text added before the subject (newer Mailpit only)", Example: "[FWD] "},
	{Key: "forward-smtp-errors", Kind: FBool, Desc: "Return forward SMTP errors to the sending client", Example: "false"},
}

// FieldByKey finds a field.
func FieldByKey(fields []Field, key string) (Field, bool) {
	for _, f := range fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// CheckValue validates one value of a field.
func CheckValue(f Field, v string) error {
	switch f.Kind {
	case FBool:
		if _, err := strconv.ParseBool(v); err != nil {
			return fmt.Errorf("%s must be true or false", f.Key)
		}
	case FInt:
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%s must be a port number between 1 and 65535", f.Key)
		}
	case FEnum:
		for _, o := range f.Options {
			if strings.EqualFold(o, v) || (o == "cram-md5" && strings.HasPrefix(strings.ToLower(v), "cram")) || (o == "none" && strings.EqualFold(v, "false")) {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of: %s", f.Key, strings.Join(f.Options, ", "))
	case FRegex:
		if _, err := regexp.Compile(v); err != nil {
			return fmt.Errorf("%s is not a valid regular expression: %v", f.Key, err)
		}
	case FEmail:
		if _, err := mail.ParseAddress(v); err != nil {
			return fmt.Errorf("%s is not a valid email address", f.Key)
		}
	case FEmails:
		for _, a := range strings.Split(v, ",") {
			if _, err := mail.ParseAddress(strings.TrimSpace(a)); err != nil {
				return fmt.Errorf("%s: %q is not a valid email address", f.Key, strings.TrimSpace(a))
			}
		}
	}
	if strings.ContainsAny(v, "\r\n") {
		return fmt.Errorf("%s must be a single line", f.Key)
	}
	return nil
}

// Relay checks an SMTP relay file.
func Relay(data []byte) []Issue { return smtpFile(data, RelayFields, false) }

// Forward checks an SMTP forward file.
func Forward(data []byte) []Issue { return smtpFile(data, ForwardFields, true) }

func smtpFile(data []byte, fields []Field, forward bool) []Issue {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return []Issue{yamlError(err)}
	}
	if len(doc.Content) == 0 {
		return []Issue{{Msg: "host is not set (Mailpit fails to start)"}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return []Issue{{Line: root.Line, Msg: "top level must be a mapping of key: value"}}
	}
	var out []Issue
	vals := map[string]string{}
	lines := map[string]int{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if _, dup := vals[k.Value]; dup {
			out = append(out, Issue{Line: k.Line, Msg: "duplicate key " + k.Value})
		}
		lines[k.Value] = k.Line
		if k.Value == "recipient-allowlist" && !forward {
			out = append(out, Issue{Line: k.Line, Msg: "recipient-allowlist is deprecated, use allowed-recipients", Warn: true})
			vals[k.Value] = v.Value
			continue
		}
		f, ok := FieldByKey(fields, k.Value)
		if !ok {
			out = append(out, Issue{Line: k.Line, Msg: "unknown key " + k.Value + " (Mailpit ignores it)", Warn: true})
			continue
		}
		if v.Kind != yaml.ScalarNode {
			out = append(out, Issue{Line: v.Line, Msg: k.Value + " must be a single value"})
			continue
		}
		vals[k.Value] = v.Value
		if v.Tag == "!!null" || v.Value == "" {
			continue
		}
		if err := CheckValue(f, v.Value); err != nil {
			out = append(out, Issue{Line: v.Line, Msg: err.Error()})
		}
	}
	if strings.TrimSpace(vals["host"]) == "" {
		out = append(out, Issue{Line: lines["host"], Msg: "host is not set (Mailpit fails to start)"})
	}
	if forward && strings.TrimSpace(vals["to"]) == "" {
		out = append(out, Issue{Line: lines["to"], Msg: "to is not set: add the addresses to forward to"})
	}
	auth := strings.ToLower(vals["auth"])
	switch {
	case auth == "plain" || auth == "login":
		if vals["username"] == "" || vals["password"] == "" {
			out = append(out, Issue{Line: lines["auth"], Msg: "auth " + auth + " needs username and password"})
		}
	case strings.HasPrefix(auth, "cram"):
		if vals["username"] == "" || vals["secret"] == "" {
			out = append(out, Issue{Line: lines["auth"], Msg: "auth cram-md5 needs username and secret"})
		}
	}
	if vals["starttls"] == "true" && vals["tls"] == "true" {
		out = append(out, Issue{Line: lines["tls"], Msg: "tls and starttls cannot both be true"})
	}
	return sortIssues(out)
}
