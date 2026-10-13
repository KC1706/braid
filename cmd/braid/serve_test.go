package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KC1706/braid/internal/demo"
	"github.com/KC1706/braid/internal/run"
)

func TestServeHealthz(t *testing.T) {
	srv := httptest.NewServer(serveMux(run.Service{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", resp.StatusCode)
	}
}

func TestServeRunBadBody(t *testing.T) {
	srv := httptest.NewServer(serveMux(run.Service{}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/run", "application/json", strings.NewReader("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad body = %d, want 400", resp.StatusCode)
	}
}

func TestServeRunMissingFields(t *testing.T) {
	srv := httptest.NewServer(serveMux(run.Service{}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/run", "application/json", strings.NewReader(`{"baseline":"main"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("missing repoDir = %d, want 422", resp.StatusCode)
	}
}

func TestServeRunEndToEnd(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "repo")
	baseline, agents, err := demo.Seed(repoDir)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	specs := make([]run.AgentSpec, len(agents))
	for i, a := range agents {
		specs[i] = run.AgentSpec{ID: a.ID, Branch: a.Branch, Intent: a.Intent}
	}

	srv := httptest.NewServer(serveMux(run.Service{}))
	defer srv.Close()

	body, _ := json.Marshal(run.RunSpec{
		RepoDir: repoDir, Baseline: baseline, Agents: specs, MaxRetries: 2,
	})
	resp, err := http.Post(srv.URL+"/run", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run = %d, want 200", resp.StatusCode)
	}
	var got run.RunResult
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Green || got.Summary.AgentsMerged != 3 {
		t.Fatalf("want green with 3 merged, got green=%v merged=%d verdict=%q",
			got.Green, got.Summary.AgentsMerged, got.Verdict)
	}
}
