package watch

import (
	"bufio"
	"fmt"
	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/heraldyx/internal/stream"
	"os"
	"path/filepath"
	"strings"
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
	a := filepath.Join(dir, "tokenfuse.ndjson")
	b := filepath.Join(dir, "tokenfuse-cloud.ndjson")
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

// The defect this was written for, and the most expensive one this component
// has had: a poll where the file is not in the resolved set used to throw the
// read position away, so the poll after it re-read the log from byte zero and
// mailed the operator its whole history again.
//
// Resolving the set can come back short for a moment: a directory that cannot
// be stat'ed once, a mount not yet visible. That is a blink, not a new file.
func TestAPathOutOfSightForOnePollKeepsItsPlace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tokenfuse.ndjson")
	write(t, p, line("r1"))

	w := New([]string{p}, nil)
	if got := w.Poll(); len(got) != 1 {
		t.Fatalf("first read: %+v", got)
	}
	at := w.Offsets()[p]
	if at == 0 {
		t.Fatal("nothing was read")
	}

	// One blind poll: the resolver came back with nothing.
	w.SetPaths(nil)
	if len(w.Poll()) != 0 {
		t.Fatal("watching nothing must read nothing")
	}
	// And the file is back.
	w.SetPaths([]string{p})
	if got := w.Poll(); len(got) != 0 {
		t.Fatalf("re-read %d event(s) that had already been read", len(got))
	}
	if w.Offsets()[p] != at {
		t.Fatalf("position moved: %d, was %d", w.Offsets()[p], at)
	}
}

// The other half of the same rule: a file genuinely replaced is still read
// from the start, because its size is smaller than the position held for it.
// Keeping a position is only safe because this case is caught here.
func TestAReplacedFileIsStillReadFromTheStart(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tokenfuse.ndjson")
	write(t, p, line("r1")+line("r2"))

	w := New([]string{p}, nil)
	if got := w.Poll(); len(got) != 2 {
		t.Fatalf("first read: %+v", got)
	}
	// Rotated away and started again, shorter than what was read before.
	if err := os.WriteFile(p, []byte(line("r3")), 0o600); err != nil {
		t.Fatal(err)
	}
	got := w.Poll()
	if len(got) != 1 || got[0].RunID != "r3" {
		t.Fatalf("%+v", got)
	}
	if w.Truncations != 1 {
		t.Fatalf("truncations: %d", w.Truncations)
	}
}

// A plane that appends more than maxBytesPerPoll's worth of lines inside one
// poll interval must not be read in one buffer: that is an unbounded read
// held against a process an operator relies on to say something is wrong,
// and a looping or compromised producer is exactly the case where the box
// that would tell them is the one that gets OOM-killed for trying.
//
// One Poll() must stop at the cap (offset advanced no further than the cap,
// fewer lines delivered than were written), the next Poll() must pick up the
// remainder with nothing lost and nothing delivered twice, and the Capped
// counter must show the cap was actually hit.
func TestAFileGrowingPastTheCapIsReadInPieces(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")

	oneLine := line("r0000000")
	lineLen := int64(len(oneLine))
	total := int(maxBytesPerPoll/lineLen) + 1000

	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	bw := bufio.NewWriter(f)
	for i := 0; i < total; i++ {
		if _, err := bw.WriteString(line(fmt.Sprintf("r%07d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	w := New([]string{p}, nil)
	first := w.Poll()

	if w.Capped != 1 {
		t.Fatalf("want the cap counter at 1 after a capped poll, got %d", w.Capped)
	}
	if off := w.Offsets()[p]; off > maxBytesPerPoll {
		t.Fatalf("one poll advanced the offset %d bytes, past the %d byte cap", off, maxBytesPerPoll)
	}
	if len(first) >= total {
		t.Fatalf("one poll delivered all %d lines; the cap capped nothing", total)
	}

	second := w.Poll()
	if len(first)+len(second) != total {
		t.Fatalf("lost or duplicated across two polls: %d + %d != %d", len(first), len(second), total)
	}
	if w.Capped != 1 {
		t.Fatalf("the second, smaller poll must not add to the cap counter, got %d", w.Capped)
	}
	seen := make(map[string]bool, total)
	for _, e := range first {
		if seen[e.RunID] {
			t.Fatalf("run %s delivered twice within the first poll", e.RunID)
		}
		seen[e.RunID] = true
	}
	for _, e := range second {
		if seen[e.RunID] {
			t.Fatalf("run %s delivered twice across the two polls", e.RunID)
		}
		seen[e.RunID] = true
	}
	if len(seen) != total {
		t.Fatalf("want %d distinct runs delivered, got %d", total, len(seen))
	}
}

// The negative control for the cap: a burst that fits under it in one poll
// must be delivered whole and must not touch the Capped counter. Without
// this, a cap that fired on every poll regardless of size would still pass
// the test above.
func TestABurstUnderTheCapIsReadWholeInOnePoll(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	const total = 500

	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	bw := bufio.NewWriter(f)
	for i := 0; i < total; i++ {
		if _, err := bw.WriteString(line(fmt.Sprintf("s%07d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	w := New([]string{p}, nil)
	got := w.Poll()
	if len(got) != total {
		t.Fatalf("want %d events read whole, got %d", total, len(got))
	}
	if w.Capped != 0 {
		t.Fatalf("a burst under the cap must not touch the cap counter, got %d", w.Capped)
	}
}

// One line longer than the cap must not freeze the file. Under the cap alone
// a full buffer with no newline in it is "a write in progress", the offset
// stays where it was, and the next poll reads the same first cap of the same
// line forever: Capped climbs, nothing is delivered, and every later event in
// that plane's log is invisible. Before the cap that line was read whole and
// counted malformed, and the file kept flowing. So a completed line the cap
// cannot hold is skipped, counted, and the lines after it arrive.
func TestALineLongerThanTheCapIsSkippedAndTheFileKeepsFlowing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")

	blob := strings.Repeat("x", int(maxBytesPerPoll)+1)
	huge := `{"schema":"taipanbox.dev/agent-event/v0.2","ts":"2026-08-02T14:00:00Z","source":"tokenfuse","type":"budget_exhausted","agent_id":"agent://acme/biller","run_id":"huge","severity":"critical","data":{"blob":"` + blob + `"}}` + "\n"
	write(t, p, huge+line("after"))

	w := New([]string{p}, nil)
	var got []event.Event
	for i := 0; i < 4 && len(got) == 0; i++ {
		got = append(got, w.Poll()...)
	}
	if len(got) != 1 || got[0].RunID != "after" {
		t.Fatalf("the line after the oversized one was never delivered: got %d event(s), offset %d, Capped %d, Oversized %d",
			len(got), w.Offsets()[p], w.Capped, w.Oversized)
	}
	if w.Oversized != 1 {
		t.Fatalf("an oversized line must be counted so an operator can see it, got Oversized=%d", w.Oversized)
	}
	// Skipped whole, never delivered in fragments: an implementation that
	// simply advanced the offset by the cap would hand the tail of the huge
	// line to the parser as a malformed line and count it there.
	if w.Malformed != 0 {
		t.Fatalf("the oversized line's tail was parsed as %d malformed line(s); it must be skipped whole", w.Malformed)
	}
	if off, size := w.Offsets()[p], int64(len(huge)+len(line("after"))); off != size {
		t.Fatalf("offset %d after the file was read whole, want %d", off, size)
	}
	// And nothing arrives twice: a further poll delivers nothing.
	if more := w.Poll(); len(more) != 0 {
		t.Fatalf("a poll after the file was consumed delivered %d event(s) again", len(more))
	}
}

func lineFromSource(source, run string) string {
	return `{"schema":"taipanbox.dev/agent-event/v0.2","ts":"2026-08-02T14:00:00Z","source":"` + source + `","type":"policy_deny","agent_id":"agent://acme/biller","run_id":"` + run + `","severity":"high"}` + "\n"
}

// An event whose claimed source the file may not carry is never returned. It
// is counted beside Malformed and kept as a (file, claim) pair for the caller
// to raise once.
func TestAForeignSourceIsNeverReturnedAndIsCounted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, line("r1")+lineFromSource("wardryx", "forged-1")+lineFromSource("wardryx", "forged-2")+lineFromSource("engram", "forged-3"))

	w := New([]string{p}, nil)
	got := w.Poll()
	if len(got) != 1 || got[0].RunID != "r1" {
		t.Fatalf("only the line tokenfuse.ndjson may carry should come back, got %+v", got)
	}
	if w.ForeignSource != 3 || w.Malformed != 0 {
		t.Fatalf("ForeignSource = %d, Malformed = %d, want 3 and 0", w.ForeignSource, w.Malformed)
	}
	pairs := w.TakeForeign()
	if len(pairs) != 2 || pairs[0].Claimed != "engram" || pairs[0].Count != 1 || pairs[1].Claimed != "wardryx" || pairs[1].Count != 2 {
		t.Fatalf("pairs not grouped by (file, claim) in stable order: %+v", pairs)
	}
	if pairs[1].File != p || pairs[1].Stem != "tokenfuse" || len(pairs[1].Allowed) != 1 || pairs[1].Allowed[0] != "tokenfuse" {
		t.Fatalf("a pair must name the file and what it may carry: %+v", pairs[1])
	}
	if again := w.TakeForeign(); len(again) != 0 {
		t.Fatalf("a take must forget what it handed back: %+v", again)
	}
	if w.ForeignSource != 3 {
		t.Fatalf("the counter is for the life of the process, not the take: %d", w.ForeignSource)
	}
}

// What the offset passed is gone: a refused line is not re-read and re-counted
// on the next poll.
func TestARefusedLineIsNotReadAgain(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, lineFromSource("wardryx", "forged-1"))
	w := New([]string{p}, nil)
	w.Poll()
	w.Poll()
	w.Poll()
	if w.ForeignSource != 1 {
		t.Fatalf("the same refused line was counted %d times", w.ForeignSource)
	}
}

// A stream nothing declares is read when the claim is its own name, counted,
// and a claim of any other name is refused.
func TestAnUnknownStreamIsReadAndCountedAndAnotherNameIsRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "newplane.ndjson")
	write(t, p, lineFromSource("newplane", "n1")+lineFromSource("newplane", "n2")+lineFromSource("tokenfuse", "forged"))

	w := New([]string{p}, nil)
	got := w.Poll()
	if len(got) != 2 {
		t.Fatalf("the two lines claiming the stream's own name should be read, got %+v", got)
	}
	if w.UnknownStream != 2 || w.ForeignSource != 1 {
		t.Fatalf("UnknownStream = %d, ForeignSource = %d, want 2 and 1", w.UnknownStream, w.ForeignSource)
	}
	un := w.TakeUnknown()
	if len(un) != 1 || un[0].File != p || un[0].Stem != "newplane" || un[0].Count != 2 {
		t.Fatalf("one entry per file: %+v", un)
	}
}

// A declaration set on the watcher is the rule it enforces.
func TestSetPolicyChangesWhatAFileMayCarry(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.ndjson")
	write(t, p, lineFromSource("tokenfuse", "a")+lineFromSource("wardryx", "b"))

	closed := New([]string{p}, nil)
	if got := closed.Poll(); len(got) != 0 {
		t.Fatalf("an undeclared events.ndjson must not carry other planes' names: %+v", got)
	}

	open := New([]string{p}, nil)
	open.SetPolicy(stream.Default().Extend(map[string][]string{"events": {"tokenfuse", "wardryx"}}))
	if got := open.Poll(); len(got) != 2 || open.ForeignSource != 0 || open.UnknownStream != 0 {
		t.Fatalf("a declared file reads whole and raises nothing: %d events, %d foreign, %d unknown", len(got), open.ForeignSource, open.UnknownStream)
	}
}

// A producer minting a new claimed source per line cannot make the pending
// map the size of its log: the counter keeps counting, the map stops growing.
func TestPendingNoticesAreBounded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	var b strings.Builder
	for i := 0; i < maxPendingNotices+50; i++ {
		b.WriteString(lineFromSource(fmt.Sprintf("forged-%d", i), "r"))
	}
	write(t, p, b.String())
	w := New([]string{p}, nil)
	w.Poll()
	if got := len(w.TakeForeign()); got != maxPendingNotices {
		t.Fatalf("pending pairs = %d, want the bound %d", got, maxPendingNotices)
	}
	if w.ForeignSource != maxPendingNotices+50 {
		t.Fatalf("the counter must still count every refused line: %d", w.ForeignSource)
	}
}

// Hostile bytes are Malformed exactly as before and never reach the source
// check: the rule adds a count beside theirs and removes none.
func TestHostileLinesStillCountAsMalformedNotForeign(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, "not json\n{\"source\":\"wardryx\"}\n\x00\x01\x02\n"+line("ok"))
	w := New([]string{p}, nil)
	got := w.Poll()
	if len(got) != 1 || got[0].RunID != "ok" {
		t.Fatalf("got %+v", got)
	}
	if w.Malformed != 3 || w.ForeignSource != 0 {
		t.Fatalf("Malformed = %d, ForeignSource = %d, want 3 and 0", w.Malformed, w.ForeignSource)
	}
}
