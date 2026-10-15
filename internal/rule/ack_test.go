package rule

import (
	"encoding/json"
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
