package managed

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/envfile"
	"github.com/mddanish/mailpit-crew/internal/htpasswd"
	"github.com/mddanish/mailpit-crew/internal/restart"
)

// fakeMailpit is a shell script that behaves like Mailpit for ctl.sh:
// "version" prints the version, otherwise it writes its env and sleeps.
func fakeMailpit(version string, crash bool) []byte {
	run := `echo "label=$MP_LABEL db=$MP_DATABASE"; exec sleep 300`
	if crash {
		run = `echo "fatal: listen tcp: bind: address already in use"; exit 1`
	}
	return []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo \"$0 " + version + " compiled with go1.25 on linux/amd64\"; exit 0; fi\n" + run + "\n")
}

func tarball(t *testing.T, bin []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		data []byte
	}{{"LICENSE", []byte("MIT")}, {"mailpit", bin}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.data)), Typeflag: tar.TypeReg})
		tw.Write(f.data)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func fakeGitHub(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/releases":
			io.WriteString(w, `[
				{"tag_name":"v1.32.0","published_at":"2026-10-01T00:00:00Z"},
				{"tag_name":"v1.33.0-beta","prerelease":true,"published_at":"2026-10-05T00:00:00Z"},
				{"tag_name":"v1.31.2","published_at":"2026-09-01T00:00:00Z"},
				{"tag_name":"v1.31.10","published_at":"2026-09-20T00:00:00Z"},
				{"tag_name":"v1.30.0","published_at":"2026-08-01T00:00:00Z"}]`)
		case r.URL.Path == "/feed":
			io.WriteString(w, `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
				<entry><updated>2026-10-01T00:00:00Z</updated><link href="https://github.com/axllent/mailpit/releases/tag/v1.32.0"/></entry>
				<entry><updated>2026-09-01T00:00:00Z</updated><link href="https://github.com/axllent/mailpit/releases/tag/v1.31.0"/></entry></feed>`)
		case strings.HasPrefix(r.URL.Path, "/download/"):
			parts := strings.Split(r.URL.Path, "/")
			tag := parts[2]
			if tag == "v9.9.9" {
				http.NotFound(w, r)
				return
			}
			reported := tag
			if tag == "v1.29.0" {
				reported = "v0.0.1" // binary that lies about its version
			}
			w.Write(tarball(t, fakeMailpit(reported, tag == "v1.31.10")))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReleases(t *testing.T) {
	gh := fakeGitHub(t)
	r := &Releases{APIURL: gh.URL + "/api/releases", FeedURL: gh.URL + "/feed", DownloadURL: gh.URL + "/download"}
	l, err := r.Latest(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, x := range l {
		tags = append(tags, x.Tag)
	}
	if strings.Join(tags, ",") != "v1.32.0,v1.31.10,v1.31.2" {
		t.Fatalf("latest 3 (prerelease skipped, semver order): %v", tags)
	}

	feedOnly := &Releases{APIURL: gh.URL + "/missing", FeedURL: gh.URL + "/feed"}
	l, err = feedOnly.Latest(context.Background(), 3)
	if err != nil || len(l) != 2 || l[0].Tag != "v1.32.0" {
		t.Fatalf("feed fallback: %v %v", l, err)
	}
}

func TestDownload(t *testing.T) {
	gh := fakeGitHub(t)
	r := &Releases{DownloadURL: gh.URL + "/download"}
	bin := t.TempDir()
	p, err := r.Download(context.Background(), "v1.32.0", bin)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "mailpit-v1.32.0" {
		t.Fatalf("path %s", p)
	}
	if v, err := BinaryVersion(context.Background(), p); err != nil || v != "v1.32.0" {
		t.Fatalf("version %q %v", v, err)
	}
	if _, err := r.Download(context.Background(), "v1.29.0", bin); err == nil {
		t.Fatal("binary with wrong version accepted")
	}
	if _, err := r.Download(context.Background(), "v9.9.9", bin); err == nil {
		t.Fatal("missing release accepted")
	}
	if _, err := r.Download(context.Background(), "../../etc", bin); err == nil {
		t.Fatal("bad tag accepted")
	}
	entries, _ := os.ReadDir(bin)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestCompareTags(t *testing.T) {
	if CompareTags("v1.31.10", "v1.31.2") != 1 || CompareTags("v1.2.0", "v1.10.0") != -1 || CompareTags("dev", "v1.0.0") != -1 {
		t.Fatal("compare wrong")
	}
}

type env struct {
	l   Layout
	st  *State
	svc *Service
	gh  *httptest.Server
}

type alwaysReady struct{ script *restart.Script }

func (a alwaysReady) WaitReady(ctx context.Context) error {
	if !a.script.Running() {
		return errors.New("not running")
	}
	return nil
}
func (a alwaysReady) Uptime(ctx context.Context) (time.Duration, error) {
	if !a.script.Running() {
		return 0, errors.New("not running")
	}
	return time.Hour, nil
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	home := t.TempDir()
	l := NewLayout(filepath.Join(home, "mailpit"), filepath.Join(home, "data"))
	st, err := LoadState(l.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Ensure(st); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	script := &restart.Script{Path: l.Ctl, LogFile: l.Log, Log: log}
	mgr := apply.New(apply.Options{
		EnvFile: l.EnvFile, ManagedDir: l.Dir, BackupDir: l.Backups, ActiveDir: l.Active,
		Restarter: script, Ready: alwaysReady{script}, Timeout: 10 * time.Second, StableFor: 300 * time.Millisecond,
		ServiceUser: st.APIUser, Locked: l.Locked(), Log: log,
	})
	gh := fakeGitHub(t)
	svc := &Service{Layout: l, Script: script, Manager: mgr, Log: log,
		Releases: &Releases{APIURL: gh.URL + "/api/releases", DownloadURL: gh.URL + "/download"}}
	t.Cleanup(func() { script.Stop(context.Background()) })
	return &env{l: l, st: st, svc: svc, gh: gh}
}

func TestEnsureCreatesLayout(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{e.l.Ctl, e.l.EnvFile, e.l.UIAuth, e.l.Tags, e.l.State} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing %s", p)
		}
	}
	st, _ := os.Stat(e.l.State)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("state mode %v", st.Mode())
	}
	data, _ := os.ReadFile(e.l.EnvFile)
	ef := envfile.Parse(data)
	if v, _ := ef.Get("MP_UI_BIND_ADDR"); v != DefaultUIAddr {
		t.Fatalf("bind addr %q", v)
	}
	if probs := e.svc.Manager.Validate(); len(probs) > 0 {
		t.Fatalf("fresh layout invalid: %v", probs)
	}
	// service account repaired when its password changes
	e.st.APIPassword = "rotated-password"
	if notes, _ := e.l.Ensure(e.st); len(notes) != 1 {
		t.Fatalf("service account not updated: %v", notes)
	}
	auth, _ := os.ReadFile(e.l.UIAuth)
	if !strings.HasPrefix(string(auth), "crew:$2a$") {
		t.Fatalf("ui.auth: %s", auth)
	}
}

func TestCtlUpgrade(t *testing.T) {
	e := newEnv(t)
	os.WriteFile(e.l.Ctl, []byte("#!/bin/sh\n# my local edit\n"), 0o750)
	notes, err := e.l.Ensure(e.st)
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "updated") {
		t.Fatalf("notes %v err %v", notes, err)
	}
	if old, _ := os.ReadFile(e.l.Ctl + ".old"); !strings.Contains(string(old), "my local edit") {
		t.Fatal("old script not kept")
	}
	if notes, _ := e.l.Ensure(e.st); len(notes) != 0 {
		t.Fatalf("current script replaced again: %v", notes)
	}
}

func TestRepairLocked(t *testing.T) {
	e := newEnv(t)
	data, _ := os.ReadFile(e.l.EnvFile)
	ef := envfile.Parse(data)
	ef.Set("MP_DATABASE", "/elsewhere/mailpit.db")
	ef.Set("MP_WEBROOT", "/mail")
	ef.Set("MP_LOG_FILE", "/tmp/mailpit.log")
	os.WriteFile(e.l.EnvFile, ef.Bytes(), 0o640)
	if probs := e.svc.Manager.Validate(); len(probs) < 3 {
		t.Fatalf("locked violations not reported: %v", probs)
	}
	changed, err := e.svc.Manager.RepairLocked()
	if err != nil || len(changed) != 3 {
		t.Fatalf("repair: %v %v", changed, err)
	}
	if probs := e.svc.Manager.Validate(); len(probs) > 0 {
		t.Fatalf("after repair: %v", probs)
	}
}

func TestCtlScriptAndVersionSwitch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if e.svc.HasBinary() || e.svc.Running() {
		t.Fatal("fresh layout should have no binary")
	}
	// label with characters a shell would expand: must reach Mailpit literally
	data, _ := os.ReadFile(e.l.EnvFile)
	ef := envfile.Parse(data)
	ef.Set("MP_LABEL", `Lab $HOME "x" $(touch /tmp/crew-pwned)`)
	os.WriteFile(e.l.EnvFile, ef.Bytes(), 0o640)

	if _, err := e.svc.Use(ctx, "v1.32.0"); err != nil {
		t.Fatal(err)
	}
	if !e.svc.Running() || e.svc.ActiveVersion() != "v1.32.0" {
		t.Fatalf("running %v version %s", e.svc.Running(), e.svc.ActiveVersion())
	}
	time.Sleep(200 * time.Millisecond)
	logData, _ := os.ReadFile(e.l.Log)
	if !strings.Contains(string(logData), `label=Lab $HOME "x" $(touch /tmp/crew-pwned)`) {
		t.Fatalf("env not passed literally: %s", logData)
	}
	if _, err := os.Stat("/tmp/crew-pwned"); err == nil {
		os.Remove("/tmp/crew-pwned")
		t.Fatal("env value was executed by ctl.sh")
	}

	// a version that crashes rolls back to the previous one
	_, err := e.svc.Use(ctx, "v1.31.10")
	if err == nil || !strings.Contains(err.Error(), "rolled back to mailpit-v1.32.0") {
		t.Fatalf("want rollback, got %v", err)
	}
	if e.svc.ActiveVersion() != "v1.32.0" || !e.svc.Running() {
		t.Fatalf("not rolled back: %s running=%v", e.svc.ActiveVersion(), e.svc.Running())
	}

	// installed list and remove
	inst := e.svc.Installed()
	if len(inst) != 2 || !inst[0].Active {
		t.Fatalf("installed: %+v", inst)
	}
	if err := e.svc.Remove("mailpit-v1.32.0"); err == nil {
		t.Fatal("removed the active binary")
	}
	if err := e.svc.Remove("mailpit-v1.31.10"); err != nil {
		t.Fatal(err)
	}

	// ctl.sh stop and the watchdog start it again
	e.svc.Script.Stop(ctx)
	if e.svc.Running() {
		t.Fatal("still running after stop")
	}
	wctx, cancel := context.WithCancel(ctx)
	go e.svc.Watch(wctx, 100*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for !e.svc.Running() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	if !e.svc.Running() {
		t.Fatal("watchdog did not restart Mailpit")
	}
}

func TestUseCustomBinary(t *testing.T) {
	e := newEnv(t)
	src := filepath.Join(t.TempDir(), "my-mailpit")
	os.WriteFile(src, fakeMailpit("v1.27.5", false), 0o755)
	name, err := e.svc.Use(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if name != "mailpit-v1.27.5" {
		t.Fatalf("name %s", name)
	}
	if _, err := e.svc.Use(context.Background(), "relative/path"); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestFirstVersionFailsCleanly(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Use(context.Background(), "v1.31.10"); err == nil {
		t.Fatal("crashing first version accepted")
	}
	if e.svc.HasBinary() {
		t.Fatal("failed first version left selected")
	}
}

func TestImport(t *testing.T) {
	e := newEnv(t)
	old := t.TempDir()
	h, _ := htpasswd.HashPassword("mamber-password")
	os.WriteFile(filepath.Join(old, "users.auth"), []byte("mamber:"+h+"\n"), 0o600)
	os.WriteFile(filepath.Join(old, "smtp.auth"), []byte("gitea:"+h+"\n"), 0o600)
	os.WriteFile(filepath.Join(old, "tags.yaml"), []byte("filters:\n    - match: from:git@x\n      tags: Git\n"), 0o644)
	os.WriteFile(filepath.Join(old, "mailpit.db"), []byte("db"), 0o644)
	os.WriteFile(filepath.Join(old, "mailpit.db-wal"), []byte("wal"), 0o644)
	os.MkdirAll(filepath.Join(old, "ssl"), 0o755)
	os.WriteFile(filepath.Join(old, "ssl", "cert.pem"), []byte("cert"), 0o644)
	os.WriteFile(filepath.Join(old, "ssl", "key.pem"), []byte("key"), 0o600)
	ini := "MP_DATABASE=" + old + "/mailpit.db\nMP_LABEL=\"Home Lab\"\nMP_MAX_MESSAGES=1024\n" +
		"MP_UI_AUTH_FILE=" + old + "/users.auth\nMP_UI_TLS_CERT=" + old + "/ssl/cert.pem\nMP_UI_TLS_KEY=" + old + "/ssl/key.pem\n" +
		"MP_SMTP_AUTH_FILE=" + old + "/smtp.auth\nMP_SMTP_TLS_CERT=" + old + "/ssl/cert.pem\nMP_SMTP_TLS_KEY=" + old + "/ssl/key.pem\n" +
		"MP_POP3_AUTH_FILE=" + old + "/users.auth\nMP_TAGS_CONFIG=" + old + "/tags.yaml\nMP_LOG_FILE=" + old + "/mailpit.log\n"
	os.WriteFile(filepath.Join(old, "mailpit.ini"), []byte(ini), 0o644)

	notes, err := Import(e.l, filepath.Join(old, "mailpit.ini"))
	if err != nil {
		t.Fatal(err, notes)
	}
	data, _ := os.ReadFile(e.l.EnvFile)
	m := envfile.Parse(data).Map()
	want := map[string]string{
		"MP_DATABASE":       e.l.DB,
		"MP_LABEL":          "Home Lab",
		"MP_MAX_MESSAGES":   "1024",
		"MP_UI_AUTH_FILE":   e.l.UIAuth,
		"MP_SMTP_AUTH_FILE": filepath.Join(e.l.Dir, "smtp.auth"),
		"MP_POP3_AUTH_FILE": filepath.Join(e.l.Dir, "pop3.auth"),
		"MP_SMTP_TLS_CERT":  filepath.Join(e.l.SSL, "cert.pem"),
		"MP_UI_TLS_CERT":    filepath.Join(e.l.SSL, "cert.pem"),
		"MP_TAGS_CONFIG":    e.l.Tags,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	for _, k := range []string{"MP_LOG_FILE"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s should not be imported", k)
		}
	}
	if b, _ := os.ReadFile(e.l.DB + "-wal"); string(b) != "wal" {
		t.Error("WAL not copied")
	}
	if st, _ := os.Stat(filepath.Join(e.l.SSL, "key.pem")); st.Mode().Perm() != 0o600 {
		t.Error("key file not private")
	}
	ui, _ := os.ReadFile(e.l.UIAuth)
	if !strings.Contains(string(ui), "mamber:") || !strings.Contains(string(ui), "crew:") {
		t.Errorf("UI accounts not merged into ui.auth: %s", ui)
	}
	for _, p := range e.svc.Manager.Validate() {
		if !strings.Contains(p.Msg, "certificate and key do not load") { // test certs are fake
			t.Errorf("imported setup invalid: %v", p)
		}
	}
	if _, err := Import(e.l, filepath.Join(old, "mailpit.ini")); err == nil {
		t.Error("second import over existing database accepted")
	}
}

func TestAPIBase(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{nil, "http://127.0.0.1:8025"},
		{map[string]string{"MP_UI_BIND_ADDR": "0.0.0.0:9000"}, "http://127.0.0.1:9000"},
		{map[string]string{"MP_UI_BIND_ADDR": "192.168.1.5:8025", "MP_UI_TLS_CERT": "/c.pem"}, "https://192.168.1.5:8025"},
		{map[string]string{"MP_UI_BIND_ADDR": "[::1]:8025"}, "http://[::1]:8025"},
	}
	for _, c := range cases {
		if got := APIBase(c.env); got != c.want {
			t.Errorf("%v: got %s want %s", c.env, got, c.want)
		}
	}
}
