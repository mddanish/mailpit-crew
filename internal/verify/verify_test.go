package verify

import (
	"strings"
	"testing"
)

func has(issues []Issue, line int, part string, warn bool) bool {
	for _, i := range issues {
		if (line == 0 || i.Line == line) && strings.Contains(i.Msg, part) && i.Warn == warn {
			return true
		}
	}
	return false
}

func TestEnv(t *testing.T) {
	data := []byte("# c\nMP_MAX_AGE=forever\nnot a line\nMP_DATABASE=/x/db\nMP_WEBROOT=/mail\nMP_FOO=1\nOTHER=2\n")
	iss := Env(data, EnvOptions{Locked: map[string]string{"MP_DATABASE": "/crew/db", "MP_WEBROOT": ""}})
	cases := []struct {
		line int
		part string
		warn bool
	}{
		{2, "MP_MAX_AGE", false},
		{3, "not a KEY=value", false},
		{4, "must be /crew/db", false},
		{5, "must not be set", false},
		{6, "not a Mailpit setting", true},
		{7, "does not start with MP_", true},
	}
	for _, c := range cases {
		if !has(iss, c.line, c.part, c.warn) {
			t.Errorf("missing line %d %q in %v", c.line, c.part, iss)
		}
	}
	if len(Errors(iss)) != 5 { // includes "directory does not exist: /x"
		t.Errorf("errors: %v", Errors(iss))
	}
}

func TestTags(t *testing.T) {
	if iss := Tags([]byte("filters: []\n")); len(iss) != 0 {
		t.Fatalf("empty list: %v", iss)
	}
	iss := Tags([]byte("filters:\n  - match: from:a\n    tags: [x, y]\n  - tags: B\n    color: red\nextra: 1\n"))
	if !has(iss, 3, "comma separated", false) || !has(iss, 4, "has no match", false) || !has(iss, 5, "unknown key color", true) || !has(iss, 6, "unknown key extra", true) {
		t.Fatalf("issues: %v", iss)
	}
	if !has(Tags([]byte("foo: bar\n")), 1, "missing filters", false) {
		t.Fatal("missing filters")
	}
	if !has(Tags(nil), 0, "empty", false) {
		t.Fatal("empty file")
	}
	iss = Tags([]byte("filters:\n  - match: x\n   tags: y\n"))
	if len(iss) != 1 || iss[0].Line == 0 || !strings.HasPrefix(iss[0].Msg, "YAML:") || strings.Contains(iss[0].Msg, "line") {
		t.Fatalf("yaml error: %v", iss)
	}
}

func TestRelay(t *testing.T) {
	good := "host: smtp.example.com\nport: 587\nstarttls: true\nauth: plain\nusername: u\npassword: p\nallowed-recipients: '@example\\.com$'\n"
	if iss := Relay([]byte(good)); len(iss) != 0 {
		t.Fatalf("good relay: %v", iss)
	}
	iss := Relay([]byte("port: 99999\nauth: plain\nstarttls: true\ntls: true\nallowed-recipients: '(['\noverride-from: not-an-email\nfoo: 1\nrecipient-allowlist: x\n"))
	for _, want := range []string{"host is not set", "port number", "needs username and password", "tls and starttls", "regular expression", "valid email"} {
		if !has(iss, 0, want, false) {
			t.Errorf("missing %q in %v", want, iss)
		}
	}
	if !has(iss, 7, "unknown key foo", true) || !has(iss, 8, "deprecated", true) {
		t.Errorf("warnings: %v", iss)
	}
	if !has(Relay([]byte("host: x\nauth: oauth\n")), 2, "must be one of", false) {
		t.Error("bad auth")
	}
	if !has(Relay([]byte("host: x\nauth: cram-md5\nusername: u\n")), 2, "needs username and secret", false) {
		t.Error("cram without secret")
	}
}

func TestForward(t *testing.T) {
	if iss := Forward([]byte("to: a@b.c, d@e.f\nhost: x\n")); len(iss) != 0 {
		t.Fatalf("good forward: %v", iss)
	}
	if !has(Forward([]byte("host: x\n")), 0, "to is not set", false) {
		t.Fatal("missing to")
	}
	if !has(Forward([]byte("host: x\nto: a@b.c, nope\n")), 2, "not a valid email", false) {
		t.Fatal("bad to")
	}
}

func TestAuth(t *testing.T) {
	if iss := Auth([]byte("a:$2a$10$x\n"), ""); len(iss) != 0 {
		t.Fatal(iss)
	}
	if !has(Auth([]byte("a:$2a$10$x\nbroken\n"), "crew"), 2, "user:hash", false) {
		t.Fatal("broken line")
	}
	if !has(Auth([]byte("a:$2a$10$x\n"), "crew"), 0, "service account", false) {
		t.Fatal("missing service account")
	}
	if !has(Auth(nil, ""), 0, "no accounts", false) {
		t.Fatal("empty")
	}
}
