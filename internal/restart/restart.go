// Package restart restarts Mailpit after a configuration change, either by
// supervising it as a child process or by running a user-defined command.
package restart

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mddanish/mailpit-crew/internal/envfile"
)

// Restarter starts, restarts and stops Mailpit.
type Restarter interface {
	// Mode returns "child" or "command".
	Mode() string
	// Start starts Mailpit (child mode). It is a no-op in command mode.
	Start() error
	// Restart restarts Mailpit and returns when the restart was triggered.
	Restart(ctx context.Context) error
	// Stop stops Mailpit (child mode). It is a no-op in command mode.
	Stop(ctx context.Context) error
	// LogTail returns recent Mailpit output lines, when known.
	LogTail() []string
}

// Command runs a fixed command to restart Mailpit (for example
// "systemctl restart mailpit"). The command never goes through a shell.
type Command struct {
	Args []string
	Log  *slog.Logger
	tail ring
}

// Mode implements Restarter.
func (c *Command) Mode() string { return "command" }

// Start implements Restarter.
func (c *Command) Start() error { return nil }

// Stop implements Restarter.
func (c *Command) Stop(context.Context) error { return nil }

// LogTail implements Restarter.
func (c *Command) LogTail() []string { return c.tail.lines() }

// Restart implements Restarter.
func (c *Command) Restart(ctx context.Context) error {
	if len(c.Args) == 0 {
		return errors.New("no restart command configured")
	}
	cmd := exec.CommandContext(ctx, c.Args[0], c.Args[1:]...)
	out, err := cmd.CombinedOutput()
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			c.tail.add("[restart] " + l)
		}
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if msg != "" {
			return fmt.Errorf("restart command failed: %v: %s", err, msg)
		}
		return fmt.Errorf("restart command failed: %v", err)
	}
	return nil
}

// Child runs Mailpit as a child process and restarts it when it exits.
type Child struct {
	Binary  string
	Args    []string
	EnvFile string // optional, loaded fresh on every start
	Log     *slog.Logger

	mu       sync.Mutex
	cmd      *exec.Cmd
	done     chan struct{}
	stopping bool
	tail     ring
}

// Mode implements Restarter.
func (c *Child) Mode() string { return "child" }

// LogTail implements Restarter.
func (c *Child) LogTail() []string { return c.tail.lines() }

// Start implements Restarter.
func (c *Child) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopping = false
	return c.startLocked()
}

func (c *Child) environ() ([]string, error) {
	var env []string
	for _, e := range os.Environ() {
		// MP_* from Crew's own environment would override the env file
		if !strings.HasPrefix(e, "MP_") {
			env = append(env, e)
		}
	}
	if c.EnvFile != "" {
		data, err := os.ReadFile(c.EnvFile)
		if err != nil {
			return nil, err
		}
		f := envfile.Parse(data)
		for _, k := range f.Keys() {
			v, _ := f.Get(k)
			env = append(env, k+"="+v)
		}
	}
	return env, nil
}

func (c *Child) startLocked() error {
	if c.cmd != nil {
		return errors.New("mailpit is already running")
	}
	if c.Binary == "" {
		return errors.New("no Mailpit binary selected")
	}
	env, err := c.environ()
	if err != nil {
		return fmt.Errorf("mailpit env file: %w", err)
	}
	cmd := exec.Command(c.Binary, c.Args...)
	cmd.Env = env
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		return fmt.Errorf("start mailpit: %w", err)
	}
	c.Log.Info("mailpit started", "pid", cmd.Process.Pid)
	c.cmd = cmd
	done := make(chan struct{})
	c.done = done
	started := time.Now()

	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			c.tail.add(sc.Text())
			c.Log.Info(sc.Text(), "source", "mailpit")
		}
	}()

	go func() {
		err := cmd.Wait()
		pw.Close()
		c.mu.Lock()
		c.cmd = nil
		stopping := c.stopping
		c.mu.Unlock()
		close(done)
		if stopping {
			return
		}
		c.Log.Warn("mailpit exited", "error", err)
		go c.supervise(time.Since(started))
	}()
	return nil
}

// supervise restarts Mailpit after an unexpected exit, with back-off.
func (c *Child) supervise(uptime time.Duration) {
	delay := time.Second
	if uptime < 10*time.Second {
		delay = 5 * time.Second
	}
	for {
		time.Sleep(delay)
		c.mu.Lock()
		if c.stopping || c.cmd != nil {
			c.mu.Unlock()
			return
		}
		err := c.startLocked()
		c.mu.Unlock()
		if err == nil {
			return
		}
		c.Log.Error("mailpit restart failed", "error", err)
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

// SetBinary changes the Mailpit binary used on the next start.
func (c *Child) SetBinary(path string) {
	c.mu.Lock()
	c.Binary = path
	c.mu.Unlock()
}

// CurrentBinary returns the binary used for starts.
func (c *Child) CurrentBinary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Binary
}

// Running reports whether Mailpit is running.
func (c *Child) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cmd != nil
}

// Stop implements Restarter.
func (c *Child) Stop(ctx context.Context) error {
	c.mu.Lock()
	c.stopping = true
	cmd, done := c.cmd, c.done
	c.mu.Unlock()
	if cmd == nil {
		return nil
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
		return nil
	case <-time.After(10 * time.Second):
	case <-ctx.Done():
	}
	_ = cmd.Process.Kill()
	<-done
	return nil
}

// Restart implements Restarter.
func (c *Child) Restart(ctx context.Context) error {
	if err := c.Stop(ctx); err != nil {
		return err
	}
	return c.Start()
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
