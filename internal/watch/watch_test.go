package watch

import (
	"os"
	"path/filepath"
	"testing"
)

func line(run string) string {
	return `{"schema":"taipanbox.dev/agent-event/v0.2","ts":"2026-08-02T14:00:00Z","source":"tokenfuse","type":"budget_exhausted","agent_id":"agent://acme/biller","run_id":"` + run + `","severity":"critical"}` + "\n"
}

func write(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestReadsOnlyWhatIsNew(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, line("run-1"))
	w := New([]string{p}, nil)

	if got := w.Poll(); len(got) != 1 || got[0].RunID != "run-1" {
		t.Fatalf("first poll: %+v", got)
	}
	if got := w.Poll(); len(got) != 0 {
		t.Fatalf("nothing was appended, want no events, got %d", len(got))
	}
	write(t, p, line("run-2"))
	if got := w.Poll(); len(got) != 1 || got[0].RunID != "run-2" {
		t.Fatalf("second poll: %+v", got)
	}
}

// The writer on the other side is appending. Half an event parsed now is an
// event lost forever, so the offset must not advance past an unfinished line.
func TestAPartialLineIsNotConsumed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	full := line("run-1")
	write(t, p, full[:len(full)/2]) // half a line, no newline

	w := New([]string{p}, nil)
	if got := w.Poll(); len(got) != 0 {
		t.Fatalf("a partial line must not be read: %+v", got)
	}
	if w.Malformed != 0 {
		t.Fatalf("a partial line is not malformed, it is unfinished (counted %d)", w.Malformed)
	}

	write(t, p, full[len(full)/2:]) // the rest arrives
	got := w.Poll()
	if len(got) != 1 || got[0].RunID != "run-1" {
		t.Fatalf("the completed line must be read exactly once: %+v", got)
	}
}

// Rotation is not an error, and it must not leave the reader seeking past the
// end of a fresh file forever.
func TestTruncationRestartsFromTheBeginning(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, line("run-1")+line("run-2"))
	w := New([]string{p}, nil)
	if got := w.Poll(); len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if err := os.WriteFile(p, []byte(line("run-3")), 0o600); err != nil {
		t.Fatal(err)
	}
	got := w.Poll()
	if len(got) != 1 || got[0].RunID != "run-3" {
		t.Fatalf("after truncation: %+v", got)
	}
	if w.Truncations != 1 {
		t.Fatalf("truncation should be counted, got %d", w.Truncations)
	}
}

// A producer writing something this build cannot read is a fact to surface,
// not a reason to stop watching the rest of the file.
func TestMalformedLinesAreCountedAndSkipped(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, "not json at all\n"+line("run-1")+`{"schema":"x"}`+"\n")
	w := New([]string{p}, nil)
	got := w.Poll()
	if len(got) != 1 || got[0].RunID != "run-1" {
		t.Fatalf("the good line must still be read: %+v", got)
	}
	if w.Malformed != 2 {
		t.Fatalf("want 2 malformed counted, got %d", w.Malformed)
	}
}

// A plane that is not deployed writes no file, and that is not an error.
func TestAMissingFileIsNotAnError(t *testing.T) {
	w := New([]string{filepath.Join(t.TempDir(), "never-created.ndjson")}, nil)
	if got := w.Poll(); len(got) != 0 {
		t.Fatalf("want nothing, got %+v", got)
	}
}

// A first run must not mail a month of history.
func TestSkipToEndReadsNoHistory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, line("old-1")+line("old-2"))
	w := New([]string{p}, nil)
	if err := w.SkipToEnd(); err != nil {
		t.Fatal(err)
	}
	if got := w.Poll(); len(got) != 0 {
		t.Fatalf("history must not be read: %+v", got)
	}
	write(t, p, line("new-1"))
	if got := w.Poll(); len(got) != 1 || got[0].RunID != "new-1" {
		t.Fatalf("what happens after the start must be read: %+v", got)
	}
}

// A plane deployed later appears as a new file, and it is read from its start.
func TestSetPathsKeepsOffsetsAndPicksUpNewFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.ndjson")
	b := filepath.Join(dir, "b.ndjson")
	write(t, a, line("a-1"))

	w := New([]string{a}, nil)
	if got := w.Poll(); len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	write(t, b, line("b-1"))
	w.SetPaths([]string{a, b})
	got := w.Poll()
	if len(got) != 1 || got[0].RunID != "b-1" {
		t.Fatalf("the new file should be read, the old one not re-read: %+v", got)
	}
}
