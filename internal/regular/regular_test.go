//go:build unix

package regular

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A regular file opens and reads as itself.
func TestARegularFileOpensAndReads(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokenfuse.ndjson")
	if err := os.WriteFile(p, []byte("{\"a\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "{\"a\":1}\n" {
		t.Fatalf("read %q, %v", b, err)
	}
}

// A link at the path is not followed, even to a regular file: a co-tenant
// that can plant a link must not make this process read another stream under
// this one's name.
func TestALinkIsNotFollowedEvenToARegularFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wardryx.ndjson")
	if err := os.WriteFile(target, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "tokenfuse.ndjson")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f, err := Open(link)
	if err == nil {
		f.Close()
		t.Fatal("a link was followed")
	}
	if !errors.Is(err, syscall.ELOOP) && !errors.Is(err, syscall.EMLINK) {
		t.Fatalf("want the no-follow refusal, got %v", err)
	}
}

// A directory and a FIFO are there and are not regular files: both are
// refused with ErrNotRegular naming the path, and the FIFO returns at once
// rather than waiting for a writer.
func TestADirectoryOrAFIFOIsNotRegularAndNeverWaits(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "a-directory.ndjson")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "a-fifo.ndjson")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{sub, fifo} {
		done := make(chan error, 1)
		go func() {
			f, err := Open(p)
			if f != nil {
				f.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, ErrNotRegular) {
				t.Fatalf("Open(%s) = %v, want ErrNotRegular", p, err)
			}
			if err.Error() != p+": not a regular file" {
				t.Fatalf("the error does not name the path: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("Open(%s) blocked", p)
		}
	}
}

// A path that is not there is the open's own error, so a caller can tell
// "not there yet" from "there and refused".
func TestAMissingPathIsNotExistNotNotRegular(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "absent.ndjson"))
	if !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrNotRegular) {
		t.Fatalf("want not-exist, got %v", err)
	}
}
