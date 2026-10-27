// Package fsutil has file helpers shared by Crew packages.
package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"syscall"
)

// WriteAtomic writes data to path via a temp file and rename. An existing
// file keeps its mode and owner; a new file gets mode perm.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	mode := perm
	uid, gid := -1, -1
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
		if s, ok := st.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(s.Uid), int(s.Gid)
		}
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".crew-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if uid >= 0 {
		_ = tmp.Chown(uid, gid) // only works as root; keep going otherwise
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// Hash returns a hex SHA-256 of data.
func Hash(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}
