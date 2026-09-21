package herdr

import "testing"

const paneCurrentJSON = `{"id":"cli:pane:current","result":{"pane":{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"6d6dffb3-1677-4215-888a-819585242bac"},"cwd":"/home/somliga/projects/herdr-tree","pane_id":"wA:p1","tab_id":"wA:t1","workspace_id":"wA"},"type":"pane_current"}}`

const splitJSON = `{"id":"cli:pane:split","result":{"pane":{"pane_id":"wA:p2","cwd":"/repo"}}}`

func TestParsePaneCurrent(t *testing.T) {
	p, err := parsePaneCurrent([]byte(paneCurrentJSON))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "wA:p1" {
		t.Fatalf("id %q", p.ID)
	}
	if p.CWD != "/home/somliga/projects/herdr-tree" {
		t.Fatalf("cwd %q", p.CWD)
	}
	if p.AgentSessionID != "6d6dffb3-1677-4215-888a-819585242bac" {
		t.Fatalf("agent session %q", p.AgentSessionID)
	}
	if p.Agent != "claude" {
		t.Fatalf("agent %q", p.Agent)
	}
}

func TestParsePaneCurrentWithoutAgent(t *testing.T) {
	p, err := parsePaneCurrent([]byte(`{"result":{"pane":{"pane_id":"wA:p1","cwd":"/x"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.AgentSessionID != "" {
		t.Fatalf("want empty agent session, got %q", p.AgentSessionID)
	}
}

func TestParseSplit(t *testing.T) {
	id, err := parseSplit([]byte(splitJSON))
	if err != nil {
		t.Fatal(err)
	}
	if id != "wA:p2" {
		t.Fatalf("got %q", id)
	}
}

func TestParseSplitMissingPane(t *testing.T) {
	if _, err := parseSplit([]byte(`{"result":{}}`)); err == nil {
		t.Fatal("want an error when no pane id is returned")
	}
}
