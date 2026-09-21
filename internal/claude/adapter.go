package claude

import (
	"fmt"
	"path/filepath"
	"strings"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/herdr"
)

type claudeAdapter struct{}

// New returns the Claude Code adapter.
func New() adapter.Adapter { return claudeAdapter{} }

func (claudeAdapter) Name() string { return "claude" }

func (claudeAdapter) Discover(repoRoot string) ([]adapter.Session, error) {
	return Discover(repoRoot)
}

func (claudeAdapter) Current(p adapter.Pane) (string, error) { return Current(p) }

// TranscriptPath is where a session's transcript lives, given its cwd.
func TranscriptPath(sessionID, cwd string) string {
	return filepath.Join(ProjectsDir(), SlugFor(cwd), sessionID+".jsonl")
}

// Preview reports what a graft at atNode would carry, without writing.
func (claudeAdapter) Preview(src adapter.Session, atNode string) (turns, entries int, size int64, err error) {
	path := TranscriptPath(src.ID, src.CWD)
	es, _, err := ParseFile(path)
	if err != nil {
		return 0, 0, 0, err
	}
	keep, err := Select(es, atNode)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, e := range es {
		u := e.UUID()
		if u == "" || !keep[u] {
			continue
		}
		entries++
		if IsPrompt(e) {
			turns++
		}
		if b, merr := Marshal(e); merr == nil {
			size += int64(len(b)) + 1
		}
	}
	return turns, entries, size, nil
}

func (claudeAdapter) Branch(src adapter.Session, atNode, dstCWD string) (string, error) {
	sid, _, err := Graft(TranscriptPath(src.ID, src.CWD), atNode, dstCWD)
	if err != nil {
		return "", err
	}
	return sid, nil
}

// Resume asks Herdr for a pane and starts Claude in it. Nothing is spawned
// by this process.
func (claudeAdapter) Resume(sessionID, cwd string) error {
	paneID, err := herdr.Split(cwd)
	if err != nil {
		return fmt.Errorf("open pane: %w", err)
	}
	name := "tree-" + strings.SplitN(sessionID, "-", 2)[0]
	if err := herdr.AgentStart(name, paneID, sessionID); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}
	return nil
}
