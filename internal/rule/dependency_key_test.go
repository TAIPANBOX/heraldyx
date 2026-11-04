package rule

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/v2/event"
)

// depEv is a dependency_failed event for one run, shaped as TokenFuse writes
// it: data.dependency, data.stage, data.effect and a detail.
func depEv(run, dependency, effect string) event.Event {
	e := ev("dependency_failed", event.SeverityHigh, run)
	e.Data = map[string]any{
		"dependency": dependency,
		"stage":      "response",
		"effect":     effect,
		"detail":     "provider answered HTTP 503",
	}
	return e
}

func noCeiling() Config {
	c := DefaultConfig()
	c.MaxPerHour = 0
	return c
}

// A provider that is down fails every call, and every call is its own run.
// Keyed on the run an hour of outage was one mail per call; keyed on the
// dependency it is one.
func TestAStormOfProviderFailuresIsOneMessage(t *testing.T) {
	cfg, st, sent := noCeiling(), NewState(), 0
	for i := range 200 {
		e := depEv(fmt.Sprintf("run-%d", i), "provider", "call_failed")
		if Decide(cfg, st, e, t0.Add(time.Duration(i)*time.Second)) == Notify {
			sent++
		}
	}
	if sent != 1 {
		t.Fatalf("want 1 message for 200 failed calls of one provider, got %d", sent)
	}
}

func TestTheDependencyWindowEndsAndTheProviderCanSpeakAgain(t *testing.T) {
	cfg, st := noCeiling(), NewState()
	if got := Decide(cfg, st, depEv("run-1", "provider", "call_failed"), t0); got != Notify {
		t.Fatalf("first: %v", got)
	}
	if got := Decide(cfg, st, depEv("run-2", "provider", "call_failed"), t0.Add(9*time.Minute)); got != Drop {
		t.Fatalf("inside the window: %v", got)
	}
	if got := Decide(cfg, st, depEv("run-3", "provider", "call_failed"), t0.Add(11*time.Minute)); got != Notify {
		t.Fatalf("after the window: %v", got)
	}
}

func TestEachDependencyIsItsOwnCondition(t *testing.T) {
	cfg, st := noCeiling(), NewState()
	for i, dep := range []string{"provider", "policy_plane", "control_plane"} {
		e := depEv(fmt.Sprintf("run-%d", i), dep, "call_failed")
		if got := Decide(cfg, st, e, t0.Add(time.Duration(i)*time.Second)); got != Notify {
			t.Fatalf("%s was folded into another dependency: %v", dep, got)
		}
	}
}

// The three outcomes want opposite responses. A fail-open policy plane lets
// calls through with no policy on them, and that must not hide behind an
// earlier mail about calls that were refused.
func TestADifferentEffectOfTheSameDependencyIsNotHiddenBehindTheFirst(t *testing.T) {
	cfg, st := noCeiling(), NewState()
	if got := Decide(cfg, st, depEv("run-1", "policy_plane", "denied_unasked"), t0); got != Notify {
		t.Fatalf("first: %v", got)
	}
	if got := Decide(cfg, st, depEv("run-2", "policy_plane", "allowed_ungoverned"), t0.Add(time.Second)); got != Notify {
		t.Fatalf("calls that went through ungoverned were folded behind calls that were refused: %v", got)
	}
	if got := Decide(cfg, st, depEv("run-3", "policy_plane", "denied_unasked"), t0.Add(2*time.Second)); got != Drop {
		t.Fatalf("the same effect again: %v", got)
	}
}

// The field is read from a log this process does not own. Anything that is not
// one of the SPEC's own words, or is not there, keeps the key of its run, so
// an unexpected value can never fold anything into anything.
func TestADependencyFailureWithNoUsableDependencyKeepsItsRunKey(t *testing.T) {
	for name, data := range map[string]map[string]any{
		"no data":            nil,
		"empty data":         {},
		"empty dependency":   {"dependency": ""},
		"not a string":       {"dependency": 7},
		"unknown word":       {"dependency": "database"},
		"a newline":          {"dependency": "provider\nBcc: x"},
		"upper case":         {"dependency": "Provider"},
		"padded":             {"dependency": " provider"},
		"very long":          {"dependency": strings.Repeat("a", 10_000)},
		"a nested object":    {"dependency": map[string]any{"name": "provider"}},
		"the effect alone":   {"effect": "call_failed"},
		"a path-like effect": {"dependency": "", "effect": "../../x"},
	} {
		a := ev("dependency_failed", event.SeverityHigh, "run-a")
		a.Data = data
		if got, want := Key(a), "dependency_failed:run-a"; got != want {
			t.Errorf("%s: Key = %q, want the run key %q", name, got, want)
		}
		cfg, st := noCeiling(), NewState()
		b := ev("dependency_failed", event.SeverityHigh, "run-b")
		b.Data = data
		if Decide(cfg, st, a, t0) != Notify || Decide(cfg, st, b, t0.Add(time.Second)) != Notify {
			t.Errorf("%s: two runs were folded on a field that is not a dependency", name)
		}
	}
}

func TestAnUnknownEffectLeavesTheEffectOutOfTheKey(t *testing.T) {
	e := depEv("run-1", "provider", "something_new")
	if got, want := Key(e), "dependency_failed:dependency/provider"; got != want {
		t.Fatalf("Key = %q, want %q", got, want)
	}
}

// Only dependency_failed folds. Another type that carries a data.dependency of
// its own does not borrow the rule.
func TestOnlyDependencyFailedFoldsOnTheDependency(t *testing.T) {
	e := ev("policy_deny", event.SeverityHigh, "run-1")
	e.Data = map[string]any{"dependency": "provider", "effect": "call_failed"}
	if got, want := Key(e), "policy_deny:run-1"; got != want {
		t.Fatalf("Key = %q, want %q", got, want)
	}
}

// Events as TokenFuse wrote them before this key existed carry data, and ones
// from other producers may carry none; neither changes meaning.
func TestTheKeyOfEveryOtherEventIsStillTheTypeAndTheRun(t *testing.T) {
	if got := Key(ev("budget_exhausted", event.SeverityCritical, "run-9")); got != "budget_exhausted:run-9" {
		t.Fatalf("Key = %q", got)
	}
	noRun := ev("run_stalled", event.SeverityMedium, "")
	if got, want := Key(noRun), "run_stalled:agent://acme.example/biller"; got != want {
		t.Fatalf("Key = %q, want %q", got, want)
	}
}

// The mail prints this key for the operator to acknowledge. It has to be one an
// acknowledgement accepts, and acknowledging it has to hold the whole storm.
func TestTheDependencyKeyCanBeAcknowledgedAndHoldsTheStorm(t *testing.T) {
	key := Key(depEv("run-1", "provider", "call_failed"))
	a := Ack{Key: key, At: ackT0.UnixMilli(), Until: ackT0.Add(time.Hour).UnixMilli(), By: "ops"}
	if err := ValidateAck(a); err != nil {
		t.Fatalf("the key the mail prints is not one an acknowledgement accepts: %v", err)
	}
	cfg, st, held := noCeiling(), NewState(), 0
	cfg.Acks = map[string]Ack{key: a}
	for i := range 20 {
		e := depEv(fmt.Sprintf("run-%d", i), "provider", "call_failed")
		if Decide(cfg, st, e, ackT0.Add(time.Duration(i+1)*time.Second)) == Held {
			held++
		}
	}
	if held != 20 {
		t.Fatalf("an acknowledgement of the provider key held %d of 20 failures", held)
	}
}

// The link in the mail points at the money plane's incident id, which is keyed
// on the run. Folding the dedup key must not move it.
func TestTheIncidentIDIsStillTheTypeAndTheRun(t *testing.T) {
	e := depEv("run-7", "provider", "call_failed")
	if got, want := IncidentID(e), "dependency_failed:run-7"; got != want {
		t.Fatalf("IncidentID = %q, want %q", got, want)
	}
}
