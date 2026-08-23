package render

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// costcrew emits these, read from its own code on 2026-08-23, which is what a
// catalog entry is required to be written against.
var costcrewTypes = []string{
	"anomaly_triaged", "anomaly_explained", "anomaly_accepted", "anomaly_dismissed",
	"agent_hired", "agent_removed", "agent_transferred", "agent_rebriefed",
	"agent_state_changed", "budgets_set", "forecast_frozen", "sprint_planned",
	"explainer_published",
}

// Every type that console emits has a sentence, so none of them mails as
// "raised an event this build does not have a description for".
//
// Found by running this binary over that console's real stream on 2026-08-23:
// every message it wrote carried the fallback, which names no fault at all.
func TestEveryCostcrewTypeIsDescribed(t *testing.T) {
	for _, kind := range costcrewTypes {
		if _, ok := catalog[kind]; !ok {
			t.Errorf("%s has no catalog entry, so it mails as the fallback", kind)
		}
	}
}

// None of those sentences claims that console stopped anything.
//
// It has no gateway, no interceptor and no refusal path. A mail telling an
// operator at three in the morning that traffic was blocked, when nothing was,
// sends them to look for a control that does not exist.
func TestNoCostcrewSentenceClaimsAnEnforcement(t *testing.T) {
	// A word of enforcement, unless something negates it within the few words
	// before. "nothing was stopped" and "no spend was capped" are the point
	// being made; "the run was stopped" is the falsehood. A plain substring
	// match flagged the first two, which is why this reads the negation
	// instead of keeping a list of blessed phrases that grows with the prose.
	claim := regexp.MustCompile(
		`(?i)(refused|blocked|stopped|denied|402|cut off|capped)`)
	negated := regexp.MustCompile(
		`(?i)(nothing|never|not|no|does not|is not|was not)\b[^.]{0,24}$`)

	for _, kind := range costcrewTypes {
		p := catalog[kind]
		joined := p.what + " " + p.did + " " + p.next
		for _, loc := range claim.FindAllStringIndex(joined, -1) {
			before := joined[:loc[0]]
			if negated.MatchString(before) {
				continue
			}
			t.Errorf("%s claims %q with nothing negating it: that console "+
				"enforces nothing, and this sends the operator to look for a "+
				"control it does not have\n  ...%s",
				kind, joined[loc[0]:loc[1]],
				joined[max(0, loc[0]-60):min(len(joined), loc[1]+20)])
		}
	}
}

// budget_threshold means two different things depending on who raised it.
//
// The gateway that raises it WILL refuse with a 402. costcrew maps its own
// guard_passed onto the same shared word and refuses nothing, stamping
// `enforced: false` to say so. The catalog is keyed on type alone, so without
// this the mail promised an operator a refusal that will never come.
func TestBudgetThresholdDoesNotPromiseARefusalNobodyWillMake(t *testing.T) {
	cfg := Config{Box: "test"}
	now := time.Now().UTC()

	unenforced := event.Event{
		Schema: "taipanbox.dev/agent-event/v0.2", Type: "budget_threshold",
		Source: "costcrew", AgentID: "agent://costcrew.local/an-analyst",
		Severity: "high", Data: map[string]any{"enforced": false},
	}
	m := Event(cfg, unenforced, now, "", nil)
	if strings.Contains(m.Body, "402") || strings.Contains(m.Body, "are refused") {
		t.Errorf("a budget_threshold marked enforced:false still promises a "+
			"refusal:\n%s", m.Body)
	}
	if !strings.Contains(strings.ToLower(m.Body), "does not enforce") {
		t.Errorf("it does not say that the plane which raised it enforces "+
			"nothing:\n%s", m.Body)
	}

	// And the gateway's own, which must be untouched: that one does refuse.
	enforced := unenforced
	enforced.Source = "tokenfuse"
	enforced.Data = map[string]any{}
	if m := Event(cfg, enforced, now, "", nil); !strings.Contains(m.Body, "402") {
		t.Errorf("a budget_threshold with no enforced field lost the refusal "+
			"it is true about:\n%s", m.Body)
	}
}

// The field is read and never rendered: what may reach a mailbox is governed
// by dataAllowlist, and this must not have widened it.
func TestEnforcedIsReadAndNotMailed(t *testing.T) {
	if dataAllowlist["enforced"] {
		t.Error("enforced was added to the allowlist; it is a control for the " +
			"phrasing, not a fact for the operator to read")
	}
	m := Event(Config{Box: "test"}, event.Event{
		Schema: "taipanbox.dev/agent-event/v0.2", Type: "budget_threshold",
		Source: "costcrew", AgentID: "agent://costcrew.local/an-analyst",
		Severity: "high", Data: map[string]any{"enforced": false},
	}, time.Now().UTC(), "", nil)
	if strings.Contains(m.Body, "enforced") {
		t.Errorf("the raw field reached the mail:\n%s", m.Body)
	}
}
