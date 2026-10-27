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
	"github.com/mddanish/mailpit-crew/internal/users"
)

// Import copies an existing Mailpit setup (its env file and the files it
// points to) into the managed layout. Mailpit must be stopped first so the
// database copy is consistent. Web UI accounts become Crew admins with the
// same password.
func Import(l Layout, oldEnvPath string, store *users.Store) ([]string, error) {
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
	copied := map[string]string{}

	copyTo := func(src, dest string, mode os.FileMode) error {
		if prev, ok := copied[src+"|"+dest]; ok && prev == dest {
			return nil
		}
		if err := copyFile(src, dest, mode); err != nil {
			return fmt.Errorf("copy %s: %w", src, err)
		}
		copied[src+"|"+dest] = dest
		notes = append(notes, "copied "+src+" -> "+dest)
		return nil
	}
	sslCopy := func(src string) (string, error) {
		if err := os.MkdirAll(l.SSL, 0o750); err != nil {
			return "", err
		}
		dest := filepath.Join(l.SSL, filepath.Base(src))
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
			n, err := importUIAccounts(v, store)
			if err != nil {
				return notes, err
			}
			notes = append(notes, n...)
		case "MP_UI_TLS_CERT", "MP_UI_TLS_KEY":
			dest, err := sslCopy(v)
			if err != nil {
				return notes, err
			}
			notes = append(notes, fmt.Sprintf("%s is now served by Crew: set tls.cert/tls.key in the Crew config (copied to %s)", k, dest))
		case "MP_UI_AUTH", "MP_UI_BIND_ADDR", "MP_WEBROOT", "MP_LOG_FILE", "MP_TAGS_USERNAME":
			notes = append(notes, "skipped "+k+" (managed by Crew)")
		case "MP_SMTP_AUTH_FILE", "MP_POP3_AUTH_FILE", "MP_SEND_API_AUTH_FILE":
			dest := l.AuthFile(k)
			if err := copyTo(v, dest, 0o600); err != nil {
				return notes, err
			}
			_ = env.Set(k, dest)
		case "MP_TAGS_CONFIG":
			if err := copyTo(v, l.Tags, 0o644); err != nil {
				return notes, err
			}
			_ = env.Set(k, l.Tags)
		default:
			if key, ok := catalog.Lookup(k); ok && key.Kind == catalog.File {
				var dest string
				if key.PairWith != "" {
					dest, err = sslCopy(v)
				} else {
					dest = filepath.Join(l.Dir, filepath.Base(v))
					err = copyTo(v, dest, 0o640)
				}
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
	if err := fsutil.WriteAtomic(l.EnvFile, env.Bytes(), 0o640); err != nil {
		return notes, err
	}
	for _, e := range catalog.ValidateEnv(env.Map()) {
		notes = append(notes, "check setting: "+e.Error())
	}
	return notes, nil
}

func importUIAccounts(path string, store *users.Store) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, e := range htpasswd.Parse(data).Entries() {
		if e.User == ServiceUser {
			continue
		}
		if _, exists := store.Get(e.User); exists {
			notes = append(notes, "Crew user "+e.User+" already exists, kept it")
			continue
		}
		if err := store.AddHash(e.User, e.Hash, users.RoleAdmin); err != nil {
			notes = append(notes, "could not import web UI account "+e.User+": "+err.Error())
			continue
		}
		notes = append(notes, "web UI account "+e.User+" is now a Crew admin (same password)")
	}
	return notes, nil
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
