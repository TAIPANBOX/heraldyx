package statementtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/v2/delegation"
)

// The custody-mode tests trust what this package mints, so these tests hold
// it to an answer computed another way: the root's kids against RFC 7638 and
// a SHA-256 of the published ML-DSA key, and every signature verified with
// crypto/ecdsa and crypto/mldsa against the key set the root publishes, never
// against the private keys that made it.

type jwk struct {
	Kty, Crv, X, Y, Alg, Pub, Kid, Use string
}

type statement struct {
	Payload    string `json:"payload"`
	Signatures []struct {
		Protected string `json:"protected"`
		Signature string `json:"signature"`
	} `json:"signatures"`
}

func published(t *testing.T, r *Root) (es *ecdsa.PublicKey, ml *mldsa.PublicKey, keys []jwk) {
	t.Helper()
	var set struct{ Keys []jwk }
	if err := json.Unmarshal(r.JWKS(), &set); err != nil {
		t.Fatal(err)
	}
	if len(set.Keys) != 2 {
		t.Fatalf("the root publishes %d keys, want 2", len(set.Keys))
	}
	x, err := b64.DecodeString(set.Keys[0].X)
	if err != nil {
		t.Fatal(err)
	}
	y, err := b64.DecodeString(set.Keys[0].Y)
	if err != nil {
		t.Fatal(err)
	}
	if es, err = ecdsa.ParseUncompressedPublicKey(elliptic.P384(), append(append([]byte{4}, x...), y...)); err != nil {
		t.Fatalf("published ES384 key: %v", err)
	}
	mp, err := b64.DecodeString(set.Keys[1].Pub)
	if err != nil {
		t.Fatal(err)
	}
	if ml, err = mldsa.NewPublicKey(mldsa.MLDSA65(), mp); err != nil {
		t.Fatalf("published ML-DSA-65 key: %v", err)
	}
	return es, ml, set.Keys
}

func producerKey(t *testing.T) *ecdsa.PublicKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &k.PublicKey
}

func decode(t *testing.T, raw []byte) (statement, map[string]any) {
	t.Helper()
	var s statement
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	p, err := b64.DecodeString(s.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(p, &claims); err != nil {
		t.Fatal(err)
	}
	return s, claims
}

func header(t *testing.T, protected string) map[string]string {
	t.Helper()
	h, err := b64.DecodeString(protected)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(h, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func verifyES384(pub *ecdsa.PublicKey, input []byte, sig64 string) bool {
	sig, err := b64.DecodeString(sig64)
	if err != nil || len(sig) != 96 {
		return false
	}
	d := sha512.Sum384(input)
	return ecdsa.Verify(pub, d[:], new(big.Int).SetBytes(sig[:48]), new(big.Int).SetBytes(sig[48:]))
}

// The root publishes an ES384 and an ML-DSA-65 key, each under the kid the pin
// names: the RFC 7638 thumbprint for the first, mldsa65: and the SHA-256 of the
// encoded key for the second.
func TestTheRootPublishesBothHalvesUnderThePinnedKids(t *testing.T) {
	r := NewRoot(t)
	es, _, keys := published(t, r)
	want, err := delegation.Thumbprint(delegation.FromPublic(es, ""))
	if err != nil {
		t.Fatal(err)
	}
	if keys[0].Kid != want || r.ESKid != want || keys[0].Alg != "ES384" || keys[0].Crv != "P-384" {
		t.Fatalf("ES384 half %+v, kid %q, want kid %q", keys[0], r.ESKid, want)
	}
	mp, _ := b64.DecodeString(keys[1].Pub)
	sum := sha256.Sum256(mp)
	if keys[1].Kid != "mldsa65:"+b64.EncodeToString(sum[:]) || r.MLKid != keys[1].Kid || keys[1].Alg != "ML-DSA-65" || keys[1].Kty != "AKP" {
		t.Fatalf("ML-DSA-65 half %+v, kid %q", keys[1], r.MLKid)
	}
	if r.Pin() != r.ESKid+","+r.MLKid {
		t.Fatalf("Pin = %q", r.Pin())
	}
}

// A statement carries the claims in keyryx's format and two signatures, an
// ES384 and an ML-DSA-65, each over its own protected header and the payload,
// each verifying under the published key set and neither over another input.
func TestAStatementIsSignedByBothHalvesOfThePublishedRoot(t *testing.T) {
	r := NewRoot(t)
	es, ml, _ := published(t, r)
	key := producerKey(t)
	nb := time.Unix(1_800_000_000, 0)
	s, claims := decode(t, r.Sign(t, Claims{Name: "tokenfuse", Key: key, NotBefore: nb, NotAfter: nb.Add(time.Hour)}))

	kid := Kid(t, key)
	if claims["iss"] != "keyryx" || claims["name"] != "tokenfuse" || claims["owner"] != "producer" || claims["kid"] != kid {
		t.Fatalf("claims %+v", claims)
	}
	if claims["not_before"] != float64(nb.Unix()) || claims["not_after"] != float64(nb.Add(time.Hour).Unix()) {
		t.Fatalf("validity %v..%v", claims["not_before"], claims["not_after"])
	}
	roots := claims["roots"].(map[string]any)
	if roots["ES384"] != r.ESKid || roots["ML-DSA-65"] != r.MLKid {
		t.Fatalf("roots %+v", roots)
	}
	cert := claims["jwk"].(map[string]any)
	x, _ := b64.DecodeString(cert["x"].(string))
	y, _ := b64.DecodeString(cert["y"].(string))
	got, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
	if err != nil || !got.Equal(key) || cert["kid"] != kid {
		t.Fatalf("the certified key is not the producer's: %v", err)
	}

	if len(s.Signatures) != 2 {
		t.Fatalf("%d signatures, want 2", len(s.Signatures))
	}
	h0, h1 := header(t, s.Signatures[0].Protected), header(t, s.Signatures[1].Protected)
	if h0["alg"] != "ES384" || h0["kid"] != r.ESKid || h1["alg"] != "ML-DSA-65" || h1["kid"] != r.MLKid || h0["typ"] != "keyryx-statement+jwt" {
		t.Fatalf("headers %v %v", h0, h1)
	}
	in0 := []byte(s.Signatures[0].Protected + "." + s.Payload)
	in1 := []byte(s.Signatures[1].Protected + "." + s.Payload)
	if !verifyES384(es, in0, s.Signatures[0].Signature) {
		t.Fatal("the ES384 half does not verify under the published key")
	}
	if verifyES384(es, in1, s.Signatures[0].Signature) {
		t.Fatal("the ES384 half verifies over another header")
	}
	mlSig, _ := b64.DecodeString(s.Signatures[1].Signature)
	if err := mldsa.Verify(ml, in1, mlSig, &mldsa.Options{}); err != nil {
		t.Fatalf("the ML-DSA-65 half does not verify under the published key: %v", err)
	}
	if err := mldsa.Verify(ml, in0, mlSig, &mldsa.Options{}); err == nil {
		t.Fatal("the ML-DSA-65 half verifies over another header")
	}

	_, named := decode(t, r.Sign(t, Claims{Name: "n", Owner: "team-a", Key: key, NotBefore: nb, NotAfter: nb}))
	if named["owner"] != "team-a" {
		t.Fatalf("a given owner was replaced: %v", named["owner"])
	}
}

// The downgraded statement is what a holder of the classical half alone could
// make: its second half claims ML-DSA-65 under the ES384 kid and is an ES384
// signature, which ML-DSA-65 verification must refuse.
func TestADowngradedStatementsSecondHalfIsClassical(t *testing.T) {
	r := NewRoot(t)
	es, ml, _ := published(t, r)
	nb := time.Unix(1_800_000_000, 0)
	s, claims := decode(t, r.SignDowngraded(t, Claims{Name: "n", Key: producerKey(t), NotBefore: nb, NotAfter: nb}))
	h1 := header(t, s.Signatures[1].Protected)
	if h1["alg"] != "ML-DSA-65" || h1["kid"] != r.ESKid {
		t.Fatalf("second header %v, want ML-DSA-65 under the ES384 kid", h1)
	}
	if claims["roots"].(map[string]any)["ML-DSA-65"] != r.ESKid {
		t.Fatalf("roots %v", claims["roots"])
	}
	in1 := []byte(s.Signatures[1].Protected + "." + s.Payload)
	if !verifyES384(es, in1, s.Signatures[1].Signature) {
		t.Fatal("the second half is not an ES384 signature by the root")
	}
	sig, _ := b64.DecodeString(s.Signatures[1].Signature)
	if err := mldsa.Verify(ml, in1, sig, &mldsa.Options{}); err == nil {
		t.Fatal("the classical second half verified as ML-DSA-65")
	}
}

func readJSON(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Custody publishes the root's key set at once, a file per certified kid
// holding the statement as it is and its revocation time, takes a revocation
// back only when told to with the zero time, replaces the key set when told,
// and leaves no temporary file behind.
func TestCustodyPublishesWhatKeyryxCustodyWould(t *testing.T) {
	r := NewRoot(t)
	c := NewCustody(t, r)
	jwks, err := os.ReadFile(filepath.Join(c.Dir, "delegation-root.jwks.json"))
	if err != nil || string(jwks) != string(r.JWKS()) {
		t.Fatalf("root key set %q, %v", jwks, err)
	}
	nb := time.Unix(1_800_000_000, 0)
	key := producerKey(t)
	kid := Kid(t, key)
	stmt := r.Sign(t, Claims{Name: "n", Key: key, NotBefore: nb, NotAfter: nb})
	c.Put(t, kid, stmt)
	f := readJSON(t, filepath.Join(c.Dir, kid+".json"))
	if string(f["statement"]) != string(stmt) || string(f["revoked_at"]) != "0" {
		t.Fatalf("published %s / %s", f["statement"], f["revoked_at"])
	}
	at := time.Unix(1_800_000_100, 0)
	c.Revoke(t, kid, at)
	if f := readJSON(t, filepath.Join(c.Dir, kid+".json")); string(f["revoked_at"]) != "1800000100" || string(f["statement"]) != string(stmt) {
		t.Fatalf("after revoke: %s / %s", f["statement"], f["revoked_at"])
	}
	c.Revoke(t, kid, time.Time{})
	if f := readJSON(t, filepath.Join(c.Dir, kid+".json")); string(f["revoked_at"]) != "0" {
		t.Fatalf("a zero-time revoke left %s", f["revoked_at"])
	}
	c.Root(t, []byte(`{"keys":[]}`))
	c.Raw(t, "odd.json", []byte("not json"))
	if b, _ := os.ReadFile(filepath.Join(c.Dir, "delegation-root.jwks.json")); string(b) != `{"keys":[]}` {
		t.Fatalf("root not replaced: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(c.Dir, "odd.json")); string(b) != "not json" {
		t.Fatalf("raw file %q", b)
	}
	left, _ := filepath.Glob(filepath.Join(c.Dir, ".tmp-*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

// fatalTB records a Fatal and stops the calling goroutine, as testing.T does,
// so a helper's own failure path can be observed without failing this test.
type fatalTB struct {
	testing.TB
	mu    sync.Mutex
	fatal string
}

func (f *fatalTB) Helper() {}

func (f *fatalTB) Fatal(args ...any) {
	f.mu.Lock()
	f.fatal = fmt.Sprint(args...)
	f.mu.Unlock()
	runtime.Goexit()
}

func (f *fatalTB) run(fn func()) string {
	f.mu.Lock()
	f.fatal = ""
	f.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	<-done
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fatal
}

// A publish custody cannot write fails the test that asked for it, rather than
// leaving that test to run against a directory that does not hold what it put
// there.
func TestAPublishThatCannotBeWrittenFailsTheTest(t *testing.T) {
	r := NewRoot(t)
	tb := &fatalTB{TB: t}
	c := NewCustody(t, r)
	if err := os.RemoveAll(c.Dir); err != nil {
		t.Fatal(err)
	}
	if msg := tb.run(func() { c.Raw(tb, "x.json", []byte("{}")) }); msg == "" {
		t.Fatal("a write into a missing directory did not fail the test")
	}
	// The rename half: the temporary file writes, and the target is a
	// non-empty directory it cannot replace.
	c2 := NewCustody(t, r)
	if err := os.MkdirAll(filepath.Join(c2.Dir, "busy.json", "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if msg := tb.run(func() { c2.Raw(tb, "busy.json", []byte("{}")) }); msg == "" {
		t.Fatal("a rename onto a directory did not fail the test")
	}
}
