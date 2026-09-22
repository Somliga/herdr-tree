package claude

import (
	"strings"
	"testing"
)

func TestSummarisePromptNamesBothEnds(t *testing.T) {
	p := SummarisePrompt("i want to discuss the weather", "good conclusion")
	for _, want := range []string{
		"i want to discuss the weather",
		"good conclusion",
		"rejected",
		"unfinished",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt does not mention %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "tool") {
		t.Fatal("the prompt must not invite tool use; it is a -p call")
	}
}
