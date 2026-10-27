package envfile

import "testing"

const sample = `MP_DATABASE=/opt/mailpit/mailpit.db
MP_LABEL="Mamber Home Lab - MAILPIT"
# a comment
MP_MAX_MESSAGES=1024

this is not valid
MP_TAGS_TITLE_CASE=true
`

func TestRoundTrip(t *testing.T) {
	f := Parse([]byte(sample))
	if got := string(f.Bytes()); got != sample {
		t.Fatalf("round trip changed file:\n%s", got)
	}
}

func TestGet(t *testing.T) {
	f := Parse([]byte(sample))
	if v, _ := f.Get("MP_LABEL"); v != "Mamber Home Lab - MAILPIT" {
		t.Fatalf("quoted value: %q", v)
	}
	if v, ok := f.Get("MP_MAX_MESSAGES"); !ok || v != "1024" {
		t.Fatalf("plain value: %q %v", v, ok)
	}
	if _, ok := f.Get("MISSING"); ok {
		t.Fatal("missing key found")
	}
	if inv := f.Invalid(); len(inv) != 1 || inv[0].Number != 6 {
		t.Fatalf("invalid lines: %+v", inv)
	}
}

func TestSetKeepsOtherLines(t *testing.T) {
	f := Parse([]byte(sample))
	if err := f.Set("MP_MAX_MESSAGES", "2000"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("MP_UI_BIND_ADDR", "127.0.0.1:8026"); err != nil {
		t.Fatal(err)
	}
	f.Delete("MP_TAGS_TITLE_CASE")
	want := `MP_DATABASE=/opt/mailpit/mailpit.db
MP_LABEL="Mamber Home Lab - MAILPIT"
# a comment
MP_MAX_MESSAGES=2000

this is not valid
MP_UI_BIND_ADDR=127.0.0.1:8026
`
	if got := string(f.Bytes()); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestSetUnchangedValueKeepsLine(t *testing.T) {
	f := Parse([]byte("MP_LABEL='single quoted'\n"))
	_ = f.Set("MP_LABEL", "single quoted")
	if got := string(f.Bytes()); got != "MP_LABEL='single quoted'\n" {
		t.Fatalf("line rewritten: %q", got)
	}
}

func TestQuoting(t *testing.T) {
	f := Parse(nil)
	_ = f.Set("A", `say "hi" \o/`)
	_ = f.Set("B", "[::]:8025")
	if got := string(f.Bytes()); got != "A=\"say \\\"hi\\\" \\\\o/\"\nB=[::]:8025\n" {
		t.Fatalf("got %q", got)
	}
	g := Parse(f.Bytes())
	if v, _ := g.Get("A"); v != `say "hi" \o/` {
		t.Fatalf("re-parse: %q", v)
	}
	if err := f.Set("C", "two\nlines"); err == nil {
		t.Fatal("multi-line value accepted")
	}
}

func TestDuplicateKeys(t *testing.T) {
	f := Parse([]byte("A=1\nA=2\n"))
	if v, _ := f.Get("A"); v != "2" {
		t.Fatalf("last wins: %q", v)
	}
	_ = f.Set("A", "3")
	if got := string(f.Bytes()); got != "A=3\n" {
		t.Fatalf("got %q", got)
	}
}
