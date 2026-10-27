package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	os.WriteFile(file, []byte("x"), 0o600)

	cases := []struct {
		key, value string
		ok         bool
	}{
		{"MP_MAX_AGE", "30d", true},
		{"MP_MAX_AGE", "12h", true},
		{"MP_MAX_AGE", "30 days", false},
		{"MP_MAX_MESSAGES", "1024", true},
		{"MP_MAX_MESSAGES", "-1", false},
		{"MP_MAX_MESSAGES", "abc", false},
		{"MP_COMPRESSION", "4", false},
		{"MP_UI_BIND_ADDR", "127.0.0.1:8026", true},
		{"MP_UI_BIND_ADDR", "[::]:8025", true},
		{"MP_UI_BIND_ADDR", "8025", false},
		{"MP_UI_BIND_ADDR", "127.0.0.1:99999", false},
		{"MP_TAGS_USERNAME", "true", true},
		{"MP_TAGS_USERNAME", "maybe", false},
		{"MP_SMTP_AUTH_FILE", file, true},
		{"MP_SMTP_AUTH_FILE", filepath.Join(dir, "missing"), false},
		{"MP_SMTP_AUTH_FILE", dir, false},
		{"MP_DATABASE", filepath.Join(dir, "new.db"), true},
		{"MP_DATABASE", "/does/not/exist/x.db", false},
		{"MP_DATABASE", "mailpit.db", false},
		{"MP_SMTP_AUTH_FILE", "smtp.auth", false},
		{"MP_SMTP_ALLOWED_RECIPIENTS", "@example\\.com$", true},
		{"MP_SMTP_ALLOWED_RECIPIENTS", "([", false},
		{"MP_SMTP_RELAY_AUTH", "plain", true},
		{"MP_SMTP_RELAY_AUTH", "oauth", false},
		{"MP_WEBHOOK_URL", "https://hooks.example.com/x", true},
		{"MP_WEBHOOK_URL", "ftp://x", false},
		{"MP_ENABLE_PROMETHEUS", "true", true},
		{"MP_ENABLE_PROMETHEUS", "0.0.0.0:9090", true},
		{"MP_ENABLE_PROMETHEUS", "yes please", false},
		{"MP_LABEL", "any text", true},
		{"MP_LABEL", "", true},
	}
	for _, c := range cases {
		k, ok := Lookup(c.key)
		if !ok {
			t.Fatalf("unknown key %s", c.key)
		}
		err := k.Validate(c.value)
		if (err == nil) != c.ok {
			t.Errorf("%s=%q: err=%v, want ok=%v", c.key, c.value, err, c.ok)
		}
	}
}

func TestValidateEnvCrossRules(t *testing.T) {
	errs := ValidateEnv(map[string]string{"MP_SMTP_REQUIRE_TLS": "true", "MP_SMTP_REQUIRE_STARTTLS": "true"})
	if len(errs) < 2 {
		t.Fatalf("expected TLS conflict and missing cert errors, got %v", errs)
	}
	errs = ValidateEnv(map[string]string{"MP_UI_TLS_CERT": "/x"})
	found := false
	for _, e := range errs {
		if e.Key == "MP_UI_TLS_CERT" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cert without key accepted: %v", errs)
	}
}

func TestValidateEnvSMTPAuthNeedsTLS(t *testing.T) {
	has := func(errs []FieldError, key string) bool {
		for _, e := range errs {
			if e.Key == key && strings.Contains(e.Err, "SMTP authentication needs") {
				return true
			}
		}
		return false
	}
	for _, k := range []string{"MP_SMTP_AUTH_FILE", "MP_SMTP_AUTH"} {
		if !has(ValidateEnvWith(map[string]string{k: "x"}, func(string) bool { return true }), k) {
			t.Errorf("%s without TLS accepted", k)
		}
	}
	if !has(ValidateEnv(map[string]string{"MP_SMTP_AUTH_ACCEPT_ANY": "true"}), "MP_SMTP_AUTH_ACCEPT_ANY") {
		t.Error("accept-any without TLS accepted")
	}
	ok := []map[string]string{
		{"MP_SMTP_AUTH": "u:p", "MP_SMTP_AUTH_ALLOW_INSECURE": "true"},
		{"MP_SMTP_AUTH": "u:p", "MP_SMTP_TLS_CERT": "/c", "MP_SMTP_TLS_KEY": "/k"},
		{"MP_SMTP_AUTH_ACCEPT_ANY": "false"},
		{"MP_POP3_AUTH_FILE": "x", "MP_SEND_API_AUTH_FILE": "x"},
	}
	for _, m := range ok {
		errs := ValidateEnvWith(m, func(string) bool { return true })
		for _, k := range []string{"MP_SMTP_AUTH_FILE", "MP_SMTP_AUTH", "MP_SMTP_AUTH_ACCEPT_ANY", "MP_POP3_AUTH_FILE", "MP_SEND_API_AUTH_FILE"} {
			if has(errs, k) {
				t.Errorf("%v: unexpected TLS error: %v", m, errs)
			}
		}
	}
}

func TestPending(t *testing.T) {
	k, _ := Lookup("MP_TAGS_CONFIG")
	if k.ValidateWith("/nope/tags.yaml", func(string) bool { return true }) != nil {
		t.Fatal("pending file rejected")
	}
}

func TestCatalogSections(t *testing.T) {
	sections := map[string]bool{}
	for _, s := range Sections {
		sections[s] = true
	}
	for _, k := range Keys {
		if !sections[k.Section] {
			t.Errorf("%s has unknown section %q", k.Name, k.Section)
		}
	}
}
