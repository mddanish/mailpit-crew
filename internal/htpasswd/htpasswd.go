// Package htpasswd reads and writes htpasswd-style password files
// ("user:hash" per line) as used by Mailpit, preserving unknown lines.
package htpasswd

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is the cost used for new password hashes.
const BcryptCost = 10

type line struct {
	raw  string
	user string // empty for comments, blank and invalid lines
}

// File is a parsed htpasswd file.
type File struct {
	lines []line
}

// Entry is one account.
type Entry struct {
	User string
	Hash string
}

// Parse parses an htpasswd file.
func Parse(data []byte) *File {
	f := &File{}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if text == "" {
		return f
	}
	parts := strings.Split(text, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for _, raw := range parts {
		f.lines = append(f.lines, line{raw: raw, user: parseUser(raw)})
	}
	return f
}

func parseUser(raw string) string {
	t := strings.TrimSpace(raw)
	if t == "" || strings.HasPrefix(t, "#") {
		return ""
	}
	i := strings.Index(t, ":")
	if i < 1 || i == len(t)-1 {
		return ""
	}
	return t[:i]
}

// Entries returns all accounts in file order.
func (f *File) Entries() []Entry {
	var out []Entry
	for _, l := range f.lines {
		if l.user != "" {
			t := strings.TrimSpace(l.raw)
			out = append(out, Entry{User: l.user, Hash: t[len(l.user)+1:]})
		}
	}
	return out
}

// Invalid returns line numbers (1-based) of lines that are not accounts,
// comments or blank.
func (f *File) Invalid() []int {
	var out []int
	for i, l := range f.lines {
		t := strings.TrimSpace(l.raw)
		if l.user == "" && t != "" && !strings.HasPrefix(t, "#") {
			out = append(out, i+1)
		}
	}
	return out
}

// Get returns the hash for user.
func (f *File) Get(user string) (string, bool) {
	for _, e := range f.Entries() {
		if e.User == user {
			return e.Hash, true
		}
	}
	return "", false
}

// Set adds or replaces the hash for user.
func (f *File) Set(user, hash string) error {
	if err := ValidUsername(user); err != nil {
		return err
	}
	if hash == "" || strings.ContainsAny(hash, "\n\r") {
		return fmt.Errorf("invalid hash")
	}
	raw := user + ":" + hash
	for i, l := range f.lines {
		if l.user == user {
			f.lines[i].raw = raw
			return nil
		}
	}
	f.lines = append(f.lines, line{raw: raw, user: user})
	return nil
}

// Delete removes user. It reports whether the user existed.
func (f *File) Delete(user string) bool {
	found := false
	out := f.lines[:0]
	for _, l := range f.lines {
		if l.user == user {
			found = true
			continue
		}
		out = append(out, l)
	}
	f.lines = out
	return found
}

// Bytes renders the file.
func (f *File) Bytes() []byte {
	var b strings.Builder
	for _, l := range f.lines {
		b.WriteString(l.raw)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// ValidUsername checks a username is safe to store.
func ValidUsername(u string) error {
	if u == "" {
		return fmt.Errorf("username is required")
	}
	if len(u) > 128 {
		return fmt.Errorf("username is too long")
	}
	if strings.ContainsAny(u, ": \t\r\n#") {
		return fmt.Errorf("username must not contain spaces, ':' or '#'")
	}
	return nil
}

// HashPassword returns a bcrypt hash of password.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password is required")
	}
	if len(password) > 72 {
		return "", fmt.Errorf("password must be 72 bytes or less")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// HashType returns a short label for the hash format.
func HashType(hash string) string {
	switch {
	case strings.HasPrefix(hash, "$2a$"), strings.HasPrefix(hash, "$2b$"), strings.HasPrefix(hash, "$2y$"):
		return "bcrypt"
	case strings.HasPrefix(hash, "$apr1$"):
		return "md5 (apr1)"
	case strings.HasPrefix(hash, "{SHA}"):
		return "sha1"
	case strings.HasPrefix(hash, "$5$"):
		return "sha256-crypt"
	case strings.HasPrefix(hash, "$6$"):
		return "sha512-crypt"
	default:
		return "other"
	}
}

// Fingerprint returns a short, non-reversible label for a hash, for display.
func Fingerprint(hash string) string {
	s := sha256.Sum256([]byte(hash))
	return hex.EncodeToString(s[:4])
}

const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratePassword returns a random password of n characters.
func GeneratePassword(n int) (string, error) {
	b := make([]byte, n)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := range b {
		x, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = passwordAlphabet[x.Int64()]
	}
	return string(b), nil
}
