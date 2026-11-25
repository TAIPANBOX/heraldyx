package keystatement_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/heraldyx/internal/keystatement"
	"github.com/TAIPANBOX/heraldyx/internal/keystatement/statementtest"
)

var t0 = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type rig struct {
	root    *statementtest.Root
	custody *statementtest.Custody
	clock   *clock
	r       *keystatement.Resolver
	logs    *strings.Builder
	logMu   sync.Mutex
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := statementtest.NewRoot(t)
	g := &rig{root: root, custody: statementtest.NewCustody(t, root), clock: &clock{t: t0}, logs: &strings.Builder{}}
	g.r = g.resolver(t, root.Pin(), "tokenfuse.ndjson|tokenfuse-checkpoint,wardryx.ndjson|wardryx-checkpoint")
	return g
}

func (g *rig) resolver(t *testing.T, pinSpec, bindSpec string) *keystatement.Resolver {
	t.Helper()
	pin, err := keystatement.ParsePin(pinSpec)
	if err != nil {
		t.Fatal(err)
	}
	bind, err := keystatement.ParseStreamKeys(bindSpec)
	if err != nil {
		t.Fatal(err)
	}
	return keystatement.NewResolver(g.custody.Dir, pin, bind, g.clock.now, func(f string, a ...any) {
		g.logMu.Lock()
		defer g.logMu.Unlock()
		g.logs.WriteString(f + "\n")
	})
}

// certify mints a P-256 key and publishes a statement for it under name.
func (g *rig) certify(t *testing.T, name string, life time.Duration) (string, *ecdsa.PrivateKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kid := statementtest.Kid(t, &k.PublicKey)
	g.custody.Put(t, kid, g.root.Sign(t, statementtest.Claims{Name: name, Key: &k.PublicKey, NotBefore: t0, NotAfter: t0.Add(life)}))
	return kid, k
}

// step is one turn of the resolver's own loop, a little later.
func (g *rig) step() {
	g.clock.add(10 * time.Second)
	g.r.Step()
}

// ask asks, lets the resolver's loop take one step, and asks again.
func (g *rig) ask(stream, kid string, at time.Time) keystatement.Verdict {
	g.r.Key(stream, kid, at)
	g.step()
	_, v := g.r.Key(stream, kid, at)
	return v
}

func TestACertifiedKeyIsFoundOnceItsFileIsRead(t *testing.T) {
	g := newRig(t)
	kid, k := g.certify(t, "tokenfuse-checkpoint", 6*time.Hour)
	if _, v := g.r.Key("tokenfuse.ndjson", kid, t0.Add(time.Minute)); v != keystatement.Wait {
		t.Fatalf("before its file was read: %v, want Wait", v)
	}
	g.step()
	pub, v := g.r.Key("tokenfuse.ndjson", kid, t0.Add(time.Minute))
	if v != keystatement.Found || !pub.Equal(&k.PublicKey) {
		t.Fatalf("after its file was read: %v", v)
	}
}

func TestAKeyCertifiedForAnotherNameDoesNotCoverThisStream(t *testing.T) {
	g := newRig(t)
	kid, _ := g.certify(t, "wardryx-checkpoint", 6*time.Hour)
	if v := g.ask("tokenfuse.ndjson", kid, t0.Add(time.Minute)); v != keystatement.NotCertified {
		t.Fatalf("another producer's valid key on this stream: %v", v)
	}
	if _, v := g.r.Key("wardryx.ndjson", kid, t0.Add(time.Minute)); v != keystatement.Found {
		t.Fatalf("control: its own stream: %v", v)
	}
	if _, v := g.r.Key("typryx.ndjson", kid, t0.Add(time.Minute)); v != keystatement.NotCertified {
		t.Fatalf("a stream bound to no name: %v", v)
	}
	if _, v := g.r.Key("wardryx.ndjson", "../../etc/passwd-xxxxxxxxxxxxxxxxxxxxxxxxxx", t0); v != keystatement.NotCertified {
		t.Fatalf("a kid that is a path: %v", v)
	}
}

func TestARecordOutsideItsKeysLifeIsNotCertified(t *testing.T) {
	g := newRig(t)
	kid, _ := g.certify(t, "tokenfuse-checkpoint", 6*time.Hour)
	g.ask("tokenfuse.ndjson", kid, t0)
	for name, c := range map[string]struct {
		at   time.Time
		want keystatement.Verdict
	}{
		"at not_before":         {t0, keystatement.Found},
		"a second before":       {t0.Add(-time.Second), keystatement.NotCertified},
		"the last second":       {t0.Add(6*time.Hour - time.Second), keystatement.Found},
		"at not_after":          {t0.Add(6 * time.Hour), keystatement.NotCertified},
		"a day after not_after": {t0.Add(30 * time.Hour), keystatement.NotCertified},
	} {
		if _, v := g.r.Still("tokenfuse.ndjson", kid, c.at); v != c.want {
			t.Errorf("%s: %v, want %v", name, v, c.want)
		}
	}
	g.clock.add(10 * time.Hour)
	g.r.Step()
	if _, v := g.r.Still("tokenfuse.ndjson", kid, t0.Add(time.Hour)); v != keystatement.Found {
		t.Fatalf("a record taken in its key's life, the key ended since: %v", v)
	}
}

func TestARecordIsTakenOnlyWhileItsKeyIsAliveAndNotDatedAhead(t *testing.T) {
	g := newRig(t)
	kid, _ := g.certify(t, "tokenfuse-checkpoint", 6*time.Hour)
	if v := g.ask("tokenfuse.ndjson", kid, t0); v != keystatement.Found {
		t.Fatalf("control: %v", v)
	}
	now := g.clock.now()
	if _, v := g.r.Key("tokenfuse.ndjson", kid, now.Add(keystatement.Skew-time.Second)); v != keystatement.Found {
		t.Fatalf("a record dated just inside the skew: %v", v)
	}
	if _, v := g.r.Key("tokenfuse.ndjson", kid, now.Add(keystatement.Skew+time.Second)); v != keystatement.NotCertified {
		t.Fatalf("a record dated ahead of this box's clock: %v", v)
	}
	g.clock.add(6*time.Hour + keystatement.Skew - 20*time.Second)
	g.r.Step()
	if _, v := g.r.Key("tokenfuse.ndjson", kid, t0.Add(time.Hour)); v != keystatement.Found {
		t.Fatalf("read just inside the skew after not_after: %v", v)
	}
	g.clock.add(time.Minute)
	g.r.Step()
	if _, v := g.r.Key("tokenfuse.ndjson", kid, t0.Add(time.Hour)); v != keystatement.NotCertified {
		t.Fatalf("a record its ended key dated into its life, read first now: %v", v)
	}
	if _, v := g.r.Still("tokenfuse.ndjson", kid, t0.Add(time.Hour)); v != keystatement.Found {
		t.Fatalf("the same record, taken earlier: %v", v)
	}
}

func TestARevocationUncoversOnTheNextReadAndStays(t *testing.T) {
	g := newRig(t)
	kid, _ := g.certify(t, "tokenfuse-checkpoint", 6*time.Hour)
	if v := g.ask("tokenfuse.ndjson", kid, t0.Add(time.Minute)); v != keystatement.Found {
		t.Fatalf("control: %v", v)
	}
	g.custody.Revoke(t, kid, g.clock.now().Add(30*time.Second))
	g.clock.add(keystatement.RefreshEvery)
	g.r.Step()
	if _, v := g.r.Key("tokenfuse.ndjson", kid, t0.Add(time.Minute)); v != keystatement.NotCertified {
		t.Fatalf("after its revocation: %v", v)
	}
	if _, v := g.r.Key("tokenfuse.ndjson", kid, t0); v != keystatement.NotCertified {
		t.Fatalf("a backdated record: %v", v)
	}
	g.custody.Revoke(t, kid, time.Time{})
	g.clock.add(keystatement.RefreshEvery)
	g.r.Step()
	if _, v := g.r.Key("tokenfuse.ndjson", kid, t0.Add(time.Minute)); v != keystatement.NotCertified {
		t.Fatalf("a revocation taken back: %v", v)
	}
}

func TestARevocationNotYetInForceStillCovers(t *testing.T) {
	g := newRig(t)
	kid, _ := g.certify(t, "tokenfuse-checkpoint", 6*time.Hour)
	g.custody.Revoke(t, kid, t0.Add(time.Hour))
	if v := g.ask("tokenfuse.ndjson", kid, t0.Add(time.Minute)); v != keystatement.Found {
		t.Fatalf("a revocation set for later, read now: %v", v)
	}
	g.clock.add(time.Hour)
	if _, v := g.r.Key("tokenfuse.ndjson", kid, t0.Add(time.Minute)); v != keystatement.NotCertified {
		t.Fatalf("once its time came: %v", v)
	}
}

func TestAKidWithNoFileWaitsAWhileAndThenIsNotCertified(t *testing.T) {
	g := newRig(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	kid := statementtest.Kid(t, &k.PublicKey)
	if v := g.ask("tokenfuse.ndjson", kid, t0); v != keystatement.Wait {
		t.Fatalf("a kid custody has not published yet: %v, want Wait", v)
	}
	g.custody.Put(t, kid, g.root.Sign(t, statementtest.Claims{Name: "tokenfuse-checkpoint", Key: &k.PublicKey, NotBefore: t0, NotAfter: t0.Add(time.Hour)}))
	g.step()
	if _, v := g.r.Key("tokenfuse.ndjson", kid, t0); v != keystatement.Found {
		t.Fatalf("published a moment later: %v", v)
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	okid := statementtest.Kid(t, &other.PublicKey)
	g.ask("tokenfuse.ndjson", okid, t0)
	g.clock.add(keystatement.MissingAfter)
	g.r.Step()
	if _, v := g.r.Key("tokenfuse.ndjson", okid, t0); v != keystatement.NotCertified {
		t.Fatalf("a kid still without a file after %s: %v", keystatement.MissingAfter, v)
	}
}

func TestARootHalfCustodyNoLongerPublishesVerifiesNothing(t *testing.T) {
	g := newRig(t)
	kid, _ := g.certify(t, "tokenfuse-checkpoint", 6*time.Hour)
	if v := g.ask("tokenfuse.ndjson", kid, t0); v != keystatement.Found {
		t.Fatalf("control: %v", v)
	}
	other := statementtest.NewRoot(t)
	g.custody.Root(t, other.JWKS())
	g.clock.add(keystatement.RefreshEvery)
	g.r.Step()
	if _, v := g.r.Still("tokenfuse.ndjson", kid, t0); v != keystatement.NotCertified {
		t.Fatalf("a statement under a root custody no longer publishes: %v", v)
	}
	if !strings.Contains(g.logs.String(), "does not carry the pinned kids") {
		t.Fatalf("the missing pin is not said: %q", g.logs.String())
	}
}

func TestAStatementUnderAnotherRootIsNotCertified(t *testing.T) {
	g := newRig(t)
	other := statementtest.NewRoot(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	kid := statementtest.Kid(t, &k.PublicKey)
	g.custody.Put(t, kid, other.Sign(t, statementtest.Claims{Name: "tokenfuse-checkpoint", Key: &k.PublicKey, NotBefore: t0, NotAfter: t0.Add(time.Hour)}))
	if v := g.ask("tokenfuse.ndjson", kid, t0); v != keystatement.NotCertified {
		t.Fatalf("a statement under another root: %v", v)
	}
	if !strings.Contains(g.logs.String(), "is not trusted") {
		t.Fatalf("the refusal is not in the log: %q", g.logs.String())
	}
}

func TestOnlyRegularFilesOfCustodysShapeAreRead(t *testing.T) {
	g := newRig(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	kid := statementtest.Kid(t, &k.PublicKey)
	good := g.root.Sign(t, statementtest.Claims{Name: "tokenfuse-checkpoint", Key: &k.PublicKey, NotBefore: t0, NotAfter: t0.Add(time.Hour)})
	elsewhere := filepath.Join(t.TempDir(), "x.json")
	_ = os.WriteFile(elsewhere, []byte(`{"statement":`+string(good)+`,"revoked_at":0}`), 0o644)
	if err := os.Symlink(elsewhere, filepath.Join(g.custody.Dir, kid+".json")); err != nil {
		t.Fatal(err)
	}
	if v := g.ask("tokenfuse.ndjson", kid, t0); v == keystatement.Found {
		t.Fatal("a statement read through a link")
	}
	_ = os.Remove(filepath.Join(g.custody.Dir, kid+".json"))
	g.custody.Raw(t, kid+".json", []byte(`{"statement":`+string(good)+`,"revoked_at":0,"extra":1}`))
	g.clock.add(keystatement.RefreshEvery)
	g.r.Step()
	if _, v := g.r.Key("tokenfuse.ndjson", kid, t0); v != keystatement.NotCertified {
		t.Fatalf("a file with a member custody never writes: %v", v)
	}
	g.custody.Raw(t, kid+".json", []byte(`{"statement":`+string(good)+`,"revoked_at":0}`+strings.Repeat(" ", 1<<20)))
	g.clock.add(keystatement.RefreshEvery)
	g.r.Step()
	if _, v := g.r.Key("tokenfuse.ndjson", kid, t0); v != keystatement.NotCertified {
		t.Fatalf("a file over its size cap: %v", v)
	}
}

func TestAFileNamingKidAfterKidCannotFillTheResolver(t *testing.T) {
	g := newRig(t)
	for i := 0; i < keystatement.MaxKids; i++ {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if _, v := g.r.Key("tokenfuse.ndjson", statementtest.Kid(t, &k.PublicKey), t0); v != keystatement.Wait {
			t.Fatalf("kid %d: %v", i, v)
		}
	}
	// A real key of another stream, asked about while the resolver is full,
	// waits rather than being refused, and is found once there is room.
	kid, _ := g.certify(t, "wardryx-checkpoint", 6*time.Hour)
	if _, v := g.r.Key("wardryx.ndjson", kid, t0); v != keystatement.Wait {
		t.Fatalf("a kid past the bound: %v, want Wait", v)
	}
	g.step()
	g.clock.add(keystatement.MissingAfter + keystatement.RefreshEvery)
	g.r.Step()
	if _, v := g.r.Key("wardryx.ndjson", kid, t0); v != keystatement.Wait {
		t.Fatalf("once the unknown kids are forgotten: %v, want Wait until read", v)
	}
	g.step()
	if _, v := g.r.Key("wardryx.ndjson", kid, t0); v != keystatement.Found {
		t.Fatalf("read after room was made: %v", v)
	}
}

func TestTheStatementsDirectoryAndBindingsAreReadStrictly(t *testing.T) {
	base := t.TempDir()
	open := filepath.Join(base, "open")
	_ = os.Mkdir(open, 0o755)
	_ = os.Chmod(open, 0o777)
	link := filepath.Join(base, "link")
	_ = os.Symlink(base, link)
	file := filepath.Join(base, "file")
	_ = os.WriteFile(file, nil, 0o644)
	for name, d := range map[string]string{
		"relative": "statements", "not clean": base + "/../x", "a link": link, "a file": file,
		"writable by others": open, "not there": filepath.Join(base, "absent"),
	} {
		if err := keystatement.CheckDir(d); err == nil {
			t.Errorf("statements directory %s: accepted", name)
		}
	}
	if os.Geteuid() != 0 {
		own := filepath.Join(base, "own")
		_ = os.Mkdir(own, 0o755)
		if err := keystatement.CheckDir(own); err == nil {
			t.Error("a directory this process owns was accepted: custody writes it, this process reads it")
		}
	}
	for _, bad := range []string{"", "tokenfuse.ndjson", "tokenfuse|tokenfuse-checkpoint", "tokenfuse.ndjson|Tokenfuse", "tokenfuse.ndjson|keyryx.delegation-root",
		"../x.ndjson|a", "a.ndjson|b,a.ndjson|c"} {
		if _, err := keystatement.ParseStreamKeys(bad); err == nil {
			t.Errorf("bindings %q accepted", bad)
		}
	}
	if b, err := keystatement.ParseStreamKeys("a.ndjson|x\nb.ndjson|y"); err != nil || b["a.ndjson"] != "x" || b["b.ndjson"] != "y" {
		t.Errorf("two bindings on two lines: %v %v", b, err)
	}
}

func TestAStatementWhosePostQuantumHalfIsClassicalIsNotCertified(t *testing.T) {
	g := newRig(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	kid := statementtest.Kid(t, &k.PublicKey)
	c := statementtest.Claims{Name: "tokenfuse-checkpoint", Key: &k.PublicKey, NotBefore: t0, NotAfter: t0.Add(time.Hour)}
	g.custody.Put(t, kid, g.root.SignDowngraded(t, c))
	if v := g.ask("tokenfuse.ndjson", kid, t0); v != keystatement.NotCertified {
		t.Fatalf("an ML-DSA-65 half that is an ES384 signature by the classical root key: %v", v)
	}
	g.custody.Put(t, kid, g.root.Sign(t, c))
	g.clock.add(keystatement.RefreshEvery)
	g.r.Step()
	if _, v := g.r.Key("tokenfuse.ndjson", kid, t0); v != keystatement.Found {
		t.Fatalf("control: the same claims, both halves real: %v", v)
	}
}

func TestAnAnswerNotConfirmedLatelyTurnsIntoAWait(t *testing.T) {
	g := newRig(t)
	kid, _ := g.certify(t, "tokenfuse-checkpoint", 72*time.Hour)
	if v := g.ask("tokenfuse.ndjson", kid, t0); v != keystatement.Found {
		t.Fatalf("control: %v", v)
	}
	// The directory stops reading: nothing is confirmed any more.
	if err := os.Rename(g.custody.Dir, g.custody.Dir+".gone"); err != nil {
		t.Fatal(err)
	}
	g.clock.add(keystatement.StaleAfter - time.Second)
	g.r.Step()
	if _, v := g.r.Still("tokenfuse.ndjson", kid, t0); v != keystatement.Found {
		t.Fatalf("inside the time an answer is trusted: %v", v)
	}
	g.clock.add(2 * time.Second)
	g.r.Step()
	if _, v := g.r.Still("tokenfuse.ndjson", kid, t0); v != keystatement.Wait {
		t.Fatalf("past it: %v, want Wait", v)
	}
	_ = os.Rename(g.custody.Dir+".gone", g.custody.Dir)
	g.clock.add(keystatement.RefreshEvery)
	g.r.Step()
	if _, v := g.r.Still("tokenfuse.ndjson", kid, t0); v != keystatement.Found {
		t.Fatalf("the directory back: %v", v)
	}
	// A root key set that stops reading does the same.
	g.custody.Raw(t, keystatement.RootFile, []byte("not json"))
	g.clock.add(keystatement.RefreshEvery)
	g.r.Step()
	g.clock.add(keystatement.StaleAfter)
	g.r.Step()
	if _, v := g.r.Still("tokenfuse.ndjson", kid, t0); v != keystatement.Wait {
		t.Fatalf("a root key set that does not read for a while: %v, want Wait", v)
	}
}

func TestAFileThatDoesNotOpenNowIsReadAgainNotRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a file whatever its mode")
	}
	g := newRig(t)
	kid, _ := g.certify(t, "tokenfuse-checkpoint", 6*time.Hour)
	if v := g.ask("tokenfuse.ndjson", kid, t0); v != keystatement.Found {
		t.Fatalf("control: %v", v)
	}
	p := filepath.Join(g.custody.Dir, kid+".json")
	_ = os.Chmod(p, 0)
	g.clock.add(keystatement.RefreshEvery)
	g.r.Step()
	if _, v := g.r.Still("tokenfuse.ndjson", kid, t0); v != keystatement.Found {
		t.Fatalf("a verified key whose file did not open once: %v", v)
	}
	_ = os.Chmod(p, 0o644)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	okid := statementtest.Kid(t, &other.PublicKey)
	g.custody.Put(t, okid, g.root.Sign(t, statementtest.Claims{Name: "tokenfuse-checkpoint", Key: &other.PublicKey, NotBefore: t0, NotAfter: t0.Add(6 * time.Hour)}))
	_ = os.Chmod(filepath.Join(g.custody.Dir, okid+".json"), 0)
	if v := g.ask("tokenfuse.ndjson", okid, g.clock.now()); v != keystatement.Wait {
		t.Fatalf("a new key whose file does not open now: %v, want Wait", v)
	}
	_ = os.Chmod(filepath.Join(g.custody.Dir, okid+".json"), 0o644)
	g.step()
	if _, v := g.r.Key("tokenfuse.ndjson", okid, g.clock.now()); v != keystatement.Found {
		t.Fatalf("once it opens: %v", v)
	}
}

func TestAKeyNobodyAsksAboutIsForgottenAndMakesRoom(t *testing.T) {
	g := newRig(t)
	var kids []string
	for i := 0; i < keystatement.MaxKids; i++ {
		kid, _ := g.certify(t, "tokenfuse-checkpoint", 72*time.Hour)
		kids = append(kids, kid)
		g.r.Key("tokenfuse.ndjson", kid, t0)
	}
	g.step()
	if _, v := g.r.Key("tokenfuse.ndjson", kids[0], t0); v != keystatement.Found {
		t.Fatalf("control: %v", v)
	}
	kid, _ := g.certify(t, "tokenfuse-checkpoint", 72*time.Hour)
	if _, v := g.r.Key("tokenfuse.ndjson", kid, g.clock.now()); v != keystatement.Wait {
		t.Fatalf("a new key with every slot taken by verified keys: %v, want Wait", v)
	}
	if !strings.Contains(g.logs.String(), "the most this box keeps") {
		t.Fatalf("a full resolver is not said: %q", g.logs.String())
	}
	// An hour later nobody has asked about the old keys but the first.
	for i := 0; i < 59; i++ {
		g.clock.add(time.Minute)
		g.r.Step()
		g.r.Still("tokenfuse.ndjson", kids[0], t0)
	}
	g.clock.add(2 * time.Minute)
	if _, v := g.r.Key("tokenfuse.ndjson", kid, g.clock.now()); v != keystatement.Wait {
		t.Fatalf("after the old keys were forgotten: %v, want Wait until read", v)
	}
	g.step()
	if _, v := g.r.Key("tokenfuse.ndjson", kid, g.clock.now()); v != keystatement.Found {
		t.Fatalf("the new key once read: %v", v)
	}
	if _, v := g.r.Still("tokenfuse.ndjson", kids[0], t0); v != keystatement.Found {
		t.Fatalf("the key still asked about was forgotten: %v", v)
	}
}

func TestARootKeySetNotReadLatelyTurnsAnAnswerIntoAWait(t *testing.T) {
	g := newRig(t)
	g.r.Step() // the root key set, read at t0
	kid, _ := g.certify(t, "tokenfuse-checkpoint", 72*time.Hour)
	g.clock.add(30 * time.Second)
	if v := g.ask("tokenfuse.ndjson", kid, t0); v != keystatement.Found {
		t.Fatalf("control: %v", v) // the kid read at t0+40s
	}
	g.custody.Raw(t, keystatement.RootFile, []byte("not json"))
	g.clock.add(t0.Add(time.Minute).Sub(g.clock.now()))
	g.r.Step() // the root key set does not read
	g.clock.add(t0.Add(keystatement.StaleAfter + 5*time.Second).Sub(g.clock.now()))
	g.r.Step()
	// The kid's own answer is younger than StaleAfter; the root under which it
	// was verified is not.
	if _, v := g.r.Still("tokenfuse.ndjson", kid, t0); v != keystatement.Wait {
		t.Fatalf("a root key set last read over %v ago: %v, want Wait", keystatement.StaleAfter, v)
	}
}
