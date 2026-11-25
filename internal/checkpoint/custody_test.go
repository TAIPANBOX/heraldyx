package checkpoint_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/v2/event"

	"github.com/TAIPANBOX/heraldyx/internal/checkpoint"
	"github.com/TAIPANBOX/heraldyx/internal/keystatement"
	"github.com/TAIPANBOX/heraldyx/internal/keystatement/statementtest"
	"github.com/TAIPANBOX/heraldyx/internal/watch"
)

// The gate in the custody mode, against a real producer: agent-stack-go's
// writer signs the stream's checkpoints with short-lived keys, a stand-in
// custody serves each key's statement under a root this box pinned, and the
// keys are renewed partway, as a live producer's are.

var c0 = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// renewing is a producer's checkpoint signer whose key is replaced when the
// test says, as keyclient renews a delegated key.
type renewing struct {
	mu  sync.Mutex
	key *ecdsa.PrivateKey
	kid string
}

func (r *renewing) use(t *testing.T, k *ecdsa.PrivateKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.key, r.kid = k, statementtest.Kid(t, &k.PublicKey)
}

func (r *renewing) KeyID() string { r.mu.Lock(); defer r.mu.Unlock(); return r.kid }

func (r *renewing) Sign(in []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := sha256.Sum256(in)
	rr, ss, err := ecdsa.Sign(rand.Reader, r.key, d[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 64)
	rr.FillBytes(out[:32])
	ss.FillBytes(out[32:])
	return out, nil
}

type custodyBench struct {
	clock   *fakeClock
	root    *statementtest.Root
	custody *statementtest.Custody
	r       *keystatement.Resolver
	gate    *checkpoint.Gate
	signer  *renewing
	w       *event.CheckpointedWriter
	stream  string
}

func newCustodyBench(t *testing.T) *custodyBench {
	t.Helper()
	b := &custodyBench{clock: &fakeClock{t: c0}, root: statementtest.NewRoot(t), signer: &renewing{}}
	b.custody = statementtest.NewCustody(t, b.root)
	pin, _ := keystatement.ParsePin(b.root.Pin())
	bind, _ := keystatement.ParseStreamKeys("wardryx.ndjson|wardryx-checkpoint")
	b.r = keystatement.NewResolver(b.custody.Dir, pin, bind, b.clock.now, nil)
	dir := t.TempDir()
	ckpts := filepath.Join(dir, "checkpoints")
	events := filepath.Join(dir, "events")
	for _, d := range []string{ckpts, events} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	b.gate = checkpoint.NewGateFrom(ckpts, b.r)
	b.stream = filepath.Join(events, "wardryx.ndjson")
	b.renew(t)
	w, err := event.NewChainedWriterWithCheckpoints(b.stream, event.CheckpointConfig{Dir: ckpts, Signer: b.signer, Every: 2, Now: b.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	b.w = w
	t.Cleanup(func() { _ = w.Close() })
	return b
}

// renew gives the producer a new key, certified for six hours from now.
func (b *custodyBench) renew(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b.signer.use(t, k)
	now := b.clock.now()
	b.custody.Put(t, b.signer.KeyID(), b.root.Sign(t, statementtest.Claims{Name: "wardryx-checkpoint", Key: &k.PublicKey,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(6 * time.Hour)}))
	return b.signer.KeyID()
}

func (b *custodyBench) write(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := b.w.Write(event.Event{Schema: event.SchemaV10, TS: "2026-11-25T12:00:00.000Z", Source: "wardryx",
			Type: "policy_deny", AgentID: "agent://acme.example/one", Severity: "high"}); err != nil {
			t.Fatal(err)
		}
	}
}

// step is one turn of the resolver's own loop, a little later.
func (b *custodyBench) step() {
	b.clock.add(10 * time.Second)
	b.r.Step()
}

// settle is the poll loop asking, the resolver's loop answering, and the poll
// loop asking again.
func (b *custodyBench) settle() {
	b.gate.Covered(b.stream)
	b.step()
}

func TestAProducersRenewedKeysKeepItsStreamCoveredThroughCustody(t *testing.T) {
	b := newCustodyBench(t)
	b.write(t, 4)
	if ok, why := b.gate.Covered(b.stream); ok || why != watch.Undecided {
		t.Fatalf("before the key's file was read: %v %q, want undecided", ok, why)
	}
	b.step()
	if ok, why := b.gate.Covered(b.stream); !ok {
		t.Fatalf("once custody certified the key: %q", why)
	}
	// The producer renews its key, as it does at half its life, and goes on.
	b.renew(t)
	b.write(t, 4)
	if ok, why := b.gate.Covered(b.stream); !ok {
		t.Fatalf("right after the renewal, before custody was asked about the new key: %q", why)
	}
	b.step()
	if ok, why := b.gate.Covered(b.stream); !ok {
		t.Fatalf("after the renewal: %q", why)
	}
	// A pinned-key file holding the first key would refuse here: that is
	// what the custody mode is for.
}

func TestARevokedKeyUncoversItsStream(t *testing.T) {
	b := newCustodyBench(t)
	b.write(t, 4)
	b.settle()
	if ok, _ := b.gate.Covered(b.stream); !ok {
		t.Fatal("control: not covered")
	}
	b.custody.Revoke(t, b.signer.KeyID(), b.clock.now())
	b.clock.add(keystatement.RefreshEvery)
	b.r.Step()
	if ok, why := b.gate.Covered(b.stream); ok || why != checkpoint.ReasonKeyWithdrawn {
		t.Fatalf("after the key's revocation: %v %q", ok, why)
	}
}

func TestARootHalfCustodyStopsPublishingUncoversTheStream(t *testing.T) {
	b := newCustodyBench(t)
	b.write(t, 4)
	b.settle()
	if ok, why := b.gate.Covered(b.stream); !ok {
		t.Fatalf("control: %q", why)
	}
	// Custody revoked a half of the root: its published key set no longer
	// carries the pinned kids.
	b.custody.Root(t, statementtest.NewRoot(t).JWKS())
	b.clock.add(keystatement.RefreshEvery)
	b.r.Step()
	if ok, why := b.gate.Covered(b.stream); ok || why != checkpoint.ReasonKeyWithdrawn {
		t.Fatalf("under a root custody no longer publishes: %v %q", ok, why)
	}
}

func TestAKeyCertifiedForAnotherProducerDoesNotCover(t *testing.T) {
	b := newCustodyBench(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	b.signer.use(t, k)
	b.custody.Put(t, b.signer.KeyID(), b.root.Sign(t, statementtest.Claims{Name: "tokenfuse-checkpoint", Key: &k.PublicKey,
		NotBefore: c0.Add(-time.Minute), NotAfter: c0.Add(time.Hour)}))
	b.write(t, 4)
	b.settle()
	b.settle()
	if ok, why := b.gate.Covered(b.stream); ok || why != checkpoint.ReasonNoSigned {
		t.Fatalf("checkpoints signed by another producer's key: %v %q", ok, why)
	}
}

func TestAStreamUndecidedTooLongIsDecidedUncovered(t *testing.T) {
	b := newCustodyBench(t)
	b.gate.SetClock(b.clock.now)
	// The statements directory is gone: no key can be looked up.
	if err := os.RemoveAll(b.custody.Dir); err != nil {
		t.Fatal(err)
	}
	b.write(t, 4)
	if ok, why := b.gate.Covered(b.stream); ok || why != watch.Undecided {
		t.Fatalf("at first: %v %q", ok, why)
	}
	b.clock.add(checkpoint.MaxUndecided - time.Second)
	b.r.Step()
	if ok, why := b.gate.Covered(b.stream); ok || why != watch.Undecided {
		t.Fatalf("just inside the bound: %v %q", ok, why)
	}
	b.clock.add(2 * time.Second)
	if ok, why := b.gate.Covered(b.stream); ok || why != checkpoint.ReasonNoSigned {
		t.Fatalf("past it: %v %q, want decided uncovered", ok, why)
	}
}
