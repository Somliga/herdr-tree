package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 1 || s.RepoRoot != "/repo" || len(s.Branches) != 0 {
		t.Fatalf("got %+v", s)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s, _ := Load("/repo")
	s.Add("child-sid", Branch{
		GraftedFrom: From{SessionID: "parent-sid", Node: "u3"},
		Title:       "Session-based auth",
		CreatedAt:   time.Date(2026, 9, 21, 17, 40, 0, 0, time.UTC),
	})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	b, ok := again.Branches["child-sid"]
	if !ok {
		t.Fatal("branch not persisted")
	}
	if b.GraftedFrom.Node != "u3" || b.GraftedFrom.SessionID != "parent-sid" {
		t.Fatalf("got %+v", b)
	}
	if b.Artifacts == nil {
		t.Fatal("artifacts must serialize as [] not null")
	}
}

func TestDifferentReposDoNotShareAFile(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	a, _ := Load("/repo/a")
	a.Add("s1", Branch{})
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := Load("/repo/b")
	if len(b.Branches) != 0 {
		t.Fatal("repos share state")
	}
}

func TestSaveRefusesAStoreThatDidNotComeFromLoad(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s := &Store{Version: 1, RepoRoot: "/repo", Branches: map[string]Branch{}}
	if err := s.Save(); err != ErrNoPath {
		t.Fatalf("got %v want ErrNoPath — otherwise Save writes tree.json into the process cwd", err)
	}
	if _, err := os.Stat("tree.json"); err == nil {
		os.Remove("tree.json")
		t.Fatal("Save wrote tree.json into the working directory")
	}
}

func TestConcurrentSavesKeepBothBranches(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())

	// Two panes each Load the same store...
	paneA, _ := Load("/repo")
	paneB, _ := Load("/repo")

	// ...each branches from a different turn...
	paneA.Add("session-a", Branch{GraftedFrom: From{SessionID: "src", Node: "u1"}})
	paneB.Add("session-b", Branch{GraftedFrom: From{SessionID: "src", Node: "u3"}})

	// ...and both save.
	if err := paneA.Save(); err != nil {
		t.Fatal(err)
	}
	if err := paneB.Save(); err != nil {
		t.Fatal(err)
	}

	final, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := final.Branches["session-a"]; !ok {
		t.Fatal("pane A's branch was silently discarded by pane B's save")
	}
	if _, ok := final.Branches["session-b"]; !ok {
		t.Fatal("pane B's branch is missing")
	}
}

func TestSuccessiveCorruptionsAreBothPreserved(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	s, _ := Load("/repo")
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(s.path, []byte("{{{ not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load("/repo"); err != nil {
			t.Fatal(err)
		}
	}
	m, err := filepath.Glob(filepath.Join(dir, "tree.json.corrupt.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("got %d corrupt backups want 2 — a second corruption must not overwrite the first", len(m))
	}
}

func TestCorruptStoreIsBackedUpNotFatal(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	s, _ := Load("/repo")
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte("{{{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	again, err := Load("/repo")
	if err != nil {
		t.Fatalf("corrupt store must not be fatal: %v", err)
	}
	if len(again.Branches) != 0 {
		t.Fatal("want empty store")
	}
	m, err := filepath.Glob(s.path + ".corrupt.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 {
		t.Fatal("corrupt file was not preserved as a backup")
	}
}

func TestLabelRoundTripsAndClears(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s, _ := Load("/repo")
	s.SetLabel("sess-a", "u3", "landmark")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Labels[LabelKey("sess-a", "u3")]; got != "landmark" {
		t.Fatalf("label did not survive save/load: got %q", got)
	}

	again.SetLabel("sess-a", "u3", "")
	if err := again.Save(); err != nil {
		t.Fatal(err)
	}
	final, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := final.Labels[LabelKey("sess-a", "u3")]; ok {
		t.Fatal("clearing a label with an empty string should remove the key")
	}
}

func TestConcurrentSavesKeepBothLabels(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())

	// Two panes each Load the same store...
	paneA, _ := Load("/repo")
	paneB, _ := Load("/repo")

	// ...each labels a DIFFERENT turn...
	paneA.SetLabel("src", "u1", "one")
	paneB.SetLabel("src", "u3", "three")

	// ...and both save.
	if err := paneA.Save(); err != nil {
		t.Fatal(err)
	}
	if err := paneB.Save(); err != nil {
		t.Fatal(err)
	}

	final, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if final.Labels[LabelKey("src", "u1")] != "one" {
		t.Fatal("pane A's label was silently discarded by pane B's save")
	}
	if final.Labels[LabelKey("src", "u3")] != "three" {
		t.Fatal("pane B's label is missing")
	}
}

func TestSummaryRoundTripAndLookup(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s, _ := Load("/repo")
	s.AddSummary(Summary{
		Text:      "Tried redis; rejected, too much operational weight.",
		SessionID: "sess-a", FromTurn: "t3", ToTurn: "t9",
		CreatedAt: time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC),
	})
	s.AddSummary(Summary{
		Text:      "Second look at the token service.",
		SessionID: "sess-a", FromTurn: "t11", ToTurn: "t14",
	})
	s.AddSummary(Summary{Text: "elsewhere", SessionID: "sess-b", FromTurn: "x", ToTurn: "y"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	got := summariesFor(again, "sess-a")
	if len(got) != 2 {
		t.Fatalf("got %d summaries for sess-a, want 2", len(got))
	}
	if got[0].Text == "" || got[0].FromTurn == "" {
		t.Fatalf("summary lost fields in the round trip: %+v", got[0])
	}
	if len(summariesFor(again, "sess-b")) != 1 {
		t.Fatal("summaries leaked between sessions")
	}
	if len(summariesFor(again, "nobody")) != 0 {
		t.Fatal("unknown session returned summaries")
	}
}

func TestConcurrentSavesKeepBothSummaries(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	a, _ := Load("/repo")
	b, _ := Load("/repo")
	a.AddSummary(Summary{Text: "from pane A", SessionID: "s", FromTurn: "t1", ToTurn: "t2"})
	b.AddSummary(Summary{Text: "from pane B", SessionID: "s", FromTurn: "t5", ToTurn: "t6"})
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	final, _ := Load("/repo")
	if len(summariesFor(final, "s")) != 2 {
		t.Fatalf("a summary was discarded by the other pane's save: %+v", final.Summaries)
	}
}

// The picker in the overlay is navigated by position, and summaries come out
// of a map whose iteration order Go randomises per call. Equal CreatedAt is
// ordinary — a caller may leave it zero, and two summaries made in the same
// second tie — so the tie-break is what stops the list reshuffling under the
// cursor between opens.
func TestSummaryOrderIsStableAcrossCalls(t *testing.T) {
	s := &Store{Version: 1, Branches: map[string]Branch{}}
	same := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	for _, span := range [][2]string{{"t1", "t2"}, {"t3", "t4"}, {"t5", "t6"}} {
		s.AddSummary(Summary{Text: "x", SessionID: "sess", FromTurn: span[0], ToTurn: span[1], CreatedAt: same})
	}
	s.AddSummary(Summary{Text: "x", SessionID: "other", FromTurn: "u1", ToTurn: "u2", CreatedAt: same})

	key := func(got []Summary) (out []string) {
		for _, v := range got {
			out = append(out, SummaryKey(v.SessionID, v.FromTurn, v.ToTurn))
		}
		return out
	}
	first, firstAll := key(summariesFor(s, "sess")), key(s.AllSummaries())
	if len(first) != 3 || len(firstAll) != 4 {
		t.Fatalf("setup: got %v and %v", first, firstAll)
	}
	for i := 0; i < 20; i++ {
		if got := key(summariesFor(s, "sess")); !equalStrings(got, first) {
			t.Fatalf("call %d reordered SummariesFor: %v then %v", i, first, got)
		}
		if got := key(s.AllSummaries()); !equalStrings(got, firstAll) {
			t.Fatalf("call %d reordered AllSummaries: %v then %v", i, firstAll, got)
		}
	}
}

// summariesFor is what the store no longer provides: nothing in the plugin
// asked for one session's summaries, so the accessor went and the filtering
// lives here, where the tests that need it are.
func summariesFor(s *Store, sessionID string) []Summary {
	var out []Summary
	for _, v := range s.AllSummaries() {
		if v.SessionID == sessionID {
			out = append(out, v)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReplaceHidesTheOldLineAndPutsTheNewOneInItsPlace(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s, _ := Load("/repo")
	s.Add("old", Branch{GraftedFrom: From{SessionID: "trunk", Node: "t4"}, Title: "b"})
	s.Replace("old", "new", Branch{Kind: KindCompacted, Title: "⤶ x"})

	if s.Branches["old"].ReplacedBy != "new" {
		t.Fatal("old is not marked replaced")
	}
	nb := s.Branches["new"]
	if nb.Replaces != "old" || nb.Kind != KindCompacted {
		t.Fatalf("new record %+v", nb)
	}
	if nb.GraftedFrom != (From{SessionID: "trunk", Node: "t4"}) {
		t.Fatalf("new line does not hang where the old one did: %+v", nb.GraftedFrom)
	}
}

func TestReplacingALineWithNoRecordCreatesOne(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s, _ := Load("/repo")
	s.Replace("root-sess", "new", Branch{Kind: KindCut})
	if s.Branches["root-sess"].ReplacedBy != "new" {
		t.Fatal("no record was created for the replaced root session")
	}
	if s.Branches["new"].GraftedFrom.SessionID != "" {
		t.Fatal("a replaced root must stay a root")
	}
}

func TestResolveFollowsReplacementsAndSurvivesACycle(t *testing.T) {
	s := &Store{Branches: map[string]Branch{
		"a": {ReplacedBy: "b"}, "b": {ReplacedBy: "c"},
		"x": {ReplacedBy: "y"}, "y": {ReplacedBy: "x"},
	}}
	if got := s.Resolve("a"); got != "c" {
		t.Fatalf("Resolve(a) = %q, want c", got)
	}
	if got := s.Resolve("q"); got != "q" {
		t.Fatalf("Resolve of an unknown session = %q, want itself", got)
	}
	s.Resolve("x") // must return, not hang
}

// A second overlay that loaded before the replacement and saves something
// unrelated must not un-hide the old line.
func TestSaveNeverClearsAReplacement(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s1, _ := Load("/repo")
	s1.Add("old", Branch{Title: "b"})
	if err := s1.Save(); err != nil {
		t.Fatal(err)
	}
	stale, _ := Load("/repo")

	s1.Replace("old", "new", Branch{Kind: KindCut})
	if err := s1.Save(); err != nil {
		t.Fatal(err)
	}
	stale.SetLabel("old", "t1", "landmark")
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}
	after, _ := Load("/repo")
	if after.Branches["old"].ReplacedBy != "new" {
		t.Fatal("a stale save cleared replaced_by")
	}
}

func TestVersionsWalksReplacesNewestFirstAndStopsOnACycle(t *testing.T) {
	s := &Store{Branches: map[string]Branch{}}
	s.Replace("a", "b", Branch{})
	s.Replace("b", "c", Branch{})
	if got := strings.Join(s.Versions("c"), " "); got != "c b a" {
		t.Fatalf("versions %q, want c b a", got)
	}
	s.Branches["a"] = Branch{Replaces: "c"} // hand-edited cycle
	if got := strings.Join(s.Versions("c"), " "); got != "c b a" {
		t.Fatalf("versions %q on a cycle, want c b a", got)
	}
}
