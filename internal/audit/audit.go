// Package audit writes an append-only JSON lines audit log.
package audit

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Entry is one audit record. Never put passwords in it.
type Entry struct {
	Time   time.Time `json:"time"`
	User   string    `json:"user"`
	IP     string    `json:"ip,omitempty"`
	Action string    `json:"action"`
	Target string    `json:"target,omitempty"`
	Result string    `json:"result"`
	Detail string    `json:"detail,omitempty"`
}

// Log is an audit log file.
type Log struct {
	mu   sync.Mutex
	path string
}

// New returns an audit log writing to path.
func New(path string) *Log { return &Log{path: path} }

// Write appends an entry. Errors are returned but callers usually only log them.
func (l *Log) Write(e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}
