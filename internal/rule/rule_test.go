package rule

import (
	"fmt"
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
	if n, due := st.TakeSuppressionNotice(time.Hour, t0.Add(time.Second)); !due || n != 995 {
		t.Fatalf("want one notice carrying 995, got n=%d due=%v", n, due)
	}
	if _, due := st.TakeSuppressionNotice(time.Hour, t0.Add(2*time.Second)); due {
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
