package checkpoint_test

import (
	"crypto/ecdsa"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/heraldyx/internal/checkpoint"
	"github.com/TAIPANBOX/heraldyx/internal/checkpoint/checkpointtest"
)

// The gate's behaviour, tested against real signed v1 records built by
// checkpointtest (which is written from SPEC 6.5.1, not from the verifier).
// Each test writes a stream and its checkpoint file under a temp directory and
// asks the gate whether the stream is covered.

const at = "2026-10-18T09:00:10.000Z"

// fixture is one producer: a key, a stream directory and a checkpoints directory.
type fixture struct {
	t        *testing.T
	key      *checkpointtest.Key
	ckpts    string
	stream   string // full path of wardryx.ndjson
	streamNm string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	key, err := checkpointtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	f := &fixture{t: t, key: key, ckpts: filepath.Join(root, "checkpoints"), streamNm: "wardryx.ndjson"}
	bus := filepath.Join(root, "bus")
	for _, d := range []string{bus, f.ckpts} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.stream = filepath.Join(bus, f.streamNm)
	return f
}

// writeStream replaces the stream with the given lines and returns its bytes.
func (f *fixture) writeStream(lines ...string) []byte {
	f.t.Helper()
	body := []byte(strings.Join(lines, ""))
	if err := os.WriteFile(f.stream, body, 0o600); err != nil {
		f.t.Fatal(err)
	}
	return body
}

// signed returns one signed record over the first count lines of stream.
func (f *fixture) signed(stream []byte, count int, prev string) string {
	f.t.Helper()
	m, err := checkpointtest.Members(stream, f.streamNm, "checkpoint", count, prev, at)
	if err != nil {
		f.t.Fatal(err)
	}
	line, err := checkpointtest.Sign(f.key, m)
	if err != nil {
		f.t.Fatal(err)
	}
	return line
}

// checkpointFile writes the checkpoint file of the stream.
func (f *fixture) checkpointFile(records ...string) {
	f.t.Helper()
	if err := checkpointtest.File(filepath.Join(f.ckpts, checkpointtest.CheckpointName(f.streamNm)), records...); err != nil {
		f.t.Fatal(err)
	}
}

// gate is a gate that trusts exactly this fixture's key.
func (f *fixture) gate() *checkpoint.Gate {
	f.t.Helper()
	return checkpoint.NewGate(f.ckpts, map[string]*ecdsa.PublicKey{f.key.Kid: &f.key.Priv.PublicKey})
}

// rewrite changes one byte's worth of the first line: the run id of event 1.
// The line still parses and is still canonical JSON, so only the bytes differ.
func rewrite(body []byte) []byte {
	return []byte(strings.Replace(string(body), `run-1"`, `run-9"`, 1))
}

// threeLines is a stream of three events, the usual fixture.
func threeLines() []string {
	return []string{
		checkpointtest.Event("wardryx", "policy_allow", 1),
		checkpointtest.Event("wardryx", "policy_deny", 2),
		checkpointtest.Event("wardryx", "policy_allow", 3),
	}
}

// checked asserts the verdict and returns the reason, so a failure names both.
func checked(t *testing.T, g *checkpoint.Gate, stream string, want bool) string {
	t.Helper()
	got, why := g.Covered(stream)
	if got != want {
		t.Fatalf("Covered = %v (%s), want %v", got, why, want)
	}
	return why
}

func TestAValidSignedCheckpointCoversItsStream(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	f.checkpointFile(f.signed(body, 3, checkpointtest.Genesis))

	why := checked(t, f.gate(), f.stream, true)
	if why != checkpoint.ReasonCovered {
		t.Fatalf("a covered stream must say so, got %q", why)
	}
}

func TestNoCheckpointFileIsNotCovered(t *testing.T) {
	f := newFixture(t)
	f.writeStream(threeLines()...)
	why := checked(t, f.gate(), f.stream, false)
	if why != checkpoint.ReasonNoFile {
		t.Fatalf("a missing checkpoint file must say so, got %q", why)
	}
}

func TestACheckpointFromAnUnpinnedKeyDoesNotCover(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	f.checkpointFile(f.signed(body, 3, checkpointtest.Genesis))

	// A gate that pins some other key trusts nothing in this file.
	other, err := checkpointtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	g := checkpoint.NewGate(f.ckpts, map[string]*ecdsa.PublicKey{other.Kid: &other.Priv.PublicKey})
	checked(t, g, f.stream, false)
}

func TestAForgedSignatureDoesNotCover(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	good := f.signed(body, 3, checkpointtest.Genesis)
	// Change the first character of the signature, keeping the record well formed.
	i := strings.Index(good, `"sig":"`) + len(`"sig":"`)
	repl := byte('A')
	if good[i] == 'A' {
		repl = 'B'
	}
	forged := good[:i] + string(repl) + good[i+1:]
	f.checkpointFile(forged)
	checked(t, f.gate(), f.stream, false)
}

func TestARewrittenStreamIsNotCoveredByItsCheckpoint(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	f.checkpointFile(f.signed(body, 3, checkpointtest.Genesis))

	if err := os.WriteFile(f.stream, rewrite(body), 0o600); err != nil {
		t.Fatal(err)
	}
	why := checked(t, f.gate(), f.stream, false)
	if why != checkpoint.ReasonRewritten {
		t.Fatalf("a rewritten stream must say its bytes changed, got %q", why)
	}
}

func TestACheckpointForAnotherStreamDoesNotCover(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	// A record that names tokenfuse.ndjson, placed where wardryx's belongs.
	m, err := checkpointtest.Members(body, "tokenfuse.ndjson", "checkpoint", 3, checkpointtest.Genesis, at)
	if err != nil {
		t.Fatal(err)
	}
	line, err := checkpointtest.Sign(f.key, m)
	if err != nil {
		t.Fatal(err)
	}
	f.checkpointFile(line)
	why := checked(t, f.gate(), f.stream, false)
	if why != checkpoint.ReasonOtherStrm {
		t.Fatalf("a checkpoint naming another stream must say so, got %q", why)
	}
}

func TestAStreamShorterThanItsCheckpointIsNotCovered(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	f.checkpointFile(f.signed(body, 3, checkpointtest.Genesis))

	// The stream loses its last line after the checkpoint was taken.
	short := strings.Join(threeLines()[:2], "")
	if err := os.WriteFile(f.stream, []byte(short), 0o600); err != nil {
		t.Fatal(err)
	}
	why := checked(t, f.gate(), f.stream, false)
	if why != checkpoint.ReasonShorter {
		t.Fatalf("a stream shorter than its checkpoint must say so, got %q", why)
	}
}

func TestATornFinalCheckpointLineIsIgnored(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	good := f.signed(body, 3, checkpointtest.Genesis)
	// A writer is part way through its next record: no LF yet.
	path := filepath.Join(f.ckpts, checkpointtest.CheckpointName(f.streamNm))
	if err := os.WriteFile(path, []byte(good+"\n"+`{"v":1,"kind":"chec`), 0o600); err != nil {
		t.Fatal(err)
	}
	checked(t, f.gate(), f.stream, true)
}

func TestAnAlienCheckpointLineRefusesTheFile(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	good := f.signed(body, 3, checkpointtest.Genesis)
	// An extra member the format does not list: v1 refuses the line.
	alien := strings.Replace(good, `"v":1`, `"v":1,"note":"hello"`, 1)
	f.checkpointFile(alien)
	why := checked(t, f.gate(), f.stream, false)
	if why != checkpoint.ReasonRefused {
		t.Fatalf("a line outside format v1 must refuse the file, got %q", why)
	}
}

func TestACheckpointChainBreakRefusesTheFile(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	first := f.signed(body, 1, checkpointtest.Genesis)
	// The second record names a previous record that is not the first one.
	second := f.signed(body, 3, checkpointtest.Link("not the first record"))
	f.checkpointFile(first, second)
	checked(t, f.gate(), f.stream, false)
}

// The prefix matches, so only the head can refuse: a head that is the chain
// hash of another line is not what the record signed for line count.
func TestAHeadThatIsNotTheChainHashDoesNotCover(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	m, err := checkpointtest.Members(body, f.streamNm, "checkpoint", 3, checkpointtest.Genesis, at)
	if err != nil {
		t.Fatal(err)
	}
	other, err := checkpointtest.Members(body, f.streamNm, "checkpoint", 1, checkpointtest.Genesis, at)
	if err != nil {
		t.Fatal(err)
	}
	m["head"] = other["head"]
	line, err := checkpointtest.Sign(f.key, m)
	if err != nil {
		t.Fatal(err)
	}
	f.checkpointFile(line)
	why := checked(t, f.gate(), f.stream, false)
	if why != checkpoint.ReasonHead {
		t.Fatalf("a head that is not the chain hash of line 3 must say so, got %q", why)
	}
}

func TestAnUnsignedRecordNeverCovers(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	m, err := checkpointtest.Members(body, f.streamNm, "checkpoint", 3, checkpointtest.Genesis, at)
	if err != nil {
		t.Fatal(err)
	}
	line, err := checkpointtest.Unsigned(m)
	if err != nil {
		t.Fatal(err)
	}
	f.checkpointFile(line)
	checked(t, f.gate(), f.stream, false)
}

func TestCoverageIsRecomputedOnlyWhenTheCheckpointChanges(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	first := f.signed(body, 3, checkpointtest.Genesis)
	f.checkpointFile(first)
	g := f.gate()
	checked(t, g, f.stream, true)

	// The stream changes under an unchanged checkpoint: the verdict is reused,
	// because the stream is hashed only when a new checkpoint arrives. The
	// witness re-checks those bytes later (invariant 32 of CLAUDE.md).
	if err := os.WriteFile(f.stream, rewrite(body), 0o600); err != nil {
		t.Fatal(err)
	}
	checked(t, g, f.stream, true)

	// A new checkpoint over the ORIGINAL bytes is now checked against the
	// rewritten stream, and refused, so the verdict moves.
	second := f.signed(body, 3, checkpointtest.Link(first))
	f.checkpointFile(first, second)
	checked(t, g, f.stream, false)
}

func TestLoadKeysReadsAPinnedKey(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.ckpts, "keys.json")
	if err := os.WriteFile(path, []byte(`{"`+f.key.Kid+`":"`+f.key.Public()+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := checkpoint.LoadKeys(path)
	if err != nil {
		t.Fatalf("a valid keys file must load: %v", err)
	}
	if _, ok := keys[f.key.Kid]; !ok || len(keys) != 1 {
		t.Fatalf("want exactly the pinned kid, got %d key(s)", len(keys))
	}
}

func TestAGateWithNoUsableKeysRefusesStartup(t *testing.T) {
	f := newFixture(t)
	empty := filepath.Join(f.ckpts, "empty.json")
	if err := os.WriteFile(empty, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if keys, err := checkpoint.LoadKeys(empty); err == nil {
		t.Fatalf("an empty key set must fail, so the gate cannot start open; got %d key(s)", len(keys))
	}

	bad := filepath.Join(f.ckpts, "bad.json")
	point := base64.RawURLEncoding.EncodeToString([]byte("not a point"))
	if err := os.WriteFile(bad, []byte(`{"kid":"`+point+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := checkpoint.LoadKeys(bad); err == nil {
		t.Fatal("a key that is not a P-256 point must fail")
	}
	if _, err := checkpoint.LoadKeys(filepath.Join(f.ckpts, "missing.json")); err == nil {
		t.Fatal("a missing keys file must fail")
	}
}

// runOne writes a checkpoint file holding one record and returns the reason
// the stream is not covered, failing if it is covered.
func runOne(t *testing.T, f *fixture, line string) string {
	t.Helper()
	f.checkpointFile(line)
	covered, why := f.gate().Covered(f.stream)
	if covered {
		t.Fatalf("a record SPEC 6.5.1 forbids must not cover the stream (reason said %q)", why)
	}
	return why
}

// Each variant breaks one rule of SPEC 6.5.1 section "Records" or "Signing",
// and each must be refused as the format refuses it, not read past.
func TestTheFormatRefusesWhatSpecSixPointFiveOneForbids(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	good := f.signed(body, 3, checkpointtest.Genesis)

	variant := func(mut func(m map[string]any)) string {
		t.Helper()
		m, err := checkpointtest.Members(body, f.streamNm, "checkpoint", 3, checkpointtest.Genesis, at)
		if err != nil {
			t.Fatal(err)
		}
		mut(m)
		line, err := checkpointtest.Sign(f.key, m)
		if err != nil {
			t.Fatal(err)
		}
		return line
	}
	cases := map[string]string{
		"algorithm other than ES256": strings.Replace(good, `"alg":"ES256"`, `"alg":"ES384"`, 1),
		"version other than 1":       variant(func(m map[string]any) { m["v"] = 2 }),
		"kind outside the three":     variant(func(m map[string]any) { m["kind"] = "bogus" }),
		"a required member missing":  variant(func(m map[string]any) { delete(m, "prefix") }),
		"a digest that is not hex":   variant(func(m map[string]any) { m["head"] = "sha256:zz" }),
		"a time in another form":     variant(func(m map[string]any) { m["at"] = "2026-10-18" }),
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			if why := runOne(t, f, line); why != checkpoint.ReasonRefused {
				t.Fatalf("want the file refused (%q), got %q", checkpoint.ReasonRefused, why)
			}
		})
	}

	// An unsigned record that also carries a kid is neither: refused.
	unsigned := variant(func(m map[string]any) { m["unsigned_reason"] = "key_expired"; m["kid"] = f.key.Kid })
	t.Run("unsigned that also names a kid", func(t *testing.T) {
		if why := runOne(t, f, unsigned); why != checkpoint.ReasonRefused {
			t.Fatalf("want the file refused, got %q", why)
		}
	})

	// A signature of the wrong length is not a valid signature: the record
	// is chained and not counted, so nothing covers the stream.
	short := strings.Replace(good, `"sig":"`, `"sig":"AAAA`, 1)
	t.Run("a signature of the wrong length", func(t *testing.T) {
		if why := runOne(t, f, short); why != checkpoint.ReasonNoSigned {
			t.Fatalf("want no signed record (%q), got %q", checkpoint.ReasonNoSigned, why)
		}
	})
}

func TestAStreamThatCannotBeReadIsNotCovered(t *testing.T) {
	f := newFixture(t)
	body := f.writeStream(threeLines()...)
	f.checkpointFile(f.signed(body, 3, checkpointtest.Genesis))
	if err := os.Remove(f.stream); err != nil {
		t.Fatal(err)
	}
	if why := checked(t, f.gate(), f.stream, false); why != checkpoint.ReasonUnreadable {
		t.Fatalf("a stream that cannot be read must say so, got %q", why)
	}
}

func TestAKeyOffTheCurveFailsToLoad(t *testing.T) {
	f := newFixture(t)
	point := make([]byte, 65)
	point[0] = 4
	for i := 1; i < 65; i++ {
		point[i] = 1
	}
	path := filepath.Join(f.ckpts, "off.json")
	enc := base64.RawURLEncoding.EncodeToString(point)
	if err := os.WriteFile(path, []byte(`{"kid":"`+enc+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := checkpoint.LoadKeys(path); err == nil {
		t.Fatal("a 65-byte point that is not on P-256 must fail to load")
	}
}
