package claude

import "testing"

func TestTurnsFiltersNonPrompts(t *testing.T) {
	es, _, err := ParseFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got := Turns(es)
	var ids []string
	for _, n := range got {
		ids = append(ids, n.ID)
	}
	want := []string{"u1", "u3"}
	if len(ids) != len(want) {
		t.Fatalf("got %v want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v want %v", ids, want)
		}
	}
}

func TestTurnTitleIsFirstLineTruncated(t *testing.T) {
	es, _, _ := ParseFile("testdata/simple.jsonl")
	got := Turns(es)
	if got[1].Title != "second question" {
		t.Fatalf("got %q", got[1].Title)
	}
}

func TestTitleTruncation(t *testing.T) {
	cases := []struct{ in, want string }{
		{"short", "short"},
		{"", ""},
		{"\n\n  leading blanks", "leading bl…"},
		{"0123456789abcdef", "0123456789…"},
	}
	for _, c := range cases {
		if got := Title(c.in, 11); got != c.want {
			t.Fatalf("Title(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestSessionTitlePrefersAITitle(t *testing.T) {
	es, _, _ := ParseFile("testdata/simple.jsonl")
	if got := SessionTitle(es); got != "Fixture session" {
		t.Fatalf("got %q", got)
	}
}
