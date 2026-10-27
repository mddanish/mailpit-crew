// Package users stores the Crew admin login.
package users

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mddanish/mailpit-crew/internal/fsutil"
	"github.com/mddanish/mailpit-crew/internal/htpasswd"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// AdminName is the name of the Crew login created on first start.
const AdminName = "admin"

// User is a Crew login.
type User struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"` // bcrypt hash
}

type fileFormat struct {
	Users []User `yaml:"users"`
}

// Store is the users file. Crew has one admin login; older files with more
// users still load, and every user in them is an admin.
type Store struct {
	mu    sync.RWMutex
	path  string
	users []User
}

// dummyHash makes failed logins for unknown users take as long as real ones.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), htpasswd.BcryptCost)

// Open loads the users file. A missing file gives an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f fileFormat
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("users file %s: %w", path, err)
	}
	for i, u := range f.Users {
		if err := htpasswd.ValidUsername(u.Username); err != nil {
			return nil, fmt.Errorf("users file %s: user %d: %w", path, i+1, err)
		}
	}
	s.users = f.Users
	return s, nil
}

func (s *Store) saveLocked() error {
	data, err := yaml.Marshal(fileFormat{Users: s.users})
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

// Count returns the number of logins.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users)
}

// Get returns a copy of a login.
func (s *Store) Get(username string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.Username == username {
			return u, true
		}
	}
	return User{}, false
}

// Authenticate checks a username and password.
func (s *Store) Authenticate(username, password string) (User, bool) {
	u, ok := s.Get(username)
	if !ok {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return User{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password)) != nil {
		return User{}, false
	}
	return u, true
}

// SetPassword sets a new password for a login and saves the file. The admin
// login is created when it does not exist yet.
func (s *Store) SetPassword(username, password string) error {
	if err := htpasswd.ValidUsername(username); err != nil {
		return err
	}
	h, err := htpasswd.HashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.users {
		if s.users[i].Username == username {
			s.users[i].Password = h
			return s.saveLocked()
		}
	}
	if username != AdminName || len(s.users) > 0 {
		return fmt.Errorf("user %q not found", username)
	}
	s.users = append(s.users, User{Username: username, Password: h})
	return s.saveLocked()
}

// Bootstrap creates the admin login with a random password when the store
// is empty. It returns the password, or "" when nothing was created.
func (s *Store) Bootstrap() (string, error) {
	if s.Count() > 0 {
		return "", nil
	}
	pw, err := htpasswd.GeneratePassword(20)
	if err != nil {
		return "", err
	}
	return pw, s.SetPassword(AdminName, pw)
}
