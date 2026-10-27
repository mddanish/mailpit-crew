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

// Config holds all settings.
type Config struct {
	// Mode is kept for old config files; only "managed" is supported.
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

	Managed struct {
		Version     string `yaml:"version"`
		ReleasesURL string `yaml:"releases_url"`
		DownloadURL string `yaml:"download_url"`
	} `yaml:"managed"`

	Restart struct {
		Timeout Duration `yaml:"timeout"`
	} `yaml:"restart"`

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
	c.Listen = "[::]:8080"
	c.Webroot = "/"
	c.LogLevel = "info"
	c.Managed.ReleasesURL = "https://api.github.com/repos/axllent/mailpit/releases"
	c.Managed.DownloadURL = "https://github.com/axllent/mailpit/releases/download"
	c.Restart.Timeout = Duration{30 * time.Second}
	c.Backups.Keep = 50
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
	{"home", "MPC_HOME", "Crew home directory (holds data/ and mailpit/)", str(func(c *Config) *string { return &c.Home })},
	{"listen", "MPC_LISTEN", "Crew web UI bind address", str(func(c *Config) *string { return &c.Listen })},
	{"webroot", "MPC_WEBROOT", "Crew web UI URL prefix", str(func(c *Config) *string { return &c.Webroot })},
	{"tls-cert", "MPC_TLS_CERT", "TLS certificate for the Crew web UI", str(func(c *Config) *string { return &c.TLS.Cert })},
	{"tls-key", "MPC_TLS_KEY", "TLS key for the Crew web UI", str(func(c *Config) *string { return &c.TLS.Key })},
	{"data-dir", "MPC_DATA_DIR", "Directory for Crew login, state, backups and audit log (default <home>/data)", str(func(c *Config) *string { return &c.DataDir })},
	{"users-file", "MPC_USERS_FILE", "Crew login file (default <data-dir>/users.yaml)", str(func(c *Config) *string { return &c.UsersFile })},
	{"log-file", "MPC_LOG_FILE", "Log to file instead of stdout", str(func(c *Config) *string { return &c.LogFile })},
	{"log-level", "MPC_LOG_LEVEL", "Log level: error, warn, info, debug", str(func(c *Config) *string { return &c.LogLevel })},
	{"mailpit-version", "MPC_MAILPIT_VERSION", "Mailpit to install on first start: latest, a tag (v1.28.0) or a binary path", str(func(c *Config) *string { return &c.Managed.Version })},
	{"restart-timeout", "MPC_RESTART_TIMEOUT", "How long Mailpit may take to start after a restart", dur(func(c *Config) *Duration { return &c.Restart.Timeout })},
	{"backups-keep", "MPC_BACKUPS_KEEP", "Number of file backups to keep", func(c *Config, v string) error {
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
	if c.UsersFile == "" {
		c.UsersFile = filepath.Join(c.DataDir, "users.yaml")
	}
	if !strings.HasPrefix(c.Webroot, "/") {
		c.Webroot = "/" + c.Webroot
	}
	if !strings.HasSuffix(c.Webroot, "/") {
		c.Webroot += "/"
	}
	return c.Validate()
}

// MailpitDir is the managed Mailpit directory (<home>/mailpit).
func (c *Config) MailpitDir() string { return filepath.Join(c.Home, "mailpit") }

// ListenPort returns the port of the Crew web UI.
func (c *Config) ListenPort() string {
	_, port, _ := net.SplitHostPort(c.Listen)
	return port
}

// Validate checks settings that would stop Crew from working.
func (c *Config) Validate() error {
	if c.Mode != "managed" {
		return fmt.Errorf("mode %q is no longer supported: MailPit-Crew only runs in managed mode", c.Mode)
	}
	if _, port, err := net.SplitHostPort(c.Listen); err != nil || port == "" {
		return fmt.Errorf("listen must be host:port, for example [::]:8080")
	}
	if (c.TLS.Cert == "") != (c.TLS.Key == "") {
		return errors.New("tls-cert and tls-key must be set together")
	}
	switch c.LogLevel {
	case "error", "warn", "info", "debug":
	default:
		return fmt.Errorf("log-level must be error, warn, info or debug")
	}
	if c.Restart.Timeout.Duration <= 0 {
		return errors.New("restart-timeout must be positive")
	}
	return nil
}
