package merge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/KC1706/braid/internal/ladder"
	"github.com/KC1706/braid/internal/treemerge"
	"github.com/KC1706/braid/pkg/gitx"
	"github.com/KC1706/braid/pkg/models"
)

// IntegrateTree folds the agents as a balanced pairwise reduction tree
// (treemerge.Plan) instead of the linear fold of Integrate: each internal node
// independently merges two sibling subtrees — a self-contained job a sandbox pool
// can parallelize — resolving true conflicts with the resolver verified by a
// build-level ladder, and the FULL ladder (incl. the test suite) runs once at the
// ROOT. Depth is O(log N) vs the linear O(N). Agents are folded in canonical
// order, so with a caching resolver the merged tree is reproducible (2d+2e).
//
// This is a sibling entry point to Integrate; the proven linear path is
// unchanged. Incompatibility handling here is intentionally coarse (an
// unresolvable node escalates the whole run) — per-agent policy attribution over
// a tree fold is a later refinement.
func IntegrateTree(ctx context.Context, o Options) (Result, error) {
	if o.IntegrationBranch == "" {
		o.IntegrationBranch = "braid/integration"
	}
	log := o.Log
	if log == nil {
		log = func(string) {}
	}
	emit := o.Emit
	if emit == nil {
		emit = func(models.Event) {}
	}

	if _, err := gitx.Ok(o.RepoDir, "config", "merge.conflictStyle", "diff3"); err != nil {
		return Result{}, err
	}

	agents := canonicalAgentOrder(o.RepoDir, o.Baseline, o.Agents)
	plan := treemerge.Plan(len(agents))
	log(fmt.Sprintf("tree merge: folding %d agent(s), tree depth %d", len(agents), plan.Depth()))

	var conflicts []models.ConflictRecord
	te := &treeExec{
		o: o, agents: agents,
		// Internal nodes verify with structural guards + build only; the test
		// suite is the root's job (GO_PRODUCTION_PLAN.md §4 2e).
		nodeLadder: &ladder.Ladder{Build: o.Ladder.Build},
		log:        log, emit: emit, conflicts: &conflicts,
	}

	rootSHA, _, inc, err := te.fold(ctx, plan)
	if err != nil {
		return Result{}, err
	}

	var (
		agentRecords []models.AgentMergeRecord
		incompat     []models.IncompatibilityRecord
		runVerdict   = models.VerdictGreen
	)
	for _, a := range agents {
		agentRecords = append(agentRecords, models.AgentMergeRecord{
			Agent: a.ID, Branch: a.Branch, Intent: a.Intent,
			AutoMergedFiles: []string{}, ConflictedFiles: []string{},
		})
	}

	if inc != nil {
		// The tree could not be composed: land the integration branch on the
		// baseline (nothing merged) and escalate.
		if _, err := gitx.Ok(o.RepoDir, "checkout", "-B", o.IntegrationBranch, o.Baseline); err != nil {
			return Result{}, err
		}
		runVerdict = models.VerdictIncompatible
		incompat = append(incompat, models.IncompatibilityRecord{
			Reason: inc.reason, File: inc.file, Action: "escalate",
			Note:   "tree node could not be reconciled",
			Detail: tailLines(inc.lastFailure, 30),
		})
		emit(models.Event{Type: "incompatible", Message: inc.reason})
		log(fmt.Sprintf("  ⛔ incompatible: %s", inc.reason))
	} else if _, err := gitx.Ok(o.RepoDir, "checkout", "-B", o.IntegrationBranch, rootSHA); err != nil {
		return Result{}, err
	}

	mergedSHA, err := gitx.CurrentSHA(o.RepoDir)
	if err != nil {
		return Result{}, err
	}

	finalVerify, err := o.Ladder.Run(ctx, o.RepoDir, o.Baseline)
	if err != nil {
		return Result{}, err
	}
	emit(models.Event{Type: "verify", Status: "final", Verdict: finalVerify.Verdict, Rungs: finalVerify.Rungs, Message: finalVerify.Summary})
	if runVerdict != models.VerdictIncompatible {
		runVerdict = finalVerify.Verdict
	}
	green := runVerdict == models.VerdictGreen && finalVerify.Passed

	trail := models.Trail{
		Task:              o.Task,
		CreatedAt:         time.Now().UTC().Format(time.RFC3339),
		Baseline:          o.Baseline,
		MergedSHA:         mergedSHA,
		Agents:            agentRecords,
		Conflicts:         conflicts,
		Incompatibilities: incompat,
		Verdict:           runVerdict,
		FinalTest:         finalVerify.Summary,
		FinalVerification: &models.VerificationRecord{
			Verdict: finalVerify.Verdict, Summary: finalVerify.Summary, Rungs: finalVerify.Rungs,
		},
		Summary: models.TrailSummary{
			AgentsMerged:      countMerged(agentRecords),
			FilesAutoMerged:   countAutoMerged(agentRecords),
			ConflictsResolved: len(conflicts),
			LLMResolutions:    countLLM(conflicts),
		},
	}
	if runVerdict == models.VerdictIncompatible {
		trail.Summary.AgentsMerged = 0
	}

	if err := writeTrail(o.RepoDir, trail); err != nil {
		return Result{}, err
	}
	finalSHA, err := gitx.CurrentSHA(o.RepoDir)
	if err != nil {
		return Result{}, err
	}
	return Result{MergedSHA: finalSHA, Trail: trail, Green: green, Verdict: runVerdict}, nil
}

// treeExec carries the shared state while folding the reduction tree.
type treeExec struct {
	o          Options
	agents     []AgentInput
	nodeLadder *ladder.Ladder
	log        func(string)
	emit       func(models.Event)
	conflicts  *[]models.ConflictRecord
	nodeSeq    int
}

// fold executes one node of the reduction tree, returning the resulting commit
// SHA and the intents folded under it. A leaf resolves to its agent's branch
// commit; an internal node merges its two children on a fresh node branch,
// resolving conflicts with the resolver (build-level ladder). A node that cannot
// be reconciled returns a non-nil *incompat and the fold short-circuits.
func (te *treeExec) fold(ctx context.Context, n *treemerge.Node) (sha string, intents []string, inc *incompat, err error) {
	if n == nil {
		// No agents: fold is the baseline.
		s, e := gitx.Ok(te.o.RepoDir, "rev-parse", te.o.Baseline)
		return strings.TrimSpace(s), nil, nil, e
	}
	if n.IsLeaf() {
		a := te.agents[n.Leaf]
		s, e := gitx.Ok(te.o.RepoDir, "rev-parse", a.Branch)
		return strings.TrimSpace(s), []string{a.Intent}, nil, e
	}

	leftSHA, leftIntents, inc, err := te.fold(ctx, n.Left)
	if err != nil || inc != nil {
		return "", nil, inc, err
	}
	rightSHA, rightIntents, inc, err := te.fold(ctx, n.Right)
	if err != nil || inc != nil {
		return "", nil, inc, err
	}

	te.nodeSeq++
	nodeBranch := fmt.Sprintf("braid/node-%d", te.nodeSeq)
	if _, err := gitx.Ok(te.o.RepoDir, "checkout", "-B", nodeBranch, leftSHA); err != nil {
		return "", nil, nil, err
	}

	oursIntent := strings.Join(leftIntents, " + ")
	theirsIntent := strings.Join(rightIntents, " + ")
	te.log(fmt.Sprintf("  ⋈ node %s: merge [%s] ⊕ [%s]", nodeBranch, oursIntent, theirsIntent))

	mergeRes := gitx.Run(te.o.RepoDir, "merge", "--no-edit", "--no-ff", rightSHA)
	if mergeRes.Code != 0 {
		conflicted, err := gitx.ConflictedFiles(te.o.RepoDir)
		if err != nil {
			return "", nil, nil, err
		}
		// Synthetic "agent" for the right subtree, used for resolver context and
		// conflict-record attribution.
		rightAgent := AgentInput{ID: nodeBranch, Branch: rightSHA, Intent: theirsIntent}
		for _, file := range conflicted {
			resolved, lastFailure, err := resolveFileWithRetries(ctx, resolveArgs{
				o: te.o, lad: te.nodeLadder, file: file, oursIntent: oursIntent,
				agent: rightAgent, conflicts: te.conflicts, log: te.log, emit: te.emit,
			})
			if err != nil {
				return "", nil, nil, err
			}
			if !resolved {
				_, _ = gitx.Ok(te.o.RepoDir, "merge", "--abort")
				return "", nil, &incompat{
					reason:      fmt.Sprintf("tree node %s: conflict in %s unresolved after %d attempt(s)", nodeBranch, file, te.o.MaxRetries+1),
					file:        file,
					lastFailure: lastFailure,
				}, nil
			}
		}
		if _, err := gitx.Ok(te.o.RepoDir, "commit", "--no-edit", "-m",
			fmt.Sprintf("braid: tree-merge %s", nodeBranch)); err != nil {
			return "", nil, nil, err
		}
	}

	s, err := gitx.CurrentSHA(te.o.RepoDir)
	if err != nil {
		return "", nil, nil, err
	}
	return s, append(leftIntents, rightIntents...), nil, nil
}
