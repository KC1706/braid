// Package serve exposes Braid's two zones as a small HTTP API so each can run in
// its own Cloudflare Container (GO_PRODUCTION_PLAN.md §3 Phase 3c). The same
// binary serves both; which handlers are mounted is chosen by zone, and the
// container's network posture enforces the security split:
//
//   - zone "io":      the trusted git-I/O zone. Holds the scoped Artifacts tokens,
//     clones the base + each fork, and CAS-pushes the merged bundle. Egress is
//     allowed (scoped to the Artifacts host); it never runs untrusted code.
//   - zone "compute": the untrusted zone. Receives a prepared bundle, runs the
//     merge engine + the agents' tests, and emits a merged bundle. It is started
//     with internet disabled and is never handed a token.
//
// The two zones hand off only through a content-addressed git bundle (base64 on
// the wire), so nothing but merged git objects crosses the boundary.
package serve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/KC1706/braid/internal/gitio"
	"github.com/KC1706/braid/internal/run"
	"github.com/KC1706/braid/pkg/models"
)

// Zone names.
const (
	ZoneIO      = "io"
	ZoneCompute = "compute"
)

// --- wire types ---

// tokenJSON is a scoped Artifacts credential on the wire. It appears only in
// requests to the io zone; the compute zone's wire types carry no token field.
type tokenJSON struct {
	Plaintext string `json:"plaintext"`
	ExpiresAt string `json:"expiresAt,omitempty"` // RFC3339; empty = never
}

func (t tokenJSON) toToken() (gitio.Token, error) {
	tok := gitio.Token{Plaintext: t.Plaintext}
	if t.ExpiresAt != "" {
		at, err := time.Parse(time.RFC3339, t.ExpiresAt)
		if err != nil {
			return gitio.Token{}, fmt.Errorf("bad expiresAt %q: %w", t.ExpiresAt, err)
		}
		tok.ExpiresAt = at
	}
	return tok, nil
}

type repoRef struct {
	Remote string    `json:"remote"`
	Branch string    `json:"branch"`
	Token  tokenJSON `json:"token"`
}

type agentRefJSON struct {
	ID     string    `json:"id"`
	Remote string    `json:"remote"`
	Branch string    `json:"branch"`
	Intent string    `json:"intent,omitempty"`
	Token  tokenJSON `json:"token"`
}

// SnapshotRequest asks the io zone to assemble a prepared bundle for a run.
type SnapshotRequest struct {
	Base   repoRef        `json:"base"`
	Agents []agentRefJSON `json:"agents"`
}

// SnapshotResponse carries the prepared bundle (base64) back to the orchestrator,
// which hands it to the compute zone. It holds no token.
type SnapshotResponse struct {
	Bundle      string            `json:"bundle"` // base64 git bundle: heads baseline + agent/<id>
	BaselineSHA string            `json:"baselineSha"`
	Branches    map[string]string `json:"branches"`
}

// IntegrateRequest is the compute zone's job: merge the agents in the prepared
// bundle and run their tests. No token, no remote.
type IntegrateRequest struct {
	Bundle            string         `json:"bundle"` // base64 prepared bundle from /io/snapshot
	Agents            []agentRefJSON `json:"agents"` // only id/branch/intent are read
	Task              string         `json:"task,omitempty"`
	Resolver          string         `json:"resolver,omitempty"`
	IntegrationBranch string         `json:"integrationBranch,omitempty"`
	Tree              bool           `json:"tree,omitempty"`
	Vet               bool           `json:"vet,omitempty"`
	Sandbox           bool           `json:"sandbox,omitempty"`
	MaxRetries        int            `json:"maxRetries,omitempty"`
}

// IntegrateResponse is the verdict plus — only when green — the merged bundle to
// publish. MergedBundle is empty on a red verdict (nothing to publish).
type IntegrateResponse struct {
	Verdict      models.Verdict      `json:"verdict"`
	Green        bool                `json:"green"`
	BaseSHA      string              `json:"baseSha"`
	MergedSHA    string              `json:"mergedSha"`
	Ref          string              `json:"ref"`
	MergedBundle string              `json:"mergedBundle,omitempty"` // base64; empty if not green
	Summary      models.TrailSummary `json:"summary"`
	Trail        models.Trail        `json:"trail"`
}

// PublishRequest asks the io zone to CAS-push a merged bundle to the base remote.
type PublishRequest struct {
	Base         repoRef `json:"base"`
	MergedBundle string  `json:"mergedBundle"` // base64, from /compute/integrate
	BaseSHA      string  `json:"baseSha"`
	MergedSHA    string  `json:"mergedSha"`
	Ref          string  `json:"ref"`
}

// PublishResponse reports the published commit.
type PublishResponse struct {
	PushedSHA string `json:"pushedSha"`
}

// --- server ---

const defaultIntegrationBranch = "braid/integration"

// Handler returns the HTTP handler for one zone. An unknown zone yields a handler
// that 500s on every request, so a misconfigured container fails loud, not open.
func Handler(zone string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true", "zone": zone})
	})
	switch zone {
	case ZoneIO:
		mux.HandleFunc("/io/snapshot", handleSnapshot)
		mux.HandleFunc("/io/publish", handlePublish)
	case ZoneCompute:
		mux.HandleFunc("/compute/integrate", handleIntegrate)
	default:
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusInternalServerError, errBody(fmt.Sprintf("unknown zone %q", zone)))
		})
	}
	return mux
}

func handleSnapshot(w http.ResponseWriter, r *http.Request) {
	var req SnapshotRequest
	if !decode(w, r, &req) {
		return
	}
	baseTok, err := req.Base.Token.toToken()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	agents := make([]gitio.AgentRef, 0, len(req.Agents))
	for _, a := range req.Agents {
		tok, err := a.Token.toToken()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
			return
		}
		agents = append(agents, gitio.AgentRef{ID: a.ID, Remote: a.Remote, Branch: a.Branch, Token: tok})
	}

	tmp, cleanup, err := tempDir("braid-snapshot-")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(err.Error()))
		return
	}
	defer cleanup()
	work := filepath.Join(tmp, "work")
	bundlePath := filepath.Join(tmp, "prepared.bundle")

	snap, err := gitio.SnapshotForks(r.Context(), req.Base.Remote, baseTok, branchOr(req.Base.Branch), work, bundlePath, agents)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}
	b64, err := readBase64(bundlePath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, SnapshotResponse{Bundle: b64, BaselineSHA: snap.BaselineSHA, Branches: snap.Branches})
}

func handleIntegrate(w http.ResponseWriter, r *http.Request) {
	var req IntegrateRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Bundle == "" {
		writeJSON(w, http.StatusBadRequest, errBody("bundle is required"))
		return
	}

	tmp, cleanup, err := tempDir("braid-compute-")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(err.Error()))
		return
	}
	defer cleanup()

	bundlePath := filepath.Join(tmp, "prepared.bundle")
	if err := writeBase64(bundlePath, req.Bundle); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("decode bundle: "+err.Error()))
		return
	}
	repoDir := filepath.Join(tmp, "repo")
	if err := gitio.OpenPrepared(r.Context(), bundlePath, repoDir); err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}

	integration := branchOrName(req.IntegrationBranch, defaultIntegrationBranch)
	specAgents := make([]run.AgentSpec, 0, len(req.Agents))
	for _, a := range req.Agents {
		specAgents = append(specAgents, run.AgentSpec{ID: a.ID, Branch: "agent/" + a.ID, Intent: a.Intent})
	}

	svc := run.Service{}
	res, err := svc.Run(r.Context(), run.RunSpec{
		RepoDir:           repoDir,
		Baseline:          "baseline",
		IntegrationBranch: integration,
		Task:              req.Task,
		Agents:            specAgents,
		MaxRetries:        req.MaxRetries,
		Resolver:          req.Resolver,
		Sandbox:           req.Sandbox,
		Vet:               req.Vet,
		Tree:              req.Tree,
	})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(err.Error()))
		return
	}

	out := IntegrateResponse{
		Verdict:   res.Verdict,
		Green:     res.Green,
		MergedSHA: res.MergedSHA,
		Ref:       integration,
		Summary:   res.Trail.Summary,
		Trail:     res.Trail,
	}
	if res.Green {
		mergedPath := filepath.Join(tmp, "merged.bundle")
		b, err := gitio.WriteBundle(r.Context(), repoDir, "baseline", integration, mergedPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errBody("write merged bundle: "+err.Error()))
			return
		}
		b64, err := readBase64(mergedPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errBody(err.Error()))
			return
		}
		out.BaseSHA = b.BaseSHA
		out.MergedSHA = b.MergedSHA
		out.Ref = b.Ref
		out.MergedBundle = b64
	}
	writeJSON(w, http.StatusOK, out)
}

func handlePublish(w http.ResponseWriter, r *http.Request) {
	var req PublishRequest
	if !decode(w, r, &req) {
		return
	}
	if req.MergedBundle == "" || req.MergedSHA == "" || req.BaseSHA == "" {
		writeJSON(w, http.StatusBadRequest, errBody("mergedBundle, mergedSha and baseSha are required"))
		return
	}
	baseTok, err := req.Base.Token.toToken()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}

	tmp, cleanup, err := tempDir("braid-publish-")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(err.Error()))
		return
	}
	defer cleanup()

	bundlePath := filepath.Join(tmp, "merged.bundle")
	if err := writeBase64(bundlePath, req.MergedBundle); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("decode bundle: "+err.Error()))
		return
	}
	ref := branchOrName(req.Ref, defaultIntegrationBranch)
	branch := branchOr(req.Base.Branch)

	zone := gitio.Zone{Remote: req.Base.Remote, Token: baseTok}
	work := filepath.Join(tmp, "work")
	if _, err := zone.CloneBase(r.Context(), work, branch); err != nil {
		writeJSON(w, http.StatusBadGateway, errBody("clone base: "+err.Error()))
		return
	}
	pushed, err := zone.PushBundle(r.Context(), work, branch, gitio.Bundle{
		Path:      bundlePath,
		MergedSHA: req.MergedSHA,
		BaseSHA:   req.BaseSHA,
		Ref:       ref,
	})
	if err != nil {
		if err == gitio.ErrBaseMoved {
			writeJSON(w, http.StatusConflict, errBody(err.Error()))
			return
		}
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, PublishResponse{PushedSHA: pushed})
}

// --- helpers ---

func branchOr(b string) string {
	if b == "" {
		return "main"
	}
	return b
}

func branchOrName(b, def string) string {
	if b == "" {
		return def
	}
	return b
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errBody("POST required"))
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid JSON body: "+err.Error()))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func errBody(msg string) map[string]string { return map[string]string{"error": msg} }

func tempDir(prefix string) (dir string, cleanup func(), err error) {
	d, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", func() {}, err
	}
	return d, func() { _ = os.RemoveAll(d) }, nil
}

func readBase64(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func writeBase64(path, b64 string) error {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// Run starts the zone's HTTP server on addr and blocks. It is the container entry
// point (`braid serve`).
func Run(ctx context.Context, zone, addr string) error {
	srv := &http.Server{Addr: addr, Handler: Handler(zone)}
	return srv.ListenAndServe()
}
