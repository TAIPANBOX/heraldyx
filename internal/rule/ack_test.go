package rule

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"
)

var ackT0 = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

func ackedCfg(key string, until time.Time) Config {
	c := DefaultConfig()
	c.Acks = map[string]Ack{key: {Key: key, At: ackT0.UnixMilli(), Until: until.UnixMilli(), By: "ops"}}
	return c
}

func TestAnAckHoldsItsKeyBelowCritical(t *testing.T) {
	st := NewState()
	cfg := ackedCfg("policy_deny:run-1", ackT0.Add(4*time.Hour))
	if v := DecideKey(cfg, st, "policy_deny:run-1", "high", ackT0.Add(time.Minute)); v != Held {
		t.Fatalf("want held, have %s", v)
	}
	if h := st.AckHeld["policy_deny:run-1"]; h.Count != 1 || h.Worst != "high" {
		t.Fatalf("the held alert was not counted: %+v", h)
	}
}

func TestAnAckNeverHoldsACritical(t *testing.T) {
	st := NewState()
	cfg := ackedCfg("budget_exhausted:run-9", ackT0.Add(4*time.Hour))
	if v := DecideKey(cfg, st, "budget_exhausted:run-9", "critical", ackT0.Add(time.Minute)); v != Notify {
		t.Fatalf("an ack held a critical: %s", v)
	}
}

func TestAnExpiredAckHoldsNothing(t *testing.T) {
	st := NewState()
	cfg := ackedCfg("policy_deny:run-1", ackT0.Add(time.Hour))
	if v := DecideKey(cfg, st, "policy_deny:run-1", "high", ackT0.Add(time.Hour)); v != Notify {
		t.Fatalf("an ack held after its end: %s", v)
	}
}

func TestAnAckHoldsOnlyItsOwnKey(t *testing.T) {
	st := NewState()
	cfg := ackedCfg("policy_deny:run-1", ackT0.Add(4*time.Hour))
	if v := DecideKey(cfg, st, "policy_deny:run-10", "high", ackT0.Add(time.Minute)); v != Notify {
		t.Fatalf("an ack for run-1 held run-10: %s", v)
	}
}

func TestAckHeldAlertsDoNotCountTowardTheCeiling(t *testing.T) {
	st := NewState()
	cfg := ackedCfg("policy_deny:run-1", ackT0.Add(4*time.Hour))
	for i := 0; i < 25; i++ {
		DecideKey(cfg, st, "policy_deny:run-1", "high", ackT0.Add(time.Duration(i)*time.Second))
	}
	if n := st.SentInLastHour(ackT0.Add(time.Minute)); n != 0 {
		t.Fatalf("held alerts spent the hourly budget: %d", n)
	}
}

func TestValidateAckRefusesWhatMustNotBeHonoured(t *testing.T) {
	ok := Ack{Key: "policy_deny:run-1", At: ackT0.UnixMilli(), Until: ackT0.Add(time.Hour).UnixMilli()}
	if err := ValidateAck(ok); err != nil {
		t.Fatalf("a good ack was refused: %v", err)
	}
	bad := map[string]Ack{
		"no end":         {Key: ok.Key, At: ok.At, Until: ok.At},
		"over 7 days":    {Key: ok.Key, At: ok.At, Until: ackT0.Add(MaxAck + time.Minute).UnixMilli()},
		"span overflows": {Key: ok.Key, At: ok.At, Until: math.MaxInt64},
		"empty key":      {Key: "", At: ok.At, Until: ok.Until},
		"no colon":       {Key: "policy_deny", At: ok.At, Until: ok.Until},
		"newline key":    {Key: "policy_deny:run-1\nBcc: x", At: ok.At, Until: ok.Until},
		"space in key":   {Key: "policy deny:run-1", At: ok.At, Until: ok.Until},
		"newline by":     {Key: ok.Key, At: ok.At, Until: ok.Until, By: "ops\nx"},
		"note too long":  {Key: ok.Key, At: ok.At, Until: ok.Until, Note: string(make([]byte, 201))},
	}
	for name, a := range bad {
		if err := ValidateAck(a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTakeAckReleasesReportsHeldCountsWhenTheAckEnds(t *testing.T) {
	st := NewState()
	cfg := ackedCfg("policy_deny:run-1", ackT0.Add(time.Hour))
	for i := 0; i < 3; i++ {
		DecideKey(cfg, st, "policy_deny:run-1", "high", ackT0.Add(time.Duration(i)*time.Minute))
	}
	if rel := st.TakeAckReleases(cfg.Acks, ackT0.Add(30*time.Minute)); len(rel) != 0 {
		t.Fatalf("released while the ack is in force: %+v", rel)
	}
	rel := st.TakeAckReleases(cfg.Acks, ackT0.Add(time.Hour))
	if len(rel) != 1 || rel[0].Key != "policy_deny:run-1" || rel[0].Count != 3 || rel[0].Worst != "high" || rel[0].First != ackT0.UnixMilli() {
		t.Fatalf("want one release of 3, worst high, first at t0; have %+v", rel)
	}
	if len(st.AckHeld) != 0 {
		t.Fatal("a released count was kept")
	}
}

func TestAckHeldCountsSurviveARestart(t *testing.T) {
	st := NewState()
	cfg := ackedCfg("policy_deny:run-1", ackT0.Add(time.Hour))
	DecideKey(cfg, st, "policy_deny:run-1", "high", ackT0)
	raw, _ := json.Marshal(st)
	back := NewState()
	if err := json.Unmarshal(raw, back); err != nil {
		t.Fatal(err)
	}
	if back.AckHeld["policy_deny:run-1"].Count != 1 {
		t.Fatalf("held counts lost across a restart: %s", raw)
	}
}

func TestIsCriticalIsOnlyTheTopOfTheLadder(t *testing.T) {
	for sev, want := range map[string]bool{"critical": true, "CRITICAL ": true, "high": false, "nonsense": false, "": false} {
		if got := IsCritical(sev); got != want {
			t.Errorf("IsCritical(%q) = %v, want %v", sev, got, want)
		}
	}
}

// Past the bound, a new condition is counted under one label rather than
// growing the map, and nothing is lost from the count.
func TestAHeldKeyPastTheBoundIsCountedUnderOneLabel(t *testing.T) {
	st := NewState()
	for i := 0; i < maxAckHeld; i++ {
		st.NoteHeldByAck(fmt.Sprintf("policy_deny:run-%d", i), "high", ackT0)
	}
	st.NoteHeldByAck("policy_deny:run-overflow", "medium", ackT0)
	st.NoteHeldByAck("policy_deny:run-overflow-2", "high", ackT0)
	if h := st.AckHeld["(more, not listed)"]; h.Count != 2 || h.Worst != "high" {
		t.Fatalf("the overflow was not counted under one label: %+v", h)
	}
	if len(st.AckHeld) != maxAckHeld+1 {
		t.Fatalf("the map grew past its bound: %d entries", len(st.AckHeld))
	}
	if st.PulseAckHeld != maxAckHeld+2 {
		t.Fatalf("the pulse count lost holds: %d", st.PulseAckHeld)
	}
}

// Two conditions that end together are reported in key order, not map order.
func TestReleasesComeOutInKeyOrder(t *testing.T) {
	st := NewState()
	cfg := ackedCfg("policy_deny:run-2", ackT0.Add(time.Hour))
	cfg.Acks["policy_deny:run-1"] = Ack{Key: "policy_deny:run-1", At: ackT0.UnixMilli(), Until: ackT0.Add(time.Hour).UnixMilli()}
	DecideKey(cfg, st, "policy_deny:run-2", "high", ackT0)
	DecideKey(cfg, st, "policy_deny:run-1", "high", ackT0)
	rel := st.TakeAckReleases(nil, ackT0.Add(2*time.Hour))
	if len(rel) != 2 || rel[0].Key != "policy_deny:run-1" || rel[1].Key != "policy_deny:run-2" {
		t.Fatalf("releases not in key order: %+v", rel)
	}
}
