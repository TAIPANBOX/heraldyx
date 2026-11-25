package watch

import (
	"path/filepath"
	"testing"
)

// A file the checkpoint gate cannot decide yet is neither read nor refused:
// its lines stay, nothing is counted or said, and they are read once the gate
// decides it covered.
func TestAFileTheGateHasNotDecidedIsHeldNotRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tokenfuse.ndjson")
	write(t, p, line("run-1")+line("run-2"))
	w := New([]string{p}, nil)
	decided := false
	w.SetCoverage(func(string) (bool, string) {
		if !decided {
			return false, Undecided
		}
		return true, ""
	})
	for i := 0; i < 3; i++ {
		if got := w.Poll(); len(got) != 0 {
			t.Fatalf("poll %d returned %d events from a file the gate has not decided", i, len(got))
		}
	}
	if w.NotCovered != 0 || len(w.TakeUncovered()) != 0 || w.Undecided != 3 {
		t.Fatalf("an undecided file was refused: not covered %d, undecided %d", w.NotCovered, w.Undecided)
	}
	decided = true
	if got := w.Poll(); len(got) != 2 {
		t.Fatalf("once covered the held lines gave %d events, want 2", len(got))
	}
}

// A file held for the gate holds only itself: another file is read on, and
// the lines before the event it stopped at are not counted twice.
func TestAHeldFileHoldsOnlyItselfAndOnlyFromItsEvent(t *testing.T) {
	dir := t.TempDir()
	held := filepath.Join(dir, "tokenfuse.ndjson")
	other := filepath.Join(dir, "wardryx.ndjson")
	write(t, held, "not json\n\n"+line("run-1"))
	write(t, other, lineFromSource("wardryx", "run-9"))
	w := New([]string{held, other}, nil)
	decided := false
	w.SetCoverage(func(p string) (bool, string) {
		if p == held && !decided {
			return false, Undecided
		}
		return true, ""
	})
	if got := w.Poll(); len(got) != 1 || got[0].RunID != "run-9" {
		t.Fatalf("the other file while one is held: %+v", got)
	}
	if got := w.Poll(); len(got) != 0 {
		t.Fatalf("a second poll while held: %d events", len(got))
	}
	malformed := w.Malformed
	decided = true
	got := w.Poll()
	if len(got) != 1 || got[0].RunID != "run-1" {
		t.Fatalf("once decided: %+v", got)
	}
	if w.Malformed != malformed {
		t.Fatalf("the malformed line before the held event was counted again: %d then %d", malformed, w.Malformed)
	}
}
