package managed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/restart"
)

// Service manages Mailpit binaries and keeps Mailpit running.
type Service struct {
	Layout   Layout
	Releases *Releases
	Script   *restart.Script
	Manager  *apply.Manager
	Log      *slog.Logger

	mu       sync.Mutex
	lastErr  string
	switchMu sync.Mutex
}

// Installed is one binary in the bin directory.
type Installed struct {
	Name    string
	Version string
	Path    string
	Active  bool
	Size    int64
}

var binNameRe = regexp.MustCompile(`^mailpit-[A-Za-z0-9._-]+$`)

// Active returns the active binary path ("" when none is selected).
func (s *Service) Active() string {
	target, err := os.Readlink(s.Layout.Current)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(s.Layout.Bin, target)
	}
	if _, err := os.Stat(target); err != nil {
		return ""
	}
	return target
}

// HasBinary reports whether a Mailpit binary is selected.
func (s *Service) HasBinary() bool { return s.Active() != "" }

// ActiveVersion returns the version of the active binary, from its name.
func (s *Service) ActiveVersion() string {
	a := s.Active()
	if a == "" {
		return ""
	}
	return strings.TrimPrefix(filepath.Base(a), "mailpit-")
}

// Installed lists binaries in the bin directory, newest version first.
func (s *Service) Installed() []Installed {
	entries, err := os.ReadDir(s.Layout.Bin)
	if err != nil {
		return nil
	}
	active := s.Active()
	var out []Installed
	for _, e := range entries {
		if e.IsDir() || !binNameRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		p := filepath.Join(s.Layout.Bin, e.Name())
		out = append(out, Installed{Name: e.Name(), Version: strings.TrimPrefix(e.Name(), "mailpit-"), Path: p, Active: p == active, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return CompareTags(out[i].Version, out[j].Version) > 0 })
	return out
}

// setCurrent points bin/current at path (a file inside the bin dir), or
// removes the link when path is "".
func (s *Service) setCurrent(path string) error {
	if path == "" {
		err := os.Remove(s.Layout.Current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	tmp := s.Layout.Current + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Base(path), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, s.Layout.Current)
}

// Resolve turns a choice into a binary inside the bin dir, downloading or
// copying it when needed. choice is "latest", a tag (v1.28.0), the name of
// an installed binary, or a path to a Mailpit binary elsewhere.
func (s *Service) Resolve(ctx context.Context, choice string) (string, error) {
	choice = strings.TrimSpace(choice)
	switch {
	case choice == "":
		return "", errors.New("no version given")
	case choice == "latest":
		l, err := s.Releases.Latest(ctx, 1)
		if err != nil {
			return "", err
		}
		if len(l) == 0 {
			return "", errors.New("no Mailpit releases found")
		}
		return s.Releases.Download(ctx, l[0].Tag, s.Layout.Bin)
	case ValidTag(choice):
		return s.Releases.Download(ctx, choice, s.Layout.Bin)
	case binNameRe.MatchString(choice) && !strings.Contains(choice, "/"):
		p := filepath.Join(s.Layout.Bin, choice)
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s is not installed", choice)
		}
		return p, nil
	case filepath.IsAbs(choice):
		return s.copyBinary(ctx, choice)
	}
	return "", fmt.Errorf("%q is not a version (v1.28.0), \"latest\" or an absolute path", choice)
}

// copyBinary copies a Mailpit binary into the bin dir as mailpit-<version>.
func (s *Service) copyBinary(ctx context.Context, src string) (string, error) {
	st, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file", src)
	}
	v, err := BinaryVersion(ctx, src)
	if err != nil {
		return "", err
	}
	name := "mailpit-" + v
	if !ValidTag(v) || !binNameRe.MatchString(name) {
		name = "mailpit-custom-" + time.Now().UTC().Format("20060102-150405")
	}
	dest := filepath.Join(s.Layout.Bin, name)
	if _, err := os.Stat(dest); err == nil && ValidTag(v) {
		return dest, nil // same release already installed
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(s.Layout.Bin, ".copy-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return dest, os.Rename(tmp.Name(), dest)
}

// Use resolves choice, makes it the active binary and restarts Mailpit. If
// Mailpit does not come up, the previous binary is restored.
func (s *Service) Use(ctx context.Context, choice string) (string, error) {
	if !s.switchMu.TryLock() {
		return "", errors.New("a version change is already running")
	}
	defer s.switchMu.Unlock()
	bin, err := s.Resolve(ctx, choice)
	if err != nil {
		return "", err
	}
	err = s.Manager.Exclusive(func() error {
		prev := s.Active()
		if err := s.setCurrent(bin); err != nil {
			return err
		}
		if err := s.Manager.RestartAndWait(ctx); err != nil {
			s.Log.Error("mailpit failed with new binary, rolling back", "binary", bin, "error", err)
			if rbErr := s.setCurrent(prev); rbErr != nil {
				return fmt.Errorf("%v; restoring previous binary failed: %v", err, rbErr)
			}
			if prev == "" {
				_ = s.Script.Stop(context.Background())
				return fmt.Errorf("%v (no previous version to go back to)", err)
			}
			if rbErr := s.Manager.RestartAndWait(context.Background()); rbErr != nil {
				return fmt.Errorf("%v; previous version also failed: %v", err, rbErr)
			}
			return fmt.Errorf("%v (rolled back to %s)", err, filepath.Base(prev))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	s.Log.Info("mailpit version changed", "binary", filepath.Base(bin))
	return filepath.Base(bin), nil
}

// Remove deletes an installed binary that is not active.
func (s *Service) Remove(name string) error {
	if !binNameRe.MatchString(name) || strings.Contains(name, "/") {
		return errors.New("invalid name")
	}
	p := filepath.Join(s.Layout.Bin, name)
	if p == s.Active() {
		return errors.New("cannot remove the active version")
	}
	return os.Remove(p)
}

// Start starts Mailpit if a binary is selected.
func (s *Service) Start() error {
	if !s.HasBinary() {
		return nil
	}
	return s.Script.Start()
}

// Running reports whether Mailpit is running.
func (s *Service) Running() bool { return s.Script.Running() }

// LastError returns the last watchdog error.
func (s *Service) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Watch starts Mailpit again when it stopped, until ctx ends.
func (s *Service) Watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !s.HasBinary() || s.Running() {
			continue
		}
		err := s.Manager.Exclusive(func() error {
			if s.Running() {
				return nil
			}
			s.Log.Warn("mailpit is not running, starting it")
			return s.Script.Start()
		})
		s.mu.Lock()
		if err != nil {
			s.lastErr = err.Error()
		} else {
			s.lastErr = ""
		}
		s.mu.Unlock()
		if err != nil {
			s.Log.Error("watchdog", "error", err)
		}
	}
}
