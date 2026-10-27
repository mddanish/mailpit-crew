package managed

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mddanish/mailpit-crew/internal/catalog"
	"github.com/mddanish/mailpit-crew/internal/envfile"
	"github.com/mddanish/mailpit-crew/internal/fsutil"
	"github.com/mddanish/mailpit-crew/internal/htpasswd"
)

// Import copies an existing Mailpit setup (its env file and the files it
// points to) into the managed layout. Mailpit must be stopped first so the
// database copy is consistent. Web UI accounts are added to ui.auth next to
// Crew's service account.
func Import(l Layout, oldEnvPath string) ([]string, error) {
	if _, err := os.Stat(l.DB); err == nil {
		return nil, fmt.Errorf("%s already exists: import only works on a new setup", l.DB)
	}
	data, err := os.ReadFile(oldEnvPath)
	if err != nil {
		return nil, err
	}
	old := envfile.Parse(data)
	cur, err := os.ReadFile(l.EnvFile)
	if err != nil {
		return nil, err
	}
	env := envfile.Parse(cur)
	var notes []string

	copyTo := func(src, dest string, mode os.FileMode) error {
		if err := copyFile(src, dest, mode); err != nil {
			return fmt.Errorf("copy %s: %w", src, err)
		}
		notes = append(notes, "copied "+src+" -> "+dest)
		return nil
	}
	sslCopy := func(src string) (string, error) {
		if err := os.MkdirAll(l.SSL, 0o750); err != nil {
			return "", err
		}
		dest := filepath.Join(l.SSL, filepath.Base(src))
		if _, err := os.Stat(dest); err == nil {
			return dest, nil // already copied for another setting
		}
		mode := os.FileMode(0o644)
		if strings.Contains(strings.ToLower(filepath.Base(src)), "key") {
			mode = 0o600
		}
		return dest, copyTo(src, dest, mode)
	}

	for _, k := range old.Keys() {
		v, _ := old.Get(k)
		if v == "" {
			continue
		}
		switch k {
		case "MP_DATABASE":
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if _, err := os.Stat(v + suffix); err == nil {
					if err := copyTo(v+suffix, l.DB+suffix, 0o640); err != nil {
						return notes, err
					}
				}
			}
		case "MP_UI_AUTH_FILE":
			n, err := mergeAccounts(v, l.UIAuth)
			if err != nil {
				return notes, err
			}
			notes = append(notes, n...)
		case "MP_UI_AUTH", "MP_WEBROOT", "MP_LOG_FILE":
			notes = append(notes, "skipped "+k+" (managed by Crew)")
		case "MP_SMTP_AUTH_FILE", "MP_POP3_AUTH_FILE", "MP_SEND_API_AUTH_FILE":
			dest := l.StandardFile(k)
			if filepath.Clean(v) == filepath.Clean(old.Map()["MP_UI_AUTH_FILE"]) {
				notes = append(notes, k+" used the web UI password file; it now has its own copy in "+dest)
			}
			if err := copyTo(v, dest, 0o600); err != nil {
				return notes, err
			}
			_ = env.Set(k, dest)
		case "MP_TAGS_CONFIG", "MP_SMTP_RELAY_CONFIG", "MP_SMTP_FORWARD_CONFIG":
			dest := l.StandardFile(k)
			mode := os.FileMode(0o600)
			if k == "MP_TAGS_CONFIG" {
				mode = 0o644
			}
			if err := copyTo(v, dest, mode); err != nil {
				return notes, err
			}
			_ = env.Set(k, dest)
		default:
			if key, ok := catalog.Lookup(k); ok && key.Kind == catalog.File {
				dest, err := sslCopy(v)
				if err != nil {
					return notes, err
				}
				_ = env.Set(k, dest)
				continue
			}
			if err := env.Set(k, v); err != nil {
				return notes, err
			}
		}
	}
	if err := fsutil.WriteAtomic(l.EnvFile, env.Bytes(), 0o600); err != nil {
		return notes, err
	}
	for _, e := range catalog.ValidateEnv(env.Map()) {
		notes = append(notes, "check setting: "+e.Error())
	}
	return notes, nil
}

// mergeAccounts adds the accounts of src to the password file dest.
func mergeAccounts(src, dest string) ([]string, error) {
	in, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	cur, err := os.ReadFile(dest)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f := htpasswd.Parse(cur)
	var notes []string
	for _, e := range htpasswd.Parse(in).Entries() {
		if e.User == ServiceUser {
			continue
		}
		if _, exists := f.Get(e.User); exists {
			notes = append(notes, "web UI account "+e.User+" already exists, kept it")
			continue
		}
		if err := f.Set(e.User, e.Hash); err != nil {
			return notes, err
		}
		notes = append(notes, "web UI account "+e.User+" imported (same password)")
	}
	return notes, fsutil.WriteAtomic(dest, f.Bytes(), 0o600)
}

func copyFile(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".import-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}
