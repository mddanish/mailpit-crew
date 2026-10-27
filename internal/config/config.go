// Package config loads MailPit-Crew settings from defaults, an optional YAML
// file, MPC_* environment variables and command-line flags (in that order,
// later sources win).
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that reads "30s" style strings from YAML.
type Duration struct{ time.Duration }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q", n.Value)
	}
	d.Duration = v
	return nil
}

// StringList reads either a YAML list or a single shell-style string.
type StringList []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *StringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		l, err := SplitArgs(n.Value)
		*s = l
		return err
	}
	var l []string
	if err := n.Decode(&l); err != nil {
		return err
	}
	*s = l
	return nil
}

// Config holds all settings.
type Config struct {
	// Mode is "managed" (Crew owns and runs Mailpit) or "external"
	// (Crew manages a Mailpit that is installed and run separately).
	Mode    string `yaml:"mode"`
	Home    string `yaml:"home"`
	Listen  string `yaml:"listen"`
	Webroot string `yaml:"webroot"`
	TLS     struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"tls"`
	DataDir   string `yaml:"data_dir"`
	UsersFile string `yaml:"users_file"`
	LogFile   string `yaml:"log_file"`
	LogLevel  string `yaml:"log_level"`

	Mailpit struct {
		APIURL          string     `yaml:"api_url"`
		APIUser         string     `yaml:"api_user"`
		APIPassword     string     `yaml:"api_password"`
		EnvFile         string     `yaml:"env_file"`
		UIAuthFile      string     `yaml:"ui_auth_file"`
		SMTPAuthFile    string     `yaml:"smtp_auth_file"`
		POP3AuthFile    string     `yaml:"pop3_auth_file"`
		SendAPIAuthFile string     `yaml:"send_api_auth_file"`
		TagsFile        string     `yaml:"tags_file"`
		LogFile         string     `yaml:"log_file"`
		ManagedDirs     StringList `yaml:"managed_dirs"`
	} `yaml:"mailpit"`

	Managed struct {
		Version     string `yaml:"version"`
		APIListen   string `yaml:"api_listen"`
		ReleasesURL string `yaml:"releases_url"`
		DownloadURL string `yaml:"download_url"`
	} `yaml:"managed"`

	Restart struct {
		Mode    string     `yaml:"mode"`
		Command StringList `yaml:"command"`
		Timeout Duration   `yaml:"timeout"`
	} `yaml:"restart"`

	Child struct {
		Binary string     `yaml:"binary"`
		Args   StringList `yaml:"args"`
	} `yaml:"child"`

	Backups struct {
		Keep int `yaml:"keep"`
	} `yaml:"backups"`

	Session struct {
		TTL Duration `yaml:"ttl"`
	} `yaml:"session"`

	// ConfigFile is the file the settings were read from, if any.
	ConfigFile string `yaml:"-"`
}

// Defaults returns the built-in defaults.
func Defaults() *Config {
	c := &Config{}
	c.Mode = "managed"
	c.Home = "."
	c.Listen = "[::]:8025"
	c.Webroot = "/"
	c.Managed.APIListen = "127.0.0.1:8026"
	c.Managed.ReleasesURL = "https://api.github.com/repos/axllent/mailpit/releases"
	c.Managed.DownloadURL = "https://github.com/axllent/mailpit/releases/download"
	c.LogLevel = "info"
	c.Mailpit.APIURL = "http://127.0.0.1:8026"
	c.Restart.Mode = "command"
	c.Restart.Timeout = Duration{30 * time.Second}
	c.Child.Binary = "mailpit"
	c.Backups.Keep = 20
	c.Session.TTL = Duration{12 * time.Hour}
	return c
}

type setting struct {
	flag, env, usage string
	set              func(c *Config, v string) error
}

func str(p func(c *Config) *string) func(*Config, string) error {
	return func(c *Config, v string) error { *p(c) = v; return nil }
}

func list(p func(c *Config) *StringList) func(*Config, string) error {
	return func(c *Config, v string) error {
		l, err := SplitArgs(v)
		*p(c) = l
		return err
	}
}

func dur(p func(c *Config) *Duration) func(*Config, string) error {
	return func(c *Config, v string) error {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid duration %q", v)
		}
		p(c).Duration = d
		return nil
	}
}

var settings = []setting{
	{"mode", "MPC_MODE", "managed (Crew runs Mailpit) or external", str(func(c *Config) *string { return &c.Mode })},
	{"home", "MPC_HOME", "Crew home directory (managed mode: holds data/ and mailpit/)", str(func(c *Config) *string { return &c.Home })},
	{"mailpit-version", "MPC_MAILPIT_VERSION", "Managed mode: Mailpit to use on first start: latest, a tag (v1.28.0) or a binary path", str(func(c *Config) *string { return &c.Managed.Version })},
	{"mailpit-api-listen", "MPC_MAILPIT_API_LISTEN", "Managed mode: local address for the Mailpit API", str(func(c *Config) *string { return &c.Managed.APIListen })},
	{"listen", "MPC_LISTEN", "Web UI bind address", str(func(c *Config) *string { return &c.Listen })},
	{"webroot", "MPC_WEBROOT", "Web UI URL prefix", str(func(c *Config) *string { return &c.Webroot })},
	{"tls-cert", "MPC_TLS_CERT", "TLS certificate for the web UI", str(func(c *Config) *string { return &c.TLS.Cert })},
	{"tls-key", "MPC_TLS_KEY", "TLS key for the web UI", str(func(c *Config) *string { return &c.TLS.Key })},
	{"data-dir", "MPC_DATA_DIR", "Directory for users file, backups and audit log (default <home>/data)", str(func(c *Config) *string { return &c.DataDir })},
	{"users-file", "MPC_USERS_FILE", "Crew users file (default <data-dir>/users.yaml)", str(func(c *Config) *string { return &c.UsersFile })},
	{"log-file", "MPC_LOG_FILE", "Log to file instead of stdout", str(func(c *Config) *string { return &c.LogFile })},
	{"log-level", "MPC_LOG_LEVEL", "Log level: error, warn, info, debug", str(func(c *Config) *string { return &c.LogLevel })},
	{"mailpit-api", "MPC_MAILPIT_API", "Mailpit API base URL", str(func(c *Config) *string { return &c.Mailpit.APIURL })},
	{"mailpit-api-user", "MPC_MAILPIT_API_USER", "Mailpit API service account username", str(func(c *Config) *string { return &c.Mailpit.APIUser })},
	{"mailpit-api-password", "MPC_MAILPIT_API_PASSWORD", "Mailpit API service account password", str(func(c *Config) *string { return &c.Mailpit.APIPassword })},
	{"mailpit-env-file", "MPC_MAILPIT_ENV_FILE", "Mailpit env file (eg: mailpit.ini)", str(func(c *Config) *string { return &c.Mailpit.EnvFile })},
	{"mailpit-ui-auth-file", "MPC_MAILPIT_UI_AUTH_FILE", "Override Mailpit UI auth file path", str(func(c *Config) *string { return &c.Mailpit.UIAuthFile })},
	{"mailpit-smtp-auth-file", "MPC_MAILPIT_SMTP_AUTH_FILE", "Override Mailpit SMTP auth file path", str(func(c *Config) *string { return &c.Mailpit.SMTPAuthFile })},
	{"mailpit-pop3-auth-file", "MPC_MAILPIT_POP3_AUTH_FILE", "Override Mailpit POP3 auth file path", str(func(c *Config) *string { return &c.Mailpit.POP3AuthFile })},
	{"mailpit-send-api-auth-file", "MPC_MAILPIT_SEND_API_AUTH_FILE", "Override Mailpit Send API auth file path", str(func(c *Config) *string { return &c.Mailpit.SendAPIAuthFile })},
	{"mailpit-tags-file", "MPC_MAILPIT_TAGS_FILE", "Override Mailpit tags file path", str(func(c *Config) *string { return &c.Mailpit.TagsFile })},
	{"mailpit-log-file", "MPC_MAILPIT_LOG_FILE", "Override Mailpit log file path", str(func(c *Config) *string { return &c.Mailpit.LogFile })},
	{"mailpit-managed-dirs", "MPC_MAILPIT_MANAGED_DIRS", "Directories Crew may write to (default: env file directory)", list(func(c *Config) *StringList { return &c.Mailpit.ManagedDirs })},
	{"restart-mode", "MPC_RESTART_MODE", "Restart mode: child or command", str(func(c *Config) *string { return &c.Restart.Mode })},
	{"restart-command", "MPC_RESTART_COMMAND", "Restart command for command mode (eg: \"systemctl restart mailpit\")", list(func(c *Config) *StringList { return &c.Restart.Command })},
	{"restart-timeout", "MPC_RESTART_TIMEOUT", "Restart and health check timeout", dur(func(c *Config) *Duration { return &c.Restart.Timeout })},
	{"mailpit-binary", "MPC_MAILPIT_BINARY", "Mailpit binary for child mode", str(func(c *Config) *string { return &c.Child.Binary })},
	{"mailpit-args", "MPC_MAILPIT_ARGS", "Extra Mailpit arguments for child mode", list(func(c *Config) *StringList { return &c.Child.Args })},
	{"backups-keep", "MPC_BACKUPS_KEEP", "Number of apply backups to keep", func(c *Config, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("backups-keep must be a number of 1 or more")
		}
		c.Backups.Keep = n
		return nil
	}},
	{"session-ttl", "MPC_SESSION_TTL", "Login session lifetime", dur(func(c *Config) *Duration { return &c.Session.TTL })},
}

// ErrHelp is returned when -h or --help was given.
var ErrHelp = flag.ErrHelp

// Load reads configuration. args are the command-line arguments without the
// program name.
func Load(args []string, output io.Writer) (*Config, error) {
	fs := flag.NewFlagSet("mailpit-crew", flag.ContinueOnError)
	fs.SetOutput(output)
	configFile := fs.String("config", "", "Config file (YAML), env MPC_CONFIG")
	values := map[string]*string{}
	for _, s := range settings {
		values[s.flag] = fs.String(s.flag, "", s.usage+" (env "+s.env+")")
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}

	c := Defaults()

	path := *configFile
	if path == "" {
		path = os.Getenv("MPC_CONFIG")
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("config file: %w", err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("config file %s: %w", path, err)
		}
		c.ConfigFile = path
	}

	for _, s := range settings {
		if v, ok := os.LookupEnv(s.env); ok {
			if err := s.set(c, v); err != nil {
				return nil, fmt.Errorf("%s: %w", s.env, err)
			}
		}
	}

	setFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })
	for _, s := range settings {
		if setFlags[s.flag] {
			if err := s.set(c, *values[s.flag]); err != nil {
				return nil, fmt.Errorf("--%s: %w", s.flag, err)
			}
		}
	}

	if err := c.finish(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) finish() error {
	home, err := filepath.Abs(c.Home)
	if err != nil {
		return err
	}
	c.Home = home
	if c.DataDir == "" {
		c.DataDir = filepath.Join(c.Home, "data")
	}
	if c.Mode == "managed" {
		if c.Mailpit.EnvFile != "" || len(c.Mailpit.ManagedDirs) > 0 {
			return errors.New("mailpit.env_file and mailpit.managed_dirs are only used with mode external")
		}
		if !isLoopbackAddr(c.Managed.APIListen) {
			return errors.New("managed.api_listen must be a local address, for example 127.0.0.1:8026")
		}
		c.Mailpit.EnvFile = filepath.Join(c.MailpitDir(), "mailpit.ini")
		c.Mailpit.ManagedDirs = StringList{c.MailpitDir()}
		c.Mailpit.APIURL = "http://" + c.Managed.APIListen
		c.Restart.Mode = "child"
	}
	if c.UsersFile == "" {
		c.UsersFile = filepath.Join(c.DataDir, "users.yaml")
	}
	if !strings.HasPrefix(c.Webroot, "/") {
		c.Webroot = "/" + c.Webroot
	}
	if !strings.HasSuffix(c.Webroot, "/") {
		c.Webroot += "/"
	}
	c.Mailpit.APIURL = strings.TrimRight(c.Mailpit.APIURL, "/")
	if len(c.Mailpit.ManagedDirs) == 0 && c.Mailpit.EnvFile != "" {
		c.Mailpit.ManagedDirs = StringList{filepath.Dir(c.Mailpit.EnvFile)}
	}
	for i, d := range c.Mailpit.ManagedDirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return err
		}
		c.Mailpit.ManagedDirs[i] = abs
	}
	return c.Validate()
}

// MailpitDir is the managed Mailpit directory (<home>/mailpit).
func (c *Config) MailpitDir() string { return filepath.Join(c.Home, "mailpit") }

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Validate checks settings that would stop Crew from working.
func (c *Config) Validate() error {
	if c.Mode != "managed" && c.Mode != "external" {
		return fmt.Errorf("mode must be managed or external, not %q", c.Mode)
	}
	if c.Mode == "external" && c.Mailpit.EnvFile == "" && c.Restart.Mode == "command" {
		return errors.New("mode external needs mailpit.env_file")
	}
	switch c.Restart.Mode {
	case "child":
		if c.Child.Binary == "" {
			return errors.New("child mode needs mailpit-binary")
		}
	case "command":
		if len(c.Restart.Command) == 0 {
			return errors.New("command mode needs restart-command (eg: \"systemctl restart mailpit\")")
		}
	default:
		return fmt.Errorf("restart-mode must be child or command, not %q", c.Restart.Mode)
	}
	if (c.TLS.Cert == "") != (c.TLS.Key == "") {
		return errors.New("tls-cert and tls-key must be set together")
	}
	switch c.LogLevel {
	case "error", "warn", "info", "debug":
	default:
		return fmt.Errorf("log-level must be error, warn, info or debug")
	}
	if c.Mailpit.APIURL == "" {
		return errors.New("mailpit-api is required")
	}
	if c.Restart.Timeout.Duration <= 0 {
		return errors.New("restart-timeout must be positive")
	}
	return nil
}

// SplitArgs splits a command line into arguments. It supports single and
// double quotes and backslash escapes, but no other shell features.
func SplitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inArg := false
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
			inArg = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inArg = true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				out = append(out, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unclosed quote")
	}
	if escaped {
		return nil, errors.New("trailing backslash")
	}
	if inArg {
		out = append(out, cur.String())
	}
	return out, nil
}
