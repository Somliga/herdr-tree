package main

import (
	"testing"

	"herdr-tree/internal/claude"
	"herdr-tree/internal/tui"
)

// internal/tui must not import internal/claude, so it carries its own copy of
// the two seed markers. This is the only place that imports both, and the
// copies have to agree exactly: GraftSeeded refuses a seed that does not begin
// with one of them at byte zero, and Classify picks import versus compaction
// by the same strings.
func TestSeedPrefixesAgreeAcrossThePackageBoundary(t *testing.T) {
	if tui.SummaryPrefix != claude.SummaryPrefix {
		t.Fatalf("summary prefix: tui has %q, claude has %q", tui.SummaryPrefix, claude.SummaryPrefix)
	}
	if tui.CompactionPrefix != claude.CompactionPrefix {
		t.Fatalf("compaction prefix: tui has %q, claude has %q", tui.CompactionPrefix, claude.CompactionPrefix)
	}
}
