// Package restart controls the Mailpit process.
package restart

import (
	"context"
	"sync"
)

// Restarter starts, restarts and stops Mailpit.
type Restarter interface {
	Start() error
	// Restart restarts Mailpit and returns when the restart was triggered.
	Restart(ctx context.Context) error
	Stop(ctx context.Context) error
	// LogTail returns recent Mailpit output lines, when known.
	LogTail() []string
}

// ring keeps the last lines of output.
type ring struct {
	mu  sync.Mutex
	buf []string
}

const ringSize = 200

func (r *ring) add(l string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, l)
	if len(r.buf) > ringSize {
		r.buf = r.buf[len(r.buf)-ringSize:]
	}
}

func (r *ring) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.buf...)
}
