// Package record keeps the fact that a message was sent.
//
// "We wrote to you at 03:14 about this, and your mail server took it at
// 03:14:02" is a claim somebody eventually has to prove, and the person who
// has to prove it is the operator, in an argument they did not choose. So the
// dispatch is written down the same way everything else in this stack is
// written down: one agent-event per message, in the shared envelope
// (agent-passport SPEC.md section 6), appended to a hash-chained NDJSON file
// through this package's one writer.
//
// # Why this file is NOT the shared event log
//
// heraldyx mounts the planes' event log READ-ONLY, and that mount is the
// physical form of its strongest promise: it limits messages, never evidence.
// Writing its own record into that directory would mean mounting it writable,
// which hands a compromised notifier the ability to corrupt the trail it
// reads. So this journal lives on heraldyx's OWN state volume. It is the same
// format, written by the same library, verifiable by the same verifier, and
// it is somewhere a compromise of this process cannot reach anything else.
//
// # How this reaches trailryx, and why nothing here sends it
//
// It is read, not sent. The record plane grew a door for this envelope on
// 2026-08-06: `trailryx-node events --file` reads a file of shared-envelope
// NDJSON through the `trailryx-agentevent` mapper, which is the format this
// package already writes. So the seam is the file plus a read-only mount, and
// this process gains no client, no encoder, no import and no second binary.
// Measured the same day: the reader takes a journal at mode 0444 and leaves it
// byte for byte identical.
//
// That is a change of fact rather than a change of mind. Until that door
// existed the only way in was OTLP over HTTP with a protobuf body, which would
// have meant an HTTP client inside the one process in the box with a way out,
// which is what `scripts/one-way-out.sh` exists to prevent. The conclusion held
// and the reason for it has expired; a shipper process would now be a third
// component, a cursor and a second mapping to keep, for a hop a mount performs.
//
// # What this package therefore owes the door
//
// Four of the mapper's refusals are decided entirely by bytes written here: the
// schema stamped, the timestamp formatted, the agent identifier carried, and
// the run identifier carried. An edit that broke one of them would leave a
// journal that still reads, still chains and still passes every other test in
// this package, and would surface as a count of zero records in a different
// repository. `seam_test.go` holds those four, and holds them without copying
// the mapper's table of event types, which belongs to trailryx and moves.
//
// What is NOT owed, and must not be paid: a run identifier for a dispatch that
// has none. See [Journal.Sent].
//
// # What goes in, and what does not
//
// The mail carries identifiers and numbers only, because mail leaves the
// perimeter through a server nobody here controls. This journal is the other
// case: it never leaves the box, and its whole value is being specific. So it
// DOES name the recipients. An operator proving they were told needs to see
// who was written to, and "one recipient, hash a3f2" proves nothing to
// anybody. What it still never carries is the message body or any event `data`
// beyond the identifiers, because the body is built from other planes' fields
// and this package has no business copying them.
package record

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// Source is the shared-envelope `source` value for everything this process
// writes.
const Source = "heraldyx"

// TypeAlertSent is the one event type this package emits.
//
// A new type under a new source, which agent-passport SPEC.md section 6.2
// permits without a schema change ("new types may be added freely within a
// source", and v0.2's `source` is an open string rather than the closed enum
// v0.1 had). The registry's own table does not list it yet: adding a row there
// is an edit to a normative document that eight other repositories conform to,
// and that is a decision, not a side effect of this change.
const TypeAlertSent = "alert_sent"

// Kind distinguishes the three things this process sends, so a reader of the
// journal can tell "we woke somebody" from "we sent the daily summary".
type Kind string

const (
	KindAlert       Kind = "alert"
	KindDigest      Kind = "digest"
	KindSuppression Kind = "suppression"
)

// maxErrorChars bounds what a delivery failure contributes. An SMTP server's
// refusal is useful ("550 5.7.1 relay denied") and is written down; a server
// that answers with a kilobyte of prose does not get to fill an audit trail
// with it.
const maxErrorChars = 200

// Journal appends one chained agent-event per message sent.
type Journal struct {
	w *event.ChainedWriter
	// Failures counts records that could not be written, ever, for this
	// process. Surfaced rather than hidden: a journal that stopped recording
	// is worth an operator's attention, and it is not a reason to stop
	// sending mail.
	Failures int
}

// Open returns a journal appending to path, or a disabled journal when path
// is empty.
//
// A disabled journal is a real, working object whose methods do nothing. The
// alternative, a nil pointer every call site has to remember to check, is how
// a nil dereference reaches production down a path that only runs when
// somebody turned recording off.
func Open(path string) (*Journal, error) {
	if strings.TrimSpace(path) == "" {
		return &Journal{}, nil
	}
	// The directory before the file, because on a fresh volume nothing has
	// created it yet and this is the first thing that wants to write there.
	// Found by a test rather than by reasoning: the journal opened before the
	// state file did, so on a path one level deeper than the mount point it
	// failed, logged, and left the box sending mail it never recorded.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return &Journal{}, fmt.Errorf("record: mkdir %s: %w", filepath.Dir(path), err)
	}
	w, err := event.NewChainedWriter(path)
	if err != nil {
		return &Journal{}, fmt.Errorf("record: open %s: %w", path, err)
	}
	return &Journal{w: w}, nil
}

// Enabled reports whether anything is actually being written.
func (j *Journal) Enabled() bool { return j != nil && j.w != nil }

// Close closes the underlying file.
func (j *Journal) Close() error {
	if !j.Enabled() {
		return nil
	}
	return j.w.Close()
}

// Dispatch is one message this process tried to send.
type Dispatch struct {
	Kind Kind
	// AgentID and RunID identify what the message was about. The envelope
	// REQUIRES an agent id, so a dispatch with none is not recorded rather
	// than recorded under an invented one (see [Journal.Sent]).
	AgentID string
	RunID   string
	// About is the dedup key the message was raised under
	// (`{type}:{subject}`), which is also the id in the mail's own link, so a
	// record and a mail can be lined up without translation.
	About string
	// To is who was written to.
	To []string
	// Transport names what carried it: "smtp", "file", or "discard".
	Transport string
	// Err is the delivery error, or nil when the transport accepted it.
	Err error
}

// Sent records one dispatch at time now.
//
// The outcome word is "accepted", never "delivered", and the distinction is
// not pedantry: what this process observes is a mail server taking the
// message. Whether it reached a mailbox, a spam folder or a silently
// discarding filter is not knowable from here, and an audit trail that claims
// the stronger fact is worse than one that admits the weaker one.
//
// The run identifier is copied and never synthesised, which is the same rule as
// the agent id one paragraph down and has a visible cost: the record plane
// refuses a line with no run, by name, and counts it. That cost is the correct
// one to pay. A synthesised run would put this dispatch in a run it had nothing
// to do with, or invent a run that never executed, and either is a false answer
// to "what happened in run R" for as long as the store keeps the record.
func (j *Journal) Sent(d Dispatch, now time.Time) {
	if !j.Enabled() {
		return
	}
	// The envelope requires an agent id and this package will not invent one,
	// the same rule every producer in the stack follows. A dispatch with no
	// attributed agent is counted, so the gap is visible rather than silent.
	if strings.TrimSpace(d.AgentID) == "" {
		j.Failures++
		return
	}

	data := map[string]any{
		"kind":      string(d.Kind),
		"about":     d.About,
		"to":        d.To,
		"transport": d.Transport,
		"outcome":   "accepted",
	}
	if d.Err != nil {
		data["outcome"] = "refused"
		data["error"] = truncate(d.Err.Error(), maxErrorChars)
	}

	e := event.Event{
		Schema:   event.SchemaV02,
		TS:       now.UTC().Format(time.RFC3339),
		Source:   Source,
		Type:     TypeAlertSent,
		AgentID:  d.AgentID,
		RunID:    d.RunID,
		Severity: event.SeverityInfo,
		Data:     data,
	}
	if err := j.w.Write(e); err != nil {
		// The mail already went. Failing to write it down is worth counting
		// and worth reporting, and it is not worth stopping for.
		j.Failures++
	}
}

func truncate(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
