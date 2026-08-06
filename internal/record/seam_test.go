package record

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
)

// The seam to the record plane is a FILE, not a socket, and these tests hold
// heraldyx's half of it.
//
// trailryx reads this journal with `trailryx-node events --file`, through
// `trailryx-agentevent`, which maps one line of the shared envelope into one
// record. That mapper refuses a line by name rather than repairing it, and four
// of its refusals are decided entirely by bytes this package writes: the schema
// it stamps, the timestamp it formats, the agent identifier it carries, and the
// run identifier it carries. Nothing else in this repository asserts any of
// them, so an edit here that made the journal unreadable would surface as a
// count of zero in a different repository, days later.
//
// What these tests deliberately do NOT do is copy the mapper's table of event
// types. That table is a reading of the registry on a date and it belongs to
// trailryx; a copy of it here would be a second place to update and a stale
// assertion the day it moved.
const (
	// The two schema values trailryx-agentevent accepts, quoted from its
	// SCHEMAS constant. Two rather than one because agent-passport SPEC.md
	// says a consumer must accept either, so this pair is frozen by the
	// specification rather than by that crate.
	schemaV01 = "taipanbox.dev/agent-event/v0.1"
	schemaV02 = "taipanbox.dev/agent-event/v0.2"
)

// journalOf writes one dispatch of each kind and returns the lines.
func journalOf(t *testing.T, ds ...Dispatch) []map[string]any {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sent.ndjson")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range ds {
		j.Sent(d, now.Add(time.Duration(i)*time.Minute))
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("nothing was written: %v", err)
	}
	return read(t, path)
}

func kindsOfDispatch() []Dispatch {
	alert, digest, suppression := dispatch(), dispatch(), dispatch()
	digest.Kind, digest.About = KindDigest, "digest:4"
	suppression.Kind, suppression.About = KindSuppression, "suppressed:9"
	return []Dispatch{alert, digest, suppression}
}

// Every record, of every kind, satisfies the four door rules this package
// decides. Each assertion names the refusal it forecloses, because a failure
// here is read by somebody who has never opened the mapper.
func TestEveryRecordIsReadableAtTheRecordPlanesDoor(t *testing.T) {
	for _, r := range journalOf(t, kindsOfDispatch()...) {
		kind := r["data"].(map[string]any)["kind"]

		// Rejection::UnknownSchema.
		switch r["schema"] {
		case schemaV01, schemaV02:
		default:
			t.Errorf("%v: schema %q is not one the record plane accepts, so every line of this journal would be refused as unknown_schema", kind, r["schema"])
		}

		// Rejection::BadTime. The mapper wants an RFC 3339 instant and this is
		// the only member it reads a time from.
		ts, _ := r["ts"].(string)
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Errorf("%v: ts %q is not an RFC 3339 instant, refused as bad_time: %v", kind, ts, err)
		}

		// Rejection::NoAgent. agent://<trust-domain>/<path>, both halves
		// non-empty, which is AgentId::parse_strict.
		id, _ := r["agent_id"].(string)
		rest, ok := strings.CutPrefix(id, "agent://")
		domain, path, hasPath := strings.Cut(rest, "/")
		if !ok || !hasPath || domain == "" || path == "" {
			t.Errorf("%v: agent_id %q is not agent://<trust-domain>/<path>, refused as no_agent", kind, id)
		}
		if strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			t.Errorf("%v: agent_id %q carries whitespace or a control character", kind, id)
		}
	}
}

// The identifiers are carried whole.
//
// This package copies the agent and run of the event the message was about, so
// a record and the run it belongs to line up in the store without translation.
// The mail is the other case: it SHORTENS both, because a subject line with a
// forty-character URI in it is a subject line nobody reads, and
// `render.shortID` and `fleet.Short` exist for exactly that. Either of them
// reused here would produce a journal that still reads perfectly, still chains,
// and still passes every other test in this package.
//
// Which of the two things it then does at the record plane's door depends on
// where the cut falls, and the second is the worse one: an identifier cut above
// the first path separator is refused as no_agent, and one cut below it is
// ACCEPTED, as a well-formed identifier belonging to an agent that does not
// exist. That is why this is a comparison against the whole string rather than
// a shape check. A shape check passes the case worth catching.
func TestTheRecordCarriesTheIdentifiersWholeAndNotShortened(t *testing.T) {
	d := dispatch()
	d.AgentID = "agent://acme-bank.example/support/tier1-bot/instance-7"
	d.RunID = "run-8842-0f3c1d9a4b"

	r := journalOf(t, d)[0]
	if r["agent_id"] != d.AgentID {
		t.Errorf("the agent id must reach the record whole:\n want %q\n  got %q", d.AgentID, r["agent_id"])
	}
	if r["run_id"] != d.RunID {
		t.Errorf("the run id must reach the record whole:\n want %q\n  got %q", d.RunID, r["run_id"])
	}
}

// A dispatch about an event that named no run is recorded with no run, and the
// record plane refuses it by name.
//
// This is the one refusal on this side that could be made to go away by writing
// something down, and that is the reason it has a test rather than a fix. A
// synthesised run identifier would put this dispatch in a run it had nothing to
// do with, or invent a run that never executed, and either one is a false
// answer to "what happened in run R" for as long as the store keeps it. The
// same rule as the agent id one line above, which is already invariant 11:
// arriving as one refused line, counted by name, is the honest outcome.
//
// Note what is NOT symmetric. A dispatch with no AGENT is not recorded at all,
// because the envelope requires one; a dispatch with no RUN is recorded,
// because the envelope does not. Both gaps are visible, in different places.
func TestNoRunToNameIsRecordedAsNoRunRatherThanAnInventedOne(t *testing.T) {
	d := dispatch()
	d.RunID = ""

	r := journalOf(t, d)[0]
	if got, ok := r["run_id"]; ok && got != "" {
		t.Errorf("a run identifier was invented for a dispatch that had none: %q", got)
	}
	// And the record still exists: the mail went out, so the trail says so.
	if r["agent_id"] != d.AgentID {
		t.Errorf("the record itself must still be written: %v", r)
	}
}

// A recipient's address never reaches a member the record plane reads into its
// metadata plane.
//
// The mapper consumes seven members into typed fields, and everything else,
// `data` included, goes to the payload plane, which is encrypted under the
// subject's key and is where trailryx requires personal data to live. An
// address in `on_behalf_of` would be parsed as a principal and stored in the
// clear, in a plane whose whole rule is that it holds identifiers and numbers,
// and crypto-erasure would not reach it.
//
// `on_behalf_of` is the plausible mistake rather than an invented one: it is
// the envelope's list of the people an action was taken for, and the people
// this message was sent to are one short reading away from that.
func TestARecipientNeverReachesTheMetadataPlane(t *testing.T) {
	d := dispatch()
	d.To = []string{"ops@example.com", "oncall@example.com"}

	r := journalOf(t, d)[0]
	if _, ok := r["on_behalf_of"]; ok {
		t.Errorf("this record has an on_behalf_of member, which the record plane reads into the metadata plane: %v", r["on_behalf_of"])
	}
	for member, value := range r {
		if member == "data" {
			continue
		}
		rendered := strings.ToLower(strings.TrimSpace(strings.Join(strings.Fields(toString(value)), " ")))
		for _, address := range d.To {
			if strings.Contains(rendered, strings.ToLower(address)) {
				t.Errorf("recipient %q reached member %q, which is outside data and therefore outside the payload plane", address, member)
			}
		}
	}
	// The premise: they ARE in data, or this test would pass by proving the
	// journal had stopped naming recipients at all.
	to := r["data"].(map[string]any)["to"].([]any)
	if len(to) != len(d.To) {
		t.Fatalf("the journal must still name who was written to, got %v", to)
	}
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, toString(e))
		}
		return strings.Join(parts, " ")
	case map[string]any:
		parts := make([]string, 0, len(t))
		for k, e := range t {
			parts = append(parts, k, toString(e))
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}
