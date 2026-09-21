// Package claude is the Claude Code adapter. Every detail of Claude's
// on-disk transcript format lives here and nowhere else.
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"time"
)

// Entry is one line of a transcript. It keeps the decoded object so that
// grafting can rewrite a few fields and copy everything else verbatim,
// including fields this program has never heard of.
type Entry struct {
	Raw map[string]any
}

func (e Entry) str(k string) string {
	if v, ok := e.Raw[k].(string); ok {
		return v
	}
	return ""
}

func (e Entry) Type() string       { return e.str("type") }
func (e Entry) UUID() string       { return e.str("uuid") }
func (e Entry) ParentUUID() string { return e.str("parentUuid") }
func (e Entry) RequestID() string  { return e.str("requestId") }
func (e Entry) SessionID() string  { return e.str("sessionId") }
func (e Entry) CWD() string        { return e.str("cwd") }
func (e Entry) Version() string    { return e.str("version") }
func (e Entry) AITitle() string    { return e.str("aiTitle") }

func (e Entry) IsSidechain() bool {
	b, _ := e.Raw["isSidechain"].(bool)
	return b
}

func (e Entry) HasToolUseResult() bool {
	_, ok := e.Raw["toolUseResult"]
	return ok
}

func (e Entry) Timestamp() time.Time {
	t, err := time.Parse(time.RFC3339, e.str("timestamp"))
	if err != nil {
		return time.Time{}
	}
	return t
}

// Text returns the entry's human-readable text. Content may be a bare
// string or a list of blocks; only text blocks contribute.
func (e Entry) Text() string {
	msg, ok := e.Raw["message"].(map[string]any)
	if !ok {
		return ""
	}
	switch c := msg["content"].(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, b := range c {
			blk, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := blk["text"].(string); ok && blk["type"] == "text" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// IsToolResult reports whether the entry's content is a tool result block.
func (e Entry) IsToolResult() bool {
	msg, ok := e.Raw["message"].(map[string]any)
	if !ok {
		return false
	}
	blocks, ok := msg["content"].([]any)
	if !ok {
		return false
	}
	for _, b := range blocks {
		if blk, ok := b.(map[string]any); ok && blk["type"] == "tool_result" {
			return true
		}
	}
	return false
}

func parseLine(line []byte) (Entry, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber() // large ints must not become float64
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return Entry{}, err
	}
	return Entry{Raw: m}, nil
}

// Marshal re-encodes an entry. json.Number keeps integers byte-identical.
func Marshal(e Entry) ([]byte, error) { return json.Marshal(e.Raw) }

// ParseFile reads a transcript, skipping lines that are not JSON objects.
// A malformed line is skipped rather than failing the whole session: a
// partially written transcript should still render.
func ParseFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // entries can be large
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		e, err := parseLine(line)
		if err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}
