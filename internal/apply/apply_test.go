package apply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mddanish/mailpit-crew/internal/htpasswd"
)

type fakeRestarter struct {
	calls int
	fail  func(call int) error
}

func (f *fakeRestarter) Start() error                  { return nil }
func (f *fakeRestarter) Stop(context.Context) error    { return nil }
func (f *fakeRestarter) LogTail() []string             { return []string{"fake log"} }
func (f *fakeRestarter) Restart(context.Context) error { f.calls++; return f.fail(f.calls) }

type fakeReady struct{ up func() time.Duration }

func (f fakeReady) WaitReady(context.Context) error { return nil }
func (f fakeReady) Uptime(context.Context) (time.Duration, error) {
	if f.up == nil {
		return time.Hour, nil
	}
	return f.up(), nil
}

func setup(t *testing.T) (string, *Manager, *fakeRestarter) {
	t.Helper()
	dir := t.TempDir()
	mp := filepath.Join(dir, "mailpit")
	os.MkdirAll(mp, 0o755)
	h, _ := htpasswd.HashPassword("crew-password")
	os.WriteFile(filepath.Join(mp, "ui.auth"), []byte("crew:"+h+"\n"), 0o600)
	os.WriteFile(filepath.Join(mp, "tags.yaml"), []byte("filters: []\n"), 0o644)
	os.WriteFile(filepath.Join(mp, "mailpit.ini"), []byte(
		"MP_DATABASE="+filepath.Join(mp, "mailpit.db")+"\n"+
			"MP_UI_AUTH_FILE="+filepath.Join(mp, "ui.auth")+"\n"+
			"MP_TAGS_CONFIG="+filepath.Join(mp, "tags.yaml")+"\n"), 0o600)
	rs := &fakeRestarter{fail: func(int) error { return nil }}
	m := New(Options{
		EnvFile: filepath.Join(mp, "mailpit.ini"), ManagedDir: mp,
		BackupDir: filepath.Join(dir, "data", "backups"), ActiveDir: filepath.Join(dir, "data", "active"),
		Keep: 3, Restarter: rs, Ready: fakeReady{}, Timeout: 5 * time.Second, StableFor: 10 * time.Millisecond,
		ServiceUser: "crew", Locked: map[string]string{"MP_DATABASE": filepath.Join(mp, "mailpit.db"), "MP_WEBROOT": ""},
	})
	if err := m.SnapshotActive(); err != nil {
		t.Fatal(err)
	}
	return mp, m, rs
}

func TestFiles(t *testing.T) {
	mp, m, _ := setup(t)
	files := m.Files()
	if len(files) != 3 || files[1].Kind != KindAuth || files[2].Kind != KindTags {
		t.Fatalf("files: %+v", files)
	}
	if !strings.Contains(files[1].Label, "Web UI") || files[2].Path != filepath.Join(mp, "tags.yaml") {
		t.Fatalf("labels: %+v", files)
	}
}

func TestSaveDoesNotRestart(t *testing.T) {
	mp, m, rs := setup(t)
	env := filepath.Join(mp, "mailpit.ini")
	data, base, _ := m.Read(env)
	if err := m.Save(KindEnv, env, append(data, []byte("MP_MAX_AGE=30d\n")...), base); err != nil {
		t.Fatal(err)
	}
	if rs.calls != 0 {
		t.Fatal("save restarted Mailpit")
	}
	p := m.Pending()
	if len(p) != 1 || p[0].File.Kind != KindEnv {
		t.Fatalf("pending: %+v", p)
	}
	entries, _ := os.ReadDir(m.opts.BackupDir)
	if len(entries) != 1 {
		t.Fatal("no backup of the old file")
	}
	// a second save from the old base is a conflict
	if err := m.Save(KindEnv, env, data, base); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestSaveValidates(t *testing.T) {
	mp, m, _ := setup(t)
	env := filepath.Join(mp, "mailpit.ini")
	data, _, _ := m.Read(env)
	var ve *ValidationError
	if err := m.Save(KindEnv, env, append(data, []byte("MP_WEBROOT=/x\n")...), ""); !errors.As(err, &ve) {
		t.Fatalf("locked key accepted: %v", err)
	}
	if err := m.Save(KindAuth, filepath.Join(mp, "ui.auth"), []byte("other:$2a$10$x\n"), ""); !errors.As(err, &ve) {
		t.Fatalf("removing the service account accepted: %v", err)
	}
	if err := m.Save(KindTags, filepath.Join(mp, "tags.yaml"), []byte("tags: x\n"), ""); !errors.As(err, &ve) {
		t.Fatalf("bad tags accepted: %v", err)
	}
	if err := m.Save(KindTags, "/etc/passwd", []byte("filters: []\n"), ""); err == nil {
		t.Fatal("wrote outside the managed directory")
	}
}

func TestRestartSuccessMakesActive(t *testing.T) {
	mp, m, rs := setup(t)
	env := filepath.Join(mp, "mailpit.ini")
	data, base, _ := m.Read(env)
	m.Save(KindEnv, env, append(data, []byte("MP_LABEL=x\n")...), base)
	started := false
	m.opts.Started = func() { started = true }
	if err := m.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rs.calls != 1 || !started || len(m.Pending()) != 0 || m.Failure() != nil {
		t.Fatalf("calls %d started %v pending %d", rs.calls, started, len(m.Pending()))
	}
}

func TestRestartFailureKeepsFilesThenRollback(t *testing.T) {
	mp, m, rs := setup(t)
	env := filepath.Join(mp, "mailpit.ini")
	orig, base, _ := m.Read(env)
	broken := append(append([]byte(nil), orig...), []byte("MP_LABEL=breaks\n")...)
	m.Save(KindEnv, env, broken, base)
	rs.fail = func(call int) error {
		if call == 1 {
			return errors.New("bind: address already in use")
		}
		return nil
	}
	if err := m.Restart(context.Background()); err == nil {
		t.Fatal("restart error not returned")
	}
	f := m.Failure()
	if f == nil || !strings.Contains(f.Error, "address already in use") || len(f.LogTail) == 0 {
		t.Fatalf("failure: %+v", f)
	}
	if now, _ := os.ReadFile(env); string(now) != string(broken) {
		t.Fatal("files rolled back without asking")
	}
	restored, err := m.Rollback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if now, _ := os.ReadFile(env); string(now) != string(orig) || len(restored) != 1 {
		t.Fatalf("not restored: %v", restored)
	}
	if m.Failure() != nil || len(m.Pending()) != 0 || rs.calls != 2 {
		t.Fatalf("after rollback: failure %v pending %d calls %d", m.Failure(), len(m.Pending()), rs.calls)
	}
}

func TestRestartRefusesInvalidFiles(t *testing.T) {
	mp, m, rs := setup(t)
	os.WriteFile(filepath.Join(mp, "tags.yaml"), []byte("nonsense: [\n"), 0o644) // hand edit
	var ve *ValidationError
	if err := m.Restart(context.Background()); !errors.As(err, &ve) {
		t.Fatalf("want validation error, got %v", err)
	}
	if rs.calls != 0 {
		t.Fatal("restarted with invalid files")
	}
}

func TestCrashAfterReady(t *testing.T) {
	_, m, _ := setup(t)
	m.opts.Ready = fakeReady{up: func() time.Duration { return 0 }}
	err := m.Restart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "restarted again") {
		t.Fatalf("want crash detection, got %v", err)
	}
}

func TestHandEditIsPending(t *testing.T) {
	mp, m, _ := setup(t)
	f, _ := os.OpenFile(filepath.Join(mp, "mailpit.ini"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("MP_LABEL=hand\n")
	f.Close()
	if len(m.Pending()) != 1 {
		t.Fatal("hand edit not detected")
	}
}

func TestBackupsPruned(t *testing.T) {
	mp, m, _ := setup(t)
	env := filepath.Join(mp, "mailpit.ini")
	for i := 0; i < 6; i++ {
		data, base, _ := m.Read(env)
		if err := m.Save(KindEnv, env, append(data, []byte("MP_LABEL=x"+string(rune('a'+i))+"\n")...), base); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(m.opts.BackupDir)
	if len(entries) != 3 {
		t.Fatalf("backups kept: %d", len(entries))
	}
}

func TestMask(t *testing.T) {
	if out := Mask(KindAuth, []byte("gitea:$2a$10$secrethash\n")); strings.Contains(out, "secrethash") {
		t.Fatalf("auth mask: %q", out)
	}
	if out := Mask(KindEnv, []byte("MP_SMTP_RELAY_PASSWORD=hunter2\nMP_LABEL=x\n")); strings.Contains(out, "hunter2") || !strings.Contains(out, "MP_LABEL=x") {
		t.Fatalf("env mask: %q", out)
	}
	if out := Mask(KindRelay, []byte("host: x\npassword: hunter2\n")); strings.Contains(out, "hunter2") {
		t.Fatalf("relay mask: %q", out)
	}
}

func TestRepairLocked(t *testing.T) {
	mp, m, _ := setup(t)
	env := filepath.Join(mp, "mailpit.ini")
	data, _ := os.ReadFile(env)
	os.WriteFile(env, append(data, []byte("MP_WEBROOT=/x\n")...), 0o600)
	changed, err := m.RepairLocked()
	if err != nil || len(changed) != 1 || changed[0] != "MP_WEBROOT" {
		t.Fatalf("changed %v err %v", changed, err)
	}
}
