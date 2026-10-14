package ledger

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// TestPrintInteropVector emits the cross-language test vector pinned in the
// Worker's ledger.test.ts. Run with -v to see it; it always passes.
func TestPrintInteropVector(t *testing.T) {
	payload := map[string]string{"passed": "true", "tests": "12", "rung": "4"}
	h := CanonicalHash(0, "GENESIS", "run-1", "verify", "2026-01-02T03:04:05Z", payload)
	t.Logf("HASH=%s", h)
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	t.Logf("PUB_HEX=%s", hex.EncodeToString(pub))
	t.Logf("SIG_B64=%s", base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(h))))
}
