package claude

import "testing"

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
