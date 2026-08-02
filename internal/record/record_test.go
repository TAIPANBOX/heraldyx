package record

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

var now = time.Date(2026, 8, 2, 3, 14, 0, 0, time.UTC)

func dispatch() Dispatch {
	return Dispatch{
		Kind:      KindAlert,
		AgentID:   "agent://acme.example/biller",
		RunID:     "run-42",
		About:     "budget_exhausted:run-42",
		To:        []string{"ops@example.com"},
		Transport: "smtp",
	}
}

func read(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line is not JSON: %v\n%s", err, line)
		}
		out = append(out, m)
	}
	return out
}

func TestASentMessageIsRecordedInTheSharedEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.ndjson")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	j.Sent(dispatch(), now)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	got := read(t, path)
	if len(got) != 1 {
		t.Fatalf("want one record, got %d", len(got))
	}
	r := got[0]
	if r["source"] != "heraldyx" || r["type"] != "alert_sent" {
		t.Fatalf("wrong envelope identity: %v", r)
	}
	if r["agent_id"] != "agent://acme.example/biller" || r["run_id"] != "run-42" {
		t.Fatalf("the record must name what the message was about: %v", r)
	}
	data := r["data"].(map[string]any)
	if data["about"] != "budget_exhausted:run-42" {
		t.Fatalf("about: %v", data["about"])
	}
	// It names who was written to. This journal never leaves the box, and an
	// operator proving they were told needs the address, not a hash of it.
	if to := data["to"].([]any); len(to) != 1 || to[0] != "ops@example.com" {
		t.Fatalf("to: %v", data["to"])
	}
}

// The one word in this package that is easy to get wrong and expensive to have
// wrong. What is observed is a mail server taking the message.
func TestTheOutcomeIsAcceptedNotDelivered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.ndjson")
	j, _ := Open(path)
	j.Sent(dispatch(), now)
	j.Close()

	data := read(t, path)[0]["data"].(map[string]any)
	if data["outcome"] != "accepted" {
		t.Fatalf("outcome: %v", data["outcome"])
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(strings.ToLower(string(raw)), "delivered") {
		t.Fatal("this process cannot observe delivery and must not claim it")
	}
}

func TestARefusalIsRecordedWithItsReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.ndjson")
	j, _ := Open(path)
	d := dispatch()
	d.Err = errors.New("deliver: smtp send: 550 5.7.1 relay denied")
	j.Sent(d, now)
	j.Close()

	data := read(t, path)[0]["data"].(map[string]any)
	if data["outcome"] != "refused" {
		t.Fatalf("outcome: %v", data["outcome"])
	}
	if !strings.Contains(data["error"].(string), "550 5.7.1") {
		t.Fatalf("the reason a mail server gave is the useful part: %v", data["error"])
	}
}

// A server that answers with a kilobyte of prose does not get to fill an audit
// trail with it.
func TestALongRefusalIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.ndjson")
	j, _ := Open(path)
	d := dispatch()
	d.Err = errors.New(strings.Repeat("very verbose ", 500))
	j.Sent(d, now)
	j.Close()

	data := read(t, path)[0]["data"].(map[string]any)
	if len(data["error"].(string)) > maxErrorChars+3 {
		t.Fatalf("unbounded error text reached the record: %d chars", len(data["error"].(string)))
	}
}

// The rule every producer in this stack follows: the envelope requires an
// agent id, and none of them is invented.
func TestADispatchWithNoAgentIsCountedNotInvented(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.ndjson")
	j, _ := Open(path)
	d := dispatch()
	d.AgentID = "  "
	j.Sent(d, now)
	j.Close()

	if _, err := os.Stat(path); err == nil {
		if len(read(t, path)) != 0 {
			t.Fatal("a record was written with no attributed agent")
		}
	}
	if j.Failures != 1 {
		t.Fatalf("the gap must be counted, got %d", j.Failures)
	}
}

// The property this journal exists for: nobody can quietly change or shorten
// it. Written by the estate's own chained writer, verified by the estate's own
// verifier.
func TestTheJournalIsAChainAndTheVerifierAgrees(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.ndjson")
	j, _ := Open(path)
	for i := range 3 {
		d := dispatch()
		d.About = "budget_exhausted:run-" + string(rune('a'+i))
		j.Sent(d, now.Add(time.Duration(i)*time.Minute))
	}
	j.Close()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	report, err := event.VerifyChain(f)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ok() || len(report.Breaks) != 0 {
		t.Fatalf("a freshly written journal must verify clean: %+v", report)
	}
	if report.Chained != 2 {
		t.Fatalf("three records means two links, got %d", report.Chained)
	}

	// And a quiet edit does not survive it.
	raw, _ := os.ReadFile(path)
	tampered := strings.Replace(string(raw), "ops@example.com", "nobody@example.com", 1)
	rep2, err := event.VerifyChain(strings.NewReader(tampered))
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Ok() {
		t.Fatal("changing a recipient after the fact must break the chain")
	}
}

// A restart must continue the same chain rather than starting a second one in
// the same file: the container restarts on every rollout.
func TestARestartResumesTheChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.ndjson")
	j1, _ := Open(path)
	j1.Sent(dispatch(), now)
	j1.Close()

	j2, _ := Open(path)
	j2.Sent(dispatch(), now.Add(time.Minute))
	j2.Close()

	f, _ := os.Open(path)
	defer f.Close()
	report, err := event.VerifyChain(f)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ok() {
		t.Fatalf("the chain did not survive a restart: %+v", report)
	}
	if len(report.HeadLines) != 1 {
		t.Fatalf("a restart must not begin a second chain in the same file: heads at %v", report.HeadLines)
	}
	got := read(t, path)
	if len(got) != 2 || got[1]["prev_hash"] == nil {
		t.Fatalf("the second record must link to the first: %v", got)
	}
}

// Recording off is a configuration, not a failure.
func TestAnEmptyPathDisablesRecordingWithoutFailing(t *testing.T) {
	j, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	if j.Enabled() {
		t.Fatal("empty path must disable")
	}
	j.Sent(dispatch(), now) // must not panic
	if j.Failures != 0 {
		t.Fatalf("a disabled journal records no failures, got %d", j.Failures)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}
