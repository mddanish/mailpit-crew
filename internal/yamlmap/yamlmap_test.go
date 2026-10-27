package yamlmap

import (
	"strings"
	"testing"
)

func TestEditKeepsComments(t *testing.T) {
	in := "# relay for releases\nhost: smtp.example.com # provider\nport: 587\n"
	f, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Set("port", "2525", Int); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("starttls", "true", Bool); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("username", "true", String); err != nil {
		t.Fatal(err)
	}
	if !f.Delete("host") || f.Delete("nope") {
		t.Fatal("delete result")
	}
	out, _ := f.Bytes()
	s := string(out)
	for _, want := range []string{"port: 2525", "starttls: true", `username: "true"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "host") {
		t.Errorf("deleted key kept:\n%s", s)
	}
	if err := f.Set("port", "abc", Int); err == nil {
		t.Error("bad int accepted")
	}
	if _, err := Parse([]byte("- a\n- b\n")); err == nil {
		t.Error("list accepted as mapping")
	}
}

func TestEmpty(t *testing.T) {
	f, _ := Parse(nil)
	f.Set("host", "x", String)
	out, _ := f.Bytes()
	if string(out) != "host: x\n" {
		t.Fatalf("got %q", out)
	}
	if v, ok := f.Get("host"); !ok || v != "x" || len(f.Entries()) != 1 {
		t.Fatal("get")
	}
}
