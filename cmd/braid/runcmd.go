package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KC1706/braid/internal/gitio"
	"github.com/KC1706/braid/internal/run"
)

// agentFlags collects repeatable -agent id=branch[:intent] values.
type agentFlags []run.AgentSpec

func (a *agentFlags) String() string { return fmt.Sprintf("%d agent(s)", len(*a)) }

func (a *agentFlags) Set(v string) error {
	eq := strings.IndexByte(v, '=')
	if eq < 0 {
		return fmt.Errorf("agent %q must be id=branch[:intent]", v)
	}
	id := v[:eq]
	rest := v[eq+1:]
	branch, intent := rest, ""
	if c := strings.IndexByte(rest, ':'); c >= 0 {
		branch, intent = rest[:c], rest[c+1:]
	}
	if id == "" || branch == "" {
		return fmt.Errorf("agent %q must have a non-empty id and branch", v)
	}
	*a = append(*a, run.AgentSpec{ID: id, Branch: branch, Intent: intent})
	return nil
}

// runOnce integrates a prepared repo through run.Service (the compute zone), then
// — if the merge is green and a base remote is given — hands the merged tree to
// the trusted gitio zone as a content-addressed bundle and publishes it with a
// compare-and-swap on the base tip. It is the local, end-to-end demonstration of
// the two-zone flow the control-plane Worker drives in production.
func runOnce(argv []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	repoDir := fs.String("repo", "", "prepared repo dir (baseline + agent branches already present) [required]")
	baseline := fs.String("baseline", "baseline", "baseline ref the agents branched from")
	integration := fs.String("integration-branch", "braid/integration", "integration branch name")
	task := fs.String("task", "", "human-readable task description")
	resolver := fs.String("resolver", "union", "conflict resolver: union|auto|hybrid|workersai|claude")
	sandbox := fs.Bool("sandbox", false, "run build+test inside the sandbox executor (no creds, egress denied)")
	vet := fs.Bool("vet", false, "add `go vet` to the build rung")
	tree := fs.Bool("tree", false, "fold agents as a pairwise tree (O(log N) depth) instead of linearly")
	maxRetries := fs.Int("max-retries", 2, "resolver retries per conflicted file before escalating")
	remote := fs.String("remote", "", "base git remote to publish to (path or https URL); empty = compute only")
	branch := fs.String("branch", "main", "base branch on the remote to publish to")
	var agents agentFlags
	fs.Var(&agents, "agent", "repeatable: id=branch[:intent]")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *repoDir == "" {
		return fmt.Errorf("-repo is required")
	}
	if len(agents) == 0 {
		return fmt.Errorf("at least one -agent id=branch is required")
	}

	svc := run.Service{Log: func(m string) { fmt.Println(m) }}
	res, err := svc.Run(context.Background(), run.RunSpec{
		RepoDir:           *repoDir,
		Baseline:          *baseline,
		IntegrationBranch: *integration,
		Task:              *task,
		Agents:            []run.AgentSpec(agents),
		MaxRetries:        *maxRetries,
		Resolver:          *resolver,
		Sandbox:           *sandbox,
		Vet:               *vet,
		Tree:              *tree,
		DashboardURL:      os.Getenv("BRAID_WORKER_URL"),
		RunID:             os.Getenv("BRAID_RUN_ID"),
	})
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("verdict:   %s\n", res.Verdict)
	fmt.Printf("green:     %v\n", res.Green)
	fmt.Printf("merged:    %s\n", res.MergedSHA)
	s := res.Trail.Summary
	fmt.Printf("agents:    %d merged · %d file(s) auto-merged · %d conflict(s) resolved (%d via LLM)\n",
		s.AgentsMerged, s.FilesAutoMerged, s.ConflictsResolved, s.LLMResolutions)
	if m := res.Trail.Metamorphic; m != nil {
		fmt.Printf("rung-5:    %d contract(s) checked, %d violation(s)\n", m.Checked, len(m.Violations))
	}
	fmt.Printf("trail:     %s\n", filepath.Join(*repoDir, ".braid", "trail.json"))

	if !res.Green {
		return fmt.Errorf("run did not reach green (verdict %s) — not publishing", res.Verdict)
	}
	if *remote == "" {
		return nil // compute only
	}

	// --- trusted git-I/O zone: bundle hand-off + CAS publish ---
	pushed, err := publish(*repoDir, *baseline, *integration, *remote, *branch)
	if err != nil {
		return err
	}
	fmt.Printf("published: %s → %s %s\n", pushed, *remote, *branch)
	return nil
}

// publish writes a content-addressed bundle of the integration branch in the
// compute repo, then clones the base into a fresh trusted workdir and pushes the
// bundle with a parent-SHA compare-and-swap. The Artifacts token, if any, comes
// from BRAID_ARTIFACTS_TOKEN and lives only in this zone.
func publish(repoDir, baseline, integration, remote, branch string) (string, error) {
	ctx := context.Background()
	tmp, err := os.MkdirTemp("", "braid-gitio-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	bundlePath := filepath.Join(tmp, "merge.bundle")
	b, err := gitio.WriteBundle(ctx, repoDir, baseline, integration, bundlePath)
	if err != nil {
		return "", fmt.Errorf("bundle: %w", err)
	}
	zone := gitio.Zone{Remote: remote}
	if tok := os.Getenv("BRAID_ARTIFACTS_TOKEN"); tok != "" {
		zone.Token = gitio.Token{Plaintext: tok}
	}

	work := filepath.Join(tmp, "work")
	if _, err := zone.CloneBase(ctx, work, branch); err != nil {
		return "", fmt.Errorf("clone base: %w", err)
	}
	pushed, err := zone.PushBundle(ctx, work, branch, b)
	if err != nil {
		return "", fmt.Errorf("publish: %w", err)
	}
	return pushed, nil
}
