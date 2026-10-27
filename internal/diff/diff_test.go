package diff

import "testing"

func TestLines(t *testing.T) {
	a := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"
	b := "1\n2\n3\n4\n5\nsix\n7\n8\n9\n10\n11\n"
	got := Lines(a, b, 1)
	want := []Line{
		{Skip, ""},
		{Same, "5"}, {Remove, "6"}, {Add, "six"}, {Same, "7"},
		{Skip, ""},
		{Same, "10"}, {Add, "11"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestNewFile(t *testing.T) {
	got := Lines("", "a\nb\n", 3)
	if len(got) != 2 || got[0].Op != Add || got[1].Op != Add {
		t.Fatalf("got %+v", got)
	}
}
