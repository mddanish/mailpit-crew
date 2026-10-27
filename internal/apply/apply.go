// Package apply stages changes to Mailpit's files and applies them safely:
// validate, back up, write atomically, restart, health check and roll back
// on failure.
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

	"github.com/mamber/mailpit-crew/internal/catalog"
	"github.com/mamber/mailpit-crew/internal/diff"
	"github.com/mamber/mailpit-crew/internal/envfile"
	"github.com/mamber/mailpit-crew/internal/fsutil"
	"github.com/mamber/mailpit-crew/internal/htpasswd"
	"github.com/mamber/mailpit-crew/internal/restart"
	"github.com/mamber/mailpit-crew/internal/tagsfile"
)

// Kind is the type of a managed file.
type Kind string

// Managed file kinds.
const (
	KindEnv  Kind = "env"
	KindAuth Kind = "auth"
	KindTags Kind = "tags"
)

// authKeys are the env keys that point to htpasswd files, in display order.
var authKeys = []string{"MP_UI_AUTH_FILE", "MP_SMTP_AUTH_FILE", "MP_POP3_AUTH_FILE", "MP_SEND_API_AUTH_FILE"}

// File is one managed Mailpit file.
type File struct {
	ID       string
	Kind     Kind
	Path     string
	Label    string
	Uses     []string // env keys that point to this file
	Writable bool     // inside a managed directory
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
	EnvFile     string            // Mailpit env file, may be empty
	Overrides   map[string]string // env key -> path override
	ManagedDirs []string          // Crew only writes inside these
	BackupDir   string
	Keep        int
	Restarter   restart.Restarter
	Ready       Readiness
	Timeout     time.Duration
	StableFor   time.Duration // Mailpit must stay up this long after a restart (default 5s)
	ServiceUser string        // Crew's Mailpit API account, must stay in the UI auth file
	Log         *slog.Logger
}

type staged struct {
	file        File
	base        []byte
	baseExisted bool
	content     []byte
}

// Result describes one apply run.
type Result struct {
	Time          time.Time
	User          string
	OK            bool
	Files         []string
	Error         string
	RolledBack    bool
	RollbackError string
	BackupID      string
	LogTail       []string
}

// Manager holds staged changes.
type Manager struct {
	opts     Options
	mu       sync.Mutex
	staged   map[string]*staged // by path
	applying sync.Mutex
	last     *Result
}

// ErrNoEnvFile is returned when no Mailpit env file is configured.
var ErrNoEnvFile = errors.New("no Mailpit env file configured")

// New returns a Manager.
func New(opts Options) *Manager {
	if opts.Keep < 1 {
		opts.Keep = 20
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.StableFor == 0 {
		opts.StableFor = 5 * time.Second
	}
	if opts.EnvFile != "" {
		if abs, err := filepath.Abs(opts.EnvFile); err == nil {
			opts.EnvFile = abs
		}
	}
	return &Manager{opts: opts, staged: map[string]*staged{}}
}

// ServiceUser returns the protected Mailpit API account name.
func (m *Manager) ServiceUser() string { return m.opts.ServiceUser }

func (m *Manager) writable(path string) bool {
	for _, d := range m.opts.ManagedDirs {
		rel, err := filepath.Rel(d, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func fileID(kind Kind, path string) string {
	if kind == KindEnv || kind == KindTags {
		return string(kind)
	}
	s := sha256.Sum256([]byte(path))
	return "auth-" + hex.EncodeToString(s[:4])
}

// readLocked returns the working content of path (staged or disk).
func (m *Manager) readLocked(path string) ([]byte, bool, error) {
	if s, ok := m.staged[path]; ok {
		return append([]byte(nil), s.content...), true, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, false, err
}

// workingEnvLocked returns the working env file, or nil when not configured.
func (m *Manager) workingEnvLocked() (*envfile.File, error) {
	if m.opts.EnvFile == "" {
		return nil, ErrNoEnvFile
	}
	data, _, err := m.readLocked(m.opts.EnvFile)
	if err != nil {
		return nil, err
	}
	return envfile.Parse(data), nil
}

func (m *Manager) resolve(env map[string]string, key string) string {
	p := env[key]
	if o := m.opts.Overrides[key]; o != "" {
		p = o
	}
	if p == "" || !filepath.IsAbs(p) {
		// relative paths depend on Mailpit's working directory; validation
		// rejects them, so they are not managed
		return ""
	}
	return filepath.Clean(p)
}

func (m *Manager) filesLocked() []File {
	env := map[string]string{}
	var out []File
	if ef, err := m.workingEnvLocked(); err == nil {
		env = ef.Map()
		out = append(out, m.newFile(KindEnv, m.opts.EnvFile, "Mailpit settings", nil))
	}
	byPath := map[string]int{}
	for _, k := range authKeys {
		p := m.resolve(env, k)
		if p == "" {
			continue
		}
		if i, ok := byPath[p]; ok {
			out[i].Uses = append(out[i].Uses, k)
			continue
		}
		byPath[p] = len(out)
		out = append(out, m.newFile(KindAuth, p, "Password file", []string{k}))
	}
	for i := range out {
		if out[i].Kind == KindAuth {
			var labels []string
			for _, k := range out[i].Uses {
				key, _ := catalog.Lookup(k)
				labels = append(labels, key.AuthFile)
			}
			out[i].Label = strings.Join(labels, " + ") + " accounts"
		}
	}
	if p := m.resolve(env, "MP_TAGS_CONFIG"); p != "" {
		out = append(out, m.newFile(KindTags, p, "Tag rules", []string{"MP_TAGS_CONFIG"}))
	}
	return out
}

func (m *Manager) newFile(kind Kind, path, label string, uses []string) File {
	f := File{ID: fileID(kind, path), Kind: kind, Path: path, Label: label, Uses: uses, Writable: m.writable(path)}
	if _, ok := m.staged[path]; ok {
		f.Exists = true
	} else if _, err := os.Stat(path); err == nil {
		f.Exists = true
	}
	return f
}

// Files returns all managed files, based on the working env file.
func (m *Manager) Files() []File {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.filesLocked()
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

// Read returns the working content of a managed file and whether it is staged.
func (m *Manager) Read(f File) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readLocked(f.Path)
}

// Env returns the working Mailpit env file.
func (m *Manager) Env() (*envfile.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workingEnvLocked()
}

// DiskEnv returns the Mailpit env file as it is on disk (what Mailpit runs with).
func (m *Manager) DiskEnv() (*envfile.File, error) {
	if m.opts.EnvFile == "" {
		return nil, ErrNoEnvFile
	}
	data, err := os.ReadFile(m.opts.EnvFile)
	if err != nil {
		return nil, err
	}
	return envfile.Parse(data), nil
}

// Stage records new content for a managed file. Content equal to the disk
// version removes the staged change.
func (m *Manager) Stage(f File, content []byte) error {
	if !f.Writable {
		return fmt.Errorf("%s is outside the managed directories and cannot be changed", f.Path)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.staged[f.Path]
	if !ok {
		data, err := os.ReadFile(f.Path)
		existed := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		s = &staged{file: f, base: data, baseExisted: existed}
	}
	if s.baseExisted && bytes.Equal(content, s.base) {
		delete(m.staged, f.Path)
		return nil
	}
	s.content = append([]byte(nil), content...)
	m.staged[f.Path] = s
	return nil
}

// Discard drops the staged change of one file.
func (m *Manager) Discard(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.staged, path)
}

// DiscardAll drops all staged changes.
func (m *Manager) DiscardAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.staged = map[string]*staged{}
}

// PendingCount returns the number of staged files.
func (m *Manager) PendingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.staged)
}

// Change is one staged file with a masked diff.
type Change struct {
	File     File
	Diff     []diff.Line
	Conflict bool // file changed on disk since it was staged
}

// Pending returns staged changes with secrets masked.
func (m *Manager) Pending() []Change {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Change
	for _, s := range m.staged {
		disk, _ := os.ReadFile(s.file.Path)
		out = append(out, Change{
			File:     s.file,
			Diff:     diff.Lines(Mask(s.file.Kind, s.base), Mask(s.file.Kind, s.content), 3),
			Conflict: !bytes.Equal(disk, s.base),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File.Path < out[j].File.Path })
	return out
}

// Mask hides secrets in file content for display. Changed secrets still
// show as changed because a short fingerprint is kept.
func Mask(kind Kind, data []byte) string {
	text := string(data)
	switch kind {
	case KindAuth:
		f := htpasswd.Parse(data)
		masked := map[string]string{}
		for _, e := range f.Entries() {
			masked[e.User] = e.User + ":<" + htpasswd.HashType(e.Hash) + " " + htpasswd.Fingerprint(e.Hash) + ">"
		}
		var b strings.Builder
		for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
			t := strings.TrimSpace(l)
			if i := strings.Index(t, ":"); i > 0 {
				if v, ok := masked[t[:i]]; ok {
					l = v
				}
			}
			b.WriteString(l + "\n")
		}
		return b.String()
	case KindEnv:
		var b strings.Builder
		for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
			t := strings.TrimSpace(l)
			if i := strings.Index(t, "="); i > 0 {
				k := strings.TrimSpace(t[:i])
				if key, ok := catalog.Lookup(k); ok && key.Sensitive {
					l = k + "=<secret " + htpasswd.Fingerprint(t[i+1:]) + ">"
				}
			}
			b.WriteString(l + "\n")
		}
		return b.String()
	}
	return text
}

// Validate checks all working files and returns problems.
func (m *Manager) Validate() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.validateLocked()
}

func (m *Manager) validateLocked() []string {
	var errs []string
	ef, err := m.workingEnvLocked()
	if err != nil && !errors.Is(err, ErrNoEnvFile) {
		errs = append(errs, "Mailpit settings: "+err.Error())
	}
	if ef != nil {
		for _, l := range ef.Invalid() {
			errs = append(errs, fmt.Sprintf("Mailpit settings: line %d is not KEY=value", l.Number))
		}
		pending := func(p string) bool {
			_, ok := m.staged[filepath.Clean(p)]
			return ok
		}
		for _, e := range catalog.ValidateEnvWith(ef.Map(), pending) {
			errs = append(errs, "Mailpit settings: "+e.Error())
		}
	}
	for _, f := range m.filesLocked() {
		data, _, err := m.readLocked(f.Path)
		if err != nil {
			errs = append(errs, f.Path+": "+err.Error())
			continue
		}
		switch f.Kind {
		case KindAuth:
			if !f.Exists {
				continue // reported by the env validation
			}
			hf := htpasswd.Parse(data)
			for _, n := range hf.Invalid() {
				errs = append(errs, fmt.Sprintf("%s: line %d is not user:hash", f.Path, n))
			}
			if len(hf.Entries()) == 0 {
				errs = append(errs, f.Path+": has no accounts")
			}
			if m.opts.ServiceUser != "" && contains(f.Uses, "MP_UI_AUTH_FILE") {
				if _, ok := hf.Get(m.opts.ServiceUser); !ok {
					errs = append(errs, fmt.Sprintf("%s: Crew service account %q is missing (Crew would lose API access)", f.Path, m.opts.ServiceUser))
				}
			}
		case KindTags:
			if !f.Exists {
				continue
			}
			for _, e := range tagsfile.ValidateBytes(data) {
				errs = append(errs, f.Path+": "+e.Error())
			}
		}
	}
	return errs
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// LastResult returns the result of the last apply, if any.
func (m *Manager) LastResult() *Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

type manifestEntry struct {
	Path    string `json:"path"`
	Stored  string `json:"stored"`
	Existed bool   `json:"existed"`
}

// ErrNothingToApply is returned when there are no staged changes.
var ErrNothingToApply = errors.New("no pending changes")

// ValidationError lists problems that stopped an apply.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "validation failed: " + strings.Join(e.Problems, "; ")
}

// Apply writes all staged changes and restarts Mailpit. Validation and
// conflict errors are returned as errors (nothing changed). Problems after
// files were written are reported in the Result (OK=false).
func (m *Manager) Apply(ctx context.Context, user string) (*Result, error) {
	if !m.applying.TryLock() {
		return nil, errors.New("another apply is running")
	}
	defer m.applying.Unlock()

	m.mu.Lock()
	if len(m.staged) == 0 {
		m.mu.Unlock()
		return nil, ErrNothingToApply
	}
	var conflicts []string
	for p, s := range m.staged {
		disk, err := os.ReadFile(p)
		existed := err == nil
		if existed != s.baseExisted || !bytes.Equal(disk, s.base) {
			conflicts = append(conflicts, p)
		}
	}
	if len(conflicts) > 0 {
		m.mu.Unlock()
		sort.Strings(conflicts)
		return nil, fmt.Errorf("changed on disk since staging, discard and edit again: %s", strings.Join(conflicts, ", "))
	}
	if problems := m.validateLocked(); len(problems) > 0 {
		m.mu.Unlock()
		return nil, &ValidationError{Problems: problems}
	}
	snapshot := map[string]staged{}
	for p, s := range m.staged {
		snapshot[p] = *s
	}
	files := m.filesLocked()
	m.mu.Unlock()

	res := &Result{Time: time.Now(), User: user}
	for p := range snapshot {
		res.Files = append(res.Files, p)
	}
	sort.Strings(res.Files)

	backupID, err := m.backup(files, snapshot)
	if err != nil {
		return nil, fmt.Errorf("backup failed, nothing changed: %w", err)
	}
	res.BackupID = backupID

	var written []string
	for _, p := range res.Files {
		if err := fsutil.WriteAtomic(p, snapshot[p].content, newFileMode(snapshot[p].file.Kind)); err != nil {
			res.Error = fmt.Sprintf("write %s: %v", p, err)
			break
		}
		written = append(written, p)
	}
	if res.Error == "" {
		if err := m.restartAndWait(ctx); err != nil {
			res.Error = err.Error()
		}
	}
	if res.Error == "" {
		res.OK = true
		m.mu.Lock()
		for p, s := range snapshot {
			if cur, ok := m.staged[p]; ok && bytes.Equal(cur.content, s.content) {
				delete(m.staged, p)
			}
		}
		m.last = res
		m.mu.Unlock()
		m.prune()
		m.opts.Log.Info("apply succeeded", "user", user, "files", strings.Join(res.Files, ","), "backup", backupID)
		return res, nil
	}

	m.opts.Log.Error("apply failed, rolling back", "user", user, "error", res.Error)
	res.LogTail = m.logTail()
	var rbErr []string
	for _, p := range written {
		s := snapshot[p]
		if s.baseExisted {
			if err := fsutil.WriteAtomic(p, s.base, newFileMode(s.file.Kind)); err != nil {
				rbErr = append(rbErr, fmt.Sprintf("restore %s: %v", p, err))
			}
		} else if err := os.Remove(p); err != nil {
			rbErr = append(rbErr, fmt.Sprintf("remove %s: %v", p, err))
		}
	}
	if len(written) > 0 {
		if err := m.restartAndWait(ctx); err != nil {
			rbErr = append(rbErr, "restart after rollback: "+err.Error())
		}
	}
	res.RolledBack = len(rbErr) == 0
	res.RollbackError = strings.Join(rbErr, "; ")
	m.mu.Lock()
	m.last = res
	m.mu.Unlock()
	return res, nil
}

// newFileMode is the mode for files Crew creates. Password files stay private.
func newFileMode(k Kind) os.FileMode {
	if k == KindAuth {
		return 0o600
	}
	return 0o644
}

func (m *Manager) restartAndWait(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, m.opts.Timeout)
	defer cancel()
	if err := m.opts.Restarter.Restart(ctx); err != nil {
		return err
	}
	if m.opts.Ready == nil {
		return nil
	}
	// give Mailpit a moment, so a failing start is not hidden by an old process
	select {
	case <-time.After(time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := m.opts.Ready.WaitReady(ctx); err != nil {
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

func (m *Manager) logTail() []string {
	lines := m.opts.Restarter.LogTail()
	if env, err := m.DiskEnv(); err == nil {
		if p := m.resolve(env.Map(), "MP_LOG_FILE"); p != "" {
			lines = append(lines, tailFile(p, 20)...)
		}
	}
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return lines
}

func tailFile(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	const max = 64 * 1024
	off := st.Size() - max
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // first line may be partial
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (m *Manager) backup(files []File, snapshot map[string]staged) (string, error) {
	id := time.Now().UTC().Format("20060102-150405.000")
	dir := filepath.Join(m.opts.BackupDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.Path] = true
	}
	for p := range snapshot {
		paths[p] = true
	}
	var manifest []manifestEntry
	i := 0
	for p := range paths {
		data, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			manifest = append(manifest, manifestEntry{Path: p})
			continue
		}
		if err != nil {
			return "", err
		}
		stored := fmt.Sprintf("file-%d", i)
		i++
		if err := os.WriteFile(filepath.Join(dir, stored), data, 0o600); err != nil {
			return "", err
		}
		manifest = append(manifest, manifestEntry{Path: p, Stored: stored, Existed: true})
	}
	sort.Slice(manifest, func(a, b int) bool { return manifest[a].Path < manifest[b].Path })
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// prune removes old backups beyond the keep limit.
func (m *Manager) prune() {
	entries, err := os.ReadDir(m.opts.BackupDir)
	if err != nil {
		return
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	for len(dirs) > m.opts.Keep {
		_ = os.RemoveAll(filepath.Join(m.opts.BackupDir, dirs[0]))
		dirs = dirs[1:]
	}
}
