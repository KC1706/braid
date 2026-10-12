package merge

import (
	"context"
	"strings"
	"testing"

	"github.com/KC1706/braid/internal/resolver"
	"github.com/KC1706/braid/pkg/gitx"
)

// A textually-CLEAN merge that is semantically broken: agent-a renames a function
// (fixing its own caller), agent-b adds a NEW file still calling the old name.
// git reports no conflict, the merged tree fails to compile, and Braid repairs it
// deterministically (selector rewrite users.GetUser → users.FetchUser) → green.
func TestIntegrateSemanticRepairRename(t *testing.T) {
	dir := initRepo(t)
	commitBaseline(t, dir, map[string]string{
		"go.mod":         "module example.com/svc\n\ngo 1.26\n",
		"users/users.go": "package users\n\nfunc GetUser(id string) string { return \"u:\" + id }\n",
		"app.go":         "package svc\n\nimport \"example.com/svc/users\"\n\nfunc A() string { return users.GetUser(\"x\") }\n",
		"app_test.go":    "package svc\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tif A() == \"\" {\n\t\tt.Fatal(\"empty\")\n\t}\n}\n",
	})
	// agent-a: rename GetUser→FetchUser and update its own caller (builds on its branch).
	makeBranch(t, dir, "agent/rename", map[string]string{
		"users/users.go": "package users\n\nfunc FetchUser(id string) string { return \"u:\" + id }\n",
		"app.go":         "package svc\n\nimport \"example.com/svc/users\"\n\nfunc A() string { return users.FetchUser(\"x\") }\n",
	})
	// agent-b: a new file that still calls the OLD name (builds on its branch,
	// where GetUser still exists).
	makeBranch(t, dir, "agent/newcaller", map[string]string{
		"b.go": "package svc\n\nimport \"example.com/svc/users\"\n\nfunc B() string { return users.GetUser(\"y\") }\n",
	})

	res, err := Integrate(context.Background(), Options{
		RepoDir: dir, Baseline: "baseline", Task: "rename work",
		Agents: []AgentInput{
			{ID: "agent-a", Branch: "agent/rename", Intent: "rename getUser→fetchUser"},
			{ID: "agent-b", Branch: "agent/newcaller", Intent: "new caller"},
		},
		Resolver: resolver.UnionResolver{}, Ladder: newLadder(), MaxRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Green || res.Verdict != "green" {
		t.Fatalf("semantic repair should reach green, got verdict=%q green=%v\n%s",
			res.Verdict, res.Green, res.Trail.FinalTest)
	}
	// Both agents composed; neither dropped.
	if len(res.Trail.Agents) != 2 {
		t.Fatalf("want 2 agents, got %d", len(res.Trail.Agents))
	}
	for _, a := range res.Trail.Agents {
		if a.Dropped {
			t.Errorf("agent %s should not be dropped", a.Agent)
		}
	}
	// agent-b's new file was rewritten to the renamed symbol.
	b := readFileRel(t, dir, "b.go")
	if !strings.Contains(b, "users.FetchUser") || strings.Contains(b, "GetUser") {
		t.Errorf("b.go selector should be rewritten to FetchUser:\n%s", b)
	}
	// A semantic-repair commit is in the history.
	logOut, _ := gitx.Ok(dir, "log", "--oneline")
	if !strings.Contains(logOut, "semantic repair") {
		t.Errorf("expected a semantic-repair commit:\n%s", logOut)
	}
}
