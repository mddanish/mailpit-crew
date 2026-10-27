package apply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mamber/mailpit-crew/internal/htpasswd"
)

type fakeRestarter struct {
	calls int
	fail  func(call int) error
}

func (f *fakeRestarter) Mode() string                  { return "command" }
func (f *fakeRestarter) Start() error                  { return nil }
func (f *fakeRestarter) Stop(context.Context) error    { return nil }
func (f *fakeRestarter) LogTail() []string             { return []string{"fake log"} }
func (f *fakeRestarter) Restart(context.Context) error { f.calls++; return f.fail(f.calls) }

type fakeReady struct {
	err    func() error
	uptime time.Duration
}

func (f fakeReady) WaitReady(context.Context) error { return f.err() }
func (f fakeReady) Uptime(context.Context) (time.Duration, error) {
	if f.uptime == 0 {
		return time.Hour, nil
	}
	return f.uptime, nil
}

func setup(t *testing.T) (dir string, m *Manager, rs *fakeRestarter, ready *error) {
	t.Helper()
	dir = t.TempDir()
	h, _ := htpasswd.HashPassword("crew-password")
	os.WriteFile(filepath.Join(dir, "users.auth"), []byte("crew:"+h+"\nmamber:"+h+"\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "smtp.auth"), []byte("gitea:"+h+"\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "tags.yaml"), []byte("filters:\n    - match: from:a@b.c\n      tags: A\n"), 0o644)
	env := "MP_DATABASE=" + filepath.Join(dir, "mailpit.db") + "\n" +
		"MP_UI_AUTH_FILE=" + filepath.Join(dir, "users.auth") + "\n" +
		"MP_POP3_AUTH_FILE=" + filepath.Join(dir, "users.auth") + "\n" +
		"MP_SMTP_AUTH_FILE=" + filepath.Join(dir, "smtp.auth") + "\n" +
		"MP_TAGS_CONFIG=" + filepath.Join(dir, "tags.yaml") + "\n"
	os.WriteFile(filepath.Join(dir, "mailpit.ini"), []byte(env), 0o644)

	rs = &fakeRestarter{fail: func(int) error { return nil }}
	var readyErr error
	ready = &readyErr
	m = New(Options{
		EnvFile:     filepath.Join(dir, "mailpit.ini"),
		ManagedDirs: []string{dir},
		BackupDir:   filepath.Join(dir, "backups"),
		Keep:        2,
		Restarter:   rs,
		Ready:       fakeReady{err: func() error { return *ready }},
		Timeout:     5 * time.Second,
		StableFor:   10 * time.Millisecond,
		ServiceUser: "crew",
	})
	return
}

func TestFilesDiscovery(t *testing.T) {
	dir, m, _, _ := setup(t)
	files := m.Files()
	if len(files) != 4 {
		t.Fatalf("files: %+v", files)
	}
	ui := files[1]
	if ui.Path != filepath.Join(dir, "users.auth") || len(ui.Uses) != 2 {
		t.Fatalf("shared UI/POP3 file not merged: %+v", ui)
	}
	if !strings.Contains(ui.Label, "Web UI & API + POP3") {
		t.Fatalf("label: %s", ui.Label)
	}
	for _, f := range files {
		if !f.Writable || !f.Exists {
			t.Fatalf("file state: %+v", f)
		}
	}
}

func TestApplySuccess(t *testing.T) {
	dir, m, rs, _ := setup(t)
	f, _ := m.FileByID("env")
	data, _, _ := m.Read(f)
	newData := append(data, []byte("MP_MAX_MESSAGES=2000\n")...)
	if err := m.Stage(f, newData); err != nil {
		t.Fatal(err)
	}
	if m.PendingCount() != 1 {
		t.Fatal("not staged")
	}
	// disk unchanged before apply
	if disk, _ := os.ReadFile(f.Path); string(disk) != string(data) {
		t.Fatal("disk changed before apply")
	}
	res, err := m.Apply(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || rs.calls != 1 {
		t.Fatalf("result %+v calls %d", res, rs.calls)
	}
	if disk, _ := os.ReadFile(f.Path); string(disk) != string(newData) {
		t.Fatal("file not written")
	}
	if m.PendingCount() != 0 {
		t.Fatal("staged change not cleared")
	}
	if _, err := os.Stat(filepath.Join(dir, "backups", res.BackupID, "manifest.json")); err != nil {
		t.Fatal("no backup manifest")
	}
}

func TestApplyRollbackOnNotReady(t *testing.T) {
	_, m, rs, ready := setup(t)
	f, _ := m.FileByID("env")
	orig, _, _ := m.Read(f)
	m.Stage(f, append(orig, []byte("MP_LABEL=broken\n")...))
	calls := 0
	rs.fail = func(int) error { return nil }
	*ready = errors.New("not ready")
	m.opts.Ready = fakeReady{err: func() error {
		calls++
		if calls == 1 {
			return errors.New("not ready")
		}
		return nil
	}}
	res, err := m.Apply(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !res.RolledBack {
		t.Fatalf("want rolled back failure: %+v", res)
	}
	if rs.calls != 2 {
		t.Fatalf("restart calls: %d", rs.calls)
	}
	if disk, _ := os.ReadFile(f.Path); string(disk) != string(orig) {
		t.Fatalf("not restored:\n%s", disk)
	}
	if m.PendingCount() != 1 {
		t.Fatal("staged change should stay after failure")
	}
	if len(res.LogTail) == 0 {
		t.Fatal("no log tail")
	}
}

func TestApplyRollbackOnCrashAfterReady(t *testing.T) {
	_, m, rs, _ := setup(t)
	f, _ := m.FileByID("env")
	orig, _, _ := m.Read(f)
	m.Stage(f, append(orig, []byte("MP_LABEL=crashes\n")...))
	calls := 0
	m.opts.Ready = readyFunc{func() time.Duration {
		calls++
		if calls == 1 {
			return 0 // supervisor restarted it: uptime reset
		}
		return time.Hour
	}}
	res, err := m.Apply(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !res.RolledBack || !strings.Contains(res.Error, "restarted again") {
		t.Fatalf("want rollback, got %+v", res)
	}
	if rs.calls != 2 {
		t.Fatalf("restart calls: %d", rs.calls)
	}
}

type readyFunc struct{ up func() time.Duration }

func (r readyFunc) WaitReady(context.Context) error               { return nil }
func (r readyFunc) Uptime(context.Context) (time.Duration, error) { return r.up(), nil }

func TestApplyRefusesInvalid(t *testing.T) {
	_, m, rs, _ := setup(t)
	f, _ := m.FileByID("env")
	orig, _, _ := m.Read(f)
	m.Stage(f, append(orig, []byte("MP_MAX_AGE=forever\n")...))
	_, err := m.Apply(context.Background(), "admin")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want validation error, got %v", err)
	}
	if rs.calls != 0 {
		t.Fatal("restarted despite invalid config")
	}
}

func TestServiceAccountProtected(t *testing.T) {
	_, m, _, _ := setup(t)
	var ui File
	for _, f := range m.Files() {
		if f.Kind == KindAuth && len(f.Uses) == 2 {
			ui = f
		}
	}
	data, _, _ := m.Read(ui)
	hf := htpasswd.Parse(data)
	hf.Delete("crew")
	m.Stage(ui, hf.Bytes())
	probs := m.Validate()
	if len(probs) == 0 || !strings.Contains(strings.Join(probs, ";"), "service account") {
		t.Fatalf("problems: %v", probs)
	}
}

func TestConflict(t *testing.T) {
	_, m, rs, _ := setup(t)
	f, _ := m.FileByID("tags")
	m.Stage(f, []byte("filters:\n    - match: x\n      tags: Y\n"))
	os.WriteFile(f.Path, []byte("filters: []\n"), 0o644) // edited by hand meanwhile
	if _, err := m.Apply(context.Background(), "admin"); err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("want conflict, got %v", err)
	}
	if rs.calls != 0 {
		t.Fatal("restarted on conflict")
	}
}

func TestStageOutsideManagedDir(t *testing.T) {
	_, m, _, _ := setup(t)
	f := File{Kind: KindAuth, Path: "/etc/passwd", Writable: m.writable("/etc/passwd")}
	if err := m.Stage(f, []byte("x")); err == nil {
		t.Fatal("staged a file outside managed dirs")
	}
}

func TestStageSameContentUnstages(t *testing.T) {
	_, m, _, _ := setup(t)
	f, _ := m.FileByID("tags")
	orig, _, _ := m.Read(f)
	m.Stage(f, []byte("filters: []\n"))
	m.Stage(f, orig)
	if m.PendingCount() != 0 {
		t.Fatal("identical content still staged")
	}
}

func TestMask(t *testing.T) {
	out := Mask(KindAuth, []byte("gitea:$2a$10$secrethash\n# c\n"))
	if strings.Contains(out, "secrethash") || !strings.Contains(out, "gitea:<bcrypt ") {
		t.Fatalf("auth mask: %q", out)
	}
	out = Mask(KindEnv, []byte("MP_SMTP_RELAY_PASSWORD=hunter2\nMP_LABEL=x\n"))
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "MP_LABEL=x") {
		t.Fatalf("env mask: %q", out)
	}
}

func TestPrune(t *testing.T) {
	_, m, _, _ := setup(t)
	f, _ := m.FileByID("env")
	for i := 0; i < 4; i++ {
		data, _, _ := m.Read(f)
		m.Stage(f, append(data, []byte("MP_LABEL=x"+string(rune('a'+i))+"\n")...))
		if _, err := m.Apply(context.Background(), "admin"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	entries, _ := os.ReadDir(m.opts.BackupDir)
	if len(entries) != 2 {
		t.Fatalf("backups kept: %d", len(entries))
	}
}
