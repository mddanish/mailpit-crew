package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/mailpit"
	"github.com/mddanish/mailpit-crew/internal/managed"
	"github.com/mddanish/mailpit-crew/internal/users"
)

type fakeRestarter struct {
	calls int
	fail  bool
}

func (f *fakeRestarter) Start() error               { return nil }
func (f *fakeRestarter) Stop(context.Context) error { return nil }
func (f *fakeRestarter) LogTail() []string          { return []string{"mailpit log line"} }
func (f *fakeRestarter) Restart(context.Context) error {
	f.calls++
	if f.fail {
		return errors.New("bind: address already in use")
	}
	return nil
}

type env struct {
	layout managed.Layout
	srv    *httptest.Server
	client *http.Client
	rs     *fakeRestarter
	mgr    *apply.Manager
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	layout := managed.NewLayout(filepath.Join(home, "mailpit"), filepath.Join(home, "data"))
	st, err := managed.LoadState(layout.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := layout.Ensure(st); err != nil {
		t.Fatal(err)
	}
	fakeMailpit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
		case "/api/v1/info":
			io.WriteString(w, `{"Version":"v1.32.0","Messages":3,"RuntimeStats":{"Uptime":3600}}`)
		case "/api/v1/search":
			io.WriteString(w, `{"messages_count":2}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakeMailpit.Close)
	mp := mailpit.New(fakeMailpit.URL, st.APIUser, st.APIPassword)
	store, _ := users.Open(filepath.Join(layout.Data, "users.yaml"))
	if err := store.SetPassword(users.AdminName, "admin-password"); err != nil {
		t.Fatal(err)
	}
	rs := &fakeRestarter{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := apply.New(apply.Options{
		EnvFile: layout.EnvFile, ManagedDir: layout.Dir, BackupDir: layout.Backups, ActiveDir: layout.Active,
		Restarter: rs, Ready: mp, Timeout: 5 * time.Second, StableFor: 10 * time.Millisecond,
		ServiceUser: st.APIUser, Locked: layout.Locked(), Log: log,
	})
	if err := mgr.SnapshotActive(); err != nil {
		t.Fatal(err)
	}
	svc := &managed.Service{Layout: layout, Manager: mgr, Script: nil, Log: log, Releases: &managed.Releases{APIURL: fakeMailpit.URL + "/none"}}
	s, err := New(Options{
		Webroot: "/", SessionTTL: time.Hour, Version: "test", CrewPort: "8080",
		Users: store, Manager: mgr, Mailpit: mp, Managed: svc, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &env{layout: layout, srv: ts, client: c, rs: rs, mgr: mgr}
}

func (e *env) get(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := e.client.Get(e.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *env) post(t *testing.T, path string, v url.Values) (int, string) {
	t.Helper()
	resp, err := e.client.PostForm(e.srv.URL+path, v)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

var (
	csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	baseRe = regexp.MustCompile(`name="base" value="([^"]*)"`)
)

func (e *env) login(t *testing.T) string {
	t.Helper()
	code, _ := e.post(t, "/login", url.Values{"username": {"admin"}, "password": {"admin-password"}})
	if code != http.StatusSeeOther {
		t.Fatalf("login: %d", code)
	}
	_, body := e.get(t, "/admin")
	return csrfRe.FindStringSubmatch(body)[1]
}

func (e *env) base(t *testing.T, page string) string {
	_, body := e.get(t, page)
	m := baseRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no base on %s", page)
	}
	return m[1]
}

func read(t *testing.T, p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestLoginAndCSRF(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.get(t, "/admin/settings"); code != http.StatusSeeOther {
		t.Fatal("not logged in should redirect")
	}
	if code, _ := e.post(t, "/login", url.Values{"username": {"admin"}, "password": {"wrong"}}); code != http.StatusUnauthorized {
		t.Fatal("wrong password accepted")
	}
	e.login(t)
	if code, _ := e.post(t, "/admin/restart", url.Values{}); code != http.StatusForbidden {
		t.Fatal("POST without CSRF accepted")
	}
	_, body := e.get(t, "/admin")
	if !strings.Contains(body, "v1.32.0") || !strings.Contains(body, "SMTP accepts mail without login") {
		t.Fatalf("dashboard:\n%s", body)
	}
}

func TestSettingsSaveThenRestart(t *testing.T) {
	e := newEnv(t)
	tok := e.login(t)
	e.post(t, "/admin/settings/set", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/settings")}, "key": {"MP_MAX_AGE"}, "value": {"30d"}})
	if !strings.Contains(read(t, e.layout.EnvFile), "MP_MAX_AGE=30d") || e.rs.calls != 0 {
		t.Fatal("not saved, or restarted on save")
	}
	_, body := e.get(t, "/admin/settings")
	if !strings.Contains(body, "but not active") {
		t.Fatal("no pending banner")
	}
	_, body = e.get(t, "/admin/pending")
	if !strings.Contains(body, "&#43; MP_MAX_AGE=30d") {
		t.Fatalf("pending diff:\n%s", body)
	}
	e.post(t, "/admin/restart", url.Values{"csrf": {tok}})
	if e.rs.calls != 1 || e.mgr.PendingCount() != 0 {
		t.Fatalf("restart calls %d pending %d", e.rs.calls, e.mgr.PendingCount())
	}
	// locked and invalid values
	e.post(t, "/admin/settings/set", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/settings")}, "key": {"MP_DATABASE"}, "value": {"/x.db"}})
	e.post(t, "/admin/settings/set", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/settings")}, "key": {"MP_MAX_AGE"}, "value": {"forever"}})
	if strings.Contains(read(t, e.layout.EnvFile), "/x.db") || strings.Contains(read(t, e.layout.EnvFile), "forever") {
		t.Fatal("locked or invalid value saved")
	}
	// delete
	e.post(t, "/admin/settings/delete", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/settings")}, "key": {"MP_MAX_AGE"}})
	if strings.Contains(read(t, e.layout.EnvFile), "MP_MAX_AGE") {
		t.Fatal("delete did not remove the key")
	}
}

func TestFullEditorKeepsTextOnError(t *testing.T) {
	e := newEnv(t)
	tok := e.login(t)
	before := read(t, e.layout.EnvFile)
	code, body := e.post(t, "/admin/edit/env", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/settings?mode=full")}, "content": {before + "MP_MAX_AGE=forever\nbroken line\n"}})
	if code != http.StatusBadRequest || !strings.Contains(body, "broken line") || !strings.Contains(body, "not a KEY=value") {
		t.Fatalf("code %d", code)
	}
	if read(t, e.layout.EnvFile) != before {
		t.Fatal("invalid text saved")
	}
	_, vbody := e.post(t, "/admin/verify/tags", url.Values{"csrf": {tok}, "content": {"filters:\n  - match: x\n"}})
	var res struct {
		OK     bool
		Issues []struct {
			Line int
			Msg  string
		}
	}
	json.Unmarshal([]byte(vbody), &res)
	if res.OK || len(res.Issues) == 0 || res.Issues[0].Line != 2 {
		t.Fatalf("verify: %s", vbody)
	}
}

func TestTagsForm(t *testing.T) {
	e := newEnv(t)
	tok := e.login(t)
	for _, r := range [][2]string{{"from:a@x.y", "A"}, {"from:b@x.y", "B"}} {
		e.post(t, "/admin/file/tags/set", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/file/tags")}, "match": {r[0]}, "tags": {r[1]}})
	}
	e.post(t, "/admin/file/tags/move", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/file/tags")}, "index": {"1"}, "dir": {"up"}})
	tags := read(t, e.layout.Tags)
	if strings.Index(tags, "b@x.y") > strings.Index(tags, "a@x.y") {
		t.Fatalf("move:\n%s", tags)
	}
	e.post(t, "/admin/file/tags/delete", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/file/tags")}, "index": {"0"}})
	if strings.Contains(read(t, e.layout.Tags), "b@x.y") {
		t.Fatal("delete")
	}
	if _, body := e.get(t, "/admin/file/tags/test?q=from:a"); !strings.Contains(body, `"count":2`) {
		t.Fatalf("test: %s", body)
	}
}

func TestRelayEnableAndSecrets(t *testing.T) {
	e := newEnv(t)
	tok := e.login(t)
	relay := e.layout.StandardFile("MP_SMTP_RELAY_CONFIG")
	e.post(t, "/admin/file/relay/set", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/file/relay")}, "key": {"port"}, "value": {"587"}})
	if _, err := os.Stat(relay); err == nil {
		t.Fatal("relay without host saved")
	}
	e.post(t, "/admin/file/relay/set", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/file/relay")}, "key": {"host"}, "value": {"smtp.example.com"}})
	if !strings.Contains(read(t, e.layout.EnvFile), "MP_SMTP_RELAY_CONFIG="+relay) {
		t.Fatal("relay not turned on")
	}
	content := "host: smtp.example.com\nauth: plain\nusername: u\npassword: hunter2\n"
	e.post(t, "/admin/edit/relay", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/file/relay?mode=full")}, "content": {content}})
	if !strings.Contains(read(t, relay), "hunter2") {
		t.Fatal("full editor save")
	}
	_, body := e.get(t, "/admin/file/relay?mode=full")
	if strings.Contains(body, "hunter2") || !strings.Contains(body, secretPlaceholder) {
		t.Fatal("password shown in editor")
	}
	_, form := e.get(t, "/admin/file/relay")
	if strings.Contains(form, "hunter2") {
		t.Fatal("password shown in form")
	}
	masked := strings.Replace(content, "hunter2", secretPlaceholder, 1) + "port: 2525\n"
	e.post(t, "/admin/edit/relay", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/file/relay?mode=full")}, "content": {masked}})
	if r := read(t, relay); !strings.Contains(r, "password: hunter2") || !strings.Contains(r, "port: 2525") {
		t.Fatalf("placeholder not restored:\n%s", r)
	}
	e.post(t, "/admin/file/relay/disable", url.Values{"csrf": {tok}})
	if strings.Contains(read(t, e.layout.EnvFile), "MP_SMTP_RELAY_CONFIG") {
		t.Fatal("disable")
	}
}

func TestAccounts(t *testing.T) {
	e := newEnv(t)
	tok := e.login(t)
	e.post(t, "/admin/accounts/enable", url.Values{"csrf": {tok}, "key": {"MP_SMTP_AUTH_FILE"}, "username": {"gitea"}})
	smtp := e.layout.StandardFile("MP_SMTP_AUTH_FILE")
	if !strings.HasPrefix(read(t, smtp), "gitea:$2a$") || !strings.Contains(read(t, e.layout.EnvFile), "MP_SMTP_AUTH_FILE=") {
		t.Fatal("enable")
	}
	uiID := apply.FileID(apply.KindAuth, e.layout.UIAuth)
	_, page := e.get(t, "/admin/accounts/"+uiID)
	if strings.Contains(page, "<strong>crew</strong>") || !strings.Contains(page, "hidden here") {
		t.Fatal("service account shown")
	}
	for _, action := range []string{"add", "reset", "delete"} {
		e.post(t, "/admin/accounts/"+uiID+"/"+action, url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/accounts/"+uiID)}, "username": {"crew"}, "password": {"stolen-password"}})
	}
	if !strings.Contains(read(t, e.layout.UIAuth), "crew:") || e.mgr.Validate() != nil {
		t.Fatal("service account changed")
	}
	e.post(t, "/admin/accounts/"+uiID+"/add", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/accounts/"+uiID)}, "username": {"mamber"}, "password": {"mamber-password"}})
	if !strings.Contains(read(t, e.layout.UIAuth), "mamber:") {
		t.Fatal("add UI account")
	}
}

func TestFailedRestartAndRollback(t *testing.T) {
	e := newEnv(t)
	tok := e.login(t)
	before := read(t, e.layout.EnvFile)
	e.post(t, "/admin/settings/set", url.Values{"csrf": {tok}, "base": {e.base(t, "/admin/settings")}, "key": {"MP_LABEL"}, "value": {"breaks"}})
	e.rs.fail = true
	e.post(t, "/admin/restart", url.Values{"csrf": {tok}})
	_, body := e.get(t, "/admin")
	if !strings.Contains(body, "did not start after the last restart") || !strings.Contains(body, "address already in use") || !strings.Contains(body, "mailpit log line") {
		t.Fatalf("failure banner:\n%s", body)
	}
	if !strings.Contains(read(t, e.layout.EnvFile), "breaks") {
		t.Fatal("rolled back without asking")
	}
	e.rs.fail = false
	e.post(t, "/admin/rollback", url.Values{"csrf": {tok}})
	if read(t, e.layout.EnvFile) != before || e.mgr.Failure() != nil {
		t.Fatal("rollback")
	}
}
