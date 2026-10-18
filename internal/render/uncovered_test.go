package render

import (
	"strings"
	"testing"
)

// The notice a refused stream raises: it names the file, the count and the
// fixed reason, and it says which setting decides it, so it is never a silent gap.
func TestTheUncoveredNoticeSaysWhatWasRefusedAndWhy(t *testing.T) {
	m := UncoveredStream(cfg(), Uncovered{
		File:   "/var/lib/stack/events/wardryx.ndjson",
		Reason: "no checkpoint file for this stream",
		Count:  4,
	}, now)
	for _, want := range []string{"wardryx.ndjson", "signed checkpoint", "4 event(s)", "no checkpoint file for this stream", "HERALDYX_CHECKPOINT_GATE", "not processed"} {
		if !strings.Contains(m.Subject+m.Body, want) {
			t.Errorf("the notice must contain %q:\n%s\n%s", want, m.Subject, m.Body)
		}
	}
	if strings.Contains(m.Subject, "/var/lib") {
		t.Errorf("the subject must name the file, not its path:\n%s", m.Subject)
	}
}

// A count of one reads as one event, not as "1 events" and not as "1 lines".
func TestASingleLineIsSaidInTheSingular(t *testing.T) {
	m := Event(cfg(), witness(t, "witness_stream_rewritten", "high", map[string]any{
		"stream": "wardryx.ndjson", "lines": 1, "bytes": 90,
	}), now, "", nil)
	if !strings.Contains(m.Body, "1 line,") && !strings.Contains(m.Body, "1 line.") {
		t.Errorf("one line must read as one line:\n%s", m.Body)
	}
}
