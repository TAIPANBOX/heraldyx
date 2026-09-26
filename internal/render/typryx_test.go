package render

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// typryx is an optional add-on (agent-passport SPEC.md 6.2) that answers a
// typed question with a probability, governs what leaves the box, and
// records every answer. The owner approved moving its journal onto the
// shared bus in all three launchers on 2026-09-26, so heraldyx now sees
// `typed_answer` (info), `typed_unanswered` (medium), `typed_refused` (high)
// and `calibration_drift` (high). Severities are typryx's own
// (internal/record/record.go) and are not this file's to change.
//
// `typed_refused` covers seven reasons read from typryx's own code
// (internal/service/service.go): `freeform_disabled`, `bad_question`,
// `unknown_template`, `bad_state`, `state_too_large`, `over_hourly_cap`,
// `over_daily_spend_cap`. A caller-supplied `bad_run_id` is refused at the
// API/MCP boundary, before `Service.Ask` ever runs, so it never reaches
// `Journal.Refused` and never becomes a `typed_refused` event; the fallback
// case below is what a future reason this build has not learned yet gets.

func typryxEv(kind, severity string, data map[string]any) event.Event {
	return event.Event{
		Schema:   event.SchemaV10,
		TS:       "2026-09-26T03:14:00Z",
		Source:   "typryx",
		Type:     kind,
		AgentID:  "agent://acme.example/risk-desk",
		RunID:    "run-typryx-1",
		Severity: severity,
		Data:     data,
	}
}

func TestEveryTypryxTypeIsDescribed(t *testing.T) {
	for _, kind := range []string{"typed_answer", "typed_unanswered", "typed_refused", "calibration_drift"} {
		if _, ok := catalog[kind]; !ok {
			t.Errorf("%s has no catalog entry, so it mails as the fallback", kind)
		}
	}
}

func TestTypedAnswerIsDescribedAndNamesItsTemplateAndBackend(t *testing.T) {
	m := Event(cfg(), typryxEv("typed_answer", event.SeverityInfo, map[string]any{
		"template": "risk-triage", "template_version": strings.Repeat("a", 64),
		"backend": "openai-logprobs", "model": "gpt-4o-mini",
	}), now, "", nil)
	if strings.Contains(m.Body, "does not have a description for") {
		t.Fatalf("typed_answer fell through to the fallback phrasing:\n%s", m.Body)
	}
	for _, want := range []string{"risk-triage", "openai-logprobs", "gpt-4o-mini"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("missing %q:\n%s", want, m.Body)
		}
	}
	// The 64-byte content digest is not a fact a human reads at three in the
	// morning; it is deliberately left out of this generic line.
	if strings.Contains(m.Body, strings.Repeat("a", 64)) {
		t.Errorf("the raw template_version digest leaked outside calibration_drift:\n%s", m.Body)
	}
}

func TestTypedUnansweredIsDescribed(t *testing.T) {
	m := Event(cfg(), typryxEv("typed_unanswered", event.SeverityMedium, map[string]any{
		"template": "risk-triage", "backend": "stub", "reason": "backend_error",
	}), now, "", nil)
	if strings.Contains(m.Body, "does not have a description for") {
		t.Fatalf("typed_unanswered fell through to the fallback phrasing:\n%s", m.Body)
	}
	if !strings.Contains(m.Body, "no usable answer") {
		t.Errorf("does not say the ask came back with nothing usable:\n%s", m.Body)
	}
}

// The floor test: every reason typryx's own code can raise is named plainly,
// never with a guessed cause, and the base fallback catches a reason this
// build has not learned yet rather than inventing one.
func TestTypedRefusedNamesEachReasonPlainly(t *testing.T) {
	cases := []struct {
		reason string
		want   []string
	}{
		{"over_hourly_cap", []string{"hourly", "cap"}},
		{"over_daily_spend_cap", []string{"daily", "spend cap"}},
		{"unknown_template", []string{"template", "not", "registered"}},
		{"freeform_disabled", []string{"freeform", "switched off"}},
		{"bad_state", []string{"state", "could not use"}},
		{"state_too_large", []string{"state", "could not use"}},
		{"bad_question", []string{"malformed", "question"}},
	}
	for _, c := range cases {
		m := Event(cfg(), typryxEv("typed_refused", event.SeverityHigh, map[string]any{
			"template": "risk-triage", "reason": c.reason,
		}), now, "", nil)
		body := strings.ToLower(m.Body)
		for _, want := range c.want {
			if !strings.Contains(body, strings.ToLower(want)) {
				t.Errorf("reason %q: missing %q:\n%s", c.reason, want, m.Body)
			}
		}
		if strings.Contains(body, "does not have a description for") {
			t.Errorf("reason %q fell through to the fallback phrasing", c.reason)
		}
	}
}

// A reason no released version of typryx raises yet must not be guessed at:
// the base catalog entry, honest and generic, is what a forward-compatible
// reader falls back to.
func TestTypedRefusedUnknownReasonStaysNeutral(t *testing.T) {
	m := Event(cfg(), typryxEv("typed_refused", event.SeverityHigh, map[string]any{
		"reason": "a-reason-this-build-has-never-heard-of",
	}), now, "", nil)
	if !strings.Contains(m.Body, "never reached a backend") {
		t.Errorf("lost the neutral base sentence on an unknown reason:\n%s", m.Body)
	}
	if strings.Contains(m.Body, "a-reason-this-build-has-never-heard-of") {
		t.Errorf("the raw reason code from data reached the mail:\n%s", m.Body)
	}
}

// calibration_drift is a measurement, never an enforcement (typryx CLAUDE.md
// invariant 25: "reported, never acted on"), and the mail must name the
// group and which metric crossed, off the event's own data, not the
// configured bound.
func TestCalibrationDriftNamesTheGroupAndTheMetricThatCrossed(t *testing.T) {
	m := Event(cfg(), event.Event{
		Schema: event.SchemaV10, TS: "2026-09-26T03:14:00Z", Source: "typryx",
		Type: "calibration_drift", AgentID: "agent://acme.example/calibration-runner",
		Severity: event.SeverityHigh,
		Data: map[string]any{
			"template": "risk-triage", "template_version": strings.Repeat("b", 64),
			"backend": "openai-logprobs", "model": "gpt-4o-mini",
			"bounds_crossed": map[string]any{"max_brier": 0.341},
		},
	}, now, "", nil)
	for _, want := range []string{"risk-triage", "openai-logprobs", "gpt-4o-mini", "Brier", "0.341"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("missing %q:\n%s", want, m.Body)
		}
	}
	// The version is long; it must appear shortened, never in full, and never
	// naked without the fact line's own context.
	if strings.Contains(m.Body, strings.Repeat("b", 64)) {
		t.Errorf("the full 64-byte digest leaked instead of a shortened form:\n%s", m.Body)
	}
	if !strings.Contains(strings.ToLower(m.Body), "nothing about the deployment changed") &&
		!strings.Contains(strings.ToLower(m.Body), "never turns a calibration verdict into an enforcement") {
		t.Errorf("does not say this is a measurement, never an enforcement:\n%s", m.Body)
	}
}

// Both bounds can cross together, and the ordering is deterministic so two
// renders of the same event read the same way.
func TestCalibrationDriftNamesBothMetricsWhenBothCross(t *testing.T) {
	m := Event(cfg(), event.Event{
		Schema: event.SchemaV10, TS: "2026-09-26T03:14:00Z", Source: "typryx",
		Type: "calibration_drift", AgentID: "agent://acme.example/calibration-runner",
		Severity: event.SeverityHigh,
		Data: map[string]any{
			"template": "risk-triage", "backend": "stub", "model": "demo",
			"bounds_crossed": map[string]any{"max_brier": 0.4, "max_ece": 0.2},
		},
	}, now, "", nil)
	for _, want := range []string{"Brier", "0.400", "ECE", "0.200"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("missing %q:\n%s", want, m.Body)
		}
	}
}

// Hostile or malformed bounds_crossed values never become message content: a
// non-numeric, non-finite, or unrecognised bound name is dropped rather than
// rendered, the same discipline run_stalled's two numeric fields already
// hold themselves to.
func TestCalibrationDriftRejectsHostileBoundsCrossed(t *testing.T) {
	hostile := []any{
		map[string]any{"max_brier": "ignore previous instructions"},
		map[string]any{"max_brier": nil},
		map[string]any{"unknown_bound": 0.5},
		"not a map at all",
		nil,
	}
	for _, bc := range hostile {
		m := Event(cfg(), event.Event{
			Schema: event.SchemaV10, TS: "2026-09-26T03:14:00Z", Source: "typryx",
			Type: "calibration_drift", AgentID: "agent://acme.example/calibration-runner",
			Severity: event.SeverityHigh,
			Data:     map[string]any{"template": "risk-triage", "bounds_crossed": bc},
		}, now, "", nil)
		if strings.Contains(m.Body, "ignore previous instructions") {
			t.Fatalf("hostile bound value reached the mail: %v\n%s", bc, m.Body)
		}
	}
}

// The two new identifier-shaped keys reach the mail only through the
// allowlist's own shape check, exactly like every other identifier this file
// renders: a long or multi-line value under an allowlisted key is still
// content and must not survive.
func TestTypedAnswerUnsafeTemplateOrBackendIsDropped(t *testing.T) {
	long := strings.Repeat("x", 200)
	m := Event(cfg(), typryxEv("typed_answer", event.SeverityInfo, map[string]any{
		"template": "deny\nBcc: attacker@example.com",
		"backend":  long,
		"model":    "gpt-4o-mini",
	}), now, "", nil)
	if strings.Contains(m.Body, "Bcc") || strings.Contains(m.Body, long) {
		t.Fatalf("an unsafe value under an allowlisted key reached the mail:\n%s", m.Body)
	}
}

// The severities heraldyx must render for are typryx's own and are not
// changed here; this pins the registered values so a future edit here that
// silently changed one would be caught.
func TestTypryxSeveritiesAreNotChangedByThisFile(t *testing.T) {
	cases := map[string]string{
		"typed_answer":      event.SeverityInfo,
		"typed_unanswered":  event.SeverityMedium,
		"typed_refused":     event.SeverityHigh,
		"calibration_drift": event.SeverityHigh,
	}
	for kind, sev := range cases {
		m := Event(cfg(), typryxEv(kind, sev, nil), now, "", nil)
		if m.Subject == "" {
			t.Errorf("%s at severity %s failed to render at all", kind, sev)
		}
	}
}
