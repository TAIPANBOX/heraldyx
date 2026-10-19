package watch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/v2/event"
)

// These tests are about a reader on a shared volume that is behind its
// writer. On Kubernetes the event log sits on a Longhorn RWX volume, which is
// NFS: a reader on another node can be told an old size for minutes, and the
// writer's own node can hold bytes the server has not been sent yet. Neither
// is a truncation, and neither may cost the operator a second copy of mail
// they already had, or an event they never get.

// clock is a hand-wound clock for the hold window.
type clock struct{ t time.Time }

func (c *clock) Now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{t: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)} }

// sizedInfo is a real FileInfo whose size says something else.
type sizedInfo struct {
	fs.FileInfo
	size int64
}

func (s sizedInfo) Size() int64 { return s.size }

// statSaying makes the watcher's stat report size, while the file itself is
// untouched: what a reader on another node is told while its attribute cache
// is behind.
func statSaying(size int64) func(string) (fs.FileInfo, error) {
	return func(p string) (fs.FileInfo, error) {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		return sizedInfo{info, size}, nil
	}
}

func runs(events []event.Event) []string {
	out := []string{}
	for _, e := range events {
		out = append(out, e.RunID)
	}
	return out
}

func wantRuns(t *testing.T, what string, got []event.Event, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if g := runs(got); !reflect.DeepEqual(g, want) {
		t.Fatalf("%s: got runs %v, want %v", what, g, want)
	}
}

// zeroed returns s with bytes [from, to) replaced by zero bytes: what a
// reader sees where a writer's bytes have not reached the volume yet while
// later ones have.
func zeroed(s string, from, to int) string {
	b := []byte(s)
	for i := from; i < to; i++ {
		b[i] = 0
	}
	return string(b)
}

func fill(t *testing.T, path, content string, from, to int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt([]byte(content[from:to]), int64(from)); err != nil {
		t.Fatal(err)
	}
}

func replace(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The volume this reader sees is behind the writer: the file is shorter than
// what was already read from it, and it is the same file by its first line.
// That is not a truncation. Reading it again from byte zero mails the
// operator every alert in it a second time.
func TestAFileThatLooksShorterButIsTheSameFileIsNotReadAgain(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	l1, l2 := line("r1"), line("r2")
	write(t, p, l1+l2)
	w := New([]string{p}, nil)
	wantRuns(t, "first poll", w.Poll(), "r1", "r2")

	if err := os.Truncate(p, int64(len(l1))); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		wantRuns(t, fmt.Sprintf("poll %d while the file looks shorter", i), w.Poll())
	}
	if w.Truncations != 0 {
		t.Fatalf("a file that only looks shorter was counted as truncated %d time(s)", w.Truncations)
	}
	if w.Shorter != 1 {
		t.Fatalf("one stretch of looking shorter is counted once, got %d", w.Shorter)
	}
	if off := w.Offsets()[p]; off != int64(len(l1+l2)) {
		t.Fatalf("the read position moved to %d, want it kept at %d", off, len(l1+l2))
	}
}

// And when the writer's bytes reach the volume, reading resumes exactly where
// it stopped: the new line, and nothing that was read before.
func TestReadingResumesWhereItStoppedWhenTheFileCatchesUp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	l1, l2, l3 := line("r1"), line("r2"), line("r3")
	write(t, p, l1+l2)
	w := New([]string{p}, nil)
	wantRuns(t, "first poll", w.Poll(), "r1", "r2")

	if err := os.Truncate(p, int64(len(l1))); err != nil {
		t.Fatal(err)
	}
	wantRuns(t, "while the file looks shorter", w.Poll())

	replace(t, p, l1+l2+l3)
	wantRuns(t, "after the file caught up", w.Poll(), "r3")
	if w.Truncations != 0 || w.Shorter != 1 {
		t.Fatalf("Truncations = %d, Shorter = %d, want 0 and 1", w.Truncations, w.Shorter)
	}
}

// A stat that says the file is shorter while the bytes are there is the
// reader's attribute cache, not the file. The data is read through at once,
// whatever the stale size is, and nothing is read twice.
func TestAStaleSizeWithTheDataThereIsReadThroughAtOnce(t *testing.T) {
	l1, l2, l3 := line("r1"), line("r2"), line("r3")
	for _, c := range []struct {
		name string
		size int64
	}{
		{"zero", 0},
		{"one byte", 1},
		{"exactly the first line", int64(len(l1))},
		{"one byte short of what was read", int64(len(l1+l2)) - 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
			write(t, p, l1+l2)
			w := New([]string{p}, nil)
			wantRuns(t, "first poll", w.Poll(), "r1", "r2")

			write(t, p, l3)
			w.stat = statSaying(c.size)
			wantRuns(t, "with a stale size", w.Poll(), "r3")
			if w.Truncations != 0 || w.Shorter != 0 {
				t.Fatalf("Truncations = %d, Shorter = %d, want 0 and 0: the data was there", w.Truncations, w.Shorter)
			}
			w.stat = os.Stat
			wantRuns(t, "once the size is right again", w.Poll())
		})
	}
}

// A quiet file whose size stays stale for longer than the hold window is
// still the same file with nothing new in it. It must never turn into a
// re-read: the window is for a file the data says is shorter, and this one is
// not.
func TestAQuietFileWithAStaleSizeIsNeverReadAgain(t *testing.T) {
	clk := newClock()
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	l1, l2 := line("r1"), line("r2")
	write(t, p, l1+l2)
	w := New([]string{p}, nil)
	w.now = clk.Now
	wantRuns(t, "first poll", w.Poll(), "r1", "r2")

	w.stat = statSaying(int64(len(l1)))
	for i := range 20 {
		wantRuns(t, fmt.Sprintf("poll %d, %d minute(s) into the stale size", i, i), w.Poll())
		clk.advance(time.Minute)
	}
	if w.Truncations != 0 || w.ShorterExpired != 0 || w.Shorter != 0 {
		t.Fatalf("Truncations = %d, ShorterExpired = %d, Shorter = %d, want all 0", w.Truncations, w.ShorterExpired, w.Shorter)
	}
}

// A file that really is a different file is read from its start, whatever its
// size: shorter than what was read, already longer, or emptied first. The
// first line says which file it is, and the size cannot.
func TestAFileReplacedByADifferentOneIsReadFromItsStart(t *testing.T) {
	old := line("old-1") + line("old-2") + line("old-3")
	short := line("new-1")
	var long strings.Builder
	var longRuns []string
	for i := 1; i <= 6; i++ {
		long.WriteString(line(fmt.Sprintf("new-%d", i)))
		longRuns = append(longRuns, fmt.Sprintf("new-%d", i))
	}
	for _, c := range []struct {
		name        string
		emptyFirst  bool
		replacement string
		want        []string
	}{
		{"shorter than what was read", false, short, []string{"new-1"}},
		{"already longer than what was read", false, long.String(), longRuns},
		{"emptied first, then a shorter one", true, short, []string{"new-1"}},
		{"emptied first, then one already longer", true, long.String(), longRuns},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
			write(t, p, old)
			w := New([]string{p}, nil)
			wantRuns(t, "first poll", w.Poll(), "old-1", "old-2", "old-3")
			if c.emptyFirst {
				replace(t, p, "")
				wantRuns(t, "while empty", w.Poll())
			}
			replace(t, p, c.replacement)
			wantRuns(t, "the replacement", w.Poll(), c.want...)
			if w.Truncations != 1 || w.Malformed != 0 {
				t.Fatalf("Truncations = %d, Malformed = %d, want 1 and 0", w.Truncations, w.Malformed)
			}
		})
	}
}

// A file that stays shorter than what was read for the whole hold window is
// read again from its start. That is the old cost, a second copy of mail the
// dedup window and the hourly ceiling then bound, paid only after the window
// and counted so the log can say so: the alternative, waiting forever, would
// silently stop reading a file whose writer lost its tail and carried on.
func TestAFileThatStaysShorterPastTheWindowIsReadAgainFromTheStart(t *testing.T) {
	clk := newClock()
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	l1, l2 := line("r1"), line("r2")
	write(t, p, l1+l2)
	w := New([]string{p}, nil)
	w.now = clk.Now
	wantRuns(t, "first poll", w.Poll(), "r1", "r2")

	if err := os.Truncate(p, int64(len(l1))); err != nil {
		t.Fatal(err)
	}
	wantRuns(t, "the first poll that sees it shorter", w.Poll())
	clk.advance(HoldWindow - time.Second)
	wantRuns(t, "a second before the window ends", w.Poll())
	if w.ShorterExpired != 0 {
		t.Fatalf("expired a second early: %d", w.ShorterExpired)
	}
	clk.advance(time.Second)
	wantRuns(t, "at the end of the window", w.Poll(), "r1")
	if w.ShorterExpired != 1 || w.Truncations != 0 || w.Shorter != 1 {
		t.Fatalf("ShorterExpired = %d, Truncations = %d, Shorter = %d, want 1, 0 and 1", w.ShorterExpired, w.Truncations, w.Shorter)
	}
	wantRuns(t, "after reading it again", w.Poll())
}

// Each stretch of looking shorter has its own window. One that ended must not
// leave its clock running into the next, or the second stretch would be
// treated as already over the window and the file read again at once.
func TestEachStretchOfLookingShorterGetsItsOwnWindow(t *testing.T) {
	clk := newClock()
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	l1, l2 := line("r1"), line("r2")
	write(t, p, l1+l2)
	w := New([]string{p}, nil)
	w.now = clk.Now
	wantRuns(t, "first poll", w.Poll(), "r1", "r2")

	if err := os.Truncate(p, int64(len(l1))); err != nil {
		t.Fatal(err)
	}
	wantRuns(t, "first stretch", w.Poll())
	clk.advance(time.Minute)
	replace(t, p, l1+l2) // caught up, and nothing new
	wantRuns(t, "caught up", w.Poll())

	clk.advance(10 * time.Minute)
	if err := os.Truncate(p, int64(len(l1))); err != nil {
		t.Fatal(err)
	}
	wantRuns(t, "second stretch", w.Poll())
	if w.Shorter != 2 || w.ShorterExpired != 0 {
		t.Fatalf("Shorter = %d, ShorterExpired = %d, want 2 and 0", w.Shorter, w.ShorterExpired)
	}
}

// What a restart knows about which file it was reading is what lets it tell
// a replaced file at once. Without it the same restart can only wait.
func TestAWatcherRestoredWithItsHeadsTellsAReplacedFileAtOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, line("r1")+line("r2"))
	a := New([]string{p}, nil)
	wantRuns(t, "before the restart", a.Poll(), "r1", "r2")
	if _, ok := a.Heads()[p]; !ok {
		t.Fatalf("no first line remembered for %s: %v", p, a.Heads())
	}

	replace(t, p, line("new-1"))

	b := New([]string{p}, a.Offsets())
	b.SetHeads(a.Heads())
	wantRuns(t, "a restart that remembers the first line", b.Poll(), "new-1")
	if b.Truncations != 1 {
		t.Fatalf("Truncations = %d, want 1", b.Truncations)
	}

	c := New([]string{p}, a.Offsets())
	wantRuns(t, "a restart that does not", c.Poll())
	if c.Shorter != 1 {
		t.Fatalf("without a first line to compare, the file is waited for: Shorter = %d", c.Shorter)
	}
}

// A first run starts at the end of every file without reading it, and still
// remembers which file each position belongs to.
func TestSkipToEndRemembersEachFilesFirstLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, line("old-1")+line("old-2"))
	w := New([]string{p}, nil)
	if err := w.SkipToEnd(); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Heads()[p]; !ok {
		t.Fatalf("SkipToEnd remembered no first line: %v", w.Heads())
	}
	replace(t, p, line("new-1"))
	wantRuns(t, "after the file was replaced", w.Poll(), "new-1")
	if w.Truncations != 1 {
		t.Fatalf("Truncations = %d, want 1", w.Truncations)
	}
}

// Zero bytes over the first line, in a file already being read, are a span
// the volume has not filled in, not a different file. Calling them one would
// read the whole file again.
func TestAZeroSpanOverTheFirstLineIsNotADifferentFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	l1, l2 := line("r1"), line("r2")
	write(t, p, l1+l2)
	w := New([]string{p}, nil)
	wantRuns(t, "first poll", w.Poll(), "r1", "r2")

	write(t, p, line("r3"))
	content, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	replace(t, p, zeroed(string(content), 10, 30))
	wantRuns(t, "with zeros over the first line", w.Poll(), "r3")
	if w.Truncations != 0 {
		t.Fatalf("Truncations = %d, want 0", w.Truncations)
	}
}

// A line with a zero byte in it cannot be an event (JSON has no raw zero
// byte), and on a shared volume it is what a reader sees where a writer's
// bytes have not arrived while later ones have. Parsing it now would count it
// malformed and lose every event in the span for good, so the reader stops
// before it and tries again; once the span is filled, the events in it arrive
// whole.
func TestALineWithAZeroByteIsHeldUntilItFillsIn(t *testing.T) {
	l1, l2, l3 := line("r1"), line("r2"), line("r3")
	content := l1 + l2 + l3
	n1, n2 := len(l1), len(l1)+len(l2)
	for _, c := range []struct {
		name     string
		from, to int
		before   []string
		after    []string
	}{
		{"inside one line", n1 + 10, n1 + 40, []string{"r1"}, []string{"r2", "r3"}},
		{"across the newline between two lines", n2 - 20, n2 + 20, []string{"r1"}, []string{"r2", "r3"}},
		{"over a whole line and its newline", n1, n2, []string{"r1"}, []string{"r2", "r3"}},
		{"over the first line", 5, 30, nil, []string{"r1", "r2", "r3"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
			write(t, p, zeroed(content, c.from, c.to))
			w := New([]string{p}, nil)
			wantRuns(t, "before the span fills in", w.Poll(), c.before...)
			wantRuns(t, "again, still unfilled", w.Poll())
			if w.ZeroHeld != 1 || w.Malformed != 0 {
				t.Fatalf("ZeroHeld = %d, Malformed = %d, want 1 and 0", w.ZeroHeld, w.Malformed)
			}
			fill(t, p, content, c.from, c.to)
			wantRuns(t, "after the span filled in", w.Poll(), c.after...)
			if w.ZeroSkipped != 0 || w.Malformed != 0 {
				t.Fatalf("ZeroSkipped = %d, Malformed = %d, want 0 and 0", w.ZeroSkipped, w.Malformed)
			}
			if off := w.Offsets()[p]; off != int64(len(content)) {
				t.Fatalf("offset %d after the whole file was read, want %d", off, len(content))
			}
		})
	}
}

// A span that is still zero after the hold window is not waited for any
// longer: it is skipped and counted, so the file keeps flowing and the loss is
// said rather than silent.
func TestAZeroLineStillThereAfterTheWindowIsSkippedAndCounted(t *testing.T) {
	clk := newClock()
	l1, l2, l3 := line("r1"), line("r2"), line("r3")
	content := l1 + l2 + l3
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, zeroed(content, len(l1)+10, len(l1)+40))
	w := New([]string{p}, nil)
	w.now = clk.Now

	wantRuns(t, "the first poll", w.Poll(), "r1")
	clk.advance(HoldWindow - time.Second)
	wantRuns(t, "a second before the window ends", w.Poll())
	if w.ZeroSkipped != 0 {
		t.Fatalf("skipped a second early: %d", w.ZeroSkipped)
	}
	clk.advance(time.Second)
	wantRuns(t, "at the end of the window", w.Poll(), "r3")
	if w.ZeroSkipped != 1 || w.ZeroHeld != 1 || w.Malformed != 0 {
		t.Fatalf("ZeroSkipped = %d, ZeroHeld = %d, Malformed = %d, want 1, 1 and 0", w.ZeroSkipped, w.ZeroHeld, w.Malformed)
	}
	wantRuns(t, "after the skip", w.Poll())
	if off := w.Offsets()[p]; off != int64(len(content)) {
		t.Fatalf("offset %d, want %d", off, len(content))
	}
}

// The window belongs to the line being held, not to the file: a second span
// met after the first filled in gets a window of its own.
func TestEachHeldLineGetsItsOwnWindow(t *testing.T) {
	clk := newClock()
	var ls []string
	for i := 1; i <= 5; i++ {
		ls = append(ls, line(fmt.Sprintf("r%d", i)))
	}
	content := strings.Join(ls, "")
	at := func(i int) int { return len(strings.Join(ls[:i], "")) }
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, zeroed(zeroed(content, at(1)+10, at(1)+40), at(3)+10, at(3)+40))
	w := New([]string{p}, nil)
	w.now = clk.Now

	wantRuns(t, "held at the second line", w.Poll(), "r1")
	clk.advance(3 * time.Minute)
	fill(t, p, content, at(1)+10, at(1)+40)
	wantRuns(t, "the second line filled in, held at the fourth", w.Poll(), "r2", "r3")
	if w.ZeroHeld != 2 {
		t.Fatalf("ZeroHeld = %d, want 2", w.ZeroHeld)
	}
	clk.advance(3 * time.Minute) // six minutes since the first hold, three since this one
	wantRuns(t, "three minutes into the fourth line's own window", w.Poll())
	clk.advance(2 * time.Minute)
	wantRuns(t, "at the end of the fourth line's window", w.Poll(), "r5")
	if w.ZeroSkipped != 1 {
		t.Fatalf("ZeroSkipped = %d, want 1", w.ZeroSkipped)
	}
}

// A zero span inside a line longer than the cap is held like any other, not
// skipped as an oversized line: the newline the cap cannot see may be one the
// span is hiding. Filled in, the line is what it is (here, oversized, so
// skipped and counted); never filled, it is skipped after the window.
func TestAZeroSpanInALineLongerThanTheCapIsHeldNotSkipped(t *testing.T) {
	huge := `{"schema":"taipanbox.dev/agent-event/v0.2","source":"tokenfuse","data":{"blob":"` +
		strings.Repeat("x", int(maxBytesPerPoll)+1) + `"}}` + "\n"
	content := huge + line("after")
	for _, c := range []struct {
		name    string
		filled  bool
		skipped int
	}{
		{"filled in", true, 0},
		{"never filled in", false, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			clk := newClock()
			p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
			write(t, p, zeroed(content, 1000, 9000))
			w := New([]string{p}, nil)
			w.now = clk.Now
			wantRuns(t, "the first poll", w.Poll())
			if w.ZeroHeld != 1 || w.Oversized != 0 || w.Offsets()[p] != 0 {
				t.Fatalf("ZeroHeld = %d, Oversized = %d, offset %d, want 1, 0 and 0", w.ZeroHeld, w.Oversized, w.Offsets()[p])
			}
			if c.filled {
				fill(t, p, content, 1000, 9000)
			} else {
				clk.advance(HoldWindow)
			}
			var got []event.Event
			for i := 0; i < 4 && len(got) == 0; i++ {
				got = append(got, w.Poll()...)
			}
			wantRuns(t, "after", got, "after")
			if w.Oversized != 1 || w.ZeroSkipped != c.skipped || w.Malformed != 0 {
				t.Fatalf("Oversized = %d, ZeroSkipped = %d, Malformed = %d, want 1, %d and 0", w.Oversized, w.ZeroSkipped, w.Malformed, c.skipped)
			}
		})
	}
}

// A line longer than the cap whose newline has not been written yet is
// waited for, not skipped: there is nothing to skip to.
func TestALineLongerThanTheCapWithNoNewlineYetIsWaitedFor(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, `{"blob":"`+strings.Repeat("x", int(maxBytesPerPoll)+10))
	w := New([]string{p}, nil)
	wantRuns(t, "no newline yet", w.Poll())
	if w.Oversized != 0 || w.Offsets()[p] != 0 {
		t.Fatalf("Oversized = %d, offset %d, want 0 and 0", w.Oversized, w.Offsets()[p])
	}
	write(t, p, `"}`+"\n"+line("after"))
	var got []event.Event
	for i := 0; i < 4 && len(got) == 0; i++ {
		got = append(got, w.Poll()...)
	}
	wantRuns(t, "after the newline arrived", got, "after")
	if w.Oversized != 1 {
		t.Fatalf("Oversized = %d, want 1", w.Oversized)
	}
}

// A file that cannot be stated right now is skipped for this poll; the others
// are still read.
func TestAFileThatCannotBeStatedIsSkippedAndTheOthersRead(t *testing.T) {
	dir := t.TempDir()
	bad, good := filepath.Join(dir, "wardryx.ndjson"), filepath.Join(dir, "tokenfuse.ndjson")
	write(t, bad, line("never"))
	write(t, good, line("r1"))
	w := New([]string{bad, good}, nil)
	w.stat = func(p string) (fs.FileInfo, error) {
		if p == bad {
			return nil, fs.ErrPermission
		}
		return os.Stat(p)
	}
	wantRuns(t, "one file unreadable", w.Poll(), "r1")
}

// SkipToEnd names the file it could not stat, and passes over one that does
// not exist yet.
func TestSkipToEndSaysWhichFileItCouldNotStat(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "wardryx.ndjson")
	w := New([]string{filepath.Join(dir, "missing.ndjson"), bad}, nil)
	w.stat = func(p string) (fs.FileInfo, error) {
		if p == bad {
			return nil, fs.ErrPermission
		}
		return os.Stat(p)
	}
	err := w.SkipToEnd()
	if err == nil || !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), bad) {
		t.Fatalf("want an error naming %s, got %v", bad, err)
	}
}

// Positions and first lines of files out of sight are kept, up to a bound;
// past it, only the current set's are.
func TestPositionsOutOfSightAreForgottenOnlyPastTheBound(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, line("r1"))
	offsets := map[string]int64{}
	heads := map[string]string{}
	for i := range maxRememberedPaths {
		gone := fmt.Sprintf("/gone/%d.ndjson", i)
		offsets[gone] = 10
		heads[gone] = "1:00"
	}
	w := New([]string{p}, offsets)
	w.SetHeads(heads)
	wantRuns(t, "first poll", w.Poll(), "r1")
	w.shorter["/gone/0.ndjson"], w.shorter[p] = time.Now(), time.Now()
	w.zeroed["/gone/1.ndjson"], w.zeroed[p] = zeroHold{at: 1}, zeroHold{at: 2}
	w.SetPaths([]string{p})
	if len(w.Offsets()) != 1 || len(w.Heads()) != 1 {
		t.Fatalf("past the bound only the current set is kept: %d offsets, %d heads", len(w.Offsets()), len(w.Heads()))
	}
	if _, ok := w.Heads()[p]; !ok {
		t.Fatal("the current file's first line was forgotten")
	}
	if len(w.shorter) != 1 || len(w.zeroed) != 1 || w.zeroed[p].at != 2 {
		t.Fatalf("holds of files out of sight are forgotten with their positions, the current one kept: %v %v", w.shorter, w.zeroed)
	}

	under := New([]string{p}, map[string]int64{"/gone/a.ndjson": 10})
	under.SetHeads(map[string]string{"/gone/a.ndjson": "1:00"})
	under.SetPaths([]string{p})
	if len(under.Offsets()) != 1 || len(under.Heads()) != 1 {
		t.Fatalf("under the bound a file out of sight keeps its place: %d offsets, %d heads", len(under.Offsets()), len(under.Heads()))
	}
}

// Holds hands back the counters a caller prints.
func TestHoldsReportsTheCounters(t *testing.T) {
	w := New(nil, nil)
	w.Truncations, w.Shorter, w.ShorterExpired, w.ZeroHeld, w.ZeroSkipped = 1, 2, 3, 4, 5
	if got := w.Holds(); got != (Holds{Truncations: 1, Shorter: 2, ShorterExpired: 3, ZeroHeld: 4, ZeroSkipped: 5}) {
		t.Fatalf("%+v", got)
	}
}

// A path that is not a readable file costs that path this poll and nothing
// else: a directory where a log was expected, one whose held position is past
// its size, and a file the stat saw that the open did not.
func TestAPathThatCannotBeReadIsSkippedNotFatal(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir string, w *Watcher) string
	}{
		{"a directory", func(t *testing.T, dir string, w *Watcher) string {
			d := filepath.Join(dir, "wardryx.ndjson")
			if err := os.Mkdir(d, 0o700); err != nil {
				t.Fatal(err)
			}
			return d
		}},
		{"a directory with a position past its size", func(t *testing.T, dir string, w *Watcher) string {
			d := filepath.Join(dir, "wardryx.ndjson")
			if err := os.Mkdir(d, 0o700); err != nil {
				t.Fatal(err)
			}
			w.offsets[d] = 1 << 30
			return d
		}},
		{"a file the stat saw and the open did not", func(t *testing.T, dir string, w *Watcher) string {
			gone := filepath.Join(dir, "wardryx.ndjson")
			real := filepath.Join(dir, "real")
			write(t, real, line("x"))
			w.stat = func(p string) (fs.FileInfo, error) {
				if p == gone {
					return os.Stat(real)
				}
				return os.Stat(p)
			}
			return gone
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			good := filepath.Join(dir, "tokenfuse.ndjson")
			write(t, good, line("r1"))
			w := New(nil, nil)
			bad := c.setup(t, dir, w)
			was := w.Offsets()[bad]
			w.SetPaths([]string{bad, good})
			wantRuns(t, "one path unreadable", w.Poll(), "r1")
			if w.Offsets()[bad] != was || w.Truncations != 0 || w.Shorter != 0 {
				t.Fatalf("the unreadable path moved: offset %d (was %d), Truncations %d, Shorter %d", w.Offsets()[bad], was, w.Truncations, w.Shorter)
			}
		})
	}
}

// Blank lines between events are neither events nor malformed.
func TestBlankLinesAreNeitherEventsNorMalformed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	write(t, p, "\n   \n"+line("r1")+"\n")
	w := New([]string{p}, nil)
	wantRuns(t, "blank lines around one event", w.Poll(), "r1")
	if w.Malformed != 0 {
		t.Fatalf("Malformed = %d, want 0", w.Malformed)
	}
}
