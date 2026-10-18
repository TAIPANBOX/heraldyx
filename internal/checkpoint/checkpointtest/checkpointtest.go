// Package checkpointtest builds agent-passport SPEC.md 6.5.1 checkpoint files
// for the tests of heraldyx's coverage gate. It is test support only: nothing
// in the process imports it, and it signs with a fresh P-256 key that exists
// for one test run.
//
// It is written from the SPEC, not from the verifier: the canonical form is
// RFC 8785 through the same library the verifier uses, the signing input is
// the canonical record with `sig` removed, and the chain hash of a line is
// agent-stack-go's own `event.ChainHashRaw`. A fixture that agreed with the
// verifier by sharing its code would prove nothing about the SPEC.
package checkpointtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/gowebpki/jcs"
)

// Genesis is the `prev` member of the first record in a checkpoint file.
const Genesis = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// Key is one producer's P-256 signing key.
type Key struct {
	Priv *ecdsa.PrivateKey
	// Kid is the RFC 7638 thumbprint of the public key, base64url without
	// padding, as SPEC 6.5.1 defines the `kid` member.
	Kid string
	// point is the public key as the 65-byte uncompressed point.
	point []byte
}

// uncompressed is the public key as the 65-byte uncompressed point. It is read
// through ECDH rather than the raw coordinates, which Go 1.26 deprecates.
func uncompressed(priv *ecdsa.PrivateKey) ([]byte, error) {
	pub, err := priv.PublicKey.ECDH()
	if err != nil {
		return nil, err
	}
	return pub.Bytes(), nil
}

// NewKey makes a fresh signing key.
func NewKey() (*Key, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	point, err := uncompressed(priv)
	if err != nil {
		return nil, err
	}
	enc := base64.RawURLEncoding
	// RFC 7638 section 3.2: the required members of an EC key, in lexical order.
	thumb := fmt.Sprintf(`{"crv":"P-256","kty":"EC","x":"%s","y":"%s"}`,
		enc.EncodeToString(point[1:33]), enc.EncodeToString(point[33:65]))
	sum := sha256.Sum256([]byte(thumb))
	return &Key{Priv: priv, Kid: enc.EncodeToString(sum[:]), point: point}, nil
}

// Public is the key as a keys file holds it: the 65-byte uncompressed point,
// base64url without padding.
func (k *Key) Public() string {
	return base64.RawURLEncoding.EncodeToString(k.point)
}

// Canon is the JCS serialization of an object, as one line without its LF.
func Canon(members map[string]any) (string, error) {
	raw, err := json.Marshal(members)
	if err != nil {
		return "", err
	}
	out, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Sign returns the signed record for members: alg ES256, the key's kid, and a
// sig over the canonical record without sig, as SPEC 6.5.1 "Signing" states.
func Sign(k *Key, members map[string]any) (string, error) {
	delete(members, "sig")
	members["alg"] = "ES256"
	members["kid"] = k.Kid
	signingInput, err := Canon(members)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, k.Priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	members["sig"] = base64.RawURLEncoding.EncodeToString(sig)
	return Canon(members)
}

// Unsigned returns members as an unsigned record: no alg, kid or sig, and the
// one unsigned_reason v1 defines.
func Unsigned(members map[string]any) (string, error) {
	delete(members, "alg")
	delete(members, "kid")
	delete(members, "sig")
	members["unsigned_reason"] = "key_expired"
	return Canon(members)
}

// Link is the `prev` value a record after line takes: the SHA-256 of that
// line's bytes without its LF.
func Link(line string) string {
	sum := sha256.Sum256([]byte(line))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Members builds the unsigned members of a record over the first count lines
// of stream, the way a writer takes one: prefix is the SHA-256 of the bytes
// through the LF that ends line count, and head is the chain hash of that
// line. stream is the stream file's content.
func Members(stream []byte, file, kind string, count int, prev, at string) (map[string]any, error) {
	end, last, err := cut(stream, count)
	if err != nil {
		return nil, err
	}
	head, err := event.ChainHashRaw([]byte(last))
	if err != nil {
		return nil, err
	}
	psum := sha256.Sum256(stream[:end])
	return map[string]any{
		"v":      1,
		"kind":   kind,
		"stream": file,
		"count":  count,
		"head":   head,
		"prefix": "sha256:" + hex.EncodeToString(psum[:]),
		"at":     at,
		"prev":   prev,
	}, nil
}

// cut finds the byte offset just after the LF that ends line count, and that
// line without its LF.
func cut(stream []byte, count int) (int, string, error) {
	seen := 0
	for i, b := range stream {
		if b != '\n' {
			continue
		}
		seen++
		if seen == count {
			start := strings.LastIndexByte(string(stream[:i]), '\n') + 1
			return i + 1, string(stream[start:i]), nil
		}
	}
	return 0, "", fmt.Errorf("stream holds %d line(s), want %d", seen, count)
}

// File writes a checkpoint file: each record followed by one LF.
func File(path string, records ...string) error {
	var b strings.Builder
	for _, r := range records {
		b.WriteString(r)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// Event is one event line as the bus holds it: a valid envelope claiming the
// given source, with a distinct run id per n.
func Event(source, typ string, n int) string {
	return fmt.Sprintf(`{"schema":"taipanbox.dev/agent-event/v0.2","ts":"2026-10-18T09:00:00Z",`+
		`"source":%q,"type":%q,"agent_id":"agent://acme/biller","run_id":"run-%d","severity":"info"}`+"\n",
		source, typ, n)
}

// CheckpointName is the file a stream's checkpoints live in, relative to the
// checkpoints directory: the stream's full file name, then .ckpt.ndjson.
func CheckpointName(stream string) string { return stream + ".ckpt.ndjson" }
