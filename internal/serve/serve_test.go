package serve

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KC1706/braid/internal/demo"
)

// git runs git in dir and fails the test on error.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// twoZone spins up the io and compute handlers as httptest servers.
func twoZone(t *testing.T) (io, compute *httptest.Server) {
	t.Helper()
	io = httptest.NewServer(Handler(ZoneIO))
	compute = httptest.NewServer(Handler(ZoneCompute))
	t.Cleanup(io.Close)
	t.Cleanup(compute.Close)
	return io, compute
}

func postJSON(t *testing.T, url string, in any) (int, []byte) {
	t.Helper()
	body, _ := json.Marshal(in)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := new(bytes.Buffer)
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.Bytes()
}

// seedTwoZoneFixture builds, from the demo repo, a base bare remote (main =
// baseline) and one bare "fork" remote per agent (holding that agent's branch).
// It returns the base remote path and the per-agent fork refs.
func seedTwoZoneFixture(t *testing.T) (baseRemote string, agents []agentRefJSON) {
	t.Helper()
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	baseline, demoAgents, err := demo.Seed(seed)
	if err != nil {
		t.Fatalf("demo.Seed: %v", err)
	}

	// Base remote: push the baseline branch as main.
	baseRemote = filepath.Join(root, "base.git")
	gitT(t, "", "init", "--bare", "-b", "main", baseRemote)
	gitT(t, seed, "push", "-q", baseRemote, baseline+":main")

	// One fork remote per agent, holding only that agent's branch.
	for _, a := range demoAgents {
		fork := filepath.Join(root, a.ID+".git")
		gitT(t, "", "init", "--bare", fork)
		gitT(t, seed, "push", "-q", fork, a.Branch+":"+a.Branch)
		agents = append(agents, agentRefJSON{ID: a.ID, Remote: fork, Branch: a.Branch, Intent: a.Intent})
	}
	return baseRemote, agents
}

func TestTwoZoneRoundTripPublishesMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the real go toolchain in the compute zone")
	}
	io, compute := twoZone(t)
	baseRemote, agents := seedTwoZoneFixture(t)

	// Zone IO: snapshot the base + forks into a prepared bundle (local path
	// remotes => zero token).
	code, raw := postJSON(t, io.URL+"/io/snapshot", SnapshotRequest{
		Base:   repoRef{Remote: baseRemote, Branch: "main"},
		Agents: agents,
	})
	if code != http.StatusOK {
		t.Fatalf("snapshot = %d: %s", code, raw)
	}
	var snap SnapshotResponse
	mustJSON(t, raw, &snap)
	if snap.Bundle == "" || len(snap.Branches) != len(agents) {
		t.Fatalf("snapshot resp: bundle? %v branches=%d", snap.Bundle != "", len(snap.Branches))
	}

	// Zone COMPUTE: merge + run the tests. No token, no remote on the wire.
	cAgents := make([]agentRefJSON, len(agents))
	for i, a := range agents {
		cAgents[i] = agentRefJSON{ID: a.ID, Intent: a.Intent}
	}
	code, raw = postJSON(t, compute.URL+"/compute/integrate", IntegrateRequest{
		Bundle: snap.Bundle,
		Agents: cAgents,
		Task:   "two-zone test",
	})
	if code != http.StatusOK {
		t.Fatalf("integrate = %d: %s", code, raw)
	}
	var merged IntegrateResponse
	mustJSON(t, raw, &merged)
	if !merged.Green {
		t.Fatalf("compute not green: verdict=%s summary=%+v", merged.Verdict, merged.Summary)
	}
	if merged.MergedBundle == "" || merged.MergedSHA == "" || merged.BaseSHA != snap.BaselineSHA {
		t.Fatalf("merged resp: bundle? %v mergedSha=%q baseSha=%q (baseline=%q)",
			merged.MergedBundle != "", merged.MergedSHA, merged.BaseSHA, snap.BaselineSHA)
	}

	// Zone IO: publish with the parent-SHA CAS.
	code, raw = postJSON(t, io.URL+"/io/publish", PublishRequest{
		Base:         repoRef{Remote: baseRemote, Branch: "main"},
		MergedBundle: merged.MergedBundle,
		BaseSHA:      merged.BaseSHA,
		MergedSHA:    merged.MergedSHA,
		Ref:          merged.Ref,
	})
	if code != http.StatusOK {
		t.Fatalf("publish = %d: %s", code, raw)
	}
	var pub PublishResponse
	mustJSON(t, raw, &pub)
	if pub.PushedSHA != merged.MergedSHA {
		t.Fatalf("pushed = %s, want %s", pub.PushedSHA, merged.MergedSHA)
	}
	// The base remote's main now points at the merged commit.
	if tip := gitT(t, "", "--git-dir", baseRemote, "rev-parse", "main"); tip != merged.MergedSHA {
		t.Fatalf("remote main = %s, want %s", tip, merged.MergedSHA)
	}
}

func TestPublishCASConflictReturns409(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the real go toolchain in the compute zone")
	}
	io, compute := twoZone(t)
	baseRemote, agents := seedTwoZoneFixture(t)

	_, raw := postJSON(t, io.URL+"/io/snapshot", SnapshotRequest{Base: repoRef{Remote: baseRemote, Branch: "main"}, Agents: agents})
	var snap SnapshotResponse
	mustJSON(t, raw, &snap)
	cAgents := make([]agentRefJSON, len(agents))
	for i, a := range agents {
		cAgents[i] = agentRefJSON{ID: a.ID, Intent: a.Intent}
	}
	_, raw = postJSON(t, compute.URL+"/compute/integrate", IntegrateRequest{Bundle: snap.Bundle, Agents: cAgents})
	var merged IntegrateResponse
	mustJSON(t, raw, &merged)
	if !merged.Green {
		t.Fatalf("precondition: compute not green (%s)", merged.Verdict)
	}

	// A concurrent run advances the base remote's main AFTER the snapshot.
	advance := filepath.Join(t.TempDir(), "advance")
	gitT(t, "", "clone", "-q", baseRemote, advance)
	gitT(t, advance, "config", "user.email", "other@example.com")
	gitT(t, advance, "config", "user.name", "Other")
	if err := os.WriteFile(filepath.Join(advance, "concurrent.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, advance, "add", "-A")
	gitT(t, advance, "commit", "-q", "-m", "concurrent advance")
	gitT(t, advance, "push", "-q", "origin", "main")

	code, _ := postJSON(t, io.URL+"/io/publish", PublishRequest{
		Base:         repoRef{Remote: baseRemote, Branch: "main"},
		MergedBundle: merged.MergedBundle,
		BaseSHA:      merged.BaseSHA,
		MergedSHA:    merged.MergedSHA,
		Ref:          merged.Ref,
	})
	if code != http.StatusConflict {
		t.Fatalf("publish after base moved = %d, want 409", code)
	}
}

func TestComputeZoneDoesNotServeIORoutes(t *testing.T) {
	// The compute zone must not expose the token-bearing io routes.
	compute := httptest.NewServer(Handler(ZoneCompute))
	defer compute.Close()
	code, _ := postJSON(t, compute.URL+"/io/snapshot", SnapshotRequest{})
	if code == http.StatusOK {
		t.Fatalf("compute zone served /io/snapshot (got 200); it must not")
	}
}

func TestHealthz(t *testing.T) {
	for _, zone := range []string{ZoneIO, ZoneCompute} {
		srv := httptest.NewServer(Handler(zone))
		resp, err := http.Get(srv.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("healthz[%s] = %d, want 200", zone, resp.StatusCode)
		}
		resp.Body.Close()
		srv.Close()
	}
}

func mustJSON(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal %T: %v\n%s", v, err, raw)
	}
}
