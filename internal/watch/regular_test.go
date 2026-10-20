//go:build unix

package watch

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// within runs f and fails the test if it has not returned in a few seconds.
// A watcher that blocks in an open stops every alert this process would send,
// so a hang is the failure being tested for, not a slow test.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s blocked: a stream that is not a regular file stopped the poll loop", what)
	}
}

// A process that can write the directory a stream sits in can swap the file
// for a FIFO. Opening a FIFO with no writer blocks, and the one poll loop
// would stop there, with every other stream and every alert behind it.
func TestAStreamSwappedForAFIFODoesNotStopThePoll(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "keyryx.ndjson")
	other := filepath.Join(dir, "tokenfuse.ndjson")
	write(t, p, lineFromSource("keyryx", "run-1"))
	write(t, other, line("run-2"))

	w := New([]string{p, other}, nil)
	if got := w.Poll(); len(got) != 2 {
		t.Fatalf("want both first lines, got %d", len(got))
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	write(t, other, line("run-3"))

	var got int
	within(t, "Poll", func() { got = len(w.Poll()) })
	if got != 1 {
		t.Fatalf("the other stream must still be read: got %d events", got)
	}
	within(t, "SkipToEnd", func() { _ = w.SkipToEnd() })
}

// A link swapped in for a stream is not followed: its target's lines are not
// read under the name of the stream the link stands at.
func TestAStreamSwappedForALinkIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "keyryx.ndjson")
	target := filepath.Join(dir, "elsewhere", "wardryx.ndjson")
	if err := os.Mkdir(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	// Lines the stem allows, so only the link decides whether they are read.
	write(t, target, lineFromSource("keyryx", "run-9")+lineFromSource("keyryx", "run-10"))
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}

	w := New([]string{p}, nil)
	if got := w.Poll(); len(got) != 0 {
		t.Fatalf("a link at a stream's name was followed: %d events read", len(got))
	}
}
