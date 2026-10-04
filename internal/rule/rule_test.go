package rule

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

var t0 = time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)

func ev(kind, severity, run string) event.Event {
	return event.Event{
		Schema:   event.SchemaV02,
		TS:       "2026-08-02T14:00:00Z",
		Source:   "tokenfuse",
		Type:     kind,
		AgentID:  "agent://acme.example/biller",
		RunID:    run,
		Severity: severity,
	}
}

// One condition that keeps tripping is one message, not two hundred.
func TestDedupHolds(t *testing.T) {
	cfg := DefaultConfig()
	st := NewState()
	sent := 0
	for i := range 200 {
		if Decide(cfg, st, ev("budget_exhausted", event.SeverityCritical, "run-1"), t0.Add(time.Duration(i)*time.Second)) == Notify {
			sent++
		}
	}
	if sent != 1 {
		t.Fatalf("want 1 message for 200 trips of one condition, got %d", sent)
	}
}

// The window ends, and the condition can speak again.
func TestDedupWindowExpires(t *testing.T) {
	cfg := DefaultConfig()
	st := NewState()
	e := ev("budget_exhausted", event.SeverityCritical, "run-1")
	if got := Decide(cfg, st, e, t0); got != Notify {
		t.Fatalf("first: %v", got)
	}
	if got := Decide(cfg, st, e, t0.Add(9*time.Minute)); got != Drop {
		t.Fatalf("inside the window: %v", got)
	}
	if got := Decide(cfg, st, e, t0.Add(11*time.Minute)); got != Notify {
		t.Fatalf("after the window: %v", got)
	}
}

// A different run is a different condition.
func TestDedupIsPerSubject(t *testing.T) {
	cfg := DefaultConfig()
	st := NewState()
	a := Decide(cfg, st, ev("budget_exhausted", event.SeverityCritical, "run-1"), t0)
	b := Decide(cfg, st, ev("budget_exhausted", event.SeverityCritical, "run-2"), t0)
	if a != Notify || b != Notify {
		t.Fatalf("two runs must both alert, got %v and %v", a, b)
	}
}

// The ceiling stops a broken fleet from turning this into a mail flood aimed
// at its own operator.
func TestCeilingHolds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxPerHour = 5
	st := NewState()
	notified, suppressed := 0, 0
	for i := range 1000 {
		// Every event distinct, so dedup never fires and only the ceiling can
		// hold the line.
		v := Decide(cfg, st, ev("policy_deny", event.SeverityHigh, fmt.Sprintf("run-%d", i)), t0.Add(time.Duration(i)*time.Millisecond))
		switch v {
		case Notify:
			notified++
		case Suppressed:
			suppressed++
		}
	}
	if notified != 5 {
		t.Fatalf("ceiling of 5 let through %d", notified)
	}
	if suppressed != 995 {
		t.Fatalf("want 995 suppressed, got %d", suppressed)
	}
	// And the operator is told once, not 995 times.
	if n, due := st.TakeSuppressionNotice(DefaultCadence(), t0.Add(time.Second)); !due || n.Count != 995 {
		t.Fatalf("want one notice carrying 995, got n=%d due=%v", n.Count, due)
	}
	if _, due := st.TakeSuppressionNotice(DefaultCadence(), t0.Add(2*time.Second)); due {
		t.Fatal("the suppression notice must not itself become a flood")
	}
}

// The ceiling is an hour wide, not forever.
func TestCeilingReleasesAfterAnHour(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxPerHour = 2
	st := NewState()
	for i := range 5 {
		Decide(cfg, st, ev("policy_deny", event.SeverityHigh, fmt.Sprintf("run-%d", i)), t0)
	}
	if got := Decide(cfg, st, ev("policy_deny", event.SeverityHigh, "run-late"), t0.Add(61*time.Minute)); got != Notify {
		t.Fatalf("after the hour rolls over: %v", got)
	}
}

// Below the floor is not silence: it is the daily summary.
func TestBelowTheFloorGoesToTheDigest(t *testing.T) {
	cfg := DefaultConfig() // floor: high
	st := NewState()
	if got := Decide(cfg, st, ev("tool_call", event.SeverityLow, "run-1"), t0); got != Digest {
		t.Fatalf("want digest, got %v", got)
	}
	if len(st.Digest) != 1 {
		t.Fatalf("digest did not record it: %v", st.Digest)
	}
}

// A severity this build has never heard of must neither page everyone nor
// vanish. A future producer inventing a level is a real possibility, and both
// failure modes are silent.
func TestUnknownSeverityGoesToTheDigestNotToTheOperator(t *testing.T) {
	cfg := DefaultConfig()
	st := NewState()
	if got := Decide(cfg, st, ev("weird", "catastrophic", "run-1"), t0); got != Digest {
		t.Fatalf("want digest for an unknown severity, got %v", got)
	}
	if Rank("catastrophic") != rankUnknown {
		t.Fatal("Rank must not guess")
	}
}

// Dedup is checked BEFORE the ceiling, so one noisy condition cannot eat the
// operator's whole hourly budget and crowd out the one different thing.
func TestOneNoisyConditionDoesNotEatTheCeiling(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxPerHour = 3
	st := NewState()
	for i := range 100 {
		Decide(cfg, st, ev("sustained_loop", event.SeverityHigh, "run-noisy"), t0.Add(time.Duration(i)*time.Second))
	}
	if got := Decide(cfg, st, ev("dlp_block", event.SeverityHigh, "run-different"), t0.Add(200*time.Second)); got != Notify {
		t.Fatalf("a different condition was crowded out: %v", got)
	}
}

// A clock that jumped backwards must not re-send everything.
func TestAFutureStampCountsAsRecent(t *testing.T) {
	st := NewState()
	st.NoteSent("k", t0.Add(time.Hour))
	if !st.SentWithin("k", 10*time.Minute, t0) {
		t.Fatal("a last-sent stamp in the future must be treated as recent, not ancient")
	}
}

func TestDigestIsBoundedAndOrdered(t *testing.T) {
	st := NewState()
	for i := range maxDigestKeys + 50 {
		st.NoteDigest(ev("t", event.SeverityLow, fmt.Sprintf("run-%d", i)), t0)
	}
	st.NoteDigest(ev("t", event.SeverityLow, "run-0"), t0)
	if len(st.Digest) > maxDigestKeys+1 {
		t.Fatalf("digest grew unbounded: %d keys", len(st.Digest))
	}
	entries := st.TakeDigest(t0)
	if len(entries) == 0 || entries[0].Count < 2 {
		t.Fatalf("want the most frequent condition first, got %+v", entries[:1])
	}
	if len(st.Digest) != 0 {
		t.Fatal("taking the digest must clear it")
	}
}

// A critical goes through the ceiling. The severity's whole meaning is "now",
// and the limit that keeps the mailbox usable is not a reason to sit on it.
// One message per condition: the dedup window still applies to it, and a
// different critical is a different condition.
func TestACriticalBypassesTheCeilingAndDedupStillHolds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxPerHour = 2
	st := NewState()
	for i := range 5 {
		Decide(cfg, st, ev("policy_deny", event.SeverityHigh, fmt.Sprintf("run-%d", i)), t0)
	}
	if got := st.SentInLastHour(t0); got != 2 {
		t.Fatalf("premise: the ceiling must be reached, %d sent", got)
	}
	crit := ev("budget_exhausted", event.SeverityCritical, "run-crit")
	if got := Decide(cfg, st, crit, t0.Add(time.Second)); got != Notify {
		t.Fatalf("a critical at the ceiling: want notify, got %v", got)
	}
	if got := Decide(cfg, st, crit, t0.Add(2*time.Second)); got != Drop {
		t.Fatalf("the same critical inside the dedup window: want drop, got %v", got)
	}
	other := ev("budget_exhausted", event.SeverityCritical, "run-other")
	if got := Decide(cfg, st, other, t0.Add(3*time.Second)); got != Notify {
		t.Fatalf("a different critical at the ceiling: want notify, got %v", got)
	}
	// And a high behind them is still held: the ceiling itself is unchanged.
	if got := Decide(cfg, st, ev("policy_deny", event.SeverityHigh, "run-late"), t0.Add(4*time.Second)); got != Suppressed {
		t.Fatalf("a high at the ceiling: want suppressed, got %v", got)
	}
	if st.SuppressedSince != 4 {
		t.Fatalf("want 4 held (three highs and the late one), the criticals not among them, got %d", st.SuppressedSince)
	}
}

// The summary's cadence, at its edges. A time bound, so held alerts are never
// silent for more than the interval; a burst bound, so a flood is reported
// sooner than that; and a floor under both, so the summary itself cannot
// become the flood it warns about.
func TestTheSummaryCadenceHasBothBoundsAndAFloor(t *testing.T) {
	c := DefaultCadence()
	if c.Every != 10*time.Minute || c.Burst != 50 || c.Floor != time.Minute {
		t.Fatalf("the cadence this test assumes has changed: %+v", c)
	}
	st := NewState()

	// Nothing held: nothing due.
	if _, due := st.TakeSuppressionNotice(c, t0); due {
		t.Fatal("a summary was due with nothing held")
	}

	// The first summary goes at once, and carries what it was given.
	st.NoteSuppressed(event.SeverityHigh, t0)
	n, due := st.TakeSuppressionNotice(c, t0)
	if !due || n.Count != 1 || n.Worst != event.SeverityHigh || !n.Since.Equal(t0) {
		t.Fatalf("first summary: due=%v %+v", due, n)
	}
	if st.SuppressedWorst != "" || st.SuppressedFirstAt != 0 || st.SuppressedSince != 0 {
		t.Fatalf("taking the summary must reset all three: %+v", st)
	}

	// Three held a minute later, the worst of them high, and the summary
	// waits for the interval: not at 9:59 after the previous one, at 10:00.
	at := t0.Add(time.Minute)
	for _, sev := range []string{event.SeverityMedium, event.SeverityHigh, event.SeverityMedium} {
		st.NoteSuppressed(sev, at)
	}
	if _, due := st.TakeSuppressionNotice(c, t0.Add(10*time.Minute-time.Second)); due {
		t.Fatal("a summary went a second before the interval had passed")
	}
	n, due = st.TakeSuppressionNotice(c, t0.Add(10*time.Minute))
	if !due || n.Count != 3 || n.Worst != event.SeverityHigh || !n.Since.Equal(at) {
		t.Fatalf("summary at the interval: due=%v %+v", due, n)
	}

	// Forty-nine held is not a burst; fifty is, once the floor has passed.
	at = t0.Add(11 * time.Minute)
	for range 49 {
		st.NoteSuppressed(event.SeverityHigh, at)
	}
	if _, due := st.TakeSuppressionNotice(c, t0.Add(12*time.Minute)); due {
		t.Fatal("49 held was treated as a burst")
	}
	st.NoteSuppressed(event.SeverityLow, at)
	n, due = st.TakeSuppressionNotice(c, t0.Add(12*time.Minute))
	if !due || n.Count != 50 || n.Worst != event.SeverityHigh {
		t.Fatalf("summary on a burst of 50: due=%v %+v", due, n)
	}

	// Fifty more, thirty seconds after that: the floor holds them until a
	// minute has passed since the previous summary.
	at = t0.Add(12*time.Minute + 30*time.Second)
	for range 50 {
		st.NoteSuppressed(event.SeverityHigh, at)
	}
	if _, due := st.TakeSuppressionNotice(c, at); due {
		t.Fatal("a burst went out inside the floor: the summary became the flood")
	}
	if n, due := st.TakeSuppressionNotice(c, t0.Add(13*time.Minute)); !due || n.Count != 50 {
		t.Fatalf("the floor never released the burst: due=%v %+v", due, n)
	}

	// A previous summary stamped in the future (a clock that moved) is
	// treated as recent, the same way SentWithin treats a future stamp: quiet
	// once is the recoverable way to be wrong about a clock.
	st.NoteSuppressed(event.SeverityHigh, t0.Add(14*time.Minute))
	st.SuppressNoticeAt = t0.Add(time.Hour).UnixMilli()
	if _, due := st.TakeSuppressionNotice(c, t0.Add(14*time.Minute)); due {
		t.Fatal("a summary stamped in the future must count as recent")
	}
}

// The worst severity is the worst, whatever order they were held in, and an
// unknown or empty word never wins over a known one.
func TestTheWorstHeldSeverityIsTheWorstInAnyOrder(t *testing.T) {
	st := NewState()
	for _, sev := range []string{event.SeverityHigh, event.SeverityLow, "", event.SeverityMedium, "CRITICAL "} {
		st.NoteSuppressed(sev, t0)
	}
	if st.SuppressedWorst != event.SeverityCritical {
		t.Fatalf("want critical as the worst of five, got %q", st.SuppressedWorst)
	}
	if st.SuppressedSince != 5 {
		t.Fatalf("every hold counts, whatever its word: got %d", st.SuppressedSince)
	}
}

// The word stored for a held severity is the canonical one for its rank, so
// what reaches a summary's subject line is chosen here and never spelled by a
// producer. The inverse of Rank for every rank Rank knows, and nothing for
// one it does not.
func TestTheWordForARankIsTheOneRankReads(t *testing.T) {
	for _, w := range []string{event.SeverityInfo, event.SeverityLow, event.SeverityMedium, event.SeverityHigh, event.SeverityCritical} {
		if got := word(Rank(w)); got != w {
			t.Errorf("word(Rank(%q)) = %q", w, got)
		}
		if got := word(Rank(" " + strings.ToUpper(w) + " ")); got != w {
			t.Errorf("the wire's spelling must not reach the word: got %q for %q", got, w)
		}
	}
	if got := word(rankUnknown); got != "" {
		t.Errorf("an unknown rank has no word, got %q", got)
	}
}

// DecideKey is Decide with the key and the severity named instead of read off
// an event, and nothing else: the same sequence through both lands in the same
// verdicts and the same state, so a notice raised under a key of its own is
// held to exactly the limits an event is.
func TestDecideKeyIsDecideWithTheKeyNamed(t *testing.T) {
	cfg := Config{MinRank: rankHigh, DedupWindow: 10 * time.Minute, MaxPerHour: 3}
	a, b := NewState(), NewState()
	steps := []struct {
		kind, severity, run string
		at                  time.Duration
	}{
		{"policy_deny", "high", "r1", 0},
		{"policy_deny", "high", "r1", time.Minute}, // dedup
		{"policy_deny", "high", "r2", 2 * time.Minute},
		{"budget_threshold", "medium", "r3", 3 * time.Minute}, // below the floor
		{"policy_deny", "high", "r4", 4 * time.Minute},
		{"policy_deny", "high", "r5", 5 * time.Minute},                // ceiling
		{"budget_exhausted", "critical", "r6", 6 * time.Minute},       // through it
		{"policy_deny", "weird", "r7", 7 * time.Minute},               // unknown severity
		{"policy_deny", "high", "r5", 8*time.Minute + 30*time.Second}, // dedup again
	}
	for i, st := range steps {
		e := ev(st.kind, st.severity, st.run)
		now := t0.Add(st.at)
		va := Decide(cfg, a, e, now)
		vb := DecideKey(cfg, b, Key(e), e.Severity, now)
		if va != vb {
			t.Fatalf("step %d (%s %s %s): Decide says %v, DecideKey says %v", i, st.kind, st.severity, st.run, va, vb)
		}
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("the two paths left different state:\n%+v\n%+v", a, b)
	}
}

// A notice under a key of its own is deduplicated, digested below the floor
// and held by the ceiling like an event: it is not a second way past them.
func TestANoticeKeyIsHeldToTheFloorTheDedupWindowAndTheCeiling(t *testing.T) {
	cfg := Config{MinRank: rankHigh, DedupWindow: 10 * time.Minute, MaxPerHour: 1}
	st := NewState()
	if v := DecideKey(cfg, st, "unknown_stream:a.ndjson", "medium", t0); v != Digest {
		t.Fatalf("below the floor: %v, want digest", v)
	}
	if st.Digest["unknown_stream:a.ndjson"] != 1 {
		t.Fatalf("a notice below the floor must land in the digest: %v", st.Digest)
	}
	if v := DecideKey(cfg, st, "foreign_source:t.ndjson:w", "high", t0); v != Notify {
		t.Fatalf("at the floor: %v, want notify", v)
	}
	if v := DecideKey(cfg, st, "foreign_source:t.ndjson:w", "high", t0.Add(time.Minute)); v != Drop {
		t.Fatalf("inside the dedup window: %v, want drop", v)
	}
	if v := DecideKey(cfg, st, "foreign_source:t.ndjson:e", "high", t0.Add(2*time.Minute)); v != Suppressed {
		t.Fatalf("past the ceiling: %v, want suppressed", v)
	}
}
