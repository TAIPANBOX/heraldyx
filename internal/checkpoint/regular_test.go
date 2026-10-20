//go:build unix

package checkpoint

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// With the gate on, the stream is read again to check it against its
// checkpoint. A FIFO at the stream's name must make that read refuse, not wait
// for a writer: the gate runs inside the one poll loop.
func TestAStreamThatIsAFIFOIsUnreadableNotAWait(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keyryx.ndjson")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	type verdict struct {
		ok     bool
		reason string
	}
	done := make(chan verdict, 1)
	go func() {
		ok, reason := verifyStream(p, &record{count: 1})
		done <- verdict{ok, reason}
	}()
	select {
	case v := <-done:
		if v.ok || v.reason != ReasonUnreadable {
			t.Fatalf("a FIFO must be unreadable, got ok=%v reason=%q", v.ok, v.reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("verifyStream blocked on a FIFO: the poll loop would stop there")
	}
}
