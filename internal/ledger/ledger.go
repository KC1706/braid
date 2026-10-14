// Package ledger is Braid's external, append-only, tamper-evident record of what
// a run did (GO_PRODUCTION_PLAN.md §3 Phase 4b, C17/C18). Every meaningful step —
// the inputs it hashed, the resolution it chose, the model digest it used, the
// test results it got, the commit it published — is written as a signed record
// linked into a SHA-256 hash chain. The in-repo `.braid/trail.json` stays as the
// human-readable view; this is the authority a reviewer (or an auditor) trusts.
//
// Tamper evidence is structural, two layers deep:
//
//   - Each record carries Hash = SHA-256 over its canonical contents, and
//     PrevHash = the previous record's Hash. Change any field of any record and
//     its Hash no longer matches; change its Hash and the next record's PrevHash
//     no longer links. Remove or reorder a record and the Seq/linkage breaks.
//   - Each record is signed (Ed25519) over its Hash, so an attacker who rewrites a
//     record AND recomputes the chain still cannot produce a valid signature
//     without the private key. Verification needs only the public key.
//
// The store is append-only and, in production, object-locked R2 (WORM); this
// package is storage-agnostic (a Store interface) so the chain logic is pure and
// fully testable in memory.
package ledger

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KC1706/braid/internal/determinism"
)

// genesisPrev is the PrevHash of the first record in a chain — a fixed, well-known
// sentinel so the genesis record is itself hashed unambiguously.
const genesisPrev = "GENESIS"

// Record is one signed, chained ledger entry. The fields above Hash/Sig are the
// signed contents; Hash and Sig are derived and verified, never trusted as input.
type Record struct {
	Seq       uint64            `json:"seq"`       // 0-based position in the chain
	PrevHash  string            `json:"prevHash"`  // Hash of record Seq-1 (genesisPrev at Seq 0)
	RunID     string            `json:"runId"`     // the run this step belongs to
	Kind      string            `json:"kind"`      // step kind: snapshot|merge|resolve|verify|publish|…
	At        string            `json:"at"`        // RFC3339 timestamp
	Payload   map[string]string `json:"payload"`   // structured facts (input hashes, model digest, results)
	Hash      string            `json:"hash"`      // SHA-256 over the canonical contents above
	Signature string            `json:"signature"` // base64 Ed25519 signature over Hash
}

// canonicalHash computes a record's Hash from its signed contents. Payload keys
// are folded in a canonical (sorted) order via determinism.ContentHash, so the
// hash is independent of Go map iteration order — recomputable byte-for-byte by a
// verifier.
func canonicalHash(seq uint64, prevHash, runID, kind, at string, payload map[string]string) string {
	parts := []string{
		strconv.FormatUint(seq, 10),
		prevHash,
		runID,
		kind,
		at,
	}
	// Append payload as canonically-ordered key/value parts.
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	keys = determinism.CanonicalOrder(keys, func(k string) string { return k })
	for _, k := range keys {
		parts = append(parts, k, payload[k])
	}
	return determinism.ContentHash(parts...)
}

// Signer signs a record hash. Ed25519Signer is the default; the interface lets a
// KMS-backed signer drop in without touching the chain logic.
type Signer interface {
	Sign(hash string) (string, error) // returns base64 signature
	Public() ed25519.PublicKey
}

// Ed25519Signer signs with an in-process Ed25519 private key.
type Ed25519Signer struct {
	priv ed25519.PrivateKey
}

// NewEd25519Signer generates a fresh key pair.
func NewEd25519Signer() (Ed25519Signer, error) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return Ed25519Signer{}, err
	}
	return Ed25519Signer{priv: priv}, nil
}

// Ed25519SignerFromSeed builds a deterministic signer from a 32-byte seed (for
// tests and reproducible key provisioning).
func Ed25519SignerFromSeed(seed []byte) (Ed25519Signer, error) {
	if len(seed) != ed25519.SeedSize {
		return Ed25519Signer{}, fmt.Errorf("ledger: seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	return Ed25519Signer{priv: ed25519.NewKeyFromSeed(seed)}, nil
}

func (s Ed25519Signer) Sign(hash string) (string, error) {
	if s.priv == nil {
		return "", errors.New("ledger: signer has no key")
	}
	sig := ed25519.Sign(s.priv, []byte(hash))
	return base64.StdEncoding.EncodeToString(sig), nil
}

func (s Ed25519Signer) Public() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }

// Verify checks a base64 Ed25519 signature over hash against pub.
func Verify(pub ed25519.PublicKey, hash, signature string) bool {
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, []byte(hash), sig)
}

// Store is the append-only backend. Append must be atomic and must reject a seq
// that is not exactly len(chain) (no gaps, no overwrite); in production it is an
// object-locked R2 bucket (WORM). List returns records in seq order.
type Store interface {
	Append(r Record) error
	List() ([]Record, error)
}

// Ledger appends signed, chained records to a Store.
type Ledger struct {
	store  Store
	signer Signer
	now    func() time.Time
}

// New builds a Ledger over a store + signer. now defaults to time.Now.
func New(store Store, signer Signer) *Ledger {
	return &Ledger{store: store, signer: signer, now: time.Now}
}

// WithClock overrides the clock (tests).
func (l *Ledger) WithClock(now func() time.Time) *Ledger { l.now = now; return l }

// Append writes a new signed record for (runID, kind, payload), linked to the
// current chain tip. It is not safe for concurrent use on one Ledger value;
// serialize appends through a single owner (the Base-Coordinator in prod).
func (l *Ledger) Append(runID, kind string, payload map[string]string) (Record, error) {
	existing, err := l.store.List()
	if err != nil {
		return Record{}, err
	}
	seq := uint64(len(existing))
	prev := genesisPrev
	if seq > 0 {
		prev = existing[seq-1].Hash
	}
	at := l.now().UTC().Format(time.RFC3339Nano)
	// Copy the payload so a later caller mutation can't change what we hashed.
	p := make(map[string]string, len(payload))
	for k, v := range payload {
		p[k] = v
	}
	hash := canonicalHash(seq, prev, runID, kind, at, p)
	sig, err := l.signer.Sign(hash)
	if err != nil {
		return Record{}, err
	}
	r := Record{Seq: seq, PrevHash: prev, RunID: runID, Kind: kind, At: at, Payload: p, Hash: hash, Signature: sig}
	if err := l.store.Append(r); err != nil {
		return Record{}, err
	}
	return r, nil
}

// VerifyError describes the first integrity violation found while verifying a
// chain, naming the offending record and what broke.
type VerifyError struct {
	Seq    uint64
	Reason string
}

func (e *VerifyError) Error() string {
	return fmt.Sprintf("ledger: record %d: %s", e.Seq, e.Reason)
}

// VerifyChain walks records (in seq order) and confirms, for each: the seq is
// contiguous, the recomputed Hash matches, PrevHash links to the previous record,
// and the signature is valid under pub. Any deviation — a mutated payload, a
// swapped hash, a removed/inserted/reordered record, a forged signature — returns
// a *VerifyError at the first offending record. An empty chain verifies.
func VerifyChain(pub ed25519.PublicKey, records []Record) error {
	prev := genesisPrev
	for i, r := range records {
		seq := uint64(i)
		if r.Seq != seq {
			return &VerifyError{Seq: seq, Reason: fmt.Sprintf("seq is %d, expected %d (gap or reorder)", r.Seq, seq)}
		}
		if r.PrevHash != prev {
			return &VerifyError{Seq: seq, Reason: "prevHash does not link to the previous record"}
		}
		want := canonicalHash(r.Seq, r.PrevHash, r.RunID, r.Kind, r.At, r.Payload)
		if r.Hash != want {
			return &VerifyError{Seq: seq, Reason: "content hash mismatch (record was altered)"}
		}
		if !Verify(pub, r.Hash, r.Signature) {
			return &VerifyError{Seq: seq, Reason: "invalid signature"}
		}
		prev = r.Hash
	}
	return nil
}

// Verify loads the Ledger's chain from its store and verifies it under pub.
func (l *Ledger) Verify(pub ed25519.PublicKey) error {
	records, err := l.store.List()
	if err != nil {
		return err
	}
	return VerifyChain(pub, records)
}

// MarshalRecords / UnmarshalRecords are the JSON wire form a store persists.
func MarshalRecords(records []Record) ([]byte, error) { return json.Marshal(records) }
func UnmarshalRecords(b []byte) ([]Record, error) {
	var rs []Record
	if err := json.Unmarshal(b, &rs); err != nil {
		return nil, err
	}
	return rs, nil
}
