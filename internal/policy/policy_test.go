package policy

import (
	"strings"
	"testing"
)

func ctx() Context {
	return Context{
		Agent: AgentInput{ID: "agent-c", Branch: "c", Intent: "caching"},
		IntegratedAgents: []AgentInput{
			{ID: "agent-a", Branch: "a", Intent: "validation"},
			{ID: "agent-b", Branch: "b", Intent: "ratelimit"},
		},
		Reason: "clean merge broke verification [red]",
	}
}

func TestEscalatePolicyNeverDrops(t *testing.T) {
	d := EscalatePolicy{}.Decide(ctx())
	if d.Action != ActionEscalate {
		t.Fatalf("want escalate, got %q", d.Action)
	}
	if d.Drop != "" {
		t.Errorf("escalate must drop nothing, dropped %q", d.Drop)
	}
	// The note names the incoming agent and the integrated set it clashed with.
	for _, want := range []string{"agent-c", "agent-a", "agent-b", "nothing dropped"} {
		if !strings.Contains(d.Note, want) {
			t.Errorf("note missing %q: %s", want, d.Note)
		}
	}
}

func TestEscalateWithNoIntegratedSaysBaseline(t *testing.T) {
	c := ctx()
	c.IntegratedAgents = nil
	d := EscalatePolicy{}.Decide(c)
	if !strings.Contains(d.Note, "baseline") {
		t.Errorf("empty integrated set should read as baseline: %s", d.Note)
	}
}

func TestPickWinnerDropsIncomingWhenNotOutranking(t *testing.T) {
	// agent-a and agent-b outrank agent-c → drop the incoming agent-c, continue.
	p := PickWinnerPolicy{Order: []string{"agent-a", "agent-b", "agent-c"}}
	d := p.Decide(ctx())
	if d.Action != ActionDrop {
		t.Fatalf("want drop, got %q", d.Action)
	}
	if d.Drop != "agent-c" {
		t.Errorf("want drop agent-c, got %q", d.Drop)
	}
	if !strings.Contains(d.Note, "lower-priority") {
		t.Errorf("note should explain lower priority: %s", d.Note)
	}
}

func TestPickWinnerOutrankingStillDropsIncomingButSaysSo(t *testing.T) {
	// agent-c is highest priority but prior work is already committed: still drop
	// agent-c this run, and the note must tell the operator how to prefer it.
	p := PickWinnerPolicy{Order: []string{"agent-c", "agent-a", "agent-b"}}
	d := p.Decide(ctx())
	if d.Action != ActionDrop || d.Drop != "agent-c" {
		t.Fatalf("want drop agent-c, got action=%q drop=%q", d.Action, d.Drop)
	}
	if !strings.Contains(d.Note, "ordered first") {
		t.Errorf("note should advise re-running with it first: %s", d.Note)
	}
}

func TestPickWinnerUnlistedRanksLast(t *testing.T) {
	// No order given: integrated set all rank MaxInt, incoming also MaxInt →
	// not strictly outranking → the plain lower-priority note.
	p := PickWinnerPolicy{}
	d := p.Decide(ctx())
	if d.Action != ActionDrop || d.Drop != "agent-c" {
		t.Fatalf("want drop agent-c, got %+v", d)
	}
	if strings.Contains(d.Note, "ordered first") {
		t.Errorf("equal ranks should not claim outranking: %s", d.Note)
	}
}

func TestPolicyNames(t *testing.T) {
	if (EscalatePolicy{}).Name() != "escalate" {
		t.Error("escalate name")
	}
	if (PickWinnerPolicy{}).Name() != "pick-winner" {
		t.Error("pick-winner name")
	}
}
