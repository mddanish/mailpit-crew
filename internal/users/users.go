// Package users stores Crew users (people who log in to MailPit-Crew).
package users

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/mddanish/mailpit-crew/internal/fsutil"
	"github.com/mddanish/mailpit-crew/internal/htpasswd"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// Roles.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// Rule is an ownership rule (used from phase 2).
type Rule struct {
	Type  string `yaml:"type"`
	Value string `yaml:"value"`
}

// User is one Crew user.
type User struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"` // bcrypt hash
	Role     string `yaml:"role"`
	Enabled  *bool  `yaml:"enabled,omitempty"`
	Rules    []Rule `yaml:"rules,omitempty"`
}

// IsEnabled reports whether the user may log in (default true).
func (u User) IsEnabled() bool { return u.Enabled == nil || *u.Enabled }

// IsAdmin reports whether the user has the admin role.
func (u User) IsAdmin() bool { return u.Role == RoleAdmin }

type fileFormat struct {
	Users []User `yaml:"users"`
}

// Store is the users file.
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
	seen := map[string]bool{}
	for i, u := range f.Users {
		if err := htpasswd.ValidUsername(u.Username); err != nil {
			return nil, fmt.Errorf("users file %s: user %d: %w", path, i+1, err)
		}
		if seen[u.Username] {
			return nil, fmt.Errorf("users file %s: duplicate user %q", path, u.Username)
		}
		seen[u.Username] = true
		if u.Role != RoleAdmin && u.Role != RoleUser {
			return nil, fmt.Errorf("users file %s: user %q: role must be admin or user", path, u.Username)
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

// Count returns the number of users.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users)
}

// Get returns a copy of a user.
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
	if !u.IsEnabled() {
		return User{}, false
	}
	return u, true
}

// SetPassword sets a new password for an existing user and saves the file.
func (s *Store) SetPassword(username, password string) error {
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
	return fmt.Errorf("user %q not found", username)
}

// Add adds a new user and saves the file.
func (s *Store) Add(username, password, role string) error {
	if err := htpasswd.ValidUsername(username); err != nil {
		return err
	}
	if role != RoleAdmin && role != RoleUser {
		return errors.New("role must be admin or user")
	}
	h, err := htpasswd.HashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.Username == username {
			return fmt.Errorf("user %q already exists", username)
		}
	}
	s.users = append(s.users, User{Username: username, Password: h, Role: role})
	return s.saveLocked()
}

// List returns copies of all users, sorted by name.
func (s *Store) List() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]User(nil), s.users...)
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// ErrLastAdmin is returned when a change would leave no enabled admin.
var ErrLastAdmin = errors.New("at least one enabled admin must remain")

func (s *Store) enabledAdminsLocked(except string) int {
	n := 0
	for _, u := range s.users {
		if u.Username != except && u.IsAdmin() && u.IsEnabled() {
			n++
		}
	}
	return n
}

// Update changes a user with fn and saves the file. The username and
// password hash cannot be changed with Update.
func (s *Store) Update(username string, fn func(u *User) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.users {
		if s.users[i].Username != username {
			continue
		}
		u := s.users[i]
		u.Rules = append([]Rule(nil), u.Rules...)
		if err := fn(&u); err != nil {
			return err
		}
		if u.Role != RoleAdmin && u.Role != RoleUser {
			return errors.New("role must be admin or user")
		}
		u.Username, u.Password = s.users[i].Username, s.users[i].Password
		if (!u.IsAdmin() || !u.IsEnabled()) && s.enabledAdminsLocked(username) == 0 {
			return ErrLastAdmin
		}
		old := s.users[i]
		s.users[i] = u
		if err := s.saveLocked(); err != nil {
			s.users[i] = old
			return err
		}
		return nil
	}
	return fmt.Errorf("user %q not found", username)
}

// Delete removes a user and saves the file.
func (s *Store) Delete(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, u := range s.users {
		if u.Username != username {
			continue
		}
		if s.enabledAdminsLocked(username) == 0 {
			return ErrLastAdmin
		}
		old := s.users
		s.users = append(append([]User(nil), s.users[:i]...), s.users[i+1:]...)
		if err := s.saveLocked(); err != nil {
			s.users = old
			return err
		}
		return nil
	}
	return fmt.Errorf("user %q not found", username)
}

// AddHash adds a user with an existing bcrypt hash (for imports).
func (s *Store) AddHash(username, hash, role string) error {
	if err := htpasswd.ValidUsername(username); err != nil {
		return err
	}
	if htpasswd.HashType(hash) != "bcrypt" {
		return errors.New("only bcrypt hashes can be imported")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.Username == username {
			return fmt.Errorf("user %q already exists", username)
		}
	}
	s.users = append(s.users, User{Username: username, Password: hash, Role: role})
	return s.saveLocked()
}

// Bootstrap creates an "admin" user with a random password when the store
// is empty. It returns the password, or "" when nothing was created.
func (s *Store) Bootstrap() (string, error) {
	if s.Count() > 0 {
		return "", nil
	}
	pw, err := htpasswd.GeneratePassword(20)
	if err != nil {
		return "", err
	}
	if err := s.Add("admin", pw, RoleAdmin); err != nil {
		return "", err
	}
	return pw, nil
}
