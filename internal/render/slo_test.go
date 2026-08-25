package render

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/heraldyx/internal/rule"
)

// `slo_burn` is the quality plane's error budget arriving as mail, and it is
// the entry in this catalog where the honest sentence is the uncomfortable one.
// Every other type here reports something that HAPPENED to the agent: a call
// refused, a budget cut off, a hold placed. This one reports a MEASUREMENT.
// Nothing was refused, nothing was demoted, nothing was held. verdryx counted
// runs, compared a ratio to an objective and wrote the answer down.
//
// That is not a gap waiting to be filled. Read against the estate on
// 2026-08-26: wardryx's decision point is a pure function of policy set and
// request, held there by `scripts/decision-path-purity.sh`, so it cannot read a
// budget even if somebody wanted it to; and no autonomy tier exists anywhere in
// this stack to be lowered. A mail implying the agent had been restrained would
// be the 2026-08-03 defect again, where four entries described a consequence
// the producing plane does not have.
//
// The tests below therefore split into two halves. One half checks that the
// mail says which objective and which of the two triggers, because that is the
// value an operator gets. The other half checks that it claims no consequence,
// because that is the value they must not be given falsely.

// sloMail renders one slo_burn mail off the shared fixture in render_test.go,
// with the two things this type differs on.
//
// The source is verdryx rather than the fixture's tokenfuse, and there is no
// run id. Neither is decoration. The ratio is computed over every eligible run
// in the window and grouped on the subject, so there is no single run for the
// event to be about, and `rule.Subject` therefore falls through to the agent id.
// A fixture that left `run-42` in place would render a mail addressed to one
// run about a number computed over hundreds of them.
func sloMail(data map[string]any) Message {
	e := ev("slo_burn", data)
	e.Source = "verdryx"
	e.RunID = ""
	return Event(cfg(), e, now, "", nil)
}

// sloData is the contract's payload with the two fields a case varies passed
// in. Written out in full rather than trimmed to the keys under test, because
// the mail is rendered from the whole map: a case carrying only `sli` and
// `trigger` would not notice the day one of the other nine began reaching a
// mailbox. The numbers are the shape a JSON decoder hands this package, which
// is float64 for every one of them including the count.
func sloData(sli, trigger string) map[string]any {
	return map[string]any{
		"sli":              sli,
		"target":           0.95,
		"observed":         0.9123,
		"ci_low":           0.8811,
		"ci_high":          0.9372,
		"events":           float64(412),
		"window":           "28d",
		"budget_remaining": -0.746,
		"burn_rate":        4.2,
		"trigger":          trigger,
		"identity_field":   "agent_id",
	}
}

// The floor: it is described at all. Without an entry an operator meets their
// first error budget alert as "raised an event this build does not have a
// description for", which names no fault and sends nobody anywhere.
func TestSLOBurnIsDescribed(t *testing.T) {
	m := sloMail(sloData("task_success", "exhausted"))
	if strings.Contains(m.Body, "does not have a description for") {
		t.Fatalf("slo_burn fell through to the fallback phrasing:\n%s", m.Body)
	}
	if _, ok := catalog["slo_burn"]; !ok {
		t.Fatal("slo_burn has no catalog entry")
	}
}

// THE one that matters, and the one this catalog has been wrong about before.
//
// The `did` line answers "what has already been done about this", and for this
// type the answer is nothing at all. An operator told the agent was restrained
// closes the mail and does not do the thing they would otherwise have done,
// which is the exact shape of the four entries corrected on 2026-08-03.
//
// Asserted against the catalog entry rather than only against the body, and
// that is deliberate: the fallback's own `did` is "Nothing automatic.", so a
// test that only searched the body for a denial would pass on an unwritten
// entry and prove nothing at all.
func TestSLOBurnClaimsNoConsequenceItDoesNotHave(t *testing.T) {
	p, ok := catalog["slo_burn"]
	if !ok {
		t.Fatal("slo_burn has no catalog entry")
	}

	// The sentences that would send an operator away believing this was handled.
	// Each one is a real phrase from elsewhere in this catalog, where it is
	// true, and none of them is true here.
	for _, wrong := range []string{
		"was refused",
		"were refused",
		"are being refused",
		"was blocked",
		"was denied",
		"was killed",
		"was demoted",
		"autonomy",
		"the run is stopped",
	} {
		if strings.Contains(strings.ToLower(p.what+" "+p.did+" "+p.next), wrong) {
			t.Errorf("slo_burn claims %q, and nothing in this estate does that on a budget: "+
				"the policy plane's decision point cannot read one and there is no tier to lower", wrong)
		}
	}

	// And the denial is stated rather than left to be inferred from an absence.
	// "What this box already did:" followed by a hedge is worse than followed by
	// the flat answer, because a hedge reads as something partial having
	// happened.
	if !strings.HasPrefix(p.did, "Nothing.") {
		t.Errorf("the did line does not open by saying plainly that nothing was done: %q", p.did)
	}
	if !strings.Contains(strings.ToLower(p.did), "measur") {
		t.Errorf("the did line does not say what DID happen, which is a measurement: %q", p.did)
	}
}

// The same answer on both triggers, because the trigger changes what is TRUE
// about the budget and changes nothing about what was done. A `did` line that
// varied with it would be this file inventing a consequence for the louder of
// the two.
//
// The existence check is not a formality. `catalog["slo_burn"].did` on a map
// with no such key is the empty string, and `strings.Contains(body, "")` is
// true of every message ever rendered, so without it this case passed on the
// tree that had no entry at all.
func TestNeitherTriggerBuysAConsequence(t *testing.T) {
	p, ok := catalog["slo_burn"]
	if !ok {
		t.Fatal("slo_burn has no catalog entry")
	}
	exhausted := sloMail(sloData("task_success", "exhausted"))
	fast := sloMail(sloData("task_success", "fast_burn"))
	did := p.did
	for name, m := range map[string]Message{"exhausted": exhausted, "fast_burn": fast} {
		if !strings.Contains(m.Body, did) {
			t.Errorf("%s: the mail does not carry the catalog's did line unchanged:\n%s", name, m.Body)
		}
	}
}

// An exhausted budget is a statement about runs that have already happened, and
// it will not improve on its own. Told it as a forecast, an operator waits for
// something that already finished going wrong.
func TestAnExhaustedBudgetIsNotDescribedAsAForecast(t *testing.T) {
	m := sloMail(sloData("quality_floor", "exhausted"))
	body := strings.ToLower(m.Body)

	if !strings.Contains(body, "already") {
		t.Errorf("the mail does not say the objective is already missed:\n%s", m.Body)
	}
	// The forecast wording, which belongs to the other trigger.
	for _, wrong := range []string{
		"not gone yet",
		"before the window ends",
		"will be gone",
	} {
		if strings.Contains(body, wrong) {
			t.Errorf("an exhausted budget is described as one still being spent, saying %q:\n%s", wrong, m.Body)
		}
	}
	// And the subject line too, because a mailbox is read as a list of subjects
	// before any of them is opened.
	if !strings.Contains(strings.ToLower(m.Subject), "spent") {
		t.Errorf("the subject does not say the budget is spent: %s", m.Subject)
	}
}

// And the mirror. A fast burn still has budget left, and the whole value of
// mailing it is that there is time to act. Told the objective is already
// missed, an operator treats a window they can still save as one they cannot.
func TestAFastBurnIsNotDescribedAsAlreadyMissed(t *testing.T) {
	m := sloMail(sloData("containment", "fast_burn"))
	body := strings.ToLower(m.Body)

	if !strings.Contains(body, "faster") {
		t.Errorf("the mail does not say the budget is being spent too fast:\n%s", m.Body)
	}
	if !strings.Contains(body, "before the window ends") {
		t.Errorf("the mail does not say when the budget runs out at this rate:\n%s", m.Body)
	}
	for _, wrong := range []string{
		"already missed",
		"has spent its whole error budget",
	} {
		if strings.Contains(body, wrong) {
			t.Errorf("a fast burn is described as an objective already missed, saying %q:\n%s", wrong, m.Body)
		}
	}
}

// Which objective, because it is the first thing an operator asks and the four
// are read by different people: a containment budget and a cost budget say very
// different things about the same agent.
//
// It reaches the reader as THIS FILE'S OWN words, chosen by matching a closed
// set, never as the producer's bytes. See [TestTheSLOFieldsAreNotMailedRaw].
func TestTheObjectiveIsNamedInTheMail(t *testing.T) {
	for _, c := range []struct{ sli, want string }{
		{"task_success", "task success"},
		{"quality_floor", "the quality floor"},
		{"containment", "containment"},
		{"cost_discipline", "cost discipline"},
	} {
		m := sloMail(sloData(c.sli, "exhausted"))
		if !strings.Contains(m.Body, c.want) {
			t.Errorf("objective %q is not named in the mail as %q:\n%s", c.sli, c.want, m.Body)
		}
	}
}

// An objective this build has not heard of is not guessed at and does not break
// the message. verdryx may grow a fifth indicator before this file learns of
// it, and the alert still has to go out.
//
// Green on the tree that had no entry, where nothing at all was rendered, so it
// was verified by planting the fault instead: `objectiveName`'s default made to
// return `e.Data["sli"]` fails it with "an unknown objective name was rendered
// into the mail".
func TestAnUnknownObjectiveIsNotGuessedAt(t *testing.T) {
	m := sloMail(sloData("some_future_indicator", "exhausted"))
	if strings.Contains(m.Body, "some_future_indicator") {
		t.Errorf("an unknown objective name was rendered into the mail:\n%s", m.Body)
	}
	if !strings.Contains(m.Body, "What this box already did:") {
		t.Errorf("an unknown objective cost the mail its explanatory lines:\n%s", m.Body)
	}
}

// A trigger this build does not know says so instead of picking one of the two.
// The two differ on whether the objective is already missed or is on course to
// be, which is a statement about the past against a forecast, and that is
// exactly what must not be guessed.
//
// `slow_burn` is in this table on purpose. The contract emits this type on
// "exhausted" and "fast_burn" ONLY: severity is fixed per type in this estate,
// so one type is one paging band, and "your budget will be gone by Friday" does
// not belong in the band of "your budget is gone". The slow-burn figure lives
// in verdryx's report and its JSON output, where a dashboard reads it. So an
// slo_burn carrying that trigger is a producer that has broken the contract,
// and the neutral sentence is the right answer to it: this build must not grow
// a description for a case it has agreed never to be paged about.
func TestAnUnknownTriggerFallsBackToTheNeutralSentence(t *testing.T) {
	for _, trigger := range []string{"", "slow_burn", "invented_by_a_later_verdryx"} {
		m := sloMail(sloData("task_success", trigger))
		body := strings.ToLower(m.Body)
		if strings.Contains(body, "has spent its whole error budget") || strings.Contains(body, "faster than the window allows") {
			t.Errorf("trigger %q was given one of the two specific answers:\n%s", trigger, m.Body)
		}
		if !strings.Contains(body, "this event does not say") {
			t.Errorf("trigger %q: the mail does not admit it was not told which trigger fired:\n%s", trigger, m.Body)
		}
	}
}

// `data` here is read to choose a sentence and is never rendered. This type
// carries eleven fields and nine of them are numbers an operator would quite
// like to see, which is what makes it the tempting one: adding a key to the
// allowlist is a decision CLAUDE.md sends to the user, not a thing a change
// like this may do on its way past.
//
// Verified by planting the fault rather than against the pre-change tree, where
// slo_burn had no entry and every assertion below passed for the wrong reason.
// One plant fails both blocks: `"sli": true` added to `dataAllowlist` reports
// the key on the first and then, because `factLine` renders it, "the raw field
// value \"task_success\" was rendered into the mail" on the second.
func TestTheSLOFieldsAreNotMailedRaw(t *testing.T) {
	for _, k := range []string{"sli", "trigger", "identity_field", "target", "observed",
		"ci_low", "ci_high", "events", "budget_remaining", "burn_rate"} {
		if dataAllowlist[k] {
			t.Errorf("%q was added to the data allowlist; adding a key is a decision for the user, "+
				"and this change did not ask", k)
		}
	}

	m := sloMail(sloData("task_success", "exhausted"))
	// The raw control values must not appear either: they are this file's
	// input, not its output.
	//
	// "exhausted" is the one worth defending, because it is also an ordinary
	// English word somebody would reach for here. Keeping it out is not
	// pedantry: `budget_exhausted` is already a type in this catalog and it
	// means the MONEY ran out, so an error budget mail using the same word
	// would put two different incidents under one phrase in a mailbox that is
	// read as a list. "Has spent its whole error budget" says the same thing
	// and says which budget.
	for _, raw := range []string{"task_success", "exhausted", "identity_field", "agent_id", "burn_rate"} {
		if strings.Contains(m.Body, raw) {
			t.Errorf("the raw field value %q was rendered into the mail:\n%s", raw, m.Body)
		}
	}
}

// The window does reach the operator, and it is the one field on this type that
// does. `window` has been in the allowlist since before this type existed, and
// a budget with no window attached is not a number anybody can read: 91% over
// an hour and 91% over 28 days are different situations.
//
// This one pins existing behaviour rather than proving new behaviour, so it was
// never going to be red on the unfixed tree. It can still go red, which is what
// makes it worth having: removing `"window": true` from `dataAllowlist` fails it
// with "the mail does not say what window the budget was measured over".
func TestTheWindowReachesTheMail(t *testing.T) {
	m := sloMail(sloData("task_success", "exhausted"))
	if !strings.Contains(m.Body, "28d") {
		t.Errorf("the mail does not say what window the budget was measured over:\n%s", m.Body)
	}
}

// The severity floor already delivers this type, so nothing in `internal/rule`
// changes. Pinned here because it is the assumption the whole change rests on:
// the contract fixes this type at `high` in verdryx's own EVENT_SEVERITY map,
// `rule.DefaultConfig` floors at `high`, and `Decide` sends anything at or
// above its floor immediately.
//
// It is also the assumption most likely to break from outside this repository.
// verdryx's emitter defaults an unmapped type to `info`, so a producer that
// ships this type without adding it to that map sends every error budget alert
// to tomorrow's digest, silently. This test cannot see that: it pins the
// contract's severity, not what verdryx actually stamps.
//
// Verified by planting the fault, since the floor and the severity both already
// held: `rule.DefaultConfig`'s `MinRank` raised to `rankCrit` fails it with
// "slo_burn at severity \"high\" ranks 3, below the default floor 4".
func TestSLOBurnClearsTheDefaultFloor(t *testing.T) {
	e := ev("slo_burn", sloData("task_success", "exhausted"))
	floor := rule.DefaultConfig().MinRank
	if got := rule.Rank(e.Severity); got < floor {
		t.Fatalf("slo_burn at severity %q ranks %d, below the default floor %d, "+
			"so it would go to the daily digest rather than being mailed",
			e.Severity, got, floor)
	}
}
