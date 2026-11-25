// Package keystatement reads keyryx's key statements: the evidence that keyryx
// custody certified a producer's short-lived checkpoint key.
//
// A statement is a JWS in the General JSON Serialization with exactly two
// signatures over one payload, an ES384 half and an ML-DSA-65 half, the two
// halves of keyryx's delegation root (keyryx CLAUDE.md invariant 24). This
// package verifies one under a root this box pinned, and nothing else: it
// fetches nothing (resolver.go does, off the poll loop) and trusts no key set
// it was served beyond the kids it pinned.
//
// It is a second implementation of keyryx's `internal/statement.Verify`, kept
// here rather than waiting for a shared package to be tagged, and held to the
// same answers by keyryx's own vectors, committed byte for byte in
// testdata/statement-vectors.json (`@claude 2026-11-25`, a design reviewed
// before it was written). When that verifier moves into agent-stack-go, this
// package goes and its vectors go with it.
package keystatement

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/mldsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/v2/delegation"
)

// The format's fixed words, as keyryx writes them.
const (
	typ    = "keyryx-statement+jwt"
	issuer = "keyryx"
	// MaxBytes bounds a statement, as keyryx bounds what it signs.
	MaxBytes = 16 << 10
	// MaxKeySet bounds a root key set.
	MaxKeySet = 1 << 20
	maxJWK    = 1 << 10
	// reserved is keyryx's own name space, which no checkpoint key lives in.
	reserved = "keyryx."
)

// The two halves, in the order a statement carries them.
const (
	ES384   = "ES384"
	MLDSA65 = "ML-DSA-65"
)

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	jtiRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	b64    = base64.RawURLEncoding.Strict()
	halves = []string{ES384, MLDSA65}
)

// Statement is what a verified statement says.
type Statement struct {
	Name      string
	Owner     string
	Kid       string
	Alg       string
	Public    *ecdsa.PublicKey
	NotBefore int64
	NotAfter  int64
}

// ValidAt says whether the statement certifies its key at t: from not_before
// up to and not including not_after, in whole seconds, as keyryx reads it.
func (s Statement) ValidAt(t time.Time) bool {
	return t.Unix() >= s.NotBefore && t.Unix() < s.NotAfter
}

// rootKey is one half of a pinned root.
type rootKey struct {
	alg string
	pub []byte
}

// Roots is a pinned delegation root: the root keys a statement may be signed
// under, by kid.
type Roots map[string]rootKey

// ParsePin reads HERALDYX_CHECKPOINT_ROOT_PIN: comma-separated kids of the
// delegation root, at least one of each half. An ES384 kid is an RFC 7638
// thumbprint; an ML-DSA-65 kid is `mldsa65:` and the base64url SHA-256 of
// the public key, as keyryx names them.
func ParsePin(spec string) (map[string]string, error) {
	pin := map[string]string{}
	for _, raw := range strings.Split(spec, ",") {
		kid := strings.TrimSpace(raw)
		if kid == "" {
			continue
		}
		alg := ES384
		rest := kid
		if s, ok := strings.CutPrefix(kid, "mldsa65:"); ok {
			alg, rest = MLDSA65, s
		}
		if b, err := b64.DecodeString(rest); err != nil || len(b) != sha256.Size {
			return nil, fmt.Errorf("pin: %q is not a root kid", kid)
		}
		if _, dup := pin[kid]; dup {
			return nil, fmt.Errorf("pin: %q is named twice", kid)
		}
		pin[kid] = alg
	}
	var es, ml bool
	for _, alg := range pin {
		es = es || alg == ES384
		ml = ml || alg == MLDSA65
	}
	if !es || !ml {
		return nil, errors.New("pin: it must name at least one ES384 and one ML-DSA-65 kid of the delegation root")
	}
	return pin, nil
}

// RootsFromJWKS reads keyryx's delegation root key set and keeps only the keys
// the pin names, each under the algorithm the pin gives its kid. A key whose
// kid is not its own thumbprint refuses the whole set; a served key the pin
// does not name is left out, never trusted. It says which pinned kids the set
// did not carry.
func RootsFromJWKS(raw []byte, pin map[string]string) (Roots, []string, error) {
	if len(raw) > MaxKeySet {
		return nil, nil, errors.New("root key set: over its size cap")
	}
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := strict(raw, &set); err != nil {
		return nil, nil, fmt.Errorf("root key set: %w", err)
	}
	roots := Roots{}
	for i, k := range set.Keys {
		var head struct {
			Kty string `json:"kty"`
			Alg string `json:"alg"`
		}
		_ = json.Unmarshal(k, &head)
		var kid, alg string
		var pub []byte
		switch {
		case head.Kty == "EC" && head.Alg == ES384:
			var given struct {
				Kid *string `json:"kid"`
			}
			_ = json.Unmarshal(k, &given)
			j, point, err := parseEC(k, ES384)
			if err != nil || given.Kid == nil {
				return nil, nil, fmt.Errorf("root key set: key %d is not an ES384 key with its own kid", i)
			}
			kid, alg, pub = j, ES384, point
		case head.Kty == "AKP":
			var in struct {
				Kty string `json:"kty"`
				Alg string `json:"alg"`
				Pub string `json:"pub"`
				Kid string `json:"kid"`
				Use string `json:"use"`
			}
			if err := strict(k, &in); err != nil || in.Alg != MLDSA65 || in.Use != "sig" {
				return nil, nil, fmt.Errorf("root key set: key %d is not an ML-DSA-65 key in the AKP form", i)
			}
			p, err := b64.DecodeString(in.Pub)
			if err != nil {
				return nil, nil, fmt.Errorf("root key set: key %d: pub is not base64url", i)
			}
			if _, err := mldsa.NewPublicKey(mldsa.MLDSA65(), p); err != nil {
				return nil, nil, fmt.Errorf("root key set: key %d is not an ML-DSA-65 public key", i)
			}
			sum := sha256.Sum256(p)
			if in.Kid != "mldsa65:"+base64.RawURLEncoding.EncodeToString(sum[:]) {
				return nil, nil, fmt.Errorf("root key set: key %d names a kid that is not its own", i)
			}
			kid, alg, pub = in.Kid, MLDSA65, p
		default:
			return nil, nil, fmt.Errorf("root key set: key %d is of a kind a delegation root never is", i)
		}
		if want, pinned := pin[kid]; pinned && want == alg {
			roots[kid] = rootKey{alg: alg, pub: pub}
		}
	}
	var missing []string
	for kid := range pin {
		if _, ok := roots[kid]; !ok {
			missing = append(missing, kid)
		}
	}
	sort.Strings(missing)
	return roots, missing, nil
}

type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type sigWire struct {
	Protected string `json:"protected"`
	Signature string `json:"signature"`
}

type jwsWire struct {
	Payload    string    `json:"payload"`
	Signatures []sigWire `json:"signatures"`
}

// Verify verifies a statement under roots and reads what it certifies. It is
// refused unless it carries exactly an ES384 and an ML-DSA-65 signature, in
// that order, each by a pinned root key of that algorithm that the payload
// names, each verifying over the same payload; every claim is required, none
// other is accepted, and the certified key is an EC key whose kid is its
// RFC 7638 thumbprint.
func Verify(raw []byte, roots Roots) (Statement, error) {
	if len(raw) > MaxBytes {
		return Statement{}, errors.New("statement: over its size cap")
	}
	var w jwsWire
	if err := strict(raw, &w); err != nil {
		return Statement{}, fmt.Errorf("statement: not a JWS of the known shape: %w", err)
	}
	if len(w.Signatures) != len(halves) {
		return Statement{}, fmt.Errorf("statement: %d signatures; a statement carries exactly the two halves of its root", len(w.Signatures))
	}
	var hs []header
	for i, sw := range w.Signatures {
		hb, err := b64.DecodeString(sw.Protected)
		if err != nil {
			return Statement{}, errors.New("statement: a protected header is not base64url")
		}
		var h header
		if err := strict(hb, &h); err != nil || h.Kid == "" || h.Typ != typ || h.Alg != halves[i] {
			return Statement{}, fmt.Errorf("statement: signature %d is not the %s half", i+1, halves[i])
		}
		hs = append(hs, h)
	}
	payload, err := b64.DecodeString(w.Payload)
	if err != nil {
		return Statement{}, errors.New("statement: the payload is not base64url")
	}
	st, rootsNamed, err := claims(payload)
	if err != nil {
		return Statement{}, err
	}
	if hs[0].Kid != rootsNamed[0] || hs[1].Kid != rootsNamed[1] {
		return Statement{}, errors.New("statement: a half was signed by a key the payload does not name")
	}
	for i, h := range hs {
		rk, ok := roots[h.Kid]
		if !ok || rk.alg != halves[i] {
			return Statement{}, fmt.Errorf("statement: the %s half names a root key this box did not pin", halves[i])
		}
		sig, err := b64.DecodeString(w.Signatures[i].Signature)
		if err != nil {
			return Statement{}, errors.New("statement: a signature is not base64url")
		}
		if !verifyHalf(rk, []byte(w.Signatures[i].Protected+"."+w.Payload), sig) {
			return Statement{}, fmt.Errorf("statement: the %s half does not verify", halves[i])
		}
	}
	return st, nil
}

func verifyHalf(rk rootKey, input, sig []byte) bool {
	switch rk.alg {
	case MLDSA65:
		pk, err := mldsa.NewPublicKey(mldsa.MLDSA65(), rk.pub)
		if err != nil {
			return false
		}
		return mldsa.Verify(pk, input, sig, &mldsa.Options{}) == nil
	case ES384:
		pk, err := ecdsa.ParseUncompressedPublicKey(elliptic.P384(), rk.pub)
		if err != nil || len(sig) != 96 {
			return false
		}
		d := sha512.Sum384(input)
		r, s := new(big.Int).SetBytes(sig[:48]), new(big.Int).SetBytes(sig[48:])
		return ecdsa.Verify(pk, d[:], r, s)
	}
	return false
}

func claims(payload []byte) (Statement, [2]string, error) {
	var w struct {
		V         *int            `json:"v"`
		Iss       *string         `json:"iss"`
		Name      *string         `json:"name"`
		Owner     *string         `json:"owner"`
		Kid       *string         `json:"kid"`
		JWK       json.RawMessage `json:"jwk"`
		NotBefore *int64          `json:"not_before"`
		NotAfter  *int64          `json:"not_after"`
		IssuedAt  *int64          `json:"issued_at"`
		JTI       *string         `json:"jti"`
		Roots     *struct {
			ES384   string `json:"ES384"`
			MLDSA65 string `json:"ML-DSA-65"`
		} `json:"roots"`
	}
	var none [2]string
	if err := strict(payload, &w); err != nil {
		return Statement{}, none, fmt.Errorf("statement: the claims are not of the known shape: %w", err)
	}
	if w.V == nil || w.Iss == nil || w.Name == nil || w.Owner == nil || w.Kid == nil || w.JWK == nil ||
		w.NotBefore == nil || w.NotAfter == nil || w.IssuedAt == nil || w.JTI == nil || w.Roots == nil {
		return Statement{}, none, errors.New("statement: a required claim is missing")
	}
	switch {
	case *w.V != 1:
		return Statement{}, none, fmt.Errorf("statement: version %d; this verifier reads version 1", *w.V)
	case *w.Iss != issuer:
		return Statement{}, none, errors.New("statement: another issuer")
	case !nameRe.MatchString(*w.Name) || strings.HasPrefix(*w.Name, reserved), !nameRe.MatchString(*w.Owner):
		return Statement{}, none, errors.New("statement: a name or owner outside the grammar")
	case *w.NotBefore <= 0 || *w.IssuedAt <= 0 || *w.NotAfter <= *w.NotBefore || *w.IssuedAt > *w.NotBefore:
		return Statement{}, none, errors.New("statement: its times are not issued_at <= not_before < not_after")
	case !jtiRe.MatchString(*w.JTI):
		return Statement{}, none, errors.New("statement: a jti outside the grammar")
	case w.Roots.ES384 == "" || w.Roots.MLDSA65 == "":
		return Statement{}, none, errors.New("statement: the roots are not named")
	}
	var alg struct {
		Alg string `json:"alg"`
	}
	_ = json.Unmarshal(w.JWK, &alg)
	kid, point, err := parseEC(w.JWK, alg.Alg)
	if err != nil {
		return Statement{}, none, fmt.Errorf("statement: its key: %w", err)
	}
	if *w.Kid != kid {
		return Statement{}, none, errors.New("statement: its kid is not its key's thumbprint")
	}
	curve := elliptic.P256()
	if alg.Alg == ES384 {
		curve = elliptic.P384()
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(curve, point)
	if err != nil {
		return Statement{}, none, errors.New("statement: its key is not a point on its curve")
	}
	return Statement{Name: *w.Name, Owner: *w.Owner, Kid: kid, Alg: alg.Alg, Public: pub,
		NotBefore: *w.NotBefore, NotAfter: *w.NotAfter}, [2]string{w.Roots.ES384, w.Roots.MLDSA65}, nil
}

// parseEC reads an EC public key in the one form keyryx writes, checks its
// point and returns its RFC 7638 thumbprint and the uncompressed point.
func parseEC(raw []byte, alg string) (string, []byte, error) {
	if len(raw) > maxJWK {
		return "", nil, errors.New("the key is over its size cap")
	}
	var in struct {
		Kty string `json:"kty"`
		Crv string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
		Kid string `json:"kid"`
		Alg string `json:"alg"`
		Use string `json:"use"`
	}
	if err := strict(raw, &in); err != nil {
		return "", nil, fmt.Errorf("the key is not a JWK of the known shape: %w", err)
	}
	var curve elliptic.Curve
	var crv string
	var size int
	switch alg {
	case "ES256":
		curve, crv, size = elliptic.P256(), "P-256", 32
	case ES384:
		curve, crv, size = elliptic.P384(), "P-384", 48
	default:
		return "", nil, fmt.Errorf("a key is ES256 or ES384, not %q", alg)
	}
	if in.Kty != "EC" || in.Crv != crv || in.Alg != alg || in.Use != "sig" {
		return "", nil, fmt.Errorf("the key is not an EC %s signing key", crv)
	}
	x, errX := b64.DecodeString(in.X)
	y, errY := b64.DecodeString(in.Y)
	if errX != nil || errY != nil || len(x) != size || len(y) != size {
		return "", nil, fmt.Errorf("the key's coordinates are not %d bytes of unpadded base64url", size)
	}
	point := append(append([]byte{4}, x...), y...)
	pk, err := ecdsa.ParseUncompressedPublicKey(curve, point)
	if err != nil {
		return "", nil, fmt.Errorf("the key is not a point on %s", crv)
	}
	kid, err := delegation.Thumbprint(delegation.FromPublic(pk, ""))
	if err != nil {
		return "", nil, err
	}
	if in.Kid != kid {
		return "", nil, errors.New("the key names a kid that is not its RFC 7638 thumbprint")
	}
	return kid, point, nil
}

func strict(raw []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("data after the object")
	}
	return nil
}
