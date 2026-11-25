// Package statementtest mints keyryx key statements and serves them, for tests
// only: a delegation root of its own (an ES384 and an ML-DSA-65 key), the key
// statements it signs in the format keyryx writes, and a stand-in custody that
// serves both the way keyryx custody does. No binary imports it.
package statementtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/v2/delegation"
)

var b64 = base64.RawURLEncoding

// Root is a delegation root.
type Root struct {
	es      *ecdsa.PrivateKey
	ml      *mldsa.PrivateKey
	ESKid   string
	MLKid   string
	jwksRaw []byte
}

// NewRoot makes a root.
func NewRoot(t testing.TB) *Root {
	t.Helper()
	es, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ml, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	r := &Root{es: es, ml: ml}
	ej := delegation.FromPublic(&es.PublicKey, "")
	if r.ESKid, err = delegation.Thumbprint(ej); err != nil {
		t.Fatal(err)
	}
	mp := ml.PublicKey().Bytes()
	sum := sha256.Sum256(mp)
	r.MLKid = "mldsa65:" + b64.EncodeToString(sum[:])
	r.jwksRaw, _ = json.Marshal(map[string]any{"keys": []map[string]any{
		{"kty": "EC", "crv": "P-384", "x": ej.X, "y": ej.Y, "kid": r.ESKid, "alg": "ES384", "use": "sig"},
		{"kty": "AKP", "alg": "ML-DSA-65", "pub": b64.EncodeToString(mp), "kid": r.MLKid, "use": "sig"},
	}})
	return r
}

// Pin is the root as HERALDYX_CHECKPOINT_ROOT_PIN names it.
func (r *Root) Pin() string { return r.ESKid + "," + r.MLKid }

// JWKS is the root's key set, as custody serves it.
func (r *Root) JWKS() []byte { return r.jwksRaw }

// Claims is what a statement certifies.
type Claims struct {
	Name      string
	Owner     string
	Key       *ecdsa.PublicKey // P-256
	NotBefore time.Time
	NotAfter  time.Time
}

// Kid is the RFC 7638 thumbprint of a P-256 key.
func Kid(t testing.TB, k *ecdsa.PublicKey) string {
	t.Helper()
	kid, err := delegation.Thumbprint(delegation.FromPublic(k, ""))
	if err != nil {
		t.Fatal(err)
	}
	return kid
}

// Sign signs a statement in keyryx's format.
func (r *Root) Sign(t testing.TB, c Claims) []byte {
	t.Helper()
	return r.sign(t, c, false)
}

// SignDowngraded signs a statement whose second half claims to be the
// ML-DSA-65 half and is an ES384 signature by the root's ES384 key: what a
// holder of the classical half alone could make.
func (r *Root) SignDowngraded(t testing.TB, c Claims) []byte {
	t.Helper()
	return r.sign(t, c, true)
}

func (r *Root) sign(t testing.TB, c Claims, downgrade bool) []byte {
	t.Helper()
	j := delegation.FromPublic(c.Key, "")
	kid := Kid(t, c.Key)
	owner := c.Owner
	if owner == "" {
		owner = "producer"
	}
	mlKid := r.MLKid
	if downgrade {
		mlKid = r.ESKid
	}
	payload, _ := json.Marshal(map[string]any{
		"v": 1, "iss": "keyryx", "name": c.Name, "owner": owner, "kid": kid,
		"jwk":        map[string]string{"kty": "EC", "crv": "P-256", "x": j.X, "y": j.Y, "kid": kid, "alg": "ES256", "use": "sig"},
		"not_before": c.NotBefore.Unix(), "not_after": c.NotAfter.Unix(), "issued_at": c.NotBefore.Unix(),
		"jti": "jti-" + strings.Repeat("a", 20), "roots": map[string]string{"ES384": r.ESKid, "ML-DSA-65": mlKid},
	})
	p := b64.EncodeToString(payload)
	type sig struct {
		Protected string `json:"protected"`
		Signature string `json:"signature"`
	}
	var sigs []sig
	for _, half := range []struct{ alg, kid string }{{"ES384", r.ESKid}, {"ML-DSA-65", mlKid}} {
		h, _ := json.Marshal(map[string]string{"alg": half.alg, "kid": half.kid, "typ": "keyryx-statement+jwt"})
		ph := b64.EncodeToString(h)
		input := []byte(ph + "." + p)
		var raw []byte
		if half.alg == "ES384" || downgrade {
			d := sha512.Sum384(input)
			rr, ss, err := ecdsa.Sign(rand.Reader, r.es, d[:])
			if err != nil {
				t.Fatal(err)
			}
			raw = make([]byte, 96)
			rr.FillBytes(raw[:48])
			ss.FillBytes(raw[48:])
		} else {
			var err error
			if raw, err = r.ml.Sign(rand.Reader, input, &mldsa.Options{}); err != nil {
				t.Fatal(err)
			}
		}
		sigs = append(sigs, sig{ph, b64.EncodeToString(raw)})
	}
	out, _ := json.Marshal(map[string]any{"payload": p, "signatures": sigs})
	return out
}

// Custody is a stand-in for keyryx custody's publishing directory
// (KEYRYX_STATEMENTS_DIR): the root's key set, and a file per certified key.
type Custody struct {
	Dir     string
	mu      sync.Mutex
	stmts   map[string][]byte
	revoked map[string]int64
}

// NewCustody publishes root's key set in a new directory and no statement yet.
func NewCustody(t testing.TB, root *Root) *Custody {
	t.Helper()
	c := &Custody{Dir: t.TempDir(), stmts: map[string][]byte{}, revoked: map[string]int64{}}
	c.write(t, "delegation-root.jwks.json", root.JWKS())
	return c
}

func (c *Custody) write(t testing.TB, name string, b []byte) {
	t.Helper()
	tmp := filepath.Join(c.Dir, ".tmp-"+name)
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(c.Dir, name)); err != nil {
		t.Fatal(err)
	}
}

func (c *Custody) publish(t testing.TB, kid string) {
	b, _ := json.Marshal(map[string]any{"statement": json.RawMessage(c.stmts[kid]), "revoked_at": c.revoked[kid]})
	c.write(t, kid+".json", b)
}

// Put publishes a statement for kid.
func (c *Custody) Put(t testing.TB, kid string, raw []byte) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stmts[kid] = raw
	c.publish(t, kid)
}

// Revoke publishes kid as revoked at at (the zero time takes a revocation
// back, which custody never does).
func (c *Custody) Revoke(t testing.TB, kid string, at time.Time) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if at.IsZero() {
		c.revoked[kid] = 0
	} else {
		c.revoked[kid] = at.Unix()
	}
	c.publish(t, kid)
}

// Root publishes another key set as the root's.
func (c *Custody) Root(t testing.TB, jwks []byte) {
	t.Helper()
	c.write(t, "delegation-root.jwks.json", jwks)
}

// Raw writes name with b as it is.
func (c *Custody) Raw(t testing.TB, name string, b []byte) {
	t.Helper()
	c.write(t, name, b)
}
