package tagsfile

import (
	"strings"
	"testing"
)

const sample = `filters:
    # match all words (in any order)
    #- match: search all words
    #  tags: Tag 1
    # Gitea
    - match: from:git@mamber.in
      tags: Gitea, Git
    - match: from:forge@mamber.in
      tags: Forgejo, Forge
    - match: from:vaultwarden@mamber.in
      tags: Vaultwarden, Vault
`

func TestRoundTripKeepsComments(t *testing.T) {
	f, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Rules()) != 3 {
		t.Fatalf("rules: %d", len(f.Rules()))
	}
	f.SetRules(f.Rules())
	out, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != sample {
		t.Fatalf("round trip changed file:\n%s", out)
	}
}

func TestRemoveFirstRuleKeepsExamples(t *testing.T) {
	f, _ := Parse([]byte(sample))
	r := f.Rules()
	f.SetRules(append(r[1:], Rule{Match: "from:voidauth@mamber.in", Tags: "VoidAuth"}))
	out, _ := f.Bytes()
	s := string(out)
	if !strings.Contains(s, "#- match: search all words") {
		t.Fatalf("example comments lost:\n%s", s)
	}
	if strings.Contains(s, "git@mamber.in") {
		t.Fatalf("removed rule still present:\n%s", s)
	}
	if !strings.Contains(s, "from:voidauth@mamber.in") {
		t.Fatalf("new rule missing:\n%s", s)
	}
	if errs := ValidateBytes(out); len(errs) > 0 {
		t.Fatal(errs)
	}
}

func TestEmptyFile(t *testing.T) {
	f, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	f.SetRules([]Rule{{Match: "from:a@b.c", Tags: "A"}})
	out, _ := f.Bytes()
	if string(out) != "filters:\n    - match: from:a@b.c\n      tags: A\n" {
		t.Fatalf("got %q", out)
	}
}

func TestValidate(t *testing.T) {
	if errs := ValidateBytes([]byte("tags:\n  - x\n")); len(errs) == 0 {
		t.Fatal("missing filters accepted")
	}
	if errs := ValidateBytes([]byte("filters:\n  - match: x\n")); len(errs) == 0 {
		t.Fatal("rule without tags accepted")
	}
	if _, err := Parse([]byte("filters: [")); err == nil {
		t.Fatal("bad yaml accepted")
	}
	if _, err := Parse([]byte("filters:\n  - match: x\n    tags: [a, b]\n")); err == nil {
		t.Fatal("list tags accepted")
	}
}
