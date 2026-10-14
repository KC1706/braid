package ledger

import (
	"path/filepath"
	"testing"
	"time"
)

func testSigner(t *testing.T) Ed25519Signer {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	s, err := Ed25519SignerFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func appendSteps(t *testing.T, l *Ledger) {
	t.Helper()
	steps := []struct {
		kind    string
		payload map[string]string
	}{
		{"snapshot", map[string]string{"baselineSha": "aaa", "forks": "2"}},
		{"merge", map[string]string{"strategy": "union", "files": "3"}},
		{"verify", map[string]string{"rung": "4", "passed": "true", "tests": "12"}},
		{"publish", map[string]string{"mergedSha": "bbb", "baseSha": "aaa"}},
	}
	for _, s := range steps {
		if _, err := l.Append("run-1", s.kind, s.payload); err != nil {
			t.Fatalf("append %s: %v", s.kind, err)
		}
	}
}

func TestAppendAndVerifyCleanChain(t *testing.T) {
	signer := testSigner(t)
	l := New(NewMemStore(), signer).WithClock(func() time.Time { return time.Unix(0, 0).UTC() })
	appendSteps(t, l)

	if err := l.Verify(signer.Public()); err != nil {
		t.Fatalf("clean chain should verify, got %v", err)
	}
	records, _ := l.store.List()
	if len(records) != 4 {
		t.Fatalf("want 4 records, got %d", len(records))
	}
	// Seqs contiguous and linked.
	for i, r := range records {
		if r.Seq != uint64(i) {
			t.Fatalf("record %d has seq %d", i, r.Seq)
		}
		if i == 0 && r.PrevHash != genesisPrev {
			t.Fatalf("genesis prevHash = %q, want %q", r.PrevHash, genesisPrev)
		}
		if i > 0 && r.PrevHash != records[i-1].Hash {
			t.Fatalf("record %d prevHash does not link", i)
		}
	}
}

func TestVerifyDetectsMutatedPayload(t *testing.T) {
	signer := testSigner(t)
	l := New(NewMemStore(), signer).WithClock(func() time.Time { return time.Unix(0, 0).UTC() })
	appendSteps(t, l)
	records, _ := l.store.List()

	// Tamper: flip a payload value on the verify record WITHOUT touching its hash.
	records[2].Payload["passed"] = "false"
	err := VerifyChain(signer.Public(), records)
	assertVerifyErr(t, err, 2, "content hash mismatch")
}

func TestVerifyDetectsForgedHashWithoutKey(t *testing.T) {
	signer := testSigner(t)
	l := New(NewMemStore(), signer).WithClock(func() time.Time { return time.Unix(0, 0).UTC() })
	appendSteps(t, l)
	records, _ := l.store.List()

	// Attacker rewrites the payload AND recomputes the content hash + relinks the
	// next record — but cannot forge the Ed25519 signature without the key.
	records[2].Payload["tests"] = "0"
	records[2].Hash = canonicalHash(records[2].Seq, records[2].PrevHash, records[2].RunID, records[2].Kind, records[2].At, records[2].Payload)
	records[3].PrevHash = records[2].Hash
	records[3].Hash = canonicalHash(records[3].Seq, records[3].PrevHash, records[3].RunID, records[3].Kind, records[3].At, records[3].Payload)
	// records[3] signature now also mismatches; the first failure is at seq 2.
	err := VerifyChain(signer.Public(), records)
	assertVerifyErr(t, err, 2, "invalid signature")
}

func TestVerifyDetectsRemovedRecord(t *testing.T) {
	signer := testSigner(t)
	l := New(NewMemStore(), signer).WithClock(func() time.Time { return time.Unix(0, 0).UTC() })
	appendSteps(t, l)
	records, _ := l.store.List()

	// Drop the merge record (seq 1); the chain now has a seq gap / broken linkage.
	tampered := append([]Record{records[0]}, records[2:]...)
	err := VerifyChain(signer.Public(), tampered)
	if err == nil {
		t.Fatal("removing a record should break verification")
	}
	ve, ok := err.(*VerifyError)
	if !ok || ve.Seq != 1 {
		t.Fatalf("want VerifyError at seq 1, got %v", err)
	}
}

func TestVerifyDetectsReorder(t *testing.T) {
	signer := testSigner(t)
	l := New(NewMemStore(), signer).WithClock(func() time.Time { return time.Unix(0, 0).UTC() })
	appendSteps(t, l)
	records, _ := l.store.List()

	records[1], records[2] = records[2], records[1]
	if err := VerifyChain(signer.Public(), records); err == nil {
		t.Fatal("reordering records should break verification")
	}
}

func TestVerifyRejectsWrongPublicKey(t *testing.T) {
	signer := testSigner(t)
	l := New(NewMemStore(), signer).WithClock(func() time.Time { return time.Unix(0, 0).UTC() })
	appendSteps(t, l)
	records, _ := l.store.List()

	other, err := NewEd25519Signer()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyChain(other.Public(), records); err == nil {
		t.Fatal("verification under a different public key must fail")
	}
}

func TestMemStoreRejectsNonContiguousSeq(t *testing.T) {
	m := NewMemStore()
	if err := m.Append(Record{Seq: 1}); err == nil {
		t.Fatal("append at seq 1 into empty store should fail (append-only, no gaps)")
	}
}

func TestFileStoreRoundTripAndVerify(t *testing.T) {
	signer := testSigner(t)
	path := filepath.Join(t.TempDir(), "ledger.json")
	l := New(NewFileStore(path), signer).WithClock(func() time.Time { return time.Unix(0, 0).UTC() })
	appendSteps(t, l)

	// A fresh Ledger over the same file sees the persisted chain and verifies it.
	l2 := New(NewFileStore(path), signer)
	if err := l2.Verify(signer.Public()); err != nil {
		t.Fatalf("persisted chain should verify, got %v", err)
	}
	records, _ := l2.store.List()
	if len(records) != 4 {
		t.Fatalf("want 4 persisted records, got %d", len(records))
	}
}

func assertVerifyErr(t *testing.T, err error, seq uint64, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want VerifyError at seq %d (%s), got nil", seq, contains)
	}
	ve, ok := err.(*VerifyError)
	if !ok {
		t.Fatalf("want *VerifyError, got %T: %v", err, err)
	}
	if ve.Seq != seq {
		t.Fatalf("VerifyError at seq %d, want %d (%v)", ve.Seq, seq, err)
	}
	if contains != "" && !contains1(ve.Reason, contains) {
		t.Fatalf("reason %q does not contain %q", ve.Reason, contains)
	}
}

func contains1(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
