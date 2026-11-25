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
