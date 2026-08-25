package render

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/heraldyx/internal/rule"
)

// `dependency_failed` is the one type in this catalog that is not about the
// agent. Every other entry describes something the subject did or something
// this stack refused it; this one describes a dependency of the box dying
// underneath a run that was behaving perfectly.
//
// The sentences below were written against tokenfuse's own code on 2026-08-25,
// which is what this repo requires of a catalog entry, and the specific reading
// is recorded beside each assertion. The producing plane had not been written
// yet when these were written, so what they are read against is the FAILURE
// PATH, which has existed all along: `upstream_error` at
// `crates/gateway/src/proxy.rs`, the mid-stream `?` inside `try_stream!`, and
// `Wardryx::fallback` in `crates/gateway/src/wardryx.rs`.

// depMail renders one dependency_failed mail off the shared fixture in
// render_test.go, so these read against the same box, agent and run every other
// test in this package does.
func depMail(data map[string]any) Message {
	return Event(cfg(), ev("dependency_failed", data), now, "", nil)
}

// The floor: it is described at all. Without an entry the mail says "raised an
// event this build does not have a description for", which for a provider
// outage names no fault and sends nobody anywhere.
func TestDependencyFailedIsDescribed(t *testing.T) {
	m := depMail(map[string]any{"dependency": "provider", "stage": "send", "effect": "call_failed"})
	if strings.Contains(m.Body, "does not have a description for") {
		t.Fatalf("dependency_failed fell through to the fallback phrasing:\n%s", m.Body)
	}
	if _, ok := catalog["dependency_failed"]; !ok {
		t.Fatal("dependency_failed has no catalog entry")
	}
}

// THE one that matters, and the one a single per-type phrasing would get wrong.
//
// `allowed_ungoverned` means the policy plane could not be reached, this
// gateway is configured to fail open, and the call therefore WENT THROUGH with
// nothing examining it. `Wardryx::fallback` synthesizes an `Allow` under
// `FailMode::Open` and the request proceeds. An operator who reads this as "a
// call failed" goes looking for a broken agent and never learns that their
// estate spent an interval ungoverned.
//
// So the mail has to say plainly that no policy was applied, and it must not
// say anywhere that the call did not happen.
func TestAnUngovernedCallIsNotDescribedAsAFailedCall(t *testing.T) {
	m := depMail(map[string]any{
		"dependency": "policy_plane", "stage": "decide", "effect": "allowed_ungoverned",
	})

	body := strings.ToLower(m.Body)
	if !strings.Contains(body, "policy") {
		t.Errorf("the mail never mentions policy, which is the whole fact:\n%s", m.Body)
	}
	if !strings.Contains(body, "let the call through") && !strings.Contains(body, "went through") {
		t.Errorf("the mail does not say the call went through:\n%s", m.Body)
	}
	// The sentences that would send the operator to the wrong conclusion.
	for _, wrong := range []string{
		"the call did not complete",
		"the call did not happen",
		"was given an error",
		"could not be served",
	} {
		if strings.Contains(body, wrong) {
			t.Errorf("an ungoverned call is described as a failed one, saying %q:\n%s", wrong, m.Body)
		}
	}
	// And the subject line too, because a mailbox is read as a list of subjects
	// before any of them is opened.
	if strings.Contains(strings.ToLower(m.Subject), "could not") {
		t.Errorf("the subject reads as a failure rather than as an ungoverned call: %s", m.Subject)
	}
}

// A refusal nobody decided is not a policy decision, and the difference is the
// operator's next move.
//
// Under `FailMode::Closed` the same unreachable plane synthesizes a `Deny`. No
// policy refused this call: the plane could not be asked. Told "denied by
// policy", an operator goes to read a policy that never ran, and the estate's
// real fault, a plane that is down, goes unlooked at.
func TestARefusalNobodyDecidedIsNotDescribedAsAPolicyDecision(t *testing.T) {
	m := depMail(map[string]any{
		"dependency": "policy_plane", "stage": "decide", "effect": "denied_unasked",
	})

	body := strings.ToLower(m.Body)
	if !strings.Contains(body, "refused") {
		t.Errorf("the mail does not say the call was refused:\n%s", m.Body)
	}
	// The distinction, stated rather than left for the reader to infer from an
	// absence.
	if !strings.Contains(body, "no policy") {
		t.Errorf("the mail does not say that no policy decided this:\n%s", m.Body)
	}
	if strings.Contains(body, "was denied by policy") {
		t.Errorf("a synthesized deny is described as a policy decision:\n%s", m.Body)
	}
}

// The ordinary case, and the one where the money answer is knowable. At
// `proxy.rs` both buffered failure paths settle `Microusd::ZERO` against the
// run and against the unit ledger, with the comment "Failed call cost us
// nothing", so an operator can be told plainly that this outage did not cost
// them anything.
func TestAFailedCallSaysNothingWasCharged(t *testing.T) {
	m := depMail(map[string]any{
		"dependency": "provider", "stage": "send", "effect": "call_failed",
	})

	body := strings.ToLower(m.Body)
	if !strings.Contains(body, "nothing was charged") {
		t.Errorf("the mail does not say the failed call cost nothing:\n%s", m.Body)
	}
	if !strings.Contains(body, "error") {
		t.Errorf("the mail does not say the agent got an error instead of an answer:\n%s", m.Body)
	}
}

// And the case that makes one sentence per type impossible.
//
// A call that breaks part way through a STREAM is `call_failed` too, and
// "nothing was charged" is false for it. The response has already been sent
// with its own status and part of the answer has already reached the agent;
// `SettleGuard`'s `Drop` then settles whatever usage was parsed, and on a
// stream that started 2xx and reported no usage it settles the reserved
// estimate instead of zero. Telling an operator that a truncated answer cost
// them nothing is the same class of falsehood as the four this catalog was
// audited for on 2026-08-03.
func TestACallCutOffMidStreamIsNotCalledFreeOrUnmade(t *testing.T) {
	m := depMail(map[string]any{
		"dependency": "provider", "stage": "stream", "effect": "call_failed",
	})

	body := strings.ToLower(m.Body)
	if strings.Contains(body, "nothing was charged") {
		t.Errorf("a stream that broke part way is described as free, and it is not:\n%s", m.Body)
	}
	if strings.Contains(body, "the call did not complete") {
		t.Errorf("a stream that broke part way is described as a call that never happened:\n%s", m.Body)
	}
	if !strings.Contains(body, "already reached the agent") {
		t.Errorf("the mail does not say part of the answer had already been delivered:\n%s", m.Body)
	}
}

// Which dependency died is the first thing an operator needs, because it
// decides who they call: their provider, or their own policy plane.
//
// It reaches the reader as THIS FILE'S OWN prose, chosen by matching a closed
// set, and never as the producer's bytes. See [TestTheDependencyFieldsAreNotMailedRaw].
func TestTheFailedDependencyIsNamedInTheMail(t *testing.T) {
	for _, c := range []struct{ dependency, want string }{
		{"provider", "the provider"},
		{"policy_plane", "the policy plane"},
	} {
		m := depMail(map[string]any{
			"dependency": c.dependency, "stage": "send", "effect": "call_failed",
		})
		if !strings.Contains(m.Body, c.want) {
			t.Errorf("dependency %q is not named in the mail as %q:\n%s", c.dependency, c.want, m.Body)
		}
	}
}

// A dependency this build does not know is not guessed at and does not break
// the message. The producer may grow a third one before this file learns about
// it, and the alert still has to go out.
func TestAnUnknownDependencyIsNotGuessedAt(t *testing.T) {
	m := depMail(map[string]any{
		"dependency": "some_future_plane", "stage": "send", "effect": "call_failed",
	})
	if strings.Contains(m.Body, "some_future_plane") {
		t.Errorf("an unknown dependency name was rendered into the mail:\n%s", m.Body)
	}
	if !strings.Contains(m.Body, "What this box already did:") {
		t.Errorf("an unknown dependency cost the mail its explanatory lines:\n%s", m.Body)
	}
}

// An event whose `effect` this build does not know says so instead of picking
// one of the three. The three differ on whether the call happened and whether
// policy was applied, which is exactly what must not be guessed.
func TestAnUnknownEffectFallsBackToTheNeutralSentence(t *testing.T) {
	for _, data := range []map[string]any{
		{"dependency": "provider", "stage": "send"},
		{"dependency": "provider", "stage": "send", "effect": "invented_by_a_later_gateway"},
	} {
		m := depMail(data)
		body := strings.ToLower(m.Body)
		if strings.Contains(body, "nothing was charged") || strings.Contains(body, "let the call through") {
			t.Errorf("an event with no effect this build knows was given one of the three specific answers:\n%s", m.Body)
		}
		if !strings.Contains(body, "this event does not say") {
			t.Errorf("the mail does not admit that the event did not say what the effect was:\n%s", m.Body)
		}
	}
}

// `data` here is read to choose a sentence and is never rendered. `detail`
// is the one that makes this more than a formality: the contract puts a
// transport error string in it, which is text nobody here controls, arriving
// under a key whose whole purpose is to be human-readable.
//
// Verified by planting the fault rather than against the pre-change tree,
// where these keys were absent and every assertion below passed for the wrong
// reason. Planting `dataAllowlist["detail"] = true` fails the first block, and
// rendering `dep` from the raw field fails the second.
func TestTheDependencyFieldsAreNotMailedRaw(t *testing.T) {
	for _, k := range []string{"dependency", "stage", "effect", "detail"} {
		if dataAllowlist[k] {
			t.Errorf("%q was added to the data allowlist; it is a control for the phrasing, "+
				"not a value for the operator to read", k)
		}
	}

	// Two details, and the SHORT one is the load-bearing case. The long one is
	// held out by `safeString`'s 64-byte cap whether or not the key is
	// allowlisted, so a test carrying only that value passes for a reason that
	// has nothing to do with the allowlist: planting `dataAllowlist["detail"]`
	// left it green. A short, identifier-shaped detail is what a real transport
	// error mostly looks like, and it is the one that gets in.
	for _, detail := range []string{
		"connection reset by peer while POSTing to https://api.provider.example/v1/messages",
		"connection reset",
	} {
		m := depMail(map[string]any{
			"dependency": "provider", "stage": "send", "effect": "call_failed",
			"detail": detail,
		})
		if strings.Contains(m.Body, detail) {
			t.Errorf("the transport detail reached the mail: %q\n%s", detail, m.Body)
		}
	}

	m := depMail(map[string]any{
		"dependency": "provider", "stage": "send", "effect": "call_failed",
		"detail": "connection reset",
	})
	// The raw field names must not appear either: they are this file's input,
	// not its output.
	for _, k := range []string{"call_failed", "policy_plane", "response_body"} {
		if strings.Contains(m.Body, k) {
			t.Errorf("the raw field value %q was rendered into the mail:\n%s", k, m.Body)
		}
	}
}

// The severity floor already delivers this type, so nothing in `internal/rule`
// changes. Pinned here because it is the assumption the whole change rests on:
// the contract fixes this type at `high`, `rule.DefaultConfig` floors at
// `high`, and `Decide` sends anything at or above its floor immediately.
//
// If the contract's severity is ever lowered, this fails and says why, rather
// than the alert quietly becoming a line in tomorrow's digest.
func TestDependencyFailedClearsTheDefaultFloor(t *testing.T) {
	e := ev("dependency_failed", map[string]any{"effect": "allowed_ungoverned"})
	floor := rule.DefaultConfig().MinRank
	if got := rule.Rank(e.Severity); got < floor {
		t.Fatalf("dependency_failed at severity %q ranks %d, below the default floor %d, "+
			"so it would go to the daily digest rather than being mailed",
			e.Severity, got, floor)
	}
}
