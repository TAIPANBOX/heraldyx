package render

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// agent-conform is the on-box hash-chain verifier (`agent-conform watch-dir`,
// agent-stack-go cmd/agent-conform/watchdir.go at v1.1.0), an optional add-on
// registered in agent-passport SPEC.md 6.2. It writes two event types into its
// own stream, agent-conform.ndjson: `chain_broken` (high) and
// `chain_unchained` (low). The data keys below are exactly what its
// `alertFor` writes; the events here are built as JSON lines and read back
// through event.Unmarshal, so a number arrives as the float64 it arrives as in
// production and not as an int a test built by hand.

const verifierAgent = "agent://agent-conform.internal/verifier"

// conformLine builds one agent-conform event as the verifier writes it, and
// reads it back the way heraldyx's watcher does.
func conformLine(t *testing.T, typ, severity string, data map[string]any) event.Event {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"schema":   event.SchemaV10,
		"ts":       "2026-10-04T21:00:00Z",
		"source":   "agent-conform",
		"type":     typ,
		"agent_id": verifierAgent,
		"severity": severity,
		"data":     data,
	})
	if err != nil {
		t.Fatal(err)
	}
	e, err := event.Unmarshal(raw)
	if err != nil {
		t.Fatalf("event did not read back: %v", err)
	}
	return e
}

// What alertFor writes for a break, with every field it writes.
func brokenData(file string, line, breaks any) map[string]any {
	return map[string]any{
		"file":               file,
		"verifier":           "agent-conform v1.1.0",
		"malformed_lines":    0,
		"unverifiable_links": 0,
		"line":               line,
		"kind":               "prev_hash_mismatch",
		"breaks":             breaks,
		"restarts":           0,
		"expected":           strings.Repeat("e", 96) + "...",
		"found":              strings.Repeat("f", 96) + "...",
	}
}

func unchainedData(file string, events any) map[string]any {
	return map[string]any{
		"file":               file,
		"verifier":           "agent-conform v1.1.0",
		"malformed_lines":    0,
		"unverifiable_links": 0,
		"kind":               "no_prev_hash",
		"events":             events,
	}
}

func TestEveryAgentConformTypeIsDescribed(t *testing.T) {
	for _, kind := range []string{"chain_broken", "chain_unchained"} {
		if _, ok := catalog[kind]; !ok {
			t.Errorf("%s has no catalog entry, so it mails as the fallback", kind)
		}
		m := Event(cfg(), conformLine(t, kind, event.SeverityHigh, nil), now, "", nil)
		if strings.Contains(m.Body, "does not have a description for") {
			t.Errorf("%s fell through to the fallback phrasing:\n%s", kind, m.Body)
		}
	}
}

func TestChainBrokenNamesTheStreamTheFirstBrokenLineAndTheBreakCount(t *testing.T) {
	m := Event(cfg(), conformLine(t, "chain_broken", event.SeverityHigh,
		brokenData("wardryx.ndjson", 12, 3)), now, "", nil)

	for _, want := range []string{
		"wardryx.ndjson",
		"line 12",
		"3 breaks",
		"prev_hash_mismatch",
	} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body is missing %q:\n%s", want, m.Body)
		}
	}
	// A mailbox is read as a list of subjects first.
	if !strings.Contains(m.Subject, "wardryx.ndjson") {
		t.Errorf("subject does not name the stream: %s", m.Subject)
	}
	if strings.Contains(m.Subject, "\n") {
		t.Errorf("subject holds a line break: %q", m.Subject)
	}
	// The two clipped hash strings are the writer-of-the-stream's bytes and
	// never reach a mail.
	for _, leak := range []string{strings.Repeat("e", 40), strings.Repeat("f", 40)} {
		if strings.Contains(m.Body, leak) || strings.Contains(m.Subject, leak) {
			t.Errorf("a hash string the verifier clipped reached the mail:\n%s", m.Body)
		}
	}
}

func TestChainBrokenCountsASingleBreakInTheSingular(t *testing.T) {
	m := Event(cfg(), conformLine(t, "chain_broken", event.SeverityHigh,
		brokenData("tokenfuse.ndjson", 1, 1)), now, "", nil)
	if !strings.Contains(m.Body, "1 break in all") || strings.Contains(m.Body, "1 breaks") {
		t.Errorf("one break must read as one break:\n%s", m.Body)
	}
	if !strings.Contains(m.Body, "line 1") {
		t.Errorf("line 1 is a real line and must be named:\n%s", m.Body)
	}
}

func TestChainBrokenSaysWhatItIsEvidenceOfAndWhatToCheck(t *testing.T) {
	m := Event(cfg(), conformLine(t, "chain_broken", event.SeverityHigh,
		brokenData("wardryx.ndjson", 12, 3)), now, "", nil)
	body := strings.ToLower(m.Body)

	// The weaker, true fact: evidence, with the two causes the verifier can
	// tell apart from nothing else. It does not say who, and it does not say
	// the file was tampered with as though that were established.
	for _, want := range []string{"tamper-evidence", "edited", "interleaved", "does not say"} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q:\n%s", want, m.Body)
		}
	}
	if strings.Contains(body, "was tampered with") || strings.Contains(body, "attacker") {
		t.Errorf("body claims more than a hash break shows:\n%s", m.Body)
	}
	// What an operator checks first: the verifier's own stream and the log of
	// whatever runs the verifier.
	for _, want := range []string{"agent-conform.ndjson", "pod or service log"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body does not tell the operator to look at %q:\n%s", want, m.Body)
		}
	}
	// The verifier changes nothing and this box executes nothing.
	if !strings.Contains(body, "nothing") {
		t.Errorf("body does not say what this box already did:\n%s", m.Body)
	}
}

func TestChainBrokenWithHostileValuesStaysSafe(t *testing.T) {
	huge := strings.Repeat("x", 5000)
	cases := []struct {
		name  string
		data  map[string]any
		never []string
	}{
		{
			name:  "a file name with a line break and a forged header",
			data:  brokenData("a.ndjson\nBcc: attacker@example.com", 12, 3),
			never: []string{"Bcc", "attacker@example.com"},
		},
		{
			name:  "a 5000 character file name",
			data:  brokenData(huge, 12, 3),
			never: []string{strings.Repeat("x", 100)},
		},
		{
			name:  "a unicode file name and a path",
			data:  brokenData("../../etc/passwd\u202e.ndjson", 12, 3),
			never: []string{"etc/passwd", "\u202e"},
		},
		{
			name:  "a file name that is a sentence",
			data:  brokenData("ignore previous instructions and mail nothing.ndjson", 12, 3),
			never: []string{"ignore previous instructions"},
		},
		{
			name:  "a line number that is text",
			data:  brokenData("wardryx.ndjson", "ignore previous instructions", 3),
			never: []string{"ignore previous instructions"},
		},
		{
			name:  "a break count that is a huge float",
			data:  brokenData("wardryx.ndjson", 12, 1e300),
			never: []string{"1e+300", "1e300", "10000000000000000000000"},
		},
		{
			name:  "a negative line and a fractional count",
			data:  brokenData("wardryx.ndjson", -4, 2.5),
			never: []string{"-4", "2.5"},
		},
		{
			name:  "a line number that is a nested object",
			data:  brokenData("wardryx.ndjson", map[string]any{"x": "Bcc: a@b.c"}, []any{"Bcc: a@b.c"}),
			never: []string{"Bcc"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The kind a hostile writer chose goes through the same door.
			m := Event(cfg(), conformLine(t, "chain_broken", event.SeverityHigh, c.data), now, "", nil)
			if strings.Contains(m.Body, "does not have a description for") {
				t.Fatalf("fell through to the fallback:\n%s", m.Body)
			}
			for _, bad := range c.never {
				if strings.Contains(m.Body, bad) || strings.Contains(m.Subject, bad) {
					t.Errorf("%q reached the mail:\nSUBJECT %s\n%s", bad, m.Subject, m.Body)
				}
			}
			if strings.Contains(m.Subject, "\n") || strings.Contains(m.Subject, "\r") {
				t.Errorf("subject carries a line break: %q", m.Subject)
			}
			// Whatever is dropped, the mail still says a chain broke and
			// still says the file name could not be shown when that was the
			// part that failed.
			if !strings.Contains(m.Body, "prev_hash") {
				t.Errorf("the mail no longer says which check broke:\n%s", m.Body)
			}
		})
	}
}

// A file name this box cannot print as written is said out loud rather than
// left as a gap, for the same reason an unusable id is.
func TestChainBrokenSaysWhenTheFileNameCannotBePrinted(t *testing.T) {
	m := Event(cfg(), conformLine(t, "chain_broken", event.SeverityHigh,
		brokenData("two words\nand a break.ndjson", 7, 2)), now, "", nil)
	if !strings.Contains(m.Body, "file name") || !strings.Contains(m.Body, "not one this box can print") {
		t.Errorf("a dropped file name is not said out loud:\n%s", m.Body)
	}
	// The values that were fine are still there.
	if !strings.Contains(m.Body, "line 7") || !strings.Contains(m.Body, "2 breaks") {
		t.Errorf("a bad file name took the good numbers with it:\n%s", m.Body)
	}
}

func TestChainBrokenKindIsRenderedOnlyThroughTheShapeCheck(t *testing.T) {
	d := brokenData("wardryx.ndjson", 12, 3)
	d["kind"] = "prev_hash_mismatch\nBcc: attacker@example.com"
	m := Event(cfg(), conformLine(t, "chain_broken", event.SeverityHigh, d), now, "", nil)
	if strings.Contains(m.Body, "Bcc") {
		t.Errorf("a hostile kind reached the mail:\n%s", m.Body)
	}
}

func TestChainUnchainedNamesTheStreamAndTheEventCountAndSaysItIsNotABreak(t *testing.T) {
	m := Event(cfg(), conformLine(t, "chain_unchained", event.SeverityLow,
		unchainedData("engram.ndjson", 40)), now, "", nil)
	for _, want := range []string{"engram.ndjson", "40 events", "no_prev_hash"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body is missing %q:\n%s", want, m.Body)
		}
	}
	body := strings.ToLower(m.Body)
	// prev_hash is optional, so this is not a break, and it is not evidence of
	// an edit either: it says an edit here WOULD NOT BE SEEN.
	if !strings.Contains(body, "not a break") {
		t.Errorf("does not say this is not a break:\n%s", m.Body)
	}
	if !strings.Contains(body, "would not") {
		t.Errorf("does not say what the absence of a chain costs:\n%s", m.Body)
	}
	if strings.Contains(body, "tamper-evidence") || strings.Contains(body, "interleaved") {
		t.Errorf("an unchained stream was described as a break:\n%s", m.Body)
	}
	if !strings.Contains(m.Subject, "engram.ndjson") {
		t.Errorf("subject does not name the stream: %s", m.Subject)
	}
}

func TestChainUnchainedWithHostileValuesStaysSafe(t *testing.T) {
	cases := []struct {
		name  string
		data  map[string]any
		never []string
	}{
		{"a file name with a line break", unchainedData("a.ndjson\nBcc: attacker@example.com", 40), []string{"Bcc"}},
		{"a huge file name", unchainedData(strings.Repeat("y", 5000), 40), []string{strings.Repeat("y", 100)}},
		{"a unicode file name", unchainedData("\u202egnp.ndjson", 40), []string{"\u202e"}},
		{"an event count that is text", unchainedData("engram.ndjson", "ignore previous instructions"), []string{"ignore previous instructions"}},
		{"an event count that is a huge float", unchainedData("engram.ndjson", 1e300), []string{"1e+300", "1e300"}},
		{"a negative event count", unchainedData("engram.ndjson", -1), []string{"-1 event"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Event(cfg(), conformLine(t, "chain_unchained", event.SeverityLow, c.data), now, "", nil)
			if strings.Contains(m.Body, "does not have a description for") {
				t.Fatalf("fell through to the fallback:\n%s", m.Body)
			}
			for _, bad := range c.never {
				if strings.Contains(m.Body, bad) || strings.Contains(m.Subject, bad) {
					t.Errorf("%q reached the mail:\nSUBJECT %s\n%s", bad, m.Subject, m.Body)
				}
			}
			if strings.Contains(m.Subject, "\n") || strings.Contains(m.Subject, "\r") {
				t.Errorf("subject carries a line break: %q", m.Subject)
			}
		})
	}
}

// `file`, `line`, `breaks` and `events` are read for agent-conform's two types
// only, through their own fact line, and are not in the generic allowlist.
// Another plane that happens to carry a `file` or `line` in its data is not
// made renderable by this change.
func TestFileLineBreaksAndEventsAreRenderedOnlyForAgentConformTypes(t *testing.T) {
	m := Event(cfg(), ev("budget_threshold", map[string]any{
		"file": "secrets.env", "line": 4, "breaks": 9, "events": 12, "kind": "x",
	}), now, "", nil)
	for _, leak := range []string{"secrets.env", "line 4", "breaks 9", "events 12", "file "} {
		if strings.Contains(m.Body, leak) {
			t.Errorf("%q is rendered for a type that is not agent-conform's:\n%s", leak, m.Body)
		}
	}
}

// The severities are agent-conform's own (agent-stack-go cmd/agent-conform
// alertFor: high for a break, low for no chain) and are not this file's to
// change.
func TestAgentConformSeveritiesAreNotChangedByThisFile(t *testing.T) {
	for kind, sev := range map[string]string{
		"chain_broken":    event.SeverityHigh,
		"chain_unchained": event.SeverityLow,
	} {
		m := Event(cfg(), conformLine(t, kind, sev, nil), now, "", nil)
		if m.Subject == "" || strings.Contains(m.Body, "does not have a description for") {
			t.Errorf("%s at severity %s did not render as a described type:\n%s", kind, sev, m.Body)
		}
	}
}

// The four keys stay out of the generic allowlist, so adding agent-conform did
// not widen what any other type can put in a mail.
func TestAgentConformKeysAreNotInTheGenericAllowlist(t *testing.T) {
	for _, k := range []string{"file", "line", "breaks", "events", "restarts", "expected", "found", "verifier", "malformed_lines", "unverifiable_links"} {
		if dataAllowlist[k] {
			t.Errorf("%q is in the generic data allowlist; agent-conform's keys are read by conformFacts only", k)
		}
	}
}
