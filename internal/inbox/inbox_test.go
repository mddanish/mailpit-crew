package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/mddanish/mailpit-crew/internal/mailpit"
	"github.com/mddanish/mailpit-crew/internal/users"
)

func msg(id, from, username string, to []string, tags ...string) mailpit.Summary {
	s := mailpit.Summary{ID: id, From: &mailpit.Address{Address: from}, Username: username, Tags: tags, Subject: "s " + id}
	for _, t := range to {
		s.To = append(s.To, &mailpit.Address{Address: t})
	}
	return s
}

func TestMatches(t *testing.T) {
	m := msg("aaaaaaaaaa1", "Git@Mamber.in", "gitea", []string{"qa+reset@mamber.in"}, "Gitea", "Git")
	m.Bcc = []*mailpit.Address{{Address: "hidden@ops.example"}}
	cases := []struct {
		rule users.Rule
		want bool
	}{
		{users.Rule{Type: RuleSMTPUser, Value: "GITEA"}, true},
		{users.Rule{Type: RuleSMTPUser, Value: "forgejo"}, false},
		{users.Rule{Type: RuleFrom, Value: "*@mamber.in"}, true},
		{users.Rule{Type: RuleFrom, Value: "git@mamber.in.evil"}, false},
		{users.Rule{Type: RuleFrom, Value: "g?t@mamber.in"}, true},
		{users.Rule{Type: RuleTo, Value: "qa+*@mamber.in"}, true},
		{users.Rule{Type: RuleTo, Value: "*@ops.example"}, true}, // Bcc
		{users.Rule{Type: RuleTo, Value: "qa@mamber.in"}, false},
		{users.Rule{Type: RuleTag, Value: "git"}, true},
		{users.Rule{Type: RuleTag, Value: "Vault"}, false},
		{users.Rule{Type: RuleFrom, Value: "(.*)"}, false}, // regex characters are literal
		{users.Rule{Type: "bogus", Value: "x"}, false},
	}
	for _, c := range cases {
		if got := Matches(c.rule, &m); got != c.want {
			t.Errorf("%+v: got %v", c.rule, got)
		}
	}
	// older Mailpit without Username: fall back to the username tag
	old := msg("aaaaaaaaaa2", "x@y.z", "", nil, "gitea")
	if !Matches(users.Rule{Type: RuleSMTPUser, Value: "Gitea"}, &old) {
		t.Error("username tag fallback")
	}
}

func TestValidateRule(t *testing.T) {
	if ValidateRule(users.Rule{Type: RuleFrom, Value: "a@b.c, d@e.f"}) == nil {
		t.Error("two addresses accepted")
	}
	if ValidateRule(users.Rule{Type: "x", Value: "a"}) == nil {
		t.Error("unknown type accepted")
	}
	if ValidateRule(users.Rule{Type: RuleTag, Value: " "}) == nil {
		t.Error("empty value accepted")
	}
}

func TestVisible(t *testing.T) {
	m := msg("aaaaaaaaaa1", "a@b.c", "gitea", nil)
	f := false
	if !Visible(users.User{Role: users.RoleAdmin}, &m) {
		t.Error("admin must see all")
	}
	if Visible(users.User{Role: users.RoleAdmin, Enabled: &f}, &m) {
		t.Error("disabled user sees mail")
	}
	if Visible(users.User{Role: users.RoleUser}, &m) {
		t.Error("user without rules sees mail")
	}
}

// fakeMailpit serves n messages; even ones come from gitea, odd from vaultwarden.
func fakeMailpit(t *testing.T, n int) (*httptest.Server, *int) {
	var all []mailpit.Summary
	for i := 0; i < n; i++ {
		u := "gitea"
		if i%2 == 1 {
			u = "vaultwarden"
		}
		s := msg(fmt.Sprintf("msg%08d", i), u+"@mamber.in", u, []string{"qa@mamber.in"}, strings.ToUpper(u[:1])+u[1:])
		s.Read = i%3 == 0
		all = append(all, s)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list := all
		if r.URL.Path == "/api/v1/search" {
			list = nil
			for _, m := range all {
				if strings.Contains(m.Subject, r.URL.Query().Get("query")) {
					list = append(list, m)
				}
			}
		}
		end := start + limit
		if end > len(list) {
			end = len(list)
		}
		page := []mailpit.Summary{}
		if start < len(list) {
			page = list[start:end]
		}
		json.NewEncoder(w).Encode(map[string]any{"total": len(all), "messages_count": len(list), "messages": page})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestListFiltersAndPages(t *testing.T) {
	srv, calls := fakeMailpit(t, 600)
	ib := &Inbox{Client: mailpit.New(srv.URL, "", "")}
	u := users.User{Username: "dev", Role: users.RoleUser, Rules: []users.Rule{{Type: RuleSMTPUser, Value: "gitea"}}}
	res, err := ib.List(context.Background(), u, "", "", 2, 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 300 || res.Pages != 6 || res.Page != 2 || len(res.Messages) != 50 {
		t.Fatalf("total %d pages %d page %d len %d", res.Total, res.Pages, res.Page, len(res.Messages))
	}
	for _, m := range res.Messages {
		if m.Username != "gitea" {
			t.Fatalf("foreign message %s", m.ID)
		}
	}
	if res.Latest != "msg00000000" {
		t.Fatalf("latest %s", res.Latest)
	}
	if res.Unread != 200 {
		t.Fatalf("unread %d", res.Unread)
	}
	// cached: a second call does not hit Mailpit
	before := *calls
	ib.List(context.Background(), u, "", "", 1, 50)
	if *calls != before {
		t.Fatal("list not cached")
	}
	ib.Invalidate()
	ib.List(context.Background(), u, "", "", 1, 50)
	if *calls == before {
		t.Fatal("invalidate did not drop cache")
	}
	// tag filter and search
	admin := users.User{Role: users.RoleAdmin}
	res, _ = ib.List(context.Background(), admin, "", "Vaultwarden", 1, 1000)
	if res.Total != 300 || len(res.Tags) != 2 {
		t.Fatalf("tag filter %d tags %v", res.Total, res.Tags)
	}
	res, _ = ib.List(context.Background(), u, "s msg0000001", "", 1, 50)
	if res.Total != 5 { // msg00000010..18 even
		t.Fatalf("search %d", res.Total)
	}
	// MaxScan
	small := &Inbox{Client: mailpit.New(srv.URL, "", ""), MaxScan: 250}
	res, _ = small.List(context.Background(), admin, "", "", 1, 50)
	if !res.Truncated || res.Total != 250 {
		t.Fatalf("truncated %v total %d", res.Truncated, res.Total)
	}
}
