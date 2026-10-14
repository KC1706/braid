package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KC1706/braid/internal/ledger"
)

// writeChain builds a small signed chain and returns (jsonPath, pubHex).
func writeChain(t *testing.T) (string, string) {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 7)
	}
	signer, err := ledger.Ed25519SignerFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	l := ledger.New(ledger.NewMemStore(), signer)
	for _, kind := range []string{"snapshot", "verify", "publish"} {
		if _, err := l.Append("run-x", kind, map[string]string{"k": kind}); err != nil {
			t.Fatal(err)
		}
	}
	records, _ := l.Records()
	recs, _ := ledger.MarshalRecords(records)
	path := filepath.Join(t.TempDir(), "chain.json")
	if err := os.WriteFile(path, recs, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, hex.EncodeToString(signer.Public())
}

func TestLedgerVerifyCLIAcceptsCleanChain(t *testing.T) {
	path, pub := writeChain(t)
	if err := ledgerVerify([]string{"-pubkey", pub, "-file", path}); err != nil {
		t.Fatalf("clean chain should verify, got %v", err)
	}
}

func TestLedgerVerifyCLIRejectsTamperedChain(t *testing.T) {
	path, pub := writeChain(t)
	// Alter a hashed value (a payload entry) and expect verification to fail: the
	// record's recomputed hash no longer matches, and the signature won't cover it.
	b, _ := os.ReadFile(path)
	tampered := strings.Replace(string(b), `"k":"publish"`, `"k":"PWNED"`, 1)
	if tampered == string(b) {
		t.Fatal("test fixture: expected payload token not found")
	}
	os.WriteFile(path, []byte(tampered), 0o644)
	if err := ledgerVerify([]string{"-pubkey", pub, "-file", path}); err == nil {
		t.Fatal("tampered chain must fail verification")
	}
}

func TestLedgerVerifyCLIRequiresPubkey(t *testing.T) {
	if err := ledgerVerify([]string{}); err == nil {
		t.Fatal("missing -pubkey should error")
	}
}
