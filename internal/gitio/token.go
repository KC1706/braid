// Package gitio is Braid's trusted git-I/O zone (GO_PRODUCTION_PLAN.md §1 two-zone
// invariant, §3 Phase 3a). It is the ONLY place a Cloudflare Artifacts token
// lives, and it NEVER executes untrusted code — it runs git and nothing else
// (clone, fetch-from-bundle, rev-parse, push). The two zones hand off through a
// content-addressed git bundle: the compute zone produces the merged commit and
// writes a bundle; this zone imports that bundle into a fresh clone of the base
// and pushes it, enforcing a compare-and-swap on the base tip so a concurrent
// run cannot be clobbered.
//
// The security split is structural, not conventional: there is no Executor for
// user code in this package, and the git subprocess runs with a scrubbed env
// carrying only what git itself needs — the token is passed per-invocation via
// `-c http.extraHeader=...`, never through the environment the way an API key
// might leak into a child process.
package gitio

import (
	"fmt"
	"strings"
	"time"
)

// Token is a short-lived, single-operation-scoped Cloudflare Artifacts
// credential. Plaintext is the full `art_v2_x_<hex>?expires=<unix>` string as
// minted by `handle.createToken`. The zero value means "no auth" — used for a
// local/path remote in tests. The secret is redacted in every string form so it
// can never land in a log line or an error message.
type Token struct {
	Plaintext string
	ExpiresAt time.Time
}

// IsZero reports whether the token is the empty (no-auth) value.
func (t Token) IsZero() bool { return t.Plaintext == "" }

// secret returns the token minus its `?expires=...` query suffix — the value that
// goes in the basic-auth userinfo `x:<secret>`.
func (t Token) secret() string {
	if i := strings.IndexByte(t.Plaintext, '?'); i >= 0 {
		return t.Plaintext[:i]
	}
	return t.Plaintext
}

// Expired reports whether the token is at or past its expiry as of now. A zero
// ExpiresAt is treated as "never expires" (the local/test case).
func (t Token) Expired(now time.Time) bool {
	if t.ExpiresAt.IsZero() {
		return false
	}
	return !now.Before(t.ExpiresAt)
}

// configArgs returns the leading `-c http.extraHeader=Authorization: Bearer ...`
// arguments that authenticate a git invocation, or nil for a zero token. Passing
// the credential as a per-command config value (rather than baking it into the
// remote URL) keeps it out of any URL git might persist in .git/config.
func (t Token) configArgs() []string {
	if t.IsZero() {
		return nil
	}
	return []string{"-c", "http.extraHeader=Authorization: Bearer " + t.Plaintext}
}

// BasicAuthURL rewrites an https remote to embed basic-auth userinfo
// `https://x:<secret>@host/...`. Provided for callers/remotes that prefer
// in-URL auth over the extraHeader; configArgs is the default. It returns remote
// unchanged for a zero token or a non-https remote (e.g. a local path in tests).
func (t Token) BasicAuthURL(remote string) string {
	if t.IsZero() || !strings.HasPrefix(remote, "https://") {
		return remote
	}
	return "https://x:" + t.secret() + "@" + strings.TrimPrefix(remote, "https://")
}

// String redacts the secret so the token is safe to print.
func (t Token) String() string {
	if t.IsZero() {
		return "gitio.Token(none)"
	}
	exp := "no-expiry"
	if !t.ExpiresAt.IsZero() {
		exp = t.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("gitio.Token(redacted, expires=%s)", exp)
}
