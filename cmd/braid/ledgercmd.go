package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/KC1706/braid/internal/ledger"
)

// runLedger is the `braid ledger` command group — the external verifier/CLI for
// the signed provenance chain (GO_PRODUCTION_PLAN.md §3 Phase 4b). The Worker
// writes the chain to R2 while a run executes; this verifies it offline with only
// the public key, which is the whole point of signing it: an auditor trusts the
// chain without trusting Braid.
func runLedger(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("usage: braid ledger <keygen|verify> [flags]")
	}
	switch argv[0] {
	case "keygen":
		return ledgerKeygen(argv[1:])
	case "verify":
		return ledgerVerify(argv[1:])
	default:
		return fmt.Errorf("unknown ledger subcommand %q (want keygen|verify)", argv[0])
	}
}

// ledgerKeygen prints a fresh Ed25519 seed (set it as the Worker's signing
// secret) and the matching public key (hand it to verifiers).
func ledgerKeygen(argv []string) error {
	fs := flag.NewFlagSet("ledger keygen", flag.ContinueOnError)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return err
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	fmt.Printf("seed (secret — set as LEDGER_SIGNING_SEED): %s\n", hex.EncodeToString(seed))
	fmt.Printf("public key (give to verifiers):              %s\n", hex.EncodeToString(pub))
	return nil
}

// ledgerVerify reads a JSON array of ledger records (from -file or stdin) and
// verifies the hash chain + every signature under -pubkey. It exits non-zero on
// the first integrity violation, naming the offending record.
func ledgerVerify(argv []string) error {
	fs := flag.NewFlagSet("ledger verify", flag.ContinueOnError)
	pubHex := fs.String("pubkey", "", "hex Ed25519 public key [required]")
	file := fs.String("file", "", "records JSON file (default: stdin)")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *pubHex == "" {
		return fmt.Errorf("-pubkey is required")
	}
	pubBytes, err := hex.DecodeString(*pubHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("-pubkey must be %d hex-encoded bytes", ed25519.PublicKeySize)
	}

	var raw []byte
	if *file != "" {
		raw, err = os.ReadFile(*file)
	} else {
		raw, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return err
	}
	records, err := ledger.UnmarshalRecords(raw)
	if err != nil {
		return fmt.Errorf("parse records: %w", err)
	}

	if err := ledger.VerifyChain(ed25519.PublicKey(pubBytes), records); err != nil {
		fmt.Printf("ledger: ✗ TAMPER DETECTED — %v\n", err)
		return err
	}
	fmt.Printf("ledger: ✓ verified — %d record(s), chain intact and every signature valid\n", len(records))
	// A quick human summary of what the chain records.
	for _, r := range records {
		b, _ := json.Marshal(r.Payload)
		fmt.Printf("  [%d] %-10s %s\n", r.Seq, r.Kind, string(b))
	}
	return nil
}
