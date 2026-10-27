// Package apply saves Mailpit's configuration files and restarts Mailpit on
// request. Saving validates, backs up and writes a file; it does not
// restart. Crew remembers the files Mailpit last started with ("active") so
// it can show what is saved but not active yet, and roll back to them when
// a restart fails.
package apply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mddanish/mailpit-crew/internal/catalog"
	"github.com/mddanish/mailpit-crew/internal/diff"
	"github.com/mddanish/mailpit-crew/internal/envfile"
	"github.com/mddanish/mailpit-crew/internal/fsutil"
	"github.com/mddanish/mailpit-crew/internal/htpasswd"
	"github.com/mddanish/mailpit-crew/internal/restart"
	"github.com/mddanish/mailpit-crew/internal/verify"
)

// Kind is the type of a managed file.
type Kind string

// Managed file kinds.
const (
	KindEnv     Kind = "env"
	KindAuth    Kind = "auth"
	KindTags    Kind = "tags"
	KindRelay   Kind = "relay"
	KindForward Kind = "forward"
)

// fileKeys maps env keys to the kind of file they point to, in display order.
var fileKeys = []struct {
	Key  string
	Kind Kind
}{
	{"MP_UI_AUTH_FILE", KindAuth},
	{"MP_SMTP_AUTH_FILE", KindAuth},
	{"MP_POP3_AUTH_FILE", KindAuth},
	{"MP_SEND_API_AUTH_FILE", KindAuth},
	{"MP_TAGS_CONFIG", KindTags},
	{"MP_SMTP_RELAY_CONFIG", KindRelay},
	{"MP_SMTP_FORWARD_CONFIG", KindForward},
}

// KeyKind returns the file kind an env key points to.
func KeyKind(key string) (Kind, bool) {
	for _, f := range fileKeys {
		if f.Key == key {
			return f.Kind, true
		}
	}
	return "", false
}

// File is one managed Mailpit file.
type File struct {
	ID       string
	Kind     Kind
	Path     string
	Label    string
	Uses     []string // env keys that point to this file
	Writable bool     // inside the managed directory
	Exists   bool
}

// Readiness checks Mailpit health after a restart.
type Readiness interface {
	WaitReady(ctx context.Context) error
	// Uptime returns how long the running Mailpit has been up.
	Uptime(ctx context.Context) (time.Duration, error)
}

// Options configures a Manager.
type Options struct {
	EnvFile     string
	ManagedDir  string // Crew only writes inside this directory
	BackupDir   string
	ActiveDir   string // copy of the files Mailpit last started with
	Keep        int
	Restarter   restart.Restarter
	Ready       Readiness
	Timeout     time.Duration
	StableFor   time.Duration // Mailpit must stay up this long after a restart (default 5s)
	ServiceUser string        // Crew's Mailpit API account, must stay in the UI auth file
	// Locked env keys and their required values ("" = must not be set).
	Locked map[string]string
	// Extra env checks (for example port clashes with Crew).
	Extra func(map[string]string) []catalog.FieldError
	// Started is called after Mailpit started successfully.
	Started func()
	Log     *slog.Logger
}

// Failure describes a restart that did not bring Mailpit up.
type Failure struct {
	Time    time.Time
	Error   string
	LogTail []string
}

// Manager saves files and restarts Mailpit.
type Manager struct {
	opts      Options
	mu        sync.Mutex // file writes and active snapshot
	restartMu sync.Mutex // one restart, rollback or version switch at a time
	failure   *Failure
}

// New returns a Manager.
func New(opts Options) *Manager {
	if opts.Keep < 1 {
		opts.Keep = 50
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.StableFor == 0 {
		opts.StableFor = 5 * time.Second
	}
	return &Manager{opts: opts}
}

// ServiceUser returns the protected Mailpit API account name.
func (m *Manager) ServiceUser() string { return m.opts.ServiceUser }

// Locked returns the locked env keys.
func (m *Manager) Locked() map[string]string { return m.opts.Locked }

// EnvPath returns the Mailpit env file path.
func (m *Manager) EnvPath() string { return m.opts.EnvFile }

func (m *Manager) writable(path string) bool {
	rel, err := filepath.Rel(m.opts.ManagedDir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// FileID returns the stable ID of a managed file.
func FileID(kind Kind, path string) string {
	if kind == KindAuth {
		s := sha256.Sum256([]byte(path))
		return "auth-" + hex.EncodeToString(s[:4])
	}
	return string(kind)
}

func label(kind Kind, uses []string) string {
	switch kind {
	case KindEnv:
		return "Mailpit settings"
	case KindTags:
		return "Tag rules"
	case KindRelay:
		return "SMTP relay"
	case KindForward:
		return "SMTP forward"
	}
	var parts []string
	for _, k := range uses {
		key, _ := catalog.Lookup(k)
		parts = append(parts, key.AuthFile)
	}
	return strings.Join(parts, " + ") + " accounts"
}

// Env returns the Mailpit env file as it is on disk.
func (m *Manager) Env() (*envfile.File, error) {
	data, err := os.ReadFile(m.opts.EnvFile)
	if err != nil {
		return nil, err
	}
	return envfile.Parse(data), nil
}

func filesFromEnv(m *Manager, env map[string]string) []File {
	out := []File{m.newFile(KindEnv, m.opts.EnvFile, nil)}
	byPath := map[string]int{}
	for _, fk := range fileKeys {
		p := env[fk.Key]
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		p = filepath.Clean(p)
		if i, ok := byPath[p]; ok && out[i].Kind == fk.Kind {
			out[i].Uses = append(out[i].Uses, fk.Key)
			continue
		}
		byPath[p] = len(out)
		out = append(out, m.newFile(fk.Kind, p, []string{fk.Key}))
	}
	for i := range out {
		out[i].Label = label(out[i].Kind, out[i].Uses)
	}
	return out
}

func (m *Manager) newFile(kind Kind, path string, uses []string) File {
	f := File{ID: FileID(kind, path), Kind: kind, Path: path, Uses: uses, Writable: m.writable(path)}
	if _, err := os.Stat(path); err == nil {
		f.Exists = true
	}
	return f
}

// Files returns all managed files referenced by the env file on disk.
func (m *Manager) Files() []File {
	env := map[string]string{}
	if ef, err := m.Env(); err == nil {
		env = ef.Map()
	}
	return filesFromEnv(m, env)
}

// FileByID finds a managed file.
func (m *Manager) FileByID(id string) (File, bool) {
	for _, f := range m.Files() {
		if f.ID == id {
			return f, true
		}
	}
	return File{}, false
}

// Hash is the content hash used to detect concurrent edits.
func Hash(data []byte) string { return fsutil.Hash(data) }

// Read returns a file's content and hash ("" content for a missing file).
func (m *Manager) Read(path string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, Hash(nil), nil
	}
	return data, Hash(data), err
}

// Verify checks content as if it replaced the file of the given kind.
func (m *Manager) Verify(kind Kind, path string, content []byte) []verify.Issue {
	switch kind {
	case KindEnv:
		return verify.Env(content, verify.EnvOptions{
			Locked: m.opts.Locked,
			Extra:  m.opts.Extra,
		})
	case KindAuth:
		svc := ""
		if path == m.uiAuthPath() {
			svc = m.opts.ServiceUser
		}
		return verify.Auth(content, svc)
	case KindTags:
		return verify.Tags(content)
	case KindRelay:
		return verify.Relay(content)
	case KindForward:
		return verify.Forward(content)
	}
	return nil
}

func (m *Manager) uiAuthPath() string {
	if ef, err := m.Env(); err == nil {
		if v, _ := ef.Get("MP_UI_AUTH_FILE"); v != "" {
			return filepath.Clean(v)
		}
	}
	return ""
}

// ErrConflict is returned when a file changed after the editor loaded it.
var ErrConflict = errors.New("the file changed since you opened it; reload the page and make your change again")

// ValidationError lists problems that stopped a save or restart.
type ValidationError struct{ Issues []verify.Issue }

func (e *ValidationError) Error() string {
	var s []string
	for _, i := range e.Issues {
		s = append(s, i.String())
	}
	return "validation failed: " + strings.Join(s, "; ")
}

// Save validates and writes one file. base is the hash the editor started
// from ("" skips the check). Mailpit is not restarted.
func (m *Manager) Save(kind Kind, path string, content []byte, base string) error {
	if !m.writable(path) {
		return fmt.Errorf("%s is outside %s and cannot be changed", path, m.opts.ManagedDir)
	}
	if errs := verify.Errors(m.Verify(kind, path, content)); len(errs) > 0 {
		return &ValidationError{Issues: errs}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if base != "" && Hash(cur) != base {
		return ErrConflict
	}
	if existed && bytes.Equal(cur, content) {
		return nil
	}
	if existed {
		if err := m.backup(path, cur); err != nil {
			return fmt.Errorf("backup failed, nothing changed: %w", err)
		}
	}
	return fsutil.WriteAtomic(path, content, fileMode(kind))
}

// fileMode is the mode for files Crew creates. Password files and files that
// can hold passwords stay private.
func fileMode(k Kind) os.FileMode {
	switch k {
	case KindAuth, KindRelay, KindForward, KindEnv:
		return 0o600
	}
	return 0o644
}

func (m *Manager) backup(path string, data []byte) error {
	if err := os.MkdirAll(m.opts.BackupDir, 0o700); err != nil {
		return err
	}
	name := time.Now().UTC().Format("20060102-150405.000000") + "-" + filepath.Base(path)
	if err := os.WriteFile(filepath.Join(m.opts.BackupDir, name), data, 0o600); err != nil {
		return err
	}
	m.prune()
	return nil
}

func (m *Manager) prune() {
	entries, err := os.ReadDir(m.opts.BackupDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > m.opts.Keep {
		_ = os.Remove(filepath.Join(m.opts.BackupDir, names[0]))
		names = names[1:]
	}
}

// --- active snapshot ---

type activeEntry struct {
	Path    string `json:"path"`
	Kind    Kind   `json:"kind"`
	Stored  string `json:"stored"`
	Existed bool   `json:"existed"`
	Hash    string `json:"hash"`
}

type activeManifest struct {
	Time  time.Time     `json:"time"`
	Files []activeEntry `json:"files"`
}

func (m *Manager) loadActive() (*activeManifest, error) {
	data, err := os.ReadFile(filepath.Join(m.opts.ActiveDir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var a activeManifest
	return &a, json.Unmarshal(data, &a)
}

// HasActive reports whether an active snapshot exists.
func (m *Manager) HasActive() bool {
	_, err := m.loadActive()
	return err == nil
}

// SnapshotActive records the current files as the ones Mailpit runs with.
func (m *Manager) SnapshotActive() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Manager) snapshotLocked() error {
	tmp := m.opts.ActiveDir + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	man := activeManifest{Time: time.Now().UTC()}
	for i, f := range m.Files() {
		data, err := os.ReadFile(f.Path)
		e := activeEntry{Path: f.Path, Kind: f.Kind, Hash: Hash(nil)}
		if err == nil {
			e.Existed, e.Hash, e.Stored = true, Hash(data), fmt.Sprintf("file-%d", i)
			if err := os.WriteFile(filepath.Join(tmp, e.Stored), data, 0o600); err != nil {
				return err
			}
		}
		man.Files = append(man.Files, e)
	}
	data, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "manifest.json"), data, 0o600); err != nil {
		return err
	}
	_ = os.RemoveAll(m.opts.ActiveDir)
	return os.Rename(tmp, m.opts.ActiveDir)
}

// Change is a file that differs from what Mailpit runs with.
type Change struct {
	File File
	Diff []diff.Line
	New  bool // not used by the running Mailpit at all
}

// Pending lists files whose content on disk differs from the active
// snapshot (saved in Crew or edited by hand). Secrets are masked.
func (m *Manager) Pending() []Change {
	m.mu.Lock()
	defer m.mu.Unlock()
	act, err := m.loadActive()
	if err != nil {
		return nil
	}
	activeByPath := map[string]activeEntry{}
	for _, e := range act.Files {
		activeByPath[e.Path] = e
	}
	var out []Change
	for _, f := range m.Files() {
		data, _ := os.ReadFile(f.Path)
		a, known := activeByPath[f.Path]
		if known && a.Hash == Hash(data) {
			continue
		}
		var old []byte
		if known && a.Existed {
			old, _ = os.ReadFile(filepath.Join(m.opts.ActiveDir, a.Stored))
		}
		if !known && len(data) == 0 {
			continue
		}
		out = append(out, Change{File: f, New: !known, Diff: diff.Lines(Mask(f.Kind, old), Mask(f.Kind, data), 3)})
	}
	return out
}

// PendingCount returns the number of files not active yet.
func (m *Manager) PendingCount() int { return len(m.Pending()) }

// Mask hides secrets in file content for display. Changed secrets still
// show as changed because a short fingerprint is kept.
func Mask(kind Kind, data []byte) string {
	text := string(data)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		return ""
	}
	var b strings.Builder
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch kind {
		case KindAuth:
			if i := strings.Index(t, ":"); i > 0 && !strings.HasPrefix(t, "#") {
				h := t[i+1:]
				l = t[:i] + ":<" + htpasswd.HashType(h) + " " + htpasswd.Fingerprint(h) + ">"
			}
		case KindEnv:
			if i := strings.Index(t, "="); i > 0 {
				k := strings.TrimSpace(t[:i])
				if key, ok := catalog.Lookup(k); ok && key.Sensitive {
					l = k + "=<secret " + htpasswd.Fingerprint(t[i+1:]) + ">"
				}
			}
		case KindRelay, KindForward:
			if i := strings.Index(t, ":"); i > 0 {
				k := strings.TrimSpace(t[:i])
				if k == "password" || k == "secret" {
					l = l[:strings.Index(l, ":")+1] + " <secret " + htpasswd.Fingerprint(strings.TrimSpace(t[i+1:])) + ">"
				}
			}
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

// Validate checks every managed file on disk.
func (m *Manager) Validate() []verify.Issue {
	var out []verify.Issue
	for _, f := range m.Files() {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			if f.Kind != KindEnv {
				continue // missing referenced files are reported by the env check
			}
			out = append(out, verify.Issue{Msg: f.Path + ": " + err.Error()})
			continue
		}
		for _, i := range verify.Errors(m.Verify(f.Kind, f.Path, data)) {
			i.Msg = f.Label + ": " + i.Msg
			out = append(out, i)
		}
	}
	return out
}

// --- restart and rollback ---

// Failure returns the last failed restart, or nil.
func (m *Manager) Failure() *Failure {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failure
}

// ErrBusy is returned when a restart or version switch is already running.
var ErrBusy = errors.New("Mailpit is being restarted, try again in a moment")

// Exclusive runs fn while no restart or rollback can run.
func (m *Manager) Exclusive(fn func() error) error {
	if !m.restartMu.TryLock() {
		return ErrBusy
	}
	defer m.restartMu.Unlock()
	return fn()
}

// Restart checks all files, restarts Mailpit and waits until it is ready
// and stable. On success the files become the active snapshot. On failure
// nothing is rolled back: the error is kept until a fix or rollback.
func (m *Manager) Restart(ctx context.Context) error {
	return m.Exclusive(func() error {
		if errs := m.Validate(); len(errs) > 0 {
			return &ValidationError{Issues: errs}
		}
		return m.restartLocked(ctx)
	})
}

func (m *Manager) restartLocked(ctx context.Context) error {
	if err := m.RestartAndWait(ctx); err != nil {
		f := &Failure{Time: time.Now(), Error: err.Error(), LogTail: m.LogTail()}
		m.mu.Lock()
		m.failure = f
		m.mu.Unlock()
		m.opts.Log.Error("mailpit restart failed", "error", err)
		return err
	}
	m.mu.Lock()
	m.failure = nil
	err := m.snapshotLocked()
	m.mu.Unlock()
	if m.opts.Started != nil {
		m.opts.Started()
	}
	return err
}

// Rollback restores the files Mailpit last started with and restarts it.
func (m *Manager) Rollback(ctx context.Context) ([]string, error) {
	var restored []string
	err := m.Exclusive(func() error {
		m.mu.Lock()
		act, err := m.loadActive()
		if err != nil {
			m.mu.Unlock()
			return errors.New("no working configuration recorded yet")
		}
		for _, e := range act.Files {
			cur, _ := os.ReadFile(e.Path)
			if Hash(cur) == e.Hash {
				continue
			}
			if e.Existed {
				data, err := os.ReadFile(filepath.Join(m.opts.ActiveDir, e.Stored))
				if err != nil {
					m.mu.Unlock()
					return err
				}
				if len(cur) > 0 {
					_ = m.backup(e.Path, cur)
				}
				if err := fsutil.WriteAtomic(e.Path, data, fileMode(e.Kind)); err != nil {
					m.mu.Unlock()
					return err
				}
				restored = append(restored, e.Path)
			}
		}
		m.mu.Unlock()
		return m.restartLocked(ctx)
	})
	return restored, err
}

// RestartAndWait restarts Mailpit and waits until it is ready and stable.
func (m *Manager) RestartAndWait(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, m.opts.Timeout+m.opts.StableFor+5*time.Second)
	defer cancel()
	if err := m.opts.Restarter.Restart(ctx); err != nil {
		return err
	}
	if m.opts.Ready == nil {
		return nil
	}
	wctx, wcancel := context.WithTimeout(ctx, m.opts.Timeout)
	defer wcancel()
	if err := m.opts.Ready.WaitReady(wctx); err != nil {
		return fmt.Errorf("Mailpit did not become ready within %s: %v", m.opts.Timeout, err)
	}
	// Mailpit can report ready and still exit a moment later (for example
	// when the SMTP port is taken), so it must stay up for a while.
	select {
	case <-time.After(m.opts.StableFor):
	case <-ctx.Done():
		return ctx.Err()
	}
	up, err := m.opts.Ready.Uptime(ctx)
	if err != nil {
		return fmt.Errorf("Mailpit stopped responding after start: %v", err)
	}
	if up < m.opts.StableFor {
		return fmt.Errorf("Mailpit restarted again shortly after start (uptime %s), check the log", up.Round(time.Second))
	}
	return nil
}

// MarkStarted records the current files as active after Mailpit was started
// successfully outside Restart (version switch, first start).
func (m *Manager) MarkStarted() error {
	m.mu.Lock()
	m.failure = nil
	err := m.snapshotLocked()
	m.mu.Unlock()
	if m.opts.Started != nil {
		m.opts.Started()
	}
	return err
}

// LogTail returns recent Mailpit output.
func (m *Manager) LogTail() []string {
	lines := m.opts.Restarter.LogTail()
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return lines
}

// RepairLocked sets locked values in the env file on disk. It returns the
// keys it changed. Used at startup, before Mailpit runs.
func (m *Manager) RepairLocked() ([]string, error) {
	if len(m.opts.Locked) == 0 {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.opts.EnvFile)
	if err != nil {
		return nil, err
	}
	ef := envfile.Parse(data)
	var changed []string
	keys := make([]string, 0, len(m.opts.Locked))
	for k := range m.opts.Locked {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		want := m.opts.Locked[k]
		have, set := ef.Get(k)
		if (want == "" && !set) || (want != "" && have == want) {
			continue
		}
		if want == "" {
			ef.Delete(k)
		} else if err := ef.Set(k, want); err != nil {
			return nil, err
		}
		changed = append(changed, k)
	}
	if len(changed) == 0 {
		return nil, nil
	}
	return changed, fsutil.WriteAtomic(m.opts.EnvFile, ef.Bytes(), 0o600)
}
