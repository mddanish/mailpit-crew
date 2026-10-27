package restart

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Script controls Mailpit through a ctl.sh script with start, stop, restart
// and status commands. Mailpit runs detached from Crew; the script keeps a
// pid file. Used in managed mode.
type Script struct {
	Path    string // ctl.sh
	LogFile string // Mailpit output, for LogTail
	Log     *slog.Logger
	tail    ring
}

// Mode implements Restarter.
func (s *Script) Mode() string { return "script" }

func (s *Script) run(ctx context.Context, action string) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, s.Path, action)
	cmd.Env = cleanEnv()
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	for _, l := range strings.Split(msg, "\n") {
		if l != "" {
			s.tail.add("[ctl.sh " + action + "] " + l)
		}
	}
	if err != nil {
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if msg != "" {
			return fmt.Errorf("ctl.sh %s failed: %v: %s", action, err, msg)
		}
		return fmt.Errorf("ctl.sh %s failed: %v", action, err)
	}
	if s.Log != nil && msg != "" {
		s.Log.Info(msg, "source", "ctl.sh", "action", action)
	}
	return nil
}

// Start implements Restarter.
func (s *Script) Start() error { return s.run(context.Background(), "start") }

// Restart implements Restarter.
func (s *Script) Restart(ctx context.Context) error { return s.run(ctx, "restart") }

// Stop implements Restarter.
func (s *Script) Stop(ctx context.Context) error { return s.run(ctx, "stop") }

// Running reports whether ctl.sh status says Mailpit is running.
func (s *Script) Running() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Path, "status")
	cmd.Env = cleanEnv()
	return cmd.Run() == nil
}

// cleanEnv is Crew's environment without MP_* variables, so only
// mailpit.ini configures Mailpit.
func cleanEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "MP_") {
			env = append(env, e)
		}
	}
	return env
}

// LogTail implements Restarter: script output plus the end of Mailpit's log.
func (s *Script) LogTail() []string {
	lines := s.tail.lines()
	if len(lines) > 10 {
		lines = lines[len(lines)-10:]
	}
	return append(lines, TailFile(s.LogFile, 30)...)
}

// TailFile returns the last n lines of a file.
func TailFile(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	const max = 64 * 1024
	off := st.Size() - max
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	nr, _ := f.ReadAt(buf, off)
	buf = buf[:nr]
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // first line may be partial
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}
