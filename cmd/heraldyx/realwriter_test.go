package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/v2/event"
)

// The checkpoint gate against a real producer. The other gate tests build
// their checkpoint files from SPEC 6.5.1 (internal/checkpoint/checkpointtest);
// these write the stream and its sidecar with agent-stack-go's own writer,
// event.NewChainedWriterWithCheckpoints, which is what a producer on the bus
// links, sign with an ES256 key of the producer's own, pin that key in the
// HERALDYX_CHECKPOINT_KEYS format, and drive the whole process through
// cycle(): configuration, gate, poll and mail.

// es256Signer is a producer's checkpoint signer: event.CheckpointSigner over a
// P-256 key, the raw 64-byte r||s signature over SHA-256 of the input.
type es256Signer struct {
	key *ecdsa.PrivateKey
	kid string
}

func newES256Signer(t *testing.T) *es256Signer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &es256Signer{key: key, kid: rfc7638(t, &key.PublicKey)}
}

func (s *es256Signer) KeyID() string { return s.kid }

func (s *es256Signer) Sign(in []byte) ([]byte, error) {
	d := sha256.Sum256(in)
	r, sv, err := ecdsa.Sign(rand.Reader, s.key, d[:])
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 64)
	r.FillBytes(raw[:32])
	sv.FillBytes(raw[32:])
	return raw, nil
}

// rfc7638 is the thumbprint of an EC public key, written out here so it shares
// no code with either side it checks.
func rfc7638(t *testing.T, pub *ecdsa.PublicKey) string {
	t.Helper()
	pt, err := pub.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	j := fmt.Sprintf(`{"crv":"P-256","kty":"EC","x":"%s","y":"%s"}`, enc.EncodeToString(pt[1:33]), enc.EncodeToString(pt[33:]))
	sum := sha256.Sum256([]byte(j))
	return enc.EncodeToString(sum[:])
}

// realProducer writes n high events into the bench's wardryx.ndjson through
// the real writer, a checkpoint every three lines and a close record at the
// end, and returns the stream path. The keys file pins the signer's key as
// HERALDYX_CHECKPOINT_KEYS reads it: kid to the base64url uncompressed point.
func realProducer(t *testing.T, n int) (b *streamBench, stream string) {
	t.Helper()
	root := t.TempDir()
	ckptDir := filepath.Join(root, "checkpoints")
	if err := os.MkdirAll(ckptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	signer := newES256Signer(t)
	pt, err := signer.key.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	keysPath := filepath.Join(root, "keys.json")
	write(t, keysPath, `{"`+signer.kid+`":"`+base64.RawURLEncoding.EncodeToString(pt)+`"}`)

	b = gatedBench(t, "medium", ckptDir, keysPath)
	stream = filepath.Join(b.dir, "wardryx.ndjson")
	clock := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	w, err := event.NewChainedWriterWithCheckpoints(stream, event.CheckpointConfig{
		Dir: ckptDir, Signer: signer, Every: 3,
		Now: func() time.Time { clock = clock.Add(time.Second); return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if err := w.Write(event.Event{
			Schema: event.SchemaV02, TS: "2026-11-23T09:00:00Z", Source: "wardryx",
			Type: "policy_deny", AgentID: "agent://acme/biller",
			RunID: fmt.Sprintf("run-real-%02d", i), Severity: event.SeverityHigh,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ckptDir, event.CheckpointFileName("wardryx.ndjson"))); err != nil {
		t.Fatalf("the real writer left no sidecar: %v", err)
	}
	return b, stream
}

func TestARealProducersCheckpointsAreAcceptedAsTheyAre(t *testing.T) {
	b, _ := realProducer(t, 7)
	b.poll(t0)
	got := b.out(t)
	if strings.Contains(got, "signed checkpoint") || strings.Contains(got, "is not covered") {
		t.Fatalf("a stream the real writer checkpointed was refused:\n%s", subjects(got))
	}
	for i := 1; i <= 7; i++ {
		if run := fmt.Sprintf("run-real-%02d", i); !strings.Contains(got, run) {
			t.Errorf("%s from a covered stream was not mailed:\n%s", run, subjects(got))
		}
	}
}

func TestAOneByteRewriteOfARealProducersStreamIsRefused(t *testing.T) {
	b, stream := realProducer(t, 7)
	raw, err := os.ReadFile(stream) // #nosec G304 -- temp path
	if err != nil {
		t.Fatal(err)
	}
	// One byte of the second line, well inside what the checkpoints cover:
	// run-real-02 becomes run-real-92, the same length, still valid JSON.
	at := strings.Index(string(raw), "run-real-02")
	if at < 0 {
		t.Fatal("the second line is not where the test expects it")
	}
	raw[at+len("run-real-")] = '9'
	if err := os.WriteFile(stream, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	b.poll(t0)
	got := b.out(t)
	if strings.Contains(got, "run-real-") {
		t.Fatalf("a line of a rewritten stream was mailed:\n%s", subjects(got))
	}
	if !strings.Contains(subjects(got), "wardryx.ndjson") || !strings.Contains(got, "no longer match") {
		t.Fatalf("the refusal must name the stream and say its bytes no longer match:\n%s", got)
	}
}

func TestATruncatedRealProducersStreamIsRefused(t *testing.T) {
	b, stream := realProducer(t, 7)
	raw, err := os.ReadFile(stream) // #nosec G304 -- temp path
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if err := os.WriteFile(stream, []byte(strings.Join(lines[:4], "")), 0o600); err != nil {
		t.Fatal(err)
	}
	b.poll(t0)
	got := b.out(t)
	if strings.Contains(got, "run-real-") {
		t.Fatalf("a line of a truncated stream was mailed:\n%s", subjects(got))
	}
	if !strings.Contains(subjects(got), "wardryx.ndjson") || !strings.Contains(got, "shorter than its newest checkpoint") {
		t.Fatalf("the refusal must name the stream and say it is shorter than its checkpoint:\n%s", got)
	}
}
