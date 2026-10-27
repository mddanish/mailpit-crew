package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/mddanish/mailpit-crew/internal/users"
)

// mailFake is a Mailpit API with two messages that records writes.
type mailFake struct {
	mu      sync.Mutex
	puts    [][]string
	deletes [][]string
}

const (
	giteaID = "GiteaMsg000001"
	vaultID = "VaultMsg000002"
)

func (f *mailFake) handler(w http.ResponseWriter, r *http.Request) {
	msgs := map[string]map[string]any{
		giteaID: {"ID": giteaID, "Subject": "Reset your password", "Username": "gitea",
			"From": map[string]string{"Address": "git@mamber.in"}, "To": []map[string]string{{"Address": "qa@mamber.in"}},
			"HTML": `<p>Hi</p><img src="cid:logo@x"><script>alert(1)</script>`, "Text": "Hi",
			"Inline":      []map[string]any{{"PartID": "2", "ContentID": "logo@x", "ContentType": "image/png", "Size": 4}},
			"Attachments": []map[string]any{{"PartID": "3", "FileName": "evil\".html", "ContentType": "text/html", "Size": 10}}},
		vaultID: {"ID": vaultID, "Subject": "Vault secret", "Username": "vaultwarden",
			"From": map[string]string{"Address": "vault@mamber.in"}, "To": []map[string]string{{"Address": "ops@mamber.in"}},
			"Attachments": []map[string]any{}},
	}
	p := r.URL.Path
	switch {
	case p == "/readyz":
	case p == "/api/v1/info":
		io.WriteString(w, `{"Version":"v1.28.0","RuntimeStats":{"Uptime":3600}}`)
	case p == "/api/v1/messages" && r.Method == http.MethodGet:
		var list []any
		for _, id := range []string{giteaID, vaultID} {
			sum := map[string]any{"Attachments": len(msgs[id]["Attachments"].([]map[string]any))}
			for _, k := range []string{"ID", "Subject", "Username", "From", "To"} {
				sum[k] = msgs[id][k]
			}
			list = append(list, sum)
		}
		json.NewEncoder(w).Encode(map[string]any{"total": 2, "messages_count": 2, "messages": list})
	case p == "/api/v1/messages" && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		var body struct{ IDs []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		if r.Method == http.MethodPut {
			f.puts = append(f.puts, body.IDs)
		} else {
			f.deletes = append(f.deletes, body.IDs)
		}
		f.mu.Unlock()
	case strings.HasPrefix(p, "/api/v1/message/"):
		parts := strings.Split(strings.TrimPrefix(p, "/api/v1/message/"), "/")
		m, ok := msgs[parts[0]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch {
		case len(parts) == 1:
			json.NewEncoder(w).Encode(m)
		case parts[1] == "headers":
			io.WriteString(w, `{"Subject":["x"]}`)
		case parts[1] == "raw":
			io.WriteString(w, "Subject: x\r\n\r\nbody")
		case parts[1] == "part":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "PNG!")
		}
	default:
		http.NotFound(w, r)
	}
}

func newInboxEnv(t *testing.T) (*env, *mailFake) {
	f := &mailFake{}
	e := newEnvWith(t, http.HandlerFunc(f.handler))
	if err := e.store.Update("dev", func(u *users.User) error {
		u.Rules = []users.Rule{{Type: "smtp_user", Value: "gitea"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return e, f
}

func TestUserSeesOnlyOwnMail(t *testing.T) {
	e, f := newInboxEnv(t)
	e.login(t, "dev", "dev-password")
	code, body := e.get(t, "/inbox")
	if code != 200 || !strings.Contains(body, "Reset your password") || strings.Contains(body, "Vault secret") {
		t.Fatalf("inbox %d:\n%s", code, body)
	}
	for _, p := range []string{"/inbox/" + vaultID, "/inbox/" + vaultID + "/html", "/inbox/" + vaultID + "/eml",
		"/inbox/" + vaultID + "/part/1", "/inbox/latest", "/inbox/..%2f..%2fadmin", "/inbox/NoSuchMessage01"} {
		if code, _ := e.get(t, p); code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, code)
		}
	}
	if code, body := e.get(t, "/inbox/"+giteaID); code != 200 || !strings.Contains(body, "Reset your password") {
		t.Fatalf("own message %d", code)
	}
	if len(f.puts) != 1 || len(f.puts[0]) != 1 || f.puts[0][0] != giteaID {
		t.Fatalf("mark read: %v", f.puts)
	}
	_, state := e.get(t, "/inbox/state")
	if !strings.Contains(state, `"total":1`) {
		t.Fatalf("state %s", state)
	}
}

func TestDeleteOnlyOwned(t *testing.T) {
	e, f := newInboxEnv(t)
	e.login(t, "dev", "dev-password")
	_, page := e.get(t, "/inbox")
	tok := csrfRe.FindStringSubmatch(page)[1]
	code, _ := e.post(t, "/inbox/action", url.Values{"csrf": {tok}, "action": {"delete"}, "id": {vaultID}})
	if code != http.StatusNotFound || len(f.deletes) != 0 {
		t.Fatalf("foreign delete: %d %v", code, f.deletes)
	}
	e.post(t, "/inbox/action", url.Values{"csrf": {tok}, "action": {"delete"}, "id": {giteaID, vaultID}})
	if len(f.deletes) != 1 || len(f.deletes[0]) != 1 || f.deletes[0][0] != giteaID {
		t.Fatalf("mixed delete sent %v", f.deletes)
	}
	e.post(t, "/inbox/action", url.Values{"csrf": {tok}, "action": {"delete"}})
	if len(f.deletes) != 1 {
		t.Fatal("empty delete reached Mailpit (would delete all mail)")
	}
	e.post(t, "/inbox/action", url.Values{"csrf": {tok}, "action": {"unread"}, "id": {"latest"}})
	if len(f.puts) != 0 {
		t.Fatalf("invalid id reached Mailpit: %v", f.puts)
	}
}

func TestHTMLAndPartsAreSafe(t *testing.T) {
	e, _ := newInboxEnv(t)
	e.login(t, "dev", "dev-password")
	resp, err := e.client.Get(e.srv.URL + "/inbox/" + giteaID + "/html")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.HasPrefix(csp, "sandbox;") || !strings.Contains(csp, "img-src data:;") {
		t.Fatalf("csp %q", csp)
	}
	if resp.Header.Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatal("html view not frameable by Crew")
	}
	if !strings.Contains(string(b), `src="data:image/png;base64,UE5HIQ=="`) {
		t.Fatalf("cid not embedded: %s", b)
	}
	resp, _ = e.client.Get(e.srv.URL + "/inbox/" + giteaID + "/html?remote=1")
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "img-src data: https:") {
		t.Fatal("remote toggle")
	}
	resp, _ = e.client.Get(e.srv.URL + "/inbox/" + giteaID + "/part/3")
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("part headers %v", resp.Header)
	}
	if code, _ := e.get(t, "/inbox/"+giteaID+"/part/9"); code != http.StatusNotFound {
		t.Fatal("unknown part served")
	}
	for _, tab := range []string{"text", "headers", "raw", "attachments", "html"} {
		if code, _ := e.get(t, "/inbox/"+giteaID+"?tab="+tab); code != 200 {
			t.Fatalf("tab %s: %d", tab, code)
		}
	}
}

func TestAdminManagesUsers(t *testing.T) {
	e, _ := newInboxEnv(t)
	e.login(t, "admin", "admin-password")
	tok := e.csrf(t)
	e.post(t, "/admin/users", url.Values{"csrf": {tok}, "username": {"qa"}, "role": {"user"}})
	_, page := e.get(t, "/admin/users/qa")
	if !strings.Contains(page, "Password (shown only once)") {
		t.Fatal("generated password not shown")
	}
	code, _ := e.post(t, "/admin/users/qa", url.Values{"csrf": {tok}, "role": {"user"}, "enabled": {"on"}, "rows": {"2"},
		"type_0": {"to"}, "value_0": {"ops@*"}, "type_1": {"from"}, "value_1": {"a@b, c@d"}})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid rule accepted: %d", code)
	}
	e.post(t, "/admin/users/qa", url.Values{"csrf": {tok}, "role": {"user"}, "enabled": {"on"}, "rows": {"1"},
		"type_0": {"to"}, "value_0": {"ops@*"}})
	_, page = e.get(t, "/admin/users/qa")
	if !strings.Contains(page, "qa sees <strong>1</strong>") || !strings.Contains(page, "Vault secret") {
		t.Fatalf("preview wrong:\n%s", page)
	}
	if code, _ := e.post(t, "/admin/users/admin", url.Values{"csrf": {tok}, "role": {"user"}, "enabled": {"on"}, "rows": {"0"}}); code != http.StatusBadRequest {
		t.Fatalf("admin demoted self: %d", code)
	}
	e.post(t, "/admin/users/dev/delete", url.Values{"csrf": {tok}})
	if _, ok := e.store.Get("dev"); ok {
		t.Fatal("user not deleted")
	}
	if r := e.login(t, "dev", "dev-password"); r.StatusCode != http.StatusUnauthorized {
		t.Fatal("deleted user can log in")
	}
}

func TestDisabledUserLoggedOut(t *testing.T) {
	e, _ := newInboxEnv(t)
	devClient := *e.client
	devJar := newJar()
	devClient.Jar = devJar
	dev := &env{dir: e.dir, srv: e.srv, client: &devClient, rs: e.rs, store: e.store}
	dev.login(t, "dev", "dev-password")
	if code, _ := dev.get(t, "/inbox"); code != 200 {
		t.Fatal("dev cannot open inbox")
	}
	e.login(t, "admin", "admin-password")
	tok := e.csrf(t)
	e.post(t, "/admin/users/dev", url.Values{"csrf": {tok}, "role": {"user"}, "rows": {"0"}})
	if code, _ := dev.get(t, "/inbox"); code != http.StatusSeeOther {
		t.Fatalf("disabled user still logged in: %d", code)
	}
}
