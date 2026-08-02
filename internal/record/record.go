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
// # Why this is not sent to trailryx directly
//
// The record plane's network ingest is OTLP over HTTP with a protobuf body.
// Speaking it would mean an HTTP client and a protobuf encoder inside the one
// process in the box that has a way out, which is exactly the thing
// `scripts/one-way-out.sh` exists to prevent. So this process produces the
// record and a component that is allowed to make that hop ships it. What is
// written here is already sealed and already verifiable; the hop is transport.
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
