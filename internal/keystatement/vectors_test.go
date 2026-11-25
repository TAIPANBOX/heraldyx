package keystatement

import (
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// keyryx's own statement vectors (keyryx internal/statement/testdata,
// committed here byte for byte): this verifier gives the answers keyryx's
// does, or a stream's coverage would depend on which of the two read it.

type vectors struct {
	RootJWKS json.RawMessage `json:"root_jwks"`
	Pin      []string        `json:"pin"`
	Valid    []struct {
		Statement json.RawMessage `json:"statement"`
		Name      string          `json:"name"`
		Kid       string          `json:"kid"`
		NotBefore int64           `json:"not_before"`
		NotAfter  int64           `json:"not_after"`
		Point     string          `json:"point"`
	} `json:"valid"`
	Refused []struct {
		Why       string          `json:"why"`
		Statement json.RawMessage `json:"statement"`
	} `json:"refused"`
}

func loadVectors(t *testing.T) (vectors, Roots) {
	t.Helper()
	raw, err := os.ReadFile("testdata/statement-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Valid) == 0 || len(v.Refused) == 0 {
		t.Fatal("the vectors hold no statement, so this test measured nothing")
	}
	pin, err := ParsePin(strings.Join(v.Pin, ","))
	if err != nil {
		t.Fatalf("the vectors' pin: %v", err)
	}
	roots, missing, err := RootsFromJWKS(v.RootJWKS, pin)
	if err != nil || len(missing) != 0 {
		t.Fatalf("the vectors' root key set: %v, missing %v", err, missing)
	}
	return v, roots
}

func TestKeyryxsStatementVectorsGiveKeyryxsAnswers(t *testing.T) {
	v, roots := loadVectors(t)
	for i, g := range v.Valid {
		st, err := Verify(g.Statement, roots)
		if err != nil {
			t.Fatalf("valid %d does not verify: %v", i, err)
		}
		if st.Name != g.Name || st.Kid != g.Kid || st.NotBefore != g.NotBefore || st.NotAfter != g.NotAfter || st.Alg != "ES256" {
			t.Fatalf("valid %d reads as %s %s %d %d %s", i, st.Name, st.Kid, st.NotBefore, st.NotAfter, st.Alg)
		}
		want, _ := base64.RawURLEncoding.DecodeString(g.Point)
		got, _ := st.Public.Bytes()
		if string(got) != string(want) {
			t.Fatalf("valid %d certifies another key", i)
		}
	}
	for _, r := range v.Refused {
		if _, err := Verify(r.Statement, roots); err == nil {
			t.Errorf("%s: verified", r.Why)
		}
	}
}

func TestAStatementVerifiesOnlyUnderThePinnedRoot(t *testing.T) {
	v, _ := loadVectors(t)
	// The served set carries both halves, the pin only one of them twice
	// over: the set beside the pin is never trusted.
	for name, pin := range map[string]string{
		"a pin naming only the ES384 half":  v.Pin[0],
		"a pin naming only the ML-DSA half": v.Pin[1],
	} {
		if _, err := ParsePin(pin); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	other := "mldsa65:" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	pin, err := ParsePin(v.Pin[0] + "," + other)
	if err != nil {
		t.Fatal(err)
	}
	roots, missing, err := RootsFromJWKS(v.RootJWKS, pin)
	if err != nil || len(missing) != 1 || missing[0] != other {
		t.Fatalf("a pinned kid the set does not carry: %v %v", err, missing)
	}
	if _, err := Verify(v.Valid[0].Statement, roots); err == nil {
		t.Fatal("a statement verified with a half this box did not pin")
	}
}

func TestMalformedPinsAndRootSetsAreRefused(t *testing.T) {
	v, _ := loadVectors(t)
	for name, spec := range map[string]string{
		"empty":                       "",
		"not base64url":               "not a kid!," + v.Pin[1],
		"too short":                   "abc," + v.Pin[1],
		"named twice":                 v.Pin[0] + "," + v.Pin[0] + "," + v.Pin[1],
		"an ML-DSA prefix on nothing": v.Pin[0] + ",mldsa65:",
	} {
		if _, err := ParsePin(spec); err == nil {
			t.Errorf("pin %s: accepted", name)
		}
	}
	pin, _ := ParsePin(strings.Join(v.Pin, ","))
	var set map[string][]map[string]any
	_ = json.Unmarshal(v.RootJWKS, &set)
	for name, edit := range map[string]func(k []map[string]any){
		"an EC key whose kid is not its own": func(k []map[string]any) { k[0]["kid"] = v.Pin[1][8:] },
		"an ML-DSA key whose kid is not its own": func(k []map[string]any) {
			k[1]["kid"] = "mldsa65:" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
		},
		"a key of another kind": func(k []map[string]any) { k[0]["kty"] = "RSA" },
		"an unknown member":     func(k []map[string]any) { k[1]["d"] = "secret" },
	} {
		keys := make([]map[string]any, len(set["keys"]))
		for i, k := range set["keys"] {
			c := map[string]any{}
			for kk, vv := range k {
				c[kk] = vv
			}
			keys[i] = c
		}
		edit(keys)
		raw, _ := json.Marshal(map[string]any{"keys": keys})
		if _, _, err := RootsFromJWKS(raw, pin); err == nil {
			t.Errorf("root set with %s: accepted", name)
		}
	}
	if _, _, err := RootsFromJWKS([]byte(`{"keys":[]} x`), pin); err == nil {
		t.Error("a root set with trailing data was accepted")
	}
}

func TestHostileStatementBytesNeverVerifyAndNeverPanic(t *testing.T) {
	v, roots := loadVectors(t)
	good := []byte(v.Valid[0].Statement)
	seed := uint64(1)
	next := func() uint64 { seed ^= seed << 13; seed ^= seed >> 7; seed ^= seed << 17; return seed }
	for i := 0; i < 2000; i++ {
		b := append([]byte(nil), good...)
		for j := 0; j < 1+int(next()%4); j++ {
			b[next()%uint64(len(b))] = byte(next())
		}
		if _, err := Verify(b, roots); err == nil && string(b) != string(good) {
			// A byte flip that still verifies would be a forgery, unless it
			// fell in insignificant JSON whitespace, which the format has none of.
			t.Fatalf("a mutated statement verified (seed %d)", i)
		}
	}
	for _, b := range [][]byte{nil, []byte("{}"), []byte("null"), []byte(`{"payload":"","signatures":[]}`), make([]byte, MaxBytes+1)} {
		if _, err := Verify(b, roots); err == nil {
			t.Errorf("%q verified", b[:min(len(b), 30)])
		}
	}
	var _ *ecdsa.PublicKey
}
