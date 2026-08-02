package record

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// Status is what the journal says about itself.
//
// This exists because of how the notifier is packaged: a distroless image with
// no shell, which is the right posture for the one process in the box with a
// way out, and which also means an operator cannot `cat` the file or an
// auditor `wc -l` it. Without this they would have to copy the volume out to
// answer "did it write to me, and can I still trust the record". So the
// process that owns the file reports on it.
//
// It reports; it does not repair. A broken chain is stated and never fixed:
// the whole value of the file is that nothing quietly changes it, and a tool
// that tidied a break would be the thing that quietly changed it.
type Status struct {
	Path string
	// Present is false when nothing has been recorded yet, which is the
	// ordinary state of a box that has had nothing to say.
	Present bool
	Records int
	// ByKind counts alert/digest/suppression.
	ByKind map[string]int
	// Accepted and Refused split the outcomes.
	Accepted int
	Refused  int
	// Chain is the verifier's own report.
	Chained      int
	Heads        int
	Malformed    int
	Breaks       int
	Unverifiable int
	// Last is a one-line summary of the most recent record.
	Last string
}

// Ok reports whether the journal is intact: no chain break, no malformed line.
func (s Status) Ok() bool { return s.Breaks == 0 && s.Malformed == 0 }

// ReadStatus reads the journal at path and summarises it.
//
// A missing file is not an error. Recording can be off, and a box that has
// never had anything to say has never written a line; both are ordinary and
// neither is a fault to report as one.
func ReadStatus(path string) (Status, error) {
	s := Status{Path: path, ByKind: map[string]int{}}
	if strings.TrimSpace(path) == "" {
		return s, nil
	}

	f, err := os.Open(path) // #nosec G304 -- operator-supplied path
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return s, fmt.Errorf("record: read %s: %w", path, err)
	}
	defer f.Close()

	events, err := event.ReadFile(path)
	if err != nil {
		return s, fmt.Errorf("record: read %s: %w", path, err)
	}
	s.Present = true
	s.Records = len(events)
	for _, e := range events {
		kind, _ := e.Data["kind"].(string)
		if kind == "" {
			kind = "unknown"
		}
		s.ByKind[kind]++
		switch outcome, _ := e.Data["outcome"].(string); outcome {
		case "accepted":
			s.Accepted++
		case "refused":
			s.Refused++
		}
	}
	if len(events) > 0 {
		last := events[len(events)-1]
		about, _ := last.Data["about"].(string)
		outcome, _ := last.Data["outcome"].(string)
		to := recipients(last)
		s.Last = fmt.Sprintf("%s  %s -> %s (%s)", last.TS, about, to, outcome)
	}

	report, err := event.VerifyChain(f)
	if err != nil {
		return s, fmt.Errorf("record: verify %s: %w", path, err)
	}
	s.Chained = report.Chained
	s.Heads = len(report.HeadLines)
	s.Malformed = report.Malformed
	s.Breaks = len(report.Breaks)
	s.Unverifiable = len(report.Unverifiable)
	return s, nil
}

// String renders the status for a terminal.
func (s Status) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "journal: %s\n", or(s.Path, "(recording is off)"))
	if !s.Present {
		b.WriteString("records: none yet\n")
		return b.String()
	}
	fmt.Fprintf(&b, "records: %d (%s)\n", s.Records, kinds(s.ByKind))
	fmt.Fprintf(&b, "outcome: %d accepted, %d refused\n", s.Accepted, s.Refused)

	chain := fmt.Sprintf("%d chained, %d head(s)", s.Chained, s.Heads)
	switch {
	case s.Breaks > 0:
		fmt.Fprintf(&b, "chain:   BROKEN, %d mismatch(es) (%s)\n", s.Breaks, chain)
	case s.Malformed > 0:
		fmt.Fprintf(&b, "chain:   %d malformed line(s) (%s)\n", s.Malformed, chain)
	case s.Chained == 0:
		// A chain of one binds nothing: the first record has no predecessor to
		// hash, so editing it is undetectable HERE and the report must not
		// imply otherwise. Reporting "verifies" for a single line would be a
		// check that cannot fail, which is worse than no check, because it is
		// louder.
		fmt.Fprintf(&b, "chain:   nothing to verify yet: %d record(s), and a chain of one binds nothing\n", s.Records)
	default:
		fmt.Fprintf(&b, "chain:   verifies (%s)\n", chain)
	}
	if s.Unverifiable > 0 {
		fmt.Fprintf(&b, "         %d line(s) unverifiable, the line before them did not parse\n", s.Unverifiable)
	}
	if s.Last != "" {
		fmt.Fprintf(&b, "last:    %s\n", s.Last)
	}
	return b.String()
}

func recipients(e event.Event) string {
	raw, ok := e.Data["to"].([]any)
	if !ok || len(raw) == 0 {
		return "(nobody)"
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", ")
}

func kinds(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func or(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
