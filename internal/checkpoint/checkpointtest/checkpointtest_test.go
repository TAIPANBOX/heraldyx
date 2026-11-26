package checkpointtest

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/agent-stack-go/v2/delegation"
	"github.com/TAIPANBOX/agent-stack-go/v2/event"
)

// The fixtures the checkpoint gate's tests stand on are only worth anything if
// they say what SPEC 6.5.1 says. These tests hold each builder to an answer
// computed another way: the kid against agent-stack-go's own RFC 7638
// thumbprint, the signature against crypto/ecdsa, the hashes against
// crypto/sha256 over bytes the test cut by hand.

func newKey(t *testing.T) *Key {
	t.Helper()
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// The kid is the RFC 7638 thumbprint of the public key, and Public is the
// uncompressed point of that same key.
func TestAKeysKidIsItsThumbprintAndPublicIsItsPoint(t *testing.T) {
	k := newKey(t)
	want, err := delegation.Thumbprint(delegation.FromPublic(&k.Priv.PublicKey, ""))
	if err != nil {
		t.Fatal(err)
	}
	if k.Kid != want {
		t.Fatalf("kid %q, want the RFC 7638 thumbprint %q", k.Kid, want)
	}
	raw, err := base64.RawURLEncoding.DecodeString(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		t.Fatalf("Public is not a P-256 point: %v", err)
	}
	mine, err := k.Priv.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	if !pub.Equal(mine) {
		t.Fatal("Public is not this key's point")
	}
	if other := newKey(t); other.Kid == k.Kid {
		t.Fatal("two fresh keys share a kid")
	}
}

// Canon is RFC 8785: members sorted, no whitespace, numbers in their shortest
// form. A value JSON cannot hold is an error, not a fixture.
func TestCanonIsJCS(t *testing.T) {
	got, err := Canon(map[string]any{"z": 1, "a": "x", "m": 1.50})
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"a":"x","m":1.5,"z":1}` {
		t.Fatalf("Canon = %s", got)
	}
	if _, err := Canon(map[string]any{"c": make(chan int)}); err == nil {
		t.Fatal("a channel was canonicalised")
	}
}

// Sign stamps alg and kid, drops any sig already there, and signs the
// canonical record without sig: verifying the 64-byte r||s over SHA-256 of
// that input with crypto/ecdsa must succeed, and fail for any other input.
func TestSignProducesAnES256SignatureOverTheRecordWithoutSig(t *testing.T) {
	k := newKey(t)
	members := map[string]any{"v": 1, "count": 3, "sig": "stale"}
	line, err := Sign(k, members)
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["alg"] != "ES256" || rec["kid"] != k.Kid {
		t.Fatalf("alg %v kid %v", rec["alg"], rec["kid"])
	}
	sig, err := base64.RawURLEncoding.DecodeString(rec["sig"].(string))
	if err != nil || len(sig) != 64 {
		t.Fatalf("sig is %d bytes, %v", len(sig), err)
	}
	if canon, _ := Canon(rec); canon != line {
		t.Fatalf("the signed record is not canonical:\n%s\n%s", line, canon)
	}
	delete(rec, "sig")
	input, err := Canon(rec)
	if err != nil {
		t.Fatal(err)
	}
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	digest := sha256.Sum256([]byte(input))
	if !ecdsa.Verify(&k.Priv.PublicKey, digest[:], r, s) {
		t.Fatal("the signature does not verify over the canonical record without sig")
	}
	other := sha256.Sum256([]byte(strings.Replace(input, `"count":3`, `"count":4`, 1)))
	if ecdsa.Verify(&k.Priv.PublicKey, other[:], r, s) {
		t.Fatal("the signature verifies over a different record")
	}
}

// An unsigned record carries none of alg, kid and sig, and the one reason v1
// defines.
func TestUnsignedCarriesNoSignatureMembersAndItsReason(t *testing.T) {
	line, err := Unsigned(map[string]any{"v": 1, "alg": "ES256", "kid": "k", "sig": "s"})
	if err != nil {
		t.Fatal(err)
	}
	if line != `{"unsigned_reason":"key_expired","v":1}` {
		t.Fatalf("Unsigned = %s", line)
	}
}

// Link is the SHA-256 of the line's bytes, prefixed.
func TestLinkIsTheSHA256OfTheLine(t *testing.T) {
	sum := sha256.Sum256([]byte(`{"v":1}`))
	if got := Link(`{"v":1}`); got != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("Link = %s", got)
	}
	if !strings.HasPrefix(Genesis, "sha256:") || len(Genesis) != len(Link("")) {
		t.Fatalf("Genesis %q is not a link-shaped zero", Genesis)
	}
}

// Members takes the prefix through the LF that ends line count and the chain
// hash of that line, as a writer takes them.
func TestMembersCoverTheFirstCountLines(t *testing.T) {
	l1, l2, l3 := Event("tokenfuse", "budget_threshold", 1), Event("tokenfuse", "budget_threshold", 2), Event("tokenfuse", "budget_threshold", 3)
	stream := []byte(l1 + l2 + l3)
	m, err := Members(stream, "tokenfuse.ndjson", "periodic", 2, Genesis, "2026-10-18T09:00:01Z")
	if err != nil {
		t.Fatal(err)
	}
	psum := sha256.Sum256([]byte(l1 + l2))
	if m["prefix"] != "sha256:"+hex.EncodeToString(psum[:]) {
		t.Fatalf("prefix %v covers the wrong bytes", m["prefix"])
	}
	head, err := event.ChainHashRaw([]byte(strings.TrimSuffix(l2, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if m["head"] != head {
		t.Fatalf("head %v, want the chain hash of line 2 %s", m["head"], head)
	}
	if m["count"] != 2 || m["stream"] != "tokenfuse.ndjson" || m["kind"] != "periodic" || m["prev"] != Genesis || m["v"] != 1 {
		t.Fatalf("members %+v", m)
	}
	// Line 1 is the edge case: no LF before it.
	m1, err := Members(stream, "tokenfuse.ndjson", "periodic", 1, Genesis, "t")
	if err != nil {
		t.Fatal(err)
	}
	head1, _ := event.ChainHashRaw([]byte(strings.TrimSuffix(l1, "\n")))
	if m1["head"] != head1 {
		t.Fatalf("head of line 1 %v, want %s", m1["head"], head1)
	}
}

// A count past the lines the stream holds, or a final line with no LF, is an
// error naming how many lines there are; so is a line that is not an event.
func TestMembersRefusesWhatAWriterCouldNotHaveTaken(t *testing.T) {
	l1 := Event("tokenfuse", "budget_threshold", 1)
	if _, err := Members([]byte(l1+`{"partial"`), "f", "periodic", 2, Genesis, "t"); err == nil || !strings.Contains(err.Error(), "holds 1 line(s), want 2") {
		t.Fatalf("count past the stream: %v", err)
	}
	if _, err := Members([]byte("not json\n"), "f", "periodic", 1, Genesis, "t"); err == nil {
		t.Fatal("a line that is not an event was given a chain hash")
	}
}

// File writes each record on its own line, LF-terminated, readable only by
// its owner, and reports a path it cannot write.
func TestFileWritesOneRecordPerLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), CheckpointName("tokenfuse.ndjson"))
	if !strings.HasSuffix(p, "tokenfuse.ndjson.ckpt.ndjson") {
		t.Fatalf("CheckpointName gave %s", p)
	}
	if err := File(p, "a", "b"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "a\nb\n" {
		t.Fatalf("file holds %q, %v", b, err)
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	if err := File(filepath.Join(p, "under-a-file"), "a"); err == nil {
		t.Fatal("a write under a regular file reported success")
	}
}

// Event is a valid envelope claiming the given source and type, with a run id
// per n, ending in one LF.
func TestEventIsAValidEnvelopeOfTheGivenSource(t *testing.T) {
	line := Event("wardryx", "policy_deny", 7)
	if !strings.HasSuffix(line, "}\n") || strings.Count(line, "\n") != 1 {
		t.Fatalf("not one LF-terminated line: %q", line)
	}
	e, err := event.Unmarshal([]byte(strings.TrimSuffix(line, "\n")))
	if err != nil {
		t.Fatalf("not a valid envelope: %v", err)
	}
	if e.Source != "wardryx" || e.Type != "policy_deny" || e.RunID != "run-7" {
		t.Fatalf("envelope %+v", e)
	}
}
