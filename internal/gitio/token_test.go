package gitio

import (
	"strings"
	"testing"
	"time"
)

func TestTokenSecretStripsExpiry(t *testing.T) {
	tok := Token{Plaintext: "art_v2_x_deadbeef?expires=1800000000"}
	if got := tok.secret(); got != "art_v2_x_deadbeef" {
		t.Fatalf("secret = %q", got)
	}
	// No query suffix: secret is the whole token.
	tok2 := Token{Plaintext: "art_v2_x_nope"}
	if got := tok2.secret(); got != "art_v2_x_nope" {
		t.Fatalf("secret = %q", got)
	}
}

func TestTokenExpired(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	if (Token{}).Expired(now) {
		t.Fatal("zero token (no expiry) should never be expired")
	}
	future := Token{Plaintext: "x", ExpiresAt: now.Add(time.Minute)}
	if future.Expired(now) {
		t.Fatal("token should not be expired before ExpiresAt")
	}
	past := Token{Plaintext: "x", ExpiresAt: now.Add(-time.Second)}
	if !past.Expired(now) {
		t.Fatal("token should be expired after ExpiresAt")
	}
	// Exactly at expiry counts as expired.
	if !(Token{Plaintext: "x", ExpiresAt: now}).Expired(now) {
		t.Fatal("token at ExpiresAt should be expired")
	}
}

func TestTokenBasicAuthURL(t *testing.T) {
	tok := Token{Plaintext: "art_v2_x_abc?expires=42"}
	got := tok.BasicAuthURL("https://acct.artifacts.cloudflare.net/git/braid/repo.git")
	want := "https://x:art_v2_x_abc@acct.artifacts.cloudflare.net/git/braid/repo.git"
	if got != want {
		t.Fatalf("BasicAuthURL = %q", got)
	}
	// Non-https (local path in tests) and zero token are passed through unchanged.
	if tok.BasicAuthURL("/tmp/bare") != "/tmp/bare" {
		t.Fatal("non-https remote should be unchanged")
	}
	if (Token{}).BasicAuthURL("https://x/y") != "https://x/y" {
		t.Fatal("zero token should leave the URL unchanged")
	}
}

func TestTokenConfigArgsAndRedaction(t *testing.T) {
	if (Token{}).configArgs() != nil {
		t.Fatal("zero token should contribute no config args")
	}
	tok := Token{Plaintext: "art_v2_x_secret?expires=1"}
	args := tok.configArgs()
	if len(args) != 2 || args[0] != "-c" || !strings.Contains(args[1], "Bearer art_v2_x_secret?expires=1") {
		t.Fatalf("configArgs = %v", args)
	}
	// String never leaks the secret.
	s := tok.String()
	if strings.Contains(s, "secret") {
		t.Fatalf("String leaked the secret: %q", s)
	}
	// redactArgs scrubs the header value.
	red := redactArgs(args)
	if strings.Contains(red, "secret") {
		t.Fatalf("redactArgs leaked the secret: %q", red)
	}
}
