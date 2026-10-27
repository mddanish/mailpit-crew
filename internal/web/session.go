package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

// flash is a one-time message shown after a redirect.
type flash struct {
	Kind   string // ok, error, info
	Text   string
	Secret string   // shown once in a code box (generated passwords)
	List   []string // details
}

type session struct {
	ID      string
	User    string
	CSRF    string
	Expires time.Time
	flashes []flash
}

type sessionStore struct {
	mu  sync.Mutex
	m   map[string]*session
	ttl time.Duration
}

func newSessionStore(ttl time.Duration) *sessionStore {
	s := &sessionStore{m: map[string]*session{}, ttl: ttl}
	go s.janitor()
	return s
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *sessionStore) create(user string) *session {
	sess := &session{ID: randomToken(), User: user, CSRF: randomToken(), Expires: time.Now().Add(s.ttl)}
	s.mu.Lock()
	s.m[sess.ID] = sess
	s.mu.Unlock()
	return sess
}

func (s *sessionStore) get(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[id]
	if !ok {
		return nil
	}
	if time.Now().After(sess.Expires) {
		delete(s.m, id)
		return nil
	}
	return sess
}

func (s *sessionStore) delete(id string) {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
}

// deleteUser ends all sessions of a user except keep.
func (s *sessionStore) deleteUser(user, keep string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.m {
		if sess.User == user && id != keep {
			delete(s.m, id)
		}
	}
}

func (s *sessionStore) addFlash(sess *session, f flash) {
	s.mu.Lock()
	sess.flashes = append(sess.flashes, f)
	s.mu.Unlock()
}

func (s *sessionStore) takeFlashes(sess *session) []flash {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := sess.flashes
	sess.flashes = nil
	return f
}

func (s *sessionStore) janitor() {
	for range time.Tick(time.Minute) {
		now := time.Now()
		s.mu.Lock()
		for id, sess := range s.m {
			if now.After(sess.Expires) {
				delete(s.m, id)
			}
		}
		s.mu.Unlock()
	}
}

func tokensEqual(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// limiter blocks login attempts after repeated failures.
type limiter struct {
	mu       sync.Mutex
	failures map[string]*failure
	max      int
	window   time.Duration
}

type failure struct {
	count        int
	first        time.Time
	blockedUntil time.Time
}

func newLimiter() *limiter {
	return &limiter{failures: map[string]*failure{}, max: 5, window: 15 * time.Minute}
}

// blocked reports whether any of the keys is blocked.
func (l *limiter) blocked(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		if f, ok := l.failures[k]; ok && now.Before(f.blockedUntil) {
			return true
		}
	}
	return false
}

func (l *limiter) fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		f, ok := l.failures[k]
		if !ok || now.Sub(f.first) > l.window {
			f = &failure{first: now}
			l.failures[k] = f
		}
		f.count++
		if f.count >= l.max {
			f.blockedUntil = now.Add(l.window)
		}
	}
	// keep the map small
	if len(l.failures) > 10000 {
		for k, f := range l.failures {
			if now.Sub(f.first) > l.window && now.After(f.blockedUntil) {
				delete(l.failures, k)
			}
		}
	}
}

func (l *limiter) reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.failures, k)
	}
}
