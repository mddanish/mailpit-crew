// Package managed runs Mailpit fully under MailPit-Crew: it owns the folder
// layout, downloads Mailpit releases, writes Mailpit's files and controls
// Mailpit through a ctl.sh script.
package managed

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/mddanish/mailpit-crew/internal/fsutil"
	"github.com/mddanish/mailpit-crew/internal/htpasswd"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// ServiceUser is the Mailpit API account Crew creates for itself.
const ServiceUser = "crew"

// Layout is the managed folder structure:
//
//	<home>/data/                Crew users, state, backups, audit log
//	<home>/mailpit/ctl.sh       start | stop | restart | status
//	<home>/mailpit/bin/         downloaded Mailpit binaries, "current" symlink
//	<home>/mailpit/mailpit.ini  Mailpit settings
//	<home>/mailpit/*.auth       password files
//	<home>/mailpit/tags.yaml    tag rules
//	<home>/mailpit/mailpit.db   mail storage
//	<home>/mailpit/mailpit.log  Mailpit output
type Layout struct {
	Data    string
	Dir     string
	Bin     string
	Current string // symlink to the active binary
	Ctl     string
	EnvFile string
	UIAuth  string
	Tags    string
	DB      string
	Log     string
	PID     string
	SSL     string
	State   string
	Active  string // copy of the files Mailpit last started with
	Backups string
}

// NewLayout returns the layout for a home and data directory.
func NewLayout(mailpitDir, dataDir string) Layout {
	return Layout{
		Data:    dataDir,
		Dir:     mailpitDir,
		Bin:     filepath.Join(mailpitDir, "bin"),
		Current: filepath.Join(mailpitDir, "bin", "current"),
		Ctl:     filepath.Join(mailpitDir, "ctl.sh"),
		EnvFile: filepath.Join(mailpitDir, "mailpit.ini"),
		UIAuth:  filepath.Join(mailpitDir, "ui.auth"),
		Tags:    filepath.Join(mailpitDir, "tags.yaml"),
		DB:      filepath.Join(mailpitDir, "mailpit.db"),
		Log:     filepath.Join(mailpitDir, "mailpit.log"),
		PID:     filepath.Join(mailpitDir, "mailpit.pid"),
		SSL:     filepath.Join(mailpitDir, "ssl"),
		State:   filepath.Join(dataDir, "state.yaml"),
		Active:  filepath.Join(dataDir, "active"),
		Backups: filepath.Join(dataDir, "backups"),
	}
}

// StandardFile returns the path Crew uses for an optional file setting.
func (l Layout) StandardFile(key string) string {
	switch key {
	case "MP_SMTP_AUTH_FILE":
		return filepath.Join(l.Dir, "smtp.auth")
	case "MP_POP3_AUTH_FILE":
		return filepath.Join(l.Dir, "pop3.auth")
	case "MP_SEND_API_AUTH_FILE":
		return filepath.Join(l.Dir, "send-api.auth")
	case "MP_TAGS_CONFIG":
		return l.Tags
	case "MP_SMTP_RELAY_CONFIG":
		return filepath.Join(l.Dir, "relay.yaml")
	case "MP_SMTP_FORWARD_CONFIG":
		return filepath.Join(l.Dir, "forward.yaml")
	}
	return ""
}

// Locked returns the env keys Crew controls ("" = must not be set).
func (l Layout) Locked() map[string]string {
	return map[string]string{
		"MP_DATABASE":     l.DB,
		"MP_UI_AUTH_FILE": l.UIAuth,
		"MP_UI_AUTH":      "", // inline credentials would replace ui.auth
		"MP_WEBROOT":      "", // Crew calls the API at /
		"MP_LOG_FILE":     "", // ctl.sh writes Mailpit's output to mailpit.log
	}
}

// DefaultUIAddr is where Mailpit's web UI listens on a new install.
const DefaultUIAddr = "[::]:8025"

// State is Crew's managed-mode state (data/state.yaml, mode 0600).
type State struct {
	APIUser     string `yaml:"api_user"`
	APIPassword string `yaml:"api_password"`

	mu   sync.Mutex
	path string
}

// LoadState reads the state file, creating it with a new service account
// password when missing.
func LoadState(path string) (*State, error) {
	s := &State{path: path}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := yaml.Unmarshal(data, s); err != nil {
			return nil, fmt.Errorf("state file %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if s.APIUser == "" || s.APIPassword == "" {
		pw, err := htpasswd.GeneratePassword(32)
		if err != nil {
			return nil, err
		}
		s.APIUser, s.APIPassword = ServiceUser, pw
		if err := s.Save(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Save writes the state file.
func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if err := fsutil.WriteAtomic(s.path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(s.path, 0o600)
}

const envHeader = `# Mailpit settings, managed by MailPit-Crew.
# Change them in the Crew web UI, then restart Mailpit from Crew.
# MP_DATABASE and MP_UI_AUTH_FILE are managed by Crew and reset on start.
`

// Ensure creates the folder layout and first-run files, writes ctl.sh when
// missing, makes sure the locked settings are set and the service account
// can log in. It returns notes about what it created or repaired.
func (l Layout) Ensure(st *State) ([]string, error) {
	var notes []string
	for _, d := range []string{l.Data, l.Dir, l.Bin} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	switch old, err := os.ReadFile(l.Ctl); {
	case errors.Is(err, os.ErrNotExist):
		if err := os.WriteFile(l.Ctl, []byte(ctlScript), 0o750); err != nil {
			return nil, err
		}
		notes = append(notes, "created "+l.Ctl)
	case err != nil:
		return nil, err
	case ctlVersion(string(old)) < ctlVersion(ctlScript):
		// newer script: keep the old one (it may have local edits)
		if err := os.WriteFile(l.Ctl+".old", old, 0o640); err != nil {
			return nil, err
		}
		if err := fsutil.WriteAtomic(l.Ctl, []byte(ctlScript), 0o750); err != nil {
			return nil, err
		}
		notes = append(notes, "updated "+l.Ctl+" (previous version saved as ctl.sh.old)")
	}
	if _, err := os.Stat(l.Tags); errors.Is(err, os.ErrNotExist) {
		if err := fsutil.WriteAtomic(l.Tags, []byte("filters: []\n"), 0o644); err != nil {
			return nil, err
		}
		notes = append(notes, "created "+l.Tags)
	}
	if _, err := os.Stat(l.EnvFile); errors.Is(err, os.ErrNotExist) {
		var b strings.Builder
		b.WriteString(envHeader)
		for _, kv := range [][2]string{
			{"MP_DATABASE", l.DB},
			{"MP_UI_BIND_ADDR", DefaultUIAddr},
			{"MP_UI_AUTH_FILE", l.UIAuth},
			{"MP_TAGS_CONFIG", l.Tags},
			{"MP_SMTP_BIND_ADDR", "[::]:1025"},
			{"MP_DISABLE_VERSION_CHECK", "true"},
		} {
			b.WriteString(kv[0] + "=" + kv[1] + "\n")
		}
		if err := fsutil.WriteAtomic(l.EnvFile, []byte(b.String()), 0o640); err != nil {
			return nil, err
		}
		notes = append(notes, "created "+l.EnvFile)
	}
	changed, err := l.ensureServiceAccount(st)
	if err != nil {
		return nil, err
	}
	if changed {
		notes = append(notes, "updated Crew service account in "+l.UIAuth)
	}
	return notes, nil
}

var ctlVersionRe = regexp.MustCompile(`(?m)^# mailpit-crew-ctl: (\d+)$`)

// ctlVersion returns the template version of a ctl.sh (0 when unknown).
func ctlVersion(script string) int {
	m := ctlVersionRe.FindStringSubmatch(script)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func (l Layout) ensureServiceAccount(st *State) (bool, error) {
	data, err := os.ReadFile(l.UIAuth)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	f := htpasswd.Parse(data)
	if h, ok := f.Get(st.APIUser); ok && bcrypt.CompareHashAndPassword([]byte(h), []byte(st.APIPassword)) == nil {
		return false, nil
	}
	h, err := htpasswd.HashPassword(st.APIPassword)
	if err != nil {
		return false, err
	}
	if err := f.Set(st.APIUser, h); err != nil {
		return false, err
	}
	return true, fsutil.WriteAtomic(l.UIAuth, f.Bytes(), 0o600)
}

// ctlScript controls the managed Mailpit. Crew calls it, and it can be
// used by hand. It reads mailpit.ini without eval, so values are never run
// as shell code.
const ctlScript = `#!/bin/sh
# mailpit-crew-ctl: 2
# MailPit-Crew: control script for the managed Mailpit.
# Usage: ctl.sh start | stop | restart | status | version
# MailPit-Crew calls this script. You may run it by hand or edit it; Crew
# only needs the five commands and the exit codes (status: 0 = running).

DIR=$(cd "$(dirname "$0")" && pwd)
BIN="$DIR/bin/current"
ENV_FILE="$DIR/mailpit.ini"
PID_FILE="$DIR/mailpit.pid"
LOG_FILE="$DIR/mailpit.log"
LOG_MAX_BYTES=10485760

is_running() {
	[ -f "$PID_FILE" ] || return 1
	pid=$(cat "$PID_FILE" 2>/dev/null)
	[ -n "$pid" ] && kill -0 "$pid" 2>/dev/null || return 1
	if [ -r "/proc/$pid/stat" ]; then
		# a zombie is not running
		state=$(sed 's/.*) //' "/proc/$pid/stat" | cut -d' ' -f1)
		[ "$state" = Z ] && return 1
	fi
	return 0
}

load_env() {
	while IFS= read -r line || [ -n "$line" ]; do
		case "$line" in '' | \#* | \;*) continue ;; esac
		key=${line%%=*}
		val=${line#*=}
		case "$key" in '' | *[!A-Za-z0-9_]*) continue ;; esac
		case "$val" in
		\"*\")
			val=${val#\"}
			val=${val%\"}
			val=$(printf '%s' "$val" | sed -e 's/\\"/"/g' -e 's/\\\\/\\/g')
			;;
		\'*\')
			val=${val#\'}
			val=${val%\'}
			;;
		esac
		export "$key=$val"
	done <"$ENV_FILE"
}

start() {
	if is_running; then
		echo "mailpit is already running (pid $(cat "$PID_FILE"))"
		return 0
	fi
	if [ ! -x "$BIN" ]; then
		echo "no Mailpit binary selected ($BIN)" >&2
		return 1
	fi
	if [ -f "$LOG_FILE" ] && [ "$(wc -c <"$LOG_FILE")" -gt "$LOG_MAX_BYTES" ]; then
		mv -f "$LOG_FILE" "$LOG_FILE.1"
	fi
	(
		cd "$DIR" || exit 1
		load_env
		nohup "$BIN" >>"$LOG_FILE" 2>&1 &
		echo $! >"$PID_FILE"
	)
	sleep 1
	if ! is_running; then
		rm -f "$PID_FILE"
		echo "mailpit failed to start, last log lines:" >&2
		tail -n 5 "$LOG_FILE" >&2
		return 1
	fi
	echo "mailpit started (pid $(cat "$PID_FILE"))"
}

stop() {
	if ! is_running; then
		rm -f "$PID_FILE"
		echo "mailpit is not running"
		return 0
	fi
	pid=$(cat "$PID_FILE")
	kill "$pid"
	i=0
	while kill -0 "$pid" 2>/dev/null; do
		i=$((i + 1))
		if [ "$i" -ge 100 ]; then
			kill -9 "$pid" 2>/dev/null
			break
		fi
		sleep 0.1
	done
	rm -f "$PID_FILE"
	echo "mailpit stopped"
}

case "$1" in
start) start ;;
stop) stop ;;
restart)
	stop
	start
	;;
status)
	if is_running; then
		echo "mailpit is running (pid $(cat "$PID_FILE"))"
		exit 0
	fi
	echo "mailpit is not running"
	exit 3
	;;
version) "$BIN" version --no-release-check ;;
*)
	echo "usage: $0 start|stop|restart|status|version" >&2
	exit 2
	;;
esac
`

// APIBase returns the URL Crew uses for Mailpit's API, from the env file:
// the web UI address (wildcard hosts become 127.0.0.1), https when Mailpit
// serves the UI with TLS.
func APIBase(env map[string]string) string {
	addr := env["MP_UI_BIND_ADDR"]
	if addr == "" {
		addr = DefaultUIAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = "", "8025"
	}
	switch host {
	case "", "::", "0.0.0.0":
		host = "127.0.0.1"
	}
	scheme := "http"
	if env["MP_UI_TLS_CERT"] != "" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}
