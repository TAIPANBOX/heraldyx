package watch

import (
	"path/filepath"
	"testing"
)

// The checkpoint gate (F-018) as the watcher sees it: a file the gate does not
// cover yields no events, and the refusal is counted and handed to the caller
// rather than dropped.

func TestAnUncoveredFileIsNeverReturnedAndIsCounted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, line("run-1")+line("run-2"))

	w := New([]string{p}, nil)
	w.SetCoverage(func(string) (bool, string) { return false, "no checkpoint file for this stream" })
	if got := w.Poll(); len(got) != 0 {
		t.Fatalf("an uncovered file returned %d event(s); it must return none", len(got))
	}
	if w.NotCovered != 2 {
		t.Fatalf("want 2 refused events counted, got %d", w.NotCovered)
	}
	got := w.TakeUncovered()
	if len(got) != 1 || got[0].Count != 2 || got[0].Reason != "no checkpoint file for this stream" {
		t.Fatalf("want one refused file with 2 events and its reason, got %+v", got)
	}
	if again := w.TakeUncovered(); len(again) != 0 {
		t.Fatalf("TakeUncovered must forget what it returned, got %+v", again)
	}
}

func TestACoveredFileIsReadAsBefore(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, line("run-1")+line("run-2"))

	w := New([]string{p}, nil)
	w.SetCoverage(func(string) (bool, string) { return true, "covered" })
	if got := w.Poll(); len(got) != 2 {
		t.Fatalf("a covered file must return its events, got %d", len(got))
	}
	if w.NotCovered != 0 || len(w.TakeUncovered()) != 0 {
		t.Fatalf("a covered file must refuse nothing")
	}
}
