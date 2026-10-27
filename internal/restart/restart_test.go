package restart

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommand(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ok := &Command{Args: []string{"true"}, Log: log}
	if err := ok.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := &Command{Args: []string{"sh", "-c", "echo unit not found; exit 5"}, Log: log}
	err := bad.Restart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unit not found") {
		t.Fatalf("want error with output, got %v", err)
	}
	if tail := bad.LogTail(); len(tail) != 1 {
		t.Fatalf("log tail: %v", tail)
	}
}

func TestChildRestartAndEnv(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "mailpit.ini")
	out := filepath.Join(dir, "out")
	os.WriteFile(env, []byte("MP_LABEL=\"from file\"\n"), 0o644)
	t.Setenv("MP_LABEL", "from crew env") // must not leak into the child

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := &Child{Binary: "sh", Args: []string{"-c", `echo "$MP_LABEL" >> ` + out + `; exec sleep 60`}, EnvFile: env, Log: log}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := c.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	if got := string(data); got != "from file\nfrom file\n" {
		t.Fatalf("child env / restart: %q", got)
	}
}
