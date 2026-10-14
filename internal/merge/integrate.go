package merge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/KC1706/braid/internal/contract"
	"github.com/KC1706/braid/internal/determinism"
	"github.com/KC1706/braid/internal/ladder"
	"github.com/KC1706/braid/internal/policy"
	"github.com/KC1706/braid/internal/resolver"
	"github.com/KC1706/braid/internal/semantic"
	"github.com/KC1706/braid/pkg/gitx"
	"github.com/KC1706/braid/pkg/models"
)

// AgentInput is one agent branch to integrate.
type AgentInput struct {
	ID     string
	Branch string
	Intent string
}

// Options configures one integration run.
type Options struct {
	RepoDir           string
	Baseline          string // base branch/ref, e.g. "baseline" or "main"
	IntegrationBranch string // defaults to "braid/integration"
	Task              string
	Agents            []AgentInput
	Resolver          resolver.Resolver
	Ladder            *ladder.Ladder
	Policy            policy.Policy // defaults to EscalatePolicy
	MaxRetries        int
	// CanonicalOrder folds the agents in a deterministic, input-order-independent
	// sequence (by the content hash of each branch's diff against the baseline) so
	// the merged result does not depend on the order the caller supplied them
	// (GO_PRODUCTION_PLAN.md §4 2d). Off by default (callers that need a fixed
	// order, e.g. an explicit pick-winner priority, keep it off); run.Service
	// turns it on for production reproducibility.
	CanonicalOrder bool
	// Contracts, when supplied, enable the Rung-5 metamorphic gate: each agent's
	// acceptance contract (keyed to its AgentInput.ID) is snapshotted on its own
	// fork, then re-checked on the merged tree — a behavior that passed on a fork
	// but no longer passes on the merge is a contract violation (the feature did
	// not survive). Empty → the rung is skipped (GO_PRODUCTION_PLAN.md §3 2c).
	Contracts []contract.Contract
	// MetamorphicRunner runs the contract checks; nil defaults to the local
	// go-test runner. A sandboxed executor plugs in here unchanged.
	MetamorphicRunner contract.CheckRunner
	Log               func(string)
	Emit              func(models.Event)
}

// Result is the outcome of an integration run.
type Result struct {
	MergedSHA string
	Trail     models.Trail
	Green     bool
	Verdict   models.Verdict
}

// Integrate is the hybrid merge engine. It iteratively `git merge`s each agent
// branch into one integration branch: non-overlapping changes auto-resolve; a
// true conflict hunk is handed to the resolver with full context; after every
// merge the ladder decides pass/fail; a clean merge that fails only the build
// rung is routed through deterministic rename-repair; an agent that cannot be
// composed is undone and handed to the incompatibility policy. Finally it writes
// .braid/trail.json. It never panics on an unreconcilable agent (C4).
func Integrate(ctx context.Context, o Options) (Result, error) {
	if o.IntegrationBranch == "" {
		o.IntegrationBranch = "braid/integration"
	}
	pol := o.Policy
	if pol == nil {
		pol = policy.EscalatePolicy{}
	}
	log := o.Log
	if log == nil {
		log = func(string) {}
	}
	emit := o.Emit
	if emit == nil {
		emit = func(models.Event) {}
	}

	if o.CanonicalOrder {
		o.Agents = canonicalAgentOrder(o.RepoDir, o.Baseline, o.Agents)
	}

	if _, err := gitx.Ok(o.RepoDir, "config", "merge.conflictStyle", "diff3"); err != nil {
		return Result{}, err
	}
	if _, err := gitx.Ok(o.RepoDir, "checkout", "-B", o.IntegrationBranch, o.Baseline); err != nil {
		return Result{}, err
	}
	log(fmt.Sprintf("starting integration on %s from %s", o.IntegrationBranch, o.Baseline))

	var (
		conflicts         []models.ConflictRecord
		agentRecords      []models.AgentMergeRecord
		incompatibilities []models.IncompatibilityRecord
		integratedIntents []string
		integratedAgents  []policy.AgentInput
		runVerdict        = models.VerdictGreen
	)

	for _, agent := range o.Agents {
		parent, err := gitx.CurrentSHA(o.RepoDir)
		if err != nil {
			return Result{}, err
		}
		log(fmt.Sprintf("\n▶ merging %s (%s) — intent: %q", agent.ID, agent.Branch, agent.Intent))
		emit(models.Event{Type: "merge:start", Agent: agent.ID})

		mergeRes := gitx.Run(o.RepoDir, "merge", "--no-edit", "--no-ff", agent.Branch)
		var conflicted []string
		if mergeRes.Code != 0 {
			conflicted, err = gitx.ConflictedFiles(o.RepoDir)
			if err != nil {
				return Result{}, err
			}
		}

		record := models.AgentMergeRecord{
			Agent: agent.ID, Branch: agent.Branch, Intent: agent.Intent,
			AutoMergedFiles: []string{}, ConflictedFiles: conflicted,
		}

		var incompatible *incompat
		var agentConflicts []models.ConflictRecord
		oursIntent := strings.Join(integratedIntents, " + ")
		if oursIntent == "" {
			oursIntent = o.Baseline
		}

		if len(conflicted) == 0 {
			record.AutoMergedFiles, err = gitx.ChangedFiles(o.RepoDir, parent, "HEAD")
			if err != nil {
				return Result{}, err
			}
			log(fmt.Sprintf("  ✓ git auto-merged %d file(s) — no AI needed", len(record.AutoMergedFiles)))
			emit(models.Event{Type: "merge:auto", Agent: agent.ID})
		} else {
			log(fmt.Sprintf("  ⚡ %d file(s) conflicted — resolving true overlaps", len(conflicted)))
			emit(models.Event{Type: "conflict:detected", Agent: agent.ID})
			for _, file := range conflicted {
				resolved, lastFailure, err := resolveFileWithRetries(ctx, resolveArgs{
					o: o, file: file, oursIntent: oursIntent, agent: agent,
					conflicts: &agentConflicts, log: log, emit: emit,
				})
				if err != nil {
					return Result{}, err
				}
				if !resolved {
					incompatible = &incompat{
						reason:      fmt.Sprintf("conflict in %s could not be reconciled after %d attempt(s)", file, o.MaxRetries+1),
						file:        file,
						lastFailure: lastFailure,
					}
					break
				}
			}
			if incompatible == nil {
				if _, err := gitx.Ok(o.RepoDir, "commit", "--no-edit", "-m",
					fmt.Sprintf("braid: merge %s (%s)", agent.ID, agent.Intent)); err != nil {
					return Result{}, err
				}
				changed, err := gitx.ChangedFiles(o.RepoDir, parent, "HEAD")
				if err != nil {
					return Result{}, err
				}
				record.AutoMergedFiles = without(changed, conflicted)
			}
		}

		if incompatible == nil {
			v, err := verifyWithSemanticRepair(ctx, o, agent, log, emit)
			if err != nil {
				return Result{}, err
			}
			record.TestResult = v.Summary
			mark := "✅"
			if !v.Passed {
				mark = "❌"
			}
			log(fmt.Sprintf("  %s [%s] %s", mark, v.Verdict, v.Summary))
			emit(models.Event{Type: "verify", Status: agent.ID, Verdict: v.Verdict, Rungs: v.Rungs, Message: v.Summary})
			if !v.Passed {
				incompatible = &incompat{
					reason:      fmt.Sprintf("verification failed [%s] and could not be reconciled", v.Verdict),
					lastFailure: v.Output,
				}
			}
		}

		if incompatible != nil {
			if err := abortOrReset(o.RepoDir, parent); err != nil {
				return Result{}, err
			}
			decision := pol.Decide(policy.Context{
				Agent:            policy.AgentInput(agent),
				IntegratedAgents: integratedAgents,
				File:             incompatible.file,
				Reason:           incompatible.reason,
				LastFailure:      incompatible.lastFailure,
			})
			inc := models.IncompatibilityRecord{
				Agent: agent.ID, Intent: agent.Intent,
				WithIntents: append([]string{}, integratedIntents...),
				Reason:      incompatible.reason, File: incompatible.file,
				Action: string(decision.Action), Note: decision.Note,
			}
			if incompatible.lastFailure != "" {
				inc.Detail = tailLines(incompatible.lastFailure, 30)
			}
			if decision.Action == policy.ActionDrop {
				inc.Dropped = decision.Drop
			}
			incompatibilities = append(incompatibilities, inc)
			emit(models.Event{Type: "incompatible", Agent: agent.ID, Message: decision.Note})

			if decision.Action == policy.ActionEscalate {
				log(fmt.Sprintf("  ⛔ incompatible: %s", decision.Note))
				runVerdict = models.VerdictIncompatible
				break
			}
			log(fmt.Sprintf("  ⚠ dropped %s: %s", agent.ID, decision.Note))
			record.Dropped = true
			record.DropReason = decision.Note
			record.TestResult = "dropped (incompatible): " + incompatible.reason
			agentRecords = append(agentRecords, record)
			continue
		}

		conflicts = append(conflicts, agentConflicts...)
		integratedIntents = append(integratedIntents, agent.Intent)
		integratedAgents = append(integratedAgents, policy.AgentInput(agent))
		agentRecords = append(agentRecords, record)
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

	// Rung 5 (metamorphic contract preservation): only meaningful once the ladder
	// is otherwise green — a red merge has already failed. A surviving-behavior
	// violation turns a build-and-test-green merge red (a feature silently didn't
	// survive). The integration branch is restored before we return.
	var metamorphic *models.MetamorphicRecord
	if len(o.Contracts) > 0 && finalVerify.Passed {
		rec, err := runMetamorphic(ctx, o, log, emit)
		if err != nil {
			return Result{}, err
		}
		metamorphic = rec
		if !rec.Passed {
			runVerdict = models.VerdictRed
		}
	}

	green := runVerdict == models.VerdictGreen && finalVerify.Passed && (metamorphic == nil || metamorphic.Passed)

	trail := models.Trail{
		Task:              o.Task,
		CreatedAt:         time.Now().UTC().Format(time.RFC3339),
		Baseline:          o.Baseline,
		MergedSHA:         mergedSHA,
		Agents:            agentRecords,
		Conflicts:         conflicts,
		Incompatibilities: incompatibilities,
		Verdict:           runVerdict,
		FinalTest:         finalVerify.Summary,
		FinalVerification: &models.VerificationRecord{
			Verdict: finalVerify.Verdict, Summary: finalVerify.Summary, Rungs: finalVerify.Rungs,
		},
		Metamorphic: metamorphic,
		Summary: models.TrailSummary{
			AgentsMerged:      countMerged(agentRecords),
			FilesAutoMerged:   countAutoMerged(agentRecords),
			ConflictsResolved: len(conflicts),
			LLMResolutions:    countLLM(conflicts),
		},
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

// runMetamorphic executes the Rung-5 gate. For every supplied contract whose
// agent was actually integrated, it checks out that agent's fork and snapshots
// the contract's observable behavior (the golden), restores the integration
// branch, then asserts the merged tree still satisfies every golden. A violation
// (passed on the fork, no longer on the merge) means a feature did not survive.
func runMetamorphic(ctx context.Context, o Options, log func(string), emit func(models.Event)) (*models.MetamorphicRecord, error) {
	runner := o.MetamorphicRunner
	if runner == nil {
		runner = contract.GoTestRunner{}
	}
	branchByAgent := map[string]string{}
	for _, a := range o.Agents {
		branchByAgent[a.ID] = a.Branch
	}

	var active []contract.Contract
	var forks []contract.Snapshot
	for _, c := range o.Contracts {
		branch, ok := branchByAgent[c.Agent]
		if !ok {
			continue // contract with no matching integrated agent: skip
		}
		if _, err := gitx.Ok(o.RepoDir, "checkout", "-q", branch); err != nil {
			return nil, err
		}
		snap, err := contract.TakeSnapshot(ctx, o.RepoDir, c, runner)
		if err != nil {
			return nil, err
		}
		forks = append(forks, snap)
		active = append(active, c)
	}

	// Restore the integration branch (the merged tree) before re-checking.
	if _, err := gitx.Ok(o.RepoDir, "checkout", "-q", o.IntegrationBranch); err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return &models.MetamorphicRecord{Passed: true}, nil
	}

	violations, err := contract.CheckPreservation(ctx, o.RepoDir, forks, active, runner)
	if err != nil {
		return nil, err
	}
	rec := &models.MetamorphicRecord{Passed: len(violations) == 0, Checked: len(active)}
	for _, v := range violations {
		rec.Violations = append(rec.Violations, models.ContractViolation{
			Agent: v.Agent, Check: v.Check, Test: v.Test, Reason: v.Reason, Detail: v.Detail,
		})
		log(fmt.Sprintf("⚠ metamorphic: %s's %q did not survive the merge (%s)", v.Agent, v.Check, v.Reason))
	}
	emit(models.Event{
		Type: "verify", Status: "metamorphic",
		Verdict: metamorphicVerdict(rec.Passed),
		Message: fmt.Sprintf("metamorphic: %d contract(s) checked, %d violation(s)", rec.Checked, len(rec.Violations)),
	})
	return rec, nil
}

func metamorphicVerdict(passed bool) models.Verdict {
	if passed {
		return models.VerdictGreen
	}
	return models.VerdictRed
}

// incompat captures why an agent could not be composed.
type incompat struct {
	reason      string
	file        string
	lastFailure string
}

// abortOrReset undoes an in-progress or just-committed merge, restoring HEAD to
// parent — the merge is undone, not left half-applied.
func abortOrReset(repoDir, parent string) error {
	if gitx.Run(repoDir, "merge", "--abort").Code == 0 {
		return nil
	}
	_, err := gitx.Ok(repoDir, "reset", "--hard", parent)
	return err
}

// verifyWithSemanticRepair runs the ladder; if a textually-clean merge fails
// ONLY the build rung (Rung 2 — in Go a dangling cross-package reference is a
// compile error), it routes each break through deterministic rename-repair
// (selector rewrite a.Old→a.New), commits, and re-verifies. Bounded. Turns Rung
// 3 from "detect and fail" into "detect and resolve" (C1).
func verifyWithSemanticRepair(ctx context.Context, o Options, agent AgentInput, log func(string), emit func(models.Event)) (models.LadderResult, error) {
	v, err := o.Ladder.Run(ctx, o.RepoDir, o.Baseline)
	if err != nil {
		return models.LadderResult{}, err
	}
	const maxRounds = 6
	for round := 0; !v.Passed && isBuildOnlyFailure(v) && round < maxRounds; round++ {
		repaired, err := repairOneSemanticConflict(o.RepoDir, o.Baseline, v.Output, log)
		if err != nil {
			return models.LadderResult{}, err
		}
		if repaired == "" {
			break // nothing safe to infer — hand to the policy
		}
		emit(models.Event{Type: "log", Message: "semantic repair: " + repaired})
		if _, err := gitx.Ok(o.RepoDir, "add", "-A"); err != nil {
			return models.LadderResult{}, err
		}
		if _, err := gitx.Ok(o.RepoDir, "commit", "--no-edit", "-m",
			fmt.Sprintf("braid: semantic repair for %s (%s)", agent.ID, repaired)); err != nil {
			return models.LadderResult{}, err
		}
		v, err = o.Ladder.Run(ctx, o.RepoDir, o.Baseline)
		if err != nil {
			return models.LadderResult{}, err
		}
	}
	return v, nil
}

// isBuildOnlyFailure is true when the ladder's single failing rung is the build.
func isBuildOnlyFailure(v models.LadderResult) bool {
	var failed []models.RungResult
	for _, r := range v.Rungs {
		if !r.Passed {
			failed = append(failed, r)
		}
	}
	return len(failed) == 1 && failed[0].Name == "build"
}

// repairOneSemanticConflict fixes a single dangling cross-package reference by
// inferring the rename from the target package's base-vs-now exports and
// rewriting the call sites. One per call so the caller re-detects between rounds.
// Returns a short description, or "" if nothing safe could be done.
func repairOneSemanticConflict(repoDir, baseline, buildDiag string, log func(string)) (string, error) {
	refs, err := semantic.SemanticConflicts(repoDir, buildDiag)
	if err != nil {
		return "", err
	}
	modulePath, err := moduleName(repoDir)
	if err != nil || modulePath == "" {
		return "", nil
	}
	var a semantic.GoAnalyzer
	for _, ref := range refs {
		if ref.Kind != "missing-export" || ref.Pkg == "" {
			continue
		}
		fromSrc, err := os.ReadFile(filepath.Join(repoDir, ref.FromFile))
		if err != nil {
			continue
		}
		importPath := a.ImportPathFor(string(fromSrc), ref.Pkg)
		if importPath == "" || !strings.HasPrefix(importPath, modulePath) {
			continue // external package: not ours to repair
		}
		pkgDir := filepath.Join(repoDir, filepath.FromSlash(strings.TrimPrefix(importPath, modulePath)))
		baseSrcs, nowSrcs := packageSources(repoDir, baseline, pkgDir)
		newName, ok := semantic.InferRenameAcross(baseSrcs, nowSrcs, ref.Name)
		if !ok {
			continue
		}
		n, err := semantic.RepairRename(repoDir, ref.Pkg, ref.Name, newName)
		if err != nil {
			return "", err
		}
		if n > 0 {
			msg := fmt.Sprintf("%s.%s → %s.%s (renamed in %s)", ref.Pkg, ref.Name, ref.Pkg, newName,
				strings.TrimPrefix(importPath, modulePath+"/"))
			log("    🔧 " + msg)
			return msg, nil
		}
	}
	return "", nil
}

// packageSources returns a package directory's non-test .go sources at the
// baseline ref (via git) and as they now stand on disk.
func packageSources(repoDir, baseline, pkgDir string) (baseSrcs, nowSrcs []string) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, nil
	}
	rel, _ := filepath.Rel(repoDir, pkgDir)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(pkgDir, name)); err == nil {
			nowSrcs = append(nowSrcs, string(b))
		}
		relPath := filepath.ToSlash(filepath.Join(rel, name))
		if src, exists, _ := gitx.ShowFile(repoDir, baseline, relPath); exists {
			baseSrcs = append(baseSrcs, src)
		}
	}
	return baseSrcs, nowSrcs
}

var reModule = regexp.MustCompile(`(?m)^module\s+(\S+)`)

// moduleName reads the module path from the repo's go.mod.
func moduleName(repoDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(repoDir, "go.mod"))
	if err != nil {
		return "", err
	}
	if m := reModule.FindStringSubmatch(string(b)); m != nil {
		return m[1], nil
	}
	return "", nil
}

// canonicalAgentOrder reorders agents deterministically by the content of each
// branch's diff against the baseline, so the fold is independent of the order the
// agents were supplied (GO_PRODUCTION_PLAN.md §4 2d). The diff is the "content";
// ID+Branch+Intent are folded in too as a stable tiebreak and so the ordering is
// still defined if a diff can't be computed (missing branch → empty diff).
func canonicalAgentOrder(repoDir, baseline string, agents []AgentInput) []AgentInput {
	return determinism.CanonicalOrder(agents, func(a AgentInput) string {
		diff, err := gitx.Ok(repoDir, "diff", baseline, a.Branch)
		if err != nil {
			diff = ""
		}
		return a.ID + "\x00" + a.Branch + "\x00" + a.Intent + "\x00" + diff
	})
}

// resolveArgs bundles the inputs to resolveFileWithRetries.
type resolveArgs struct {
	o          Options
	lad        *ladder.Ladder // ladder to verify each resolution against (nil → o.Ladder)
	file       string
	oursIntent string
	agent      AgentInput
	conflicts  *[]models.ConflictRecord
	log        func(string)
	emit       func(models.Event)
}

// resolveFileWithRetries resolves every conflict hunk in one file then proves it
// with the ladder; on red it re-resolves with attempt+1 and the failure fed back,
// up to MaxRetries. Returns resolved=false (not an error) when every attempt
// stays red, so the caller applies the incompatibility policy (C4).
func resolveFileWithRetries(ctx context.Context, a resolveArgs) (resolved bool, lastFailure string, err error) {
	abs := filepath.Join(a.o.RepoDir, a.file)
	raw, err := os.ReadFile(abs)
	if err != nil {
		return false, "", err
	}
	conflictedContent := string(raw)
	hunks := ParseConflicts(conflictedContent)
	lad := a.lad
	if lad == nil {
		lad = a.o.Ladder
	}

	for attempt := 0; attempt <= a.o.MaxRetries; attempt++ {
		outputs := make([]resolver.ResolveOutput, 0, len(hunks))
		for _, hunk := range hunks {
			out, rErr := a.o.Resolver.Resolve(ctx, resolver.ResolveInput{
				Path: a.file, Hunk: hunk, OursIntent: a.oursIntent,
				TheirsIntent: a.agent.Intent, Attempt: attempt, LastFailure: lastFailure,
			})
			if rErr != nil {
				return false, "", rErr
			}
			outputs = append(outputs, out)
		}
		strategy := "?"
		if len(outputs) > 0 {
			strategy = outputs[0].Strategy
		}
		emit := a.emit
		emit(models.Event{Type: "conflict:attempt", Agent: a.agent.ID, File: a.file, Attempt: attempt + 1, Strategy: strategy})

		contents := make([]string, len(outputs))
		for i, o := range outputs {
			contents[i] = o.Content
		}
		applied := ApplyResolutions(conflictedContent, contents)
		// Phase-2a guards: reject a resolution that edits outside its conflict
		// region, exceeds its bounds, smuggles a marker, or (for Go) no longer
		// parses — before it is written or the tests run. Treat a violation like
		// a red verdict: feed the reason back and retry (→ escalate on exhaustion).
		if gErr := ladder.GuardResolution(a.file, conflictedContent, applied, hunks, contents); gErr != nil {
			lastFailure = gErr.Error()
			a.log(fmt.Sprintf("    ⚠ %s: attempt %d rejected by resolution guard: %v", a.file, attempt+1, gErr))
			continue
		}
		if err := os.WriteFile(abs, []byte(applied), 0o644); err != nil {
			return false, "", err
		}
		if _, err := gitx.Ok(a.o.RepoDir, "add", a.file); err != nil {
			return false, "", err
		}

		v, err := lad.Run(ctx, a.o.RepoDir, a.o.Baseline)
		if err != nil {
			return false, "", err
		}
		if v.Passed {
			for i, hunk := range hunks {
				*a.conflicts = append(*a.conflicts, models.ConflictRecord{
					Path: a.file, Base: strings.Join(hunk.Base, "\n"),
					Candidates: []models.Candidate{
						{Agent: "integrated", Intent: a.oursIntent, Content: strings.Join(hunk.Ours, "\n")},
						{Agent: a.agent.ID, Intent: a.agent.Intent, Content: strings.Join(hunk.Theirs, "\n")},
					},
					Resolution: outputs[i].Content, Rationale: outputs[i].Rationale,
					Strategy: outputs[i].Strategy, Verified: v.Summary, Attempts: attempt + 1,
				})
			}
			a.log(fmt.Sprintf("    ✓ %s: %d hunk(s) via %s (attempt %d) — %s", a.file, len(hunks), strategy, attempt+1, v.Summary))
			emit(models.Event{Type: "conflict:resolved", Agent: a.agent.ID, File: a.file, Strategy: strategy, Attempt: attempt + 1, Message: v.Summary})
			return true, "", nil
		}
		lastFailure = v.Output
		a.log(fmt.Sprintf("    ↻ %s: attempt %d red, retrying with failure fed back", a.file, attempt+1))
	}
	a.log(fmt.Sprintf("    ⛔ %s: unresolved after %d attempt(s) — incompatible", a.file, a.o.MaxRetries+1))
	if lastFailure == "" {
		lastFailure = "no failure output captured"
	}
	return false, lastFailure, nil
}

// writeTrail serializes the trail to .braid/trail.json and commits it.
func writeTrail(repoDir string, trail models.Trail) error {
	dir := filepath.Join(repoDir, ".braid")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(trail, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "trail.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	if _, err := gitx.Ok(repoDir, "add", ".braid/trail.json"); err != nil {
		return err
	}
	_, err = gitx.Ok(repoDir, "commit", "-m", "braid: provenance trail")
	return err
}

// --- small helpers ---

func without(all, remove []string) []string {
	skip := map[string]bool{}
	for _, r := range remove {
		skip[r] = true
	}
	var out []string
	for _, f := range all {
		if !skip[f] {
			out = append(out, f)
		}
	}
	return out
}

func countMerged(records []models.AgentMergeRecord) int {
	n := 0
	for _, r := range records {
		if !r.Dropped {
			n++
		}
	}
	return n
}

func countAutoMerged(records []models.AgentMergeRecord) int {
	seen := map[string]bool{}
	for _, r := range records {
		for _, f := range r.AutoMergedFiles {
			seen[f] = true
		}
	}
	return len(seen)
}

func countLLM(conflicts []models.ConflictRecord) int {
	n := 0
	for _, c := range conflicts {
		if c.Strategy == "llm" {
			n++
		}
	}
	return n
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
