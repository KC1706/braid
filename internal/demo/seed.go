// Package demo seeds a self-contained throwaway git repo that reproduces Braid's
// worked example as a small Go net/http service: a baseline /users API, then
// three agent branches — two that both edit getUser (a guaranteed true conflict)
// and one docs-only branch (clean auto-merge). It is the Go analogue of the TS
// demo/seed.ts (GO_MIGRATION_PLAN.md §1.1 — Go target fixture).
package demo

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/KC1706/braid/internal/merge"
	"github.com/KC1706/braid/pkg/gitx"
)

const goMod = "module example.com/demo\n\ngo 1.26\n"

const dbGo = `package demo

// User is a demo record served by the API.
type User struct {
	ID    string
	Name  string
	Email string
}

var users = map[string]User{
	"1": {ID: "1", Name: "Ada Lovelace", Email: "ada@braid.dev"},
	"2": {ID: "2", Name: "Alan Turing", Email: "alan@braid.dev"},
}

func find(id string) (User, bool) {
	u, ok := users[id]
	return u, ok
}
`

const usersBase = `package demo

func getUser(id string) (User, bool) {
	return find(id)
}
`

const usersValidation = `package demo

func getUser(id string) (User, bool) {
	validate(id)
	return find(id)
}
`

const usersRatelimit = `package demo

func getUser(id string) (User, bool) {
	rate(id)
	return find(id)
}
`

const validateGo = `package demo

// validate is the input-validation guard added by agent-1.
func validate(id string) {
	_ = id
}
`

const ratelimitGo = `package demo

// rate is the rate-limiting guard added by agent-2.
func rate(id string) {
	_ = id
}
`

const serverGo = `package demo

import (
	"encoding/json"
	"net/http"
)

// Handler serves GET /users/:id using getUser.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/users/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/users/"):]
		u, ok := getUser(id)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(u)
	})
	return mux
}
`

const usersTest = `package demo

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKnownUser(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/users/1")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
}

func TestUnknownUser(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/users/999")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}
`

const readmeBase = "# Braid demo target\n\nA tiny Go net/http API the agent swarm improves.\n\n## Routes\n- `GET /users/:id`\n"
const readmeDocs = readmeBase + "\n## Hardening\nThis service validates input and rate-limits requests. (added by the docs agent)\n"

// Seed builds the demo repo at repoDir (created fresh) and returns the baseline
// ref plus the three agent inputs for Integrate.
func Seed(repoDir string) (baseline string, agents []merge.AgentInput, err error) {
	if err := os.RemoveAll(repoDir); err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return "", nil, err
	}
	run := func(args ...string) error {
		_, e := gitx.Ok(repoDir, args...)
		return e
	}
	for _, step := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "braid@example.com"},
		{"config", "user.name", "Braid"},
		{"checkout", "-q", "-b", "baseline"},
	} {
		if err := run(step...); err != nil {
			return "", nil, err
		}
	}

	if err := writeAll(repoDir, map[string]string{
		"go.mod":        goMod,
		"db.go":         dbGo,
		"users.go":      usersBase,
		"server.go":     serverGo,
		"users_test.go": usersTest,
		"README.md":     readmeBase,
	}); err != nil {
		return "", nil, err
	}
	if err := run("add", "-A"); err != nil {
		return "", nil, err
	}
	if err := run("commit", "-q", "-m", "baseline: Go /users API"); err != nil {
		return "", nil, err
	}

	branches := []struct {
		branch, msg string
		files       map[string]string
	}{
		{"agent/validation", "agent-1: input validation on getUser", map[string]string{"users.go": usersValidation, "validate.go": validateGo}},
		{"agent/ratelimit", "agent-2: rate limiting on getUser", map[string]string{"users.go": usersRatelimit, "ratelimit.go": ratelimitGo}},
		{"agent/docs", "agent-3: document hardening", map[string]string{"README.md": readmeDocs}},
	}
	for _, b := range branches {
		if err := run("checkout", "-q", "-b", b.branch, "baseline"); err != nil {
			return "", nil, err
		}
		if err := writeAll(repoDir, b.files); err != nil {
			return "", nil, err
		}
		if err := run("add", "-A"); err != nil {
			return "", nil, err
		}
		if err := run("commit", "-q", "-m", b.msg); err != nil {
			return "", nil, err
		}
	}
	if err := run("checkout", "-q", "baseline"); err != nil {
		return "", nil, err
	}

	return "baseline", []merge.AgentInput{
		{ID: "agent-1", Branch: "agent/validation", Intent: "input validation"},
		{ID: "agent-2", Branch: "agent/ratelimit", Intent: "rate limiting"},
		{ID: "agent-3", Branch: "agent/docs", Intent: "documentation"},
	}, nil
}

func writeAll(dir string, files map[string]string) error {
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	return nil
}
