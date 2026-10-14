package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/heraldyx/internal/watch"
)

// laggingBox is a box whose dedup window is off, so a second read of the same
// event shows as a second message rather than being hidden by dedup.
func laggingBox(t *testing.T) (events, mail, statePath string) {
	t.Helper()
	dir := t.TempDir()
	events = filepath.Join(dir, "tokenfuse.ndjson")
	mail = filepath.Join(dir, "mail.txt")
	statePath = filepath.Join(dir, "state.json")
	env(t, map[string]string{
		"HERALDYX_EVENTS":        events,
		"HERALDYX_TO":            "ops@example.com",
		"HERALDYX_MAIL_FILE":     mail,
		"HERALDYX_MIN_SEVERITY":  "high",
		"HERALDYX_STATE":         statePath,
		"HERALDYX_DEDUP_SECONDS": "0",
	})
	return events, mail, statePath
}

func runOnce(t *testing.T) {
	t.Helper()
	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatal(err)
	}
}

func messages(t *testing.T, mail string) int {
	t.Helper()
	b, err := os.ReadFile(mail)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "Subject:")
}

// The process restarts on a node whose view of the shared volume is behind:
// the event log looks shorter than the position the state file holds. The
// alerts already mailed must not be mailed again, and when the view catches
// up only what is new is mailed. The log says what happened.
func TestARestartOnALaggingVolumeDoesNotMailOldAlertsAgain(t *testing.T) {
	events, mail, _ := laggingBox(t)
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	l1 := ndjson("budget_exhausted", "critical", "run-1", "")
	l2 := ndjson("budget_exhausted", "critical", "run-2", "")
	l3 := ndjson("budget_exhausted", "critical", "run-3", "")
	write(t, events, l1+l2)
	runOnce(t)
	if n := messages(t, mail); n != 2 {
		t.Fatalf("want 2 messages for 2 events, got %d", n)
	}

	if err := os.Truncate(events, int64(len(l1))); err != nil {
		t.Fatal(err)
	}
	runOnce(t)
	if n := messages(t, mail); n != 2 {
		t.Fatalf("a restart onto a lagging view mailed %d message(s) again", n-2)
	}
	if !strings.Contains(out.String(), "looked shorter than what had already been read") {
		t.Fatalf("the log does not say the file looked shorter:\n%s", out.String())
	}

	if err := os.WriteFile(events, []byte(l1+l2+l3), 0o600); err != nil {
		t.Fatal(err)
	}
	runOnce(t)
	if n := messages(t, mail); n != 3 {
		t.Fatalf("after the view caught up, want exactly one new message (3 in all), got %d in all", n)
	}
	if !strings.Contains(read(t, mail), "run-3") {
		t.Fatal("the new message is not about the new event")
	}
}

// What the state file keeps is which file the position belongs to, not only
// how far into it: a file replaced while the process was down is read from
// its start on the next run, at once.
func TestARestartRemembersWhichFileItWasReading(t *testing.T) {
	events, mail, statePath := laggingBox(t)
	write(t, events, ndjson("budget_exhausted", "critical", "run-1", "")+ndjson("budget_exhausted", "critical", "run-2", ""))
	runOnce(t)

	var snap struct {
		Heads map[string]string `json:"heads"`
	}
	if err := json.Unmarshal([]byte(read(t, statePath)), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Heads[events] == "" {
		t.Fatalf("the state file does not remember the first line of %s: %v", events, snap.Heads)
	}

	if err := os.WriteFile(events, []byte(ndjson("budget_exhausted", "critical", "run-9", "")), 0o600); err != nil {
		t.Fatal(err)
	}
	runOnce(t)
	if n := messages(t, mail); n != 3 {
		t.Fatalf("want the replaced file's one event mailed at once (3 in all), got %d", n)
	}
	if !strings.Contains(read(t, mail), "run-9") {
		t.Fatal("the replaced file's event was not mailed")
	}
}

// Each of the four holds and the reset is said once per growth, not once per
// poll and not never: the same discipline as sayVolume.
func TestHoldsAndSkipsAreSaidOncePerGrowth(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	said := watch.Holds{}
	steps := []watch.Holds{
		{},
		{Shorter: 1},
		{Shorter: 1},
		{Shorter: 1, ShorterExpired: 1},
		{Truncations: 1, Shorter: 1, ShorterExpired: 1},
		{Truncations: 1, Shorter: 1, ShorterExpired: 1, ZeroHeld: 2},
		{Truncations: 1, Shorter: 1, ShorterExpired: 1, ZeroHeld: 2, ZeroSkipped: 1},
		{Truncations: 1, Shorter: 1, ShorterExpired: 1, ZeroHeld: 2, ZeroSkipped: 1},
	}
	for _, h := range steps {
		said = sayHolds(h, said)
	}
	if said != steps[len(steps)-1] {
		t.Fatalf("the high-water mark is wrong: %+v", said)
	}
	got := out.String()
	for _, want := range []string{
		"1 file(s) looked shorter than what had already been read (1 since this process started)",
		"1 file(s) stayed shorter than what had been read for 5m0s and are read again from the start (1 since this process started)",
		"1 file(s) are a different file from the one being read (its first line changed) and are read from the start (1 since this process started)",
		"2 line(s) with a zero byte in them were met (2 since this process started)",
		"1 line(s) with a zero byte in them were still there after 5m0s and were skipped (1 since this process started)",
	} {
		if n := strings.Count(got, want); n != 1 {
			t.Errorf("want %q said once, said %d time(s)", want, n)
		}
	}
	if n := strings.Count(got, "\n"); n != 5 {
		t.Fatalf("want 5 lines, one per growth of one counter, got %d:\n%s", n, got)
	}
}
