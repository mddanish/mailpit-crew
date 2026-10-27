package web

import (
	"context"
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
	"github.com/mddanish/mailpit-crew/internal/htpasswd"
	"github.com/mddanish/mailpit-crew/internal/mailpit"
	"github.com/mddanish/mailpit-crew/internal/users"
)

type nopRestarter struct{ calls int }

func (n *nopRestarter) Mode() string                  { return "command" }
func (n *nopRestarter) Start() error                  { return nil }
func (n *nopRestarter) Stop(context.Context) error    { return nil }
func (n *nopRestarter) LogTail() []string             { return nil }
func (n *nopRestarter) Restart(context.Context) error { n.calls++; return nil }

type env struct {
	dir    string
	srv    *httptest.Server
	client *http.Client
	rs     *nopRestarter
	store  *users.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	h, _ := htpasswd.HashPassword("crew-password")
	os.WriteFile(filepath.Join(dir, "users.auth"), []byte("crew:"+h+"\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "smtp.auth"), []byte("gitea:"+h+"\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "mailpit.ini"), []byte(
		"MP_DATABASE="+filepath.Join(dir, "mailpit.db")+"\n"+
			"MP_UI_AUTH_FILE="+filepath.Join(dir, "users.auth")+"\n"+
			"MP_SMTP_AUTH_FILE="+filepath.Join(dir, "smtp.auth")+"\n"), 0o644)

	fakeMailpit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
		case "/api/v1/info":
			io.WriteString(w, `{"Version":"v1.28.0","Messages":3,"RuntimeStats":{"Uptime":3600}}`)
		case "/api/v1/search":
			io.WriteString(w, `{"messages_count":2}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakeMailpit.Close)
	mp := mailpit.New(fakeMailpit.URL, "crew", "crew-password")

	store, err := users.Open(filepath.Join(dir, "crew-users.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add("admin", "admin-password", users.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := store.Add("dev", "dev-password", users.RoleUser); err != nil {
		t.Fatal(err)
	}
	rs := &nopRestarter{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := apply.New(apply.Options{
		EnvFile: filepath.Join(dir, "mailpit.ini"), ManagedDirs: []string{dir},
		BackupDir: filepath.Join(dir, "backups"), Restarter: rs, Ready: mp,
		Timeout: 5 * time.Second, StableFor: 10 * time.Millisecond, ServiceUser: "crew", Log: log,
	})
	s, err := New(Options{
		Webroot: "/", SessionTTL: time.Hour, ApplyTimeout: 5 * time.Second, Version: "test",
		RestartMode: "command", Users: store, Manager: mgr, Mailpit: mp, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &env{dir: dir, srv: ts, client: c, rs: rs, store: store}
}

func (e *env) login(t *testing.T, user, pw string) *http.Response {
	t.Helper()
	resp, err := e.client.PostForm(e.srv.URL+"/login", url.Values{"username": {user}, "password": {pw}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
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

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (e *env) csrf(t *testing.T) string {
	_, body := e.get(t, "/admin")
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no csrf token on page")
	}
	return m[1]
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

func TestLoginRequired(t *testing.T) {
	e := newEnv(t)
	code, _ := e.get(t, "/admin/settings")
	if code != http.StatusSeeOther {
		t.Fatalf("want redirect to login, got %d", code)
	}
}

func TestLoginAndDashboard(t *testing.T) {
	e := newEnv(t)
	if r := e.login(t, "admin", "wrong"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", r.StatusCode)
	}
	if r := e.login(t, "admin", "admin-password"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %d", r.StatusCode)
	}
	code, body := e.get(t, "/admin")
	if code != 200 || !strings.Contains(body, "v1.28.0") {
		t.Fatalf("dashboard %d: %s", code, body)
	}
	if !strings.Contains(body, "Mailpit UI is reachable from the network") {
		t.Fatal("missing bind address warning")
	}
}

func TestNonAdminCannotLogIn(t *testing.T) {
	e := newEnv(t)
	if r := e.login(t, "dev", "dev-password"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("non-admin login: %d", r.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		e.login(t, "admin", "wrong")
	}
	if r := e.login(t, "admin", "admin-password"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("not blocked after 5 failures: %d", r.StatusCode)
	}
}

func TestCSRFRequired(t *testing.T) {
	e := newEnv(t)
	e.login(t, "admin", "admin-password")
	code, _ := e.post(t, "/admin/settings", url.Values{"MP_LABEL": {"x"}})
	if code != http.StatusForbidden {
		t.Fatalf("post without csrf: %d", code)
	}
}

func TestSettingsStageAndApply(t *testing.T) {
	e := newEnv(t)
	e.login(t, "admin", "admin-password")
	tok := e.csrf(t)
	before, _ := os.ReadFile(filepath.Join(e.dir, "mailpit.ini"))

	form := url.Values{"csrf": {tok},
		"MP_DATABASE":       {filepath.Join(e.dir, "mailpit.db")},
		"MP_UI_AUTH_FILE":   {filepath.Join(e.dir, "users.auth")},
		"MP_SMTP_AUTH_FILE": {filepath.Join(e.dir, "smtp.auth")},
		"MP_MAX_AGE":        {"forever"},
	}
	code, body := e.post(t, "/admin/settings", form)
	if code != http.StatusBadRequest || !strings.Contains(body, "must be a number followed by h or d") {
		t.Fatalf("invalid value: %d", code)
	}

	form.Set("MP_MAX_AGE", "30d")
	form.Set("MP_TAGS_USERNAME", "on")
	if code, _ := e.post(t, "/admin/settings", form); code != http.StatusSeeOther {
		t.Fatalf("stage: %d", code)
	}
	if after, _ := os.ReadFile(filepath.Join(e.dir, "mailpit.ini")); string(after) != string(before) {
		t.Fatal("file written before apply")
	}
	_, body = e.get(t, "/admin/changes")
	if !strings.Contains(body, "&#43; MP_MAX_AGE=30d") || !strings.Contains(body, "&#43; MP_TAGS_USERNAME=true") {
		t.Fatalf("diff missing:\n%s", body)
	}

	if code, _ := e.post(t, "/admin/apply", url.Values{"csrf": {tok}}); code != http.StatusSeeOther {
		t.Fatalf("apply: %d", code)
	}
	after, _ := os.ReadFile(filepath.Join(e.dir, "mailpit.ini"))
	if !strings.Contains(string(after), "MP_MAX_AGE=30d") || e.rs.calls != 1 {
		t.Fatalf("not applied (restarts %d):\n%s", e.rs.calls, after)
	}
}

func TestAccountsAddAndProtect(t *testing.T) {
	e := newEnv(t)
	e.login(t, "admin", "admin-password")
	tok := e.csrf(t)
	_, body := e.get(t, "/admin/accounts")
	ids := regexp.MustCompile(`/admin/accounts/(auth-[0-9a-f]+)`).FindAllStringSubmatch(body, -1)
	if len(ids) != 2 {
		t.Fatalf("auth files: %v", ids)
	}
	var uiID, smtpID string
	for _, m := range ids {
		_, page := e.get(t, "/admin/accounts/"+m[1])
		if strings.Contains(page, "Crew service account") {
			uiID = m[1]
		} else {
			smtpID = m[1]
		}
	}
	code, _ := e.post(t, "/admin/accounts/"+uiID+"/delete", url.Values{"csrf": {tok}, "username": {"crew"}})
	if code != http.StatusSeeOther || e.get2(t, "/admin/changes", "No staged changes") == false {
		t.Fatal("service account delete was staged")
	}
	e.post(t, "/admin/accounts/"+smtpID+"/add", url.Values{"csrf": {tok}, "username": {"vaultwarden"}})
	_, page := e.get(t, "/admin/accounts/"+smtpID)
	if !strings.Contains(page, "Generated password") || !strings.Contains(page, "vaultwarden") {
		t.Fatalf("add with generated password:\n%s", page)
	}
}

func (e *env) get2(t *testing.T, path, want string) bool {
	_, body := e.get(t, path)
	return strings.Contains(body, want)
}

func TestTagsStage(t *testing.T) {
	e := newEnv(t)
	tags := filepath.Join(e.dir, "tags.yaml")
	ini := filepath.Join(e.dir, "mailpit.ini")
	data, _ := os.ReadFile(ini)
	os.WriteFile(ini, append(data, []byte("MP_TAGS_CONFIG="+tags+"\n")...), 0o644)
	e.login(t, "admin", "admin-password")
	tok := e.csrf(t)
	_, body := e.get(t, "/admin/tags")
	if !strings.Contains(body, "does not exist yet") {
		t.Fatal("missing-file notice")
	}
	code, _ := e.post(t, "/admin/tags", url.Values{"csrf": {tok}, "rows": {"2"},
		"match_0": {"from:git@example.com"}, "tags_0": {"Gitea"}, "pos_0": {"2"},
		"match_1": {"from:vault@example.com"}, "tags_1": {"Vault"}, "pos_1": {"1"}})
	if code != http.StatusSeeOther {
		t.Fatalf("stage tags: %d", code)
	}
	if code, _ := e.post(t, "/admin/apply", url.Values{"csrf": {tok}}); code != http.StatusSeeOther {
		t.Fatalf("apply: %d", code)
	}
	out, err := os.ReadFile(tags)
	if err != nil {
		t.Fatal("tags file not created")
	}
	if !strings.HasPrefix(string(out), "filters:\n    - match: from:vault@example.com") {
		t.Fatalf("order not applied:\n%s", out)
	}
	code, body = e.get(t, "/admin/tags/test?q=from:x")
	if code != 200 || !strings.Contains(body, `"count":2`) {
		t.Fatalf("test endpoint: %s", body)
	}
}

func TestRawEditorMasksSecrets(t *testing.T) {
	e := newEnv(t)
	ini := filepath.Join(e.dir, "mailpit.ini")
	data, _ := os.ReadFile(ini)
	os.WriteFile(ini, append(data, []byte("MP_SMTP_RELAY_PASSWORD=hunter2\n")...), 0o644)
	e.login(t, "admin", "admin-password")
	_, body := e.get(t, "/admin/settings/raw")
	if strings.Contains(body, "hunter2") || !strings.Contains(body, secretPlaceholder) {
		t.Fatal("secret shown in raw editor")
	}
	_, body = e.get(t, "/admin/settings")
	if strings.Contains(body, "hunter2") {
		t.Fatal("secret shown in settings form")
	}
}
