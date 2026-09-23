package claude

import (
	"strings"

	"herdr-tree/internal/adapter"
)

// injected marks user-entry text that Claude Code wrote on the user's behalf.
// None of it was typed by a person, and all of it appears as type "user".
// SummaryPrefix and CompactionPrefix open an injected summary's text. They are
// the guarantee that a summary reads as a summary: the herdrTree field beside
// them is more convenient to parse, but Claude Code has never been asked to
// accept a field it does not recognise, so nothing may depend on it.
const (
	SummaryPrefix    = "⤶ merged from"
	CompactionPrefix = "⤶ squashed"
)

var injected = []string{
	"Another Claude session sent a message",
	"<local-command-caveat", "<command-name>", "<command-message>",
	"<local-command-stdout", "<user-memory-input",
	"<system-reminder", "[SYSTEM NOTIFICATION", "<task-notification",
	"<bash-input>", "<bash-stdout>", "<bash-stderr>",
	"Base directory for this skill:",
	"Caveat: The messages below were generated",
	syntheticResume,
}

// HasHumanOrigin reports whether this transcript records `origin` at all.
// Newer Claude Code stamps origin.kind on user entries: "human" for something
// typed, "peer" for a message from another agent session. When present it is
// authoritative and no guessing is needed. Twelve of the 106 transcripts on
// the development machine predate it, so the heuristics below remain the
// fallback rather than the primary rule.
func HasHumanOrigin(es []Entry) bool {
	for _, e := range es {
		if o, ok := e.Raw["origin"].(map[string]any); ok {
			if o["kind"] == "human" {
				return true
			}
		}
	}
	return false
}

func originKind(e Entry) string {
	if o, ok := e.Raw["origin"].(map[string]any); ok {
		if k, ok := o["kind"].(string); ok {
			return k
		}
	}
	return ""
}

// blockType returns the single content block type of an entry. Claude Code
// writes one entry per content block, so this is unambiguous.
func blockType(e Entry) string {
	m, _ := e.Raw["message"].(map[string]any)
	if m == nil {
		return ""
	}
	blocks, _ := m["content"].([]any)
	for _, b := range blocks {
		if blk, ok := b.(map[string]any); ok {
			if t, ok := blk["type"].(string); ok {
				return t
			}
		}
	}
	return ""
}

// toolLabel renders a tool call the way Pi does: the CALL, not its output.
// The argument shown is the most identifying one per tool, shortened to the
// home-relative path where it is a path.
func toolLabel(e Entry) string {
	m, _ := e.Raw["message"].(map[string]any)
	if m == nil {
		return "[tool]"
	}
	blocks, _ := m["content"].([]any)
	for _, b := range blocks {
		blk, ok := b.(map[string]any)
		if !ok || blk["type"] != "tool_use" {
			continue
		}
		name, _ := blk["name"].(string)
		args, _ := blk["input"].(map[string]any)
		pick := func(keys ...string) string {
			for _, k := range keys {
				if v, ok := args[k].(string); ok && v != "" {
					return v
				}
			}
			return ""
		}
		arg := pick("file_path", "path", "command", "pattern", "query", "prompt", "url")
		arg = shortenHome(arg)
		if name == "Bash" && len(arg) > 50 {
			arg = arg[:50] + "..."
		}
		if arg == "" {
			return "[" + strings.ToLower(name) + "]"
		}
		return "[" + strings.ToLower(name) + ": " + arg + "]"
	}
	return "[tool]"
}

// Classify decides what an entry is and whether it belongs in the tree.
// hasOrigin selects the authoritative path over the heuristic one.
func Classify(e Entry, hasOrigin bool) (adapter.Kind, bool) {
	if e.IsSidechain() {
		return 0, false // subagent traffic is its own conversation
	}
	switch e.Type() {
	case "assistant":
		switch blockType(e) {
		case "text":
			if strings.TrimSpace(e.Text()) == "" {
				return 0, false // a text-less assistant turn renders as nothing
			}
			return adapter.KindAssistant, true
		case "tool_use":
			return adapter.KindToolCall, true
		}
		return 0, false // thinking and everything else
	case "user":
		if e.HasToolUseResult() || e.IsToolResult() {
			return 0, false // the call is shown, not the output
		}
		t := strings.TrimSpace(e.Text())
		if strings.HasPrefix(t, SummaryPrefix) {
			return adapter.KindSummaryImport, true
		}
		if strings.HasPrefix(t, CompactionPrefix) {
			return adapter.KindSummaryCompaction, true
		}
		if hasOrigin {
			return adapter.KindHuman, originKind(e) == "human"
		}
		if t == "" {
			return 0, false
		}
		for _, p := range injected {
			if strings.HasPrefix(t, p) {
				return 0, false
			}
		}
		return adapter.KindHuman, true
	}
	return 0, false
}

// Entries projects a transcript into tree nodes, in file order.
func Entries(es []Entry) []adapter.Node {
	hasOrigin := HasHumanOrigin(es)
	var out []adapter.Node
	for _, e := range es {
		k, keep := Classify(e, hasOrigin)
		if !keep {
			continue
		}
		title := Title(e.Text(), 200)
		if k == adapter.KindToolCall {
			title = toolLabel(e)
		}
		out = append(out, adapter.Node{ID: e.UUID(), Title: title, Kind: k, At: e.Timestamp()})
	}
	return out
}
