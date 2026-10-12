package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// The trail is read by the existing TS edge/dashboard, so the JSON keys must be
// the camelCase TS names, and optional fields must drop out when empty.
func TestTrailJSONFieldNamesMatchTS(t *testing.T) {
	tr := Trail{
		Task:      "demo",
		CreatedAt: "2026-10-12T00:00:00Z",
		Baseline:  "abc",
		MergedSHA: "def",
		Agents: []AgentMergeRecord{{
			Agent: "agent-1", Branch: "a1", Intent: "validation",
			AutoMergedFiles: []string{"x.go"}, ConflictedFiles: []string{},
			TestResult: "2/2",
		}},
		Conflicts: []ConflictRecord{},
		Verdict:   VerdictGreen,
		FinalTest: "green",
		Summary:   TrailSummary{AgentsMerged: 1, FilesAutoMerged: 1},
	}
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, key := range []string{
		`"createdAt"`, `"mergedSha"`, `"autoMergedFiles"`, `"conflictedFiles"`,
		`"testResult"`, `"agentsMerged"`, `"filesAutoMerged"`,
	} {
		if !strings.Contains(s, key) {
			t.Errorf("trail JSON missing expected TS key %s\n%s", key, s)
		}
	}
	// Empty optional slices/pointers must not appear.
	for _, absent := range []string{`"incompatibilities"`, `"finalVerification"`} {
		if strings.Contains(s, absent) {
			t.Errorf("empty optional %s should be omitted:\n%s", absent, s)
		}
	}
}

func TestLadderResultRoundTrip(t *testing.T) {
	in := LadderResult{
		Verdict: VerdictRed,
		Passed:  false,
		Summary: "tests red",
		Output:  "1 failed",
		Rungs: []RungResult{
			{Rung: 1, Name: "structural-guards", Passed: true, Summary: "ok"},
			{Rung: 4, Name: "test-execution", Passed: false, Summary: "1 failed", Detail: "boom"},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out LadderResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Verdict != VerdictRed || out.Passed || len(out.Rungs) != 2 {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
	if out.Rungs[0].Detail != "" {
		t.Errorf("passing rung should omit detail, got %q", out.Rungs[0].Detail)
	}
	if out.Rungs[1].Detail != "boom" {
		t.Errorf("failing rung detail lost: %q", out.Rungs[1].Detail)
	}
}
