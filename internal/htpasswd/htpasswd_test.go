package htpasswd

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestParseSetDelete(t *testing.T) {
	in := "# accounts\nmail:$2a$10$abc\ngitea:$2a$10$def\nbroken line\n"
	f := Parse([]byte(in))
	if got := string(f.Bytes()); got != in {
		t.Fatalf("round trip: %q", got)
	}
	if n := len(f.Entries()); n != 2 {
		t.Fatalf("entries: %d", n)
	}
	if inv := f.Invalid(); len(inv) != 1 || inv[0] != 4 {
		t.Fatalf("invalid: %v", inv)
	}
	if err := f.Set("gitea", "$2a$10$new"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("forgejo", "$2a$10$xyz"); err != nil {
		t.Fatal(err)
	}
	if !f.Delete("mail") || f.Delete("nobody") {
		t.Fatal("delete result wrong")
	}
	want := "# accounts\ngitea:$2a$10$new\nbroken line\nforgejo:$2a$10$xyz\n"
	if got := string(f.Bytes()); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestValidUsername(t *testing.T) {
	for _, bad := range []string{"", "a:b", "a b", "#x", strings.Repeat("a", 129)} {
		if ValidUsername(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if err := ValidUsername("vaultwarden"); err != nil {
		t.Error(err)
	}
}

func TestHashPassword(t *testing.T) {
	h, err := HashPassword("secret-password")
	if err != nil {
		t.Fatal(err)
	}
	if HashType(h) != "bcrypt" {
		t.Fatalf("type %s", HashType(h))
	}
	if bcrypt.CompareHashAndPassword([]byte(h), []byte("secret-password")) != nil {
		t.Fatal("hash does not verify")
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password accepted")
	}
}

func TestGeneratePassword(t *testing.T) {
	a, _ := GeneratePassword(20)
	b, _ := GeneratePassword(20)
	if len(a) != 20 || a == b {
		t.Fatalf("weak generator: %q %q", a, b)
	}
}
