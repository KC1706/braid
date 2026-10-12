// Command braid is the Braid integrator entrypoint — the CLI and sandbox binary
// that drives the merge + verification pipeline.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/KC1706/braid/internal/demo"
	"github.com/KC1706/braid/internal/ladder"
	"github.com/KC1706/braid/internal/merge"
	"github.com/KC1706/braid/internal/resolver"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "version", "--version":
		fmt.Printf("braid %s\n", version)
	case "demo":
		if err := runDemo(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "braid demo:", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: braid <command>")
	fmt.Fprintln(os.Stderr, "  demo      seed a throwaway Go repo + 3 agents and integrate them")
	fmt.Fprintln(os.Stderr, "  version   print the version")
}

// runDemo seeds the Go demo fixture, integrates the three agents, and prints the
// verdict + trail summary — the Go equivalent of the TS demo/run.ts.
func runDemo(argv []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	repoDir := fs.String("repo", "", "repo dir to seed (default: a temp dir)")
	keep := fs.Bool("keep", false, "keep the seeded repo after running")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	dir := *repoDir
	if dir == "" {
		tmp, err := os.MkdirTemp("", "braid-demo-")
		if err != nil {
			return err
		}
		dir = filepath.Join(tmp, "repo")
	}

	fmt.Printf("▶ seeding demo repo at %s\n", dir)
	baseline, agents, err := demo.Seed(dir)
	if err != nil {
		return err
	}
	if !*keep && *repoDir == "" {
		defer os.RemoveAll(filepath.Dir(dir))
	}

	l := &ladder.Ladder{Build: ladder.GoBuildVerifier{}, Test: ladder.GoTestVerifier{}}
	res, err := merge.Integrate(context.Background(), merge.Options{
		RepoDir:    dir,
		Baseline:   baseline,
		Task:       "demo: harden the /users API",
		Agents:     agents,
		Resolver:   resolver.UnionResolver{},
		Ladder:     l,
		MaxRetries: 2,
		Log:        func(m string) { fmt.Println(m) },
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
	if n := len(res.Trail.Incompatibilities); n > 0 {
		fmt.Printf("incompat:  %d agent(s) could not be composed\n", n)
	}
	fmt.Printf("trail:     %s\n", filepath.Join(dir, ".braid", "trail.json"))
	if url := os.Getenv("BRAID_WORKER_URL"); url != "" {
		fmt.Printf("dashboard: %s\n", url)
	}
	if !res.Green {
		return fmt.Errorf("run did not reach green (verdict %s)", res.Verdict)
	}
	return nil
}
