// Package catalog lists the Mailpit environment settings MailPit-Crew knows,
// with their types and validation rules. Based on Mailpit v1.28.
package catalog

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Kind is the value type of a setting.
type Kind string

// Setting kinds.
const (
	String   Kind = "string"
	Bool     Kind = "bool"
	Int      Kind = "int"
	Duration Kind = "duration" // Mailpit max-age style: 12h, 30d
	File     Kind = "file"     // must exist
	NewFile  Kind = "newfile"  // parent directory must exist
	Addr     Kind = "addr"     // host:port
	Regex    Kind = "regex"
	Enum     Kind = "enum"
	URL      Kind = "url"
)

// Key describes one MP_* setting.
type Key struct {
	Name      string
	Section   string
	Kind      Kind
	Desc      string
	Default   string
	Min, Max  int      // Int only, when Max > Min
	Options   []string // Enum only
	Sensitive bool     // value is a secret: masked in UI, diffs and logs
	PairWith  string   // TLS cert/key pair partner
	AuthFile  string   // label when the value is a managed htpasswd file
}

// Sections in display order.
var Sections = []string{"General", "Web UI & API", "Send API", "SMTP", "SMTP relay", "SMTP forward", "POP3", "Tags", "Integrations"}

// Keys lists all known settings.
var Keys = []Key{
	// General
	{Name: "MP_DATABASE", Section: "General", Kind: NewFile, Desc: "Database file to store persistent data"},
	{Name: "MP_LABEL", Section: "General", Kind: String, Desc: "Optional label to identify this Mailpit instance"},
	{Name: "MP_TENANT_ID", Section: "General", Kind: String, Desc: "Database tenant ID to isolate data"},
	{Name: "MP_MAX_MESSAGES", Section: "General", Kind: Int, Min: 0, Max: 100000000, Default: "500", Desc: "Max number of messages to store (0 = unlimited)"},
	{Name: "MP_MAX_AGE", Section: "General", Kind: Duration, Desc: "Max age of messages in hours or days (eg: 3d)"},
	{Name: "MP_COMPRESSION", Section: "General", Kind: Int, Min: 0, Max: 3, Default: "1", Desc: "Compression level to store raw messages (0-3)"},
	{Name: "MP_DISABLE_WAL", Section: "General", Kind: Bool, Desc: "Disable WAL for local database (allows NFS mounted DBs)"},
	{Name: "MP_USE_MESSAGE_DATES", Section: "General", Kind: Bool, Desc: "Use message dates as the received dates"},
	{Name: "MP_IGNORE_DUPLICATE_IDS", Section: "General", Kind: Bool, Desc: "Ignore duplicate messages (by Message-ID)"},
	{Name: "MP_LOG_FILE", Section: "General", Kind: NewFile, Desc: "Log output to file instead of stdout"},
	{Name: "MP_QUIET", Section: "General", Kind: Bool, Desc: "Quiet logging (errors only)"},
	{Name: "MP_VERBOSE", Section: "General", Kind: Bool, Desc: "Verbose logging"},
	{Name: "MP_DISABLE_VERSION_CHECK", Section: "General", Kind: Bool, Desc: "Disable version update checking"},

	// Web UI & API
	{Name: "MP_UI_BIND_ADDR", Section: "Web UI & API", Kind: Addr, Default: "[::]:8025", Desc: "HTTP bind interface and port for UI and API"},
	{Name: "MP_WEBROOT", Section: "Web UI & API", Kind: String, Default: "/", Desc: "Webroot for web UI and API"},
	{Name: "MP_UI_AUTH_FILE", Section: "Web UI & API", Kind: File, AuthFile: "Web UI & API", Desc: "Password file for web UI and API authentication"},
	{Name: "MP_UI_AUTH", Section: "Web UI & API", Kind: String, Sensitive: true, Desc: "Inline web UI credentials (user:password)"},
	{Name: "MP_UI_TLS_CERT", Section: "Web UI & API", Kind: File, PairWith: "MP_UI_TLS_KEY", Desc: "TLS certificate for web UI (HTTPS)"},
	{Name: "MP_UI_TLS_KEY", Section: "Web UI & API", Kind: File, PairWith: "MP_UI_TLS_CERT", Desc: "TLS key for web UI (HTTPS)"},
	{Name: "MP_API_CORS", Section: "Web UI & API", Kind: String, Desc: "API CORS Access-Control-Allow-Origin header"},
	{Name: "MP_BLOCK_REMOTE_CSS_AND_FONTS", Section: "Web UI & API", Kind: Bool, Desc: "Block access to remote CSS and fonts"},
	{Name: "MP_ALLOW_UNTRUSTED_TLS", Section: "Web UI & API", Kind: Bool, Desc: "Do not verify HTTPS certificates (link checker and screenshots)"},
	{Name: "MP_DISABLE_HTTP_COMPRESSION", Section: "Web UI & API", Kind: Bool, Desc: "Disable HTTP compression support"},
	{Name: "MP_HIDE_DELETE_ALL_BUTTON", Section: "Web UI & API", Kind: Bool, Desc: "Hide the Delete all button in the web UI"},
	{Name: "MP_DISABLE_HTML_CHECK", Section: "Web UI & API", Kind: Bool, Desc: "Disable the HTML check feature"},

	// Send API
	{Name: "MP_SEND_API_AUTH_FILE", Section: "Send API", Kind: File, AuthFile: "Send API", Desc: "Password file for Send API authentication"},
	{Name: "MP_SEND_API_AUTH", Section: "Send API", Kind: String, Sensitive: true, Desc: "Inline Send API credentials (user:password)"},
	{Name: "MP_SEND_API_AUTH_ACCEPT_ANY", Section: "Send API", Kind: Bool, Desc: "Accept any username and password for the Send API"},

	// SMTP
	{Name: "MP_SMTP_BIND_ADDR", Section: "SMTP", Kind: Addr, Default: "[::]:1025", Desc: "SMTP bind interface and port"},
	{Name: "MP_SMTP_AUTH_FILE", Section: "SMTP", Kind: File, AuthFile: "SMTP", Desc: "Password file for SMTP authentication"},
	{Name: "MP_SMTP_AUTH", Section: "SMTP", Kind: String, Sensitive: true, Desc: "Inline SMTP credentials (user:password)"},
	{Name: "MP_SMTP_AUTH_ACCEPT_ANY", Section: "SMTP", Kind: Bool, Desc: "Accept any SMTP username and password, including none"},
	{Name: "MP_SMTP_AUTH_ALLOW_INSECURE", Section: "SMTP", Kind: Bool, Desc: "Allow insecure PLAIN and LOGIN SMTP authentication"},
	{Name: "MP_SMTP_TLS_CERT", Section: "SMTP", Kind: File, PairWith: "MP_SMTP_TLS_KEY", Desc: "TLS certificate for SMTP"},
	{Name: "MP_SMTP_TLS_KEY", Section: "SMTP", Kind: File, PairWith: "MP_SMTP_TLS_CERT", Desc: "TLS key for SMTP"},
	{Name: "MP_SMTP_REQUIRE_STARTTLS", Section: "SMTP", Kind: Bool, Desc: "Require SMTP clients to use STARTTLS"},
	{Name: "MP_SMTP_REQUIRE_TLS", Section: "SMTP", Kind: Bool, Desc: "Require SMTP clients to use SSL/TLS"},
	{Name: "MP_SMTP_STRICT_RFC_HEADERS", Section: "SMTP", Kind: Bool, Desc: "Return SMTP error if headers contain <CR><CR><LF>"},
	{Name: "MP_SMTP_MAX_RECIPIENTS", Section: "SMTP", Kind: Int, Min: 1, Max: 100000, Default: "100", Desc: "Maximum SMTP recipients allowed"},
	{Name: "MP_SMTP_ALLOWED_RECIPIENTS", Section: "SMTP", Kind: Regex, Desc: "Only allow SMTP recipients matching this regular expression"},
	{Name: "MP_SMTP_IGNORE_REJECTED_RECIPIENTS", Section: "SMTP", Kind: Bool, Desc: "Ignore rejected SMTP recipients with 2xx response"},
	{Name: "MP_SMTP_DISABLE_RDNS", Section: "SMTP", Kind: Bool, Desc: "Disable SMTP reverse DNS lookups"},

	// SMTP relay
	{Name: "MP_SMTP_RELAY_CONFIG", Section: "SMTP relay", Kind: File, Desc: "SMTP relay configuration file"},
	{Name: "MP_SMTP_RELAY_ALL", Section: "SMTP relay", Kind: Bool, Desc: "Auto-relay all new messages (caution!)"},
	{Name: "MP_SMTP_RELAY_MATCHING", Section: "SMTP relay", Kind: Regex, Desc: "Auto-relay new messages to matching recipients only"},
	{Name: "MP_SMTP_RELAY_HOST", Section: "SMTP relay", Kind: String, Desc: "Relay SMTP host"},
	{Name: "MP_SMTP_RELAY_PORT", Section: "SMTP relay", Kind: Int, Min: 1, Max: 65535, Desc: "Relay SMTP port"},
	{Name: "MP_SMTP_RELAY_STARTTLS", Section: "SMTP relay", Kind: Bool, Desc: "Use STARTTLS for relay"},
	{Name: "MP_SMTP_RELAY_TLS", Section: "SMTP relay", Kind: Bool, Desc: "Use SSL/TLS for relay"},
	{Name: "MP_SMTP_RELAY_ALLOW_INSECURE", Section: "SMTP relay", Kind: Bool, Desc: "Allow insecure relay TLS"},
	{Name: "MP_SMTP_RELAY_AUTH", Section: "SMTP relay", Kind: Enum, Options: []string{"none", "plain", "login", "cram-md5"}, Desc: "Relay authentication method"},
	{Name: "MP_SMTP_RELAY_USERNAME", Section: "SMTP relay", Kind: String, Desc: "Relay username"},
	{Name: "MP_SMTP_RELAY_PASSWORD", Section: "SMTP relay", Kind: String, Sensitive: true, Desc: "Relay password"},
	{Name: "MP_SMTP_RELAY_SECRET", Section: "SMTP relay", Kind: String, Sensitive: true, Desc: "Relay CRAM-MD5 secret"},
	{Name: "MP_SMTP_RELAY_RETURN_PATH", Section: "SMTP relay", Kind: String, Desc: "Override Return-Path for relayed messages"},
	{Name: "MP_SMTP_RELAY_OVERRIDE_FROM", Section: "SMTP relay", Kind: String, Desc: "Override From address for relayed messages"},
	{Name: "MP_SMTP_RELAY_ALLOWED_RECIPIENTS", Section: "SMTP relay", Kind: Regex, Desc: "Allowed relay recipients (regular expression)"},
	{Name: "MP_SMTP_RELAY_BLOCKED_RECIPIENTS", Section: "SMTP relay", Kind: Regex, Desc: "Blocked relay recipients (regular expression)"},
	{Name: "MP_SMTP_RELAY_PRESERVE_MESSAGE_IDS", Section: "SMTP relay", Kind: Bool, Desc: "Preserve Message-IDs when relaying"},
	{Name: "MP_SMTP_RELAY_FWD_SMTP_ERRORS", Section: "SMTP relay", Kind: Bool, Desc: "Forward relay SMTP errors to the client"},

	// SMTP forward
	{Name: "MP_SMTP_FORWARD_CONFIG", Section: "SMTP forward", Kind: File, Desc: "SMTP forwarding configuration file"},
	{Name: "MP_SMTP_FORWARD_TO", Section: "SMTP forward", Kind: String, Desc: "Forward all messages to these addresses"},
	{Name: "MP_SMTP_FORWARD_HOST", Section: "SMTP forward", Kind: String, Desc: "Forward SMTP host"},
	{Name: "MP_SMTP_FORWARD_PORT", Section: "SMTP forward", Kind: Int, Min: 1, Max: 65535, Desc: "Forward SMTP port"},
	{Name: "MP_SMTP_FORWARD_STARTTLS", Section: "SMTP forward", Kind: Bool, Desc: "Use STARTTLS for forwarding"},
	{Name: "MP_SMTP_FORWARD_TLS", Section: "SMTP forward", Kind: Bool, Desc: "Use SSL/TLS for forwarding"},
	{Name: "MP_SMTP_FORWARD_ALLOW_INSECURE", Section: "SMTP forward", Kind: Bool, Desc: "Allow insecure forward TLS"},
	{Name: "MP_SMTP_FORWARD_AUTH", Section: "SMTP forward", Kind: Enum, Options: []string{"none", "plain", "login", "cram-md5"}, Desc: "Forward authentication method"},
	{Name: "MP_SMTP_FORWARD_USERNAME", Section: "SMTP forward", Kind: String, Desc: "Forward username"},
	{Name: "MP_SMTP_FORWARD_PASSWORD", Section: "SMTP forward", Kind: String, Sensitive: true, Desc: "Forward password"},
	{Name: "MP_SMTP_FORWARD_SECRET", Section: "SMTP forward", Kind: String, Sensitive: true, Desc: "Forward CRAM-MD5 secret"},
	{Name: "MP_SMTP_FORWARD_RETURN_PATH", Section: "SMTP forward", Kind: String, Desc: "Override Return-Path for forwarded messages"},
	{Name: "MP_SMTP_FORWARD_OVERRIDE_FROM", Section: "SMTP forward", Kind: String, Desc: "Override From address for forwarded messages"},
	{Name: "MP_SMTP_FORWARD_FWD_SMTP_ERRORS", Section: "SMTP forward", Kind: Bool, Desc: "Forward SMTP errors to the client"},

	// POP3
	{Name: "MP_POP3_BIND_ADDR", Section: "POP3", Kind: Addr, Default: "[::]:1110", Desc: "POP3 bind interface and port"},
	{Name: "MP_POP3_AUTH_FILE", Section: "POP3", Kind: File, AuthFile: "POP3", Desc: "Password file for POP3 (enables the POP3 server)"},
	{Name: "MP_POP3_AUTH", Section: "POP3", Kind: String, Sensitive: true, Desc: "Inline POP3 credentials (user:password)"},
	{Name: "MP_POP3_TLS_CERT", Section: "POP3", Kind: File, PairWith: "MP_POP3_TLS_KEY", Desc: "TLS certificate for POP3"},
	{Name: "MP_POP3_TLS_KEY", Section: "POP3", Kind: File, PairWith: "MP_POP3_TLS_CERT", Desc: "TLS key for POP3"},

	// Tags
	{Name: "MP_TAGS_CONFIG", Section: "Tags", Kind: File, Desc: "Tag filters YAML file"},
	{Name: "MP_TAG", Section: "Tags", Kind: String, Desc: "Tag new messages matching filters (inline)"},
	{Name: "MP_TAGS_TITLE_CASE", Section: "Tags", Kind: Bool, Desc: "TitleCase tags from plus-addresses and X-Tags"},
	{Name: "MP_TAGS_DISABLE", Section: "Tags", Kind: String, Desc: "Disable auto-tagging, comma separated (plus-addresses,x-tags)"},
	{Name: "MP_TAGS_USERNAME", Section: "Tags", Kind: Bool, Desc: "Auto-tag messages with the authenticated SMTP username"},

	// Integrations
	{Name: "MP_ENABLE_SPAMASSASSIN", Section: "Integrations", Kind: String, Desc: "SpamAssassin integration (host:port or postmark)"},
	{Name: "MP_ENABLE_PROMETHEUS", Section: "Integrations", Kind: String, Desc: "Prometheus metrics: true, false or ip:port"},
	{Name: "MP_WEBHOOK_URL", Section: "Integrations", Kind: URL, Desc: "Webhook URL for new messages"},
	{Name: "MP_WEBHOOK_LIMIT", Section: "Integrations", Kind: Int, Min: 0, Max: 100000, Default: "1", Desc: "Limit webhook requests per second"},
	{Name: "MP_ENABLE_CHAOS", Section: "Integrations", Kind: Bool, Desc: "Enable Chaos functionality (API and web UI)"},
	{Name: "MP_CHAOS_TRIGGERS", Section: "Integrations", Kind: String, Desc: "Chaos triggers for the SMTP server"},
}

var byName = func() map[string]Key {
	m := map[string]Key{}
	for _, k := range Keys {
		m[k.Name] = k
	}
	return m
}()

// Lookup finds a known key.
func Lookup(name string) (Key, bool) {
	k, ok := byName[name]
	return k, ok
}

// BySection returns known keys of one section, in catalog order.
func BySection(section string) []Key {
	var out []Key
	for _, k := range Keys {
		if k.Section == section {
			out = append(out, k)
		}
	}
	return out
}

// IsTrue reports whether Mailpit treats v as enabled.
func IsTrue(v string) bool {
	v = strings.ToLower(v)
	return v == "1" || v == "true" || v == "yes"
}

var durationRe = regexp.MustCompile(`^\d+[hd]$`)

// Validate checks a single value. Empty values are always valid (unset).
func (k Key) Validate(v string) error { return k.ValidateWith(v, nil) }

// ValidateWith is Validate, but files for which pending(path) is true are
// treated as existing (they will be written by the same apply).
func (k Key) ValidateWith(v string, pending func(string) bool) error {
	if v == "" {
		return nil
	}
	if strings.ContainsAny(v, "\n\r") {
		return fmt.Errorf("must be a single line")
	}
	switch k.Kind {
	case Bool:
		switch strings.ToLower(v) {
		case "1", "0", "true", "false", "yes", "no":
		default:
			return fmt.Errorf("must be true or false")
		}
	case Int:
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("must be a whole number")
		}
		if k.Max > k.Min && (n < k.Min || n > k.Max) {
			return fmt.Errorf("must be between %d and %d", k.Min, k.Max)
		}
	case Duration:
		if !durationRe.MatchString(v) {
			return fmt.Errorf("must be a number followed by h or d (eg: 12h, 30d)")
		}
	case File, NewFile:
		if !filepath.IsAbs(v) {
			return fmt.Errorf("must be an absolute path (Mailpit resolves relative paths from its working directory)")
		}
	}
	switch k.Kind {
	case File:
		if pending != nil && pending(v) {
			return nil
		}
		st, err := os.Stat(v)
		if err != nil {
			return fmt.Errorf("file not found: %s", v)
		}
		if st.IsDir() {
			return fmt.Errorf("is a directory, not a file")
		}
		if f, err := os.Open(v); err != nil {
			return fmt.Errorf("file is not readable")
		} else {
			f.Close()
		}
	case NewFile:
		st, err := os.Stat(filepath.Dir(v))
		if err != nil || !st.IsDir() {
			return fmt.Errorf("directory does not exist: %s", filepath.Dir(v))
		}
		if st, err := os.Stat(v); err == nil && st.IsDir() {
			return fmt.Errorf("is a directory, not a file")
		}
	case Addr:
		if err := ValidateAddr(v); err != nil {
			return err
		}
	case Regex:
		if _, err := regexp.Compile(v); err != nil {
			return fmt.Errorf("invalid regular expression: %v", err)
		}
	case Enum:
		for _, o := range k.Options {
			if strings.EqualFold(o, v) {
				return nil
			}
		}
		return fmt.Errorf("must be one of: %s", strings.Join(k.Options, ", "))
	case URL:
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("must be an http or https URL")
		}
	}
	if k.Name == "MP_ENABLE_PROMETHEUS" {
		if l := strings.ToLower(v); l != "true" && l != "false" {
			if err := ValidateAddr(v); err != nil {
				return fmt.Errorf("must be true, false or ip:port")
			}
		}
	}
	return nil
}

// ValidateAddr checks a host:port value.
func ValidateAddr(v string) error {
	_, port, err := net.SplitHostPort(v)
	if err != nil {
		return fmt.Errorf("must be host:port (eg: 127.0.0.1:8025 or [::]:8025)")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

// FieldError is a validation error for one key.
type FieldError struct {
	Key string
	Err string
}

func (e FieldError) Error() string { return e.Key + ": " + e.Err }

// ValidateEnv validates all known keys in m, plus cross-key rules.
func ValidateEnv(m map[string]string) []FieldError { return ValidateEnvWith(m, nil) }

// ValidateEnvWith is ValidateEnv with a pending-file check (see ValidateWith).
func ValidateEnvWith(m map[string]string, pending func(string) bool) []FieldError {
	var errs []FieldError
	for _, k := range Keys {
		v := m[k.Name]
		if err := k.ValidateWith(v, pending); err != nil {
			errs = append(errs, FieldError{Key: k.Name, Err: err.Error()})
		}
	}
	checked := map[string]bool{}
	for _, k := range Keys {
		if k.PairWith == "" || checked[k.Name] {
			continue
		}
		checked[k.Name], checked[k.PairWith] = true, true
		a, b := m[k.Name], m[k.PairWith]
		if (a == "") != (b == "") {
			errs = append(errs, FieldError{Key: k.Name, Err: "certificate and key must be set together (" + k.PairWith + ")"})
			continue
		}
		if a == "" {
			continue
		}
		cert, key := a, b
		if strings.HasSuffix(k.Name, "_KEY") {
			cert, key = b, a
		}
		if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
			errs = append(errs, FieldError{Key: k.Name, Err: "certificate and key do not load: " + err.Error()})
		}
	}
	if IsTrue(m["MP_SMTP_REQUIRE_TLS"]) && IsTrue(m["MP_SMTP_REQUIRE_STARTTLS"]) {
		errs = append(errs, FieldError{Key: "MP_SMTP_REQUIRE_TLS", Err: "cannot be used together with MP_SMTP_REQUIRE_STARTTLS"})
	}
	requireTLS := IsTrue(m["MP_SMTP_REQUIRE_TLS"]) || IsTrue(m["MP_SMTP_REQUIRE_STARTTLS"])
	if requireTLS && m["MP_SMTP_TLS_CERT"] == "" {
		errs = append(errs, FieldError{Key: "MP_SMTP_TLS_CERT", Err: "required when SMTP TLS is required"})
	}
	if requireTLS && IsTrue(m["MP_SMTP_AUTH_ALLOW_INSECURE"]) {
		errs = append(errs, FieldError{Key: "MP_SMTP_AUTH_ALLOW_INSECURE", Err: "cannot be used when SMTP TLS is required"})
	}
	return errs
}
