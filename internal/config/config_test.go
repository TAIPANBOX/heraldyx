package config

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The fact the startup line now rests on: one configured path can mean two
// logs, or none. Counting configured paths cannot tell those apart, and on a
// cluster where nothing had ever written an event the line said exactly what
// it says when everything is working.
func TestADirectoryWithNoLogsResolvesToNothing(t *testing.T) {
	dir := t.TempDir()
	c := Config{EventPaths: []string{dir}}
	if got := c.ResolveEventFiles(); len(got) != 0 {
		t.Fatalf("want none, got %v", got)
	}

	touch(t, filepath.Join(dir, "tokenfuse.ndjson"))
	touch(t, filepath.Join(dir, "wardryx.ndjson"))
	if got := c.ResolveEventFiles(); len(got) != 2 {
		t.Fatalf("want 2, got %v", got)
	}
}

// A plane that is not deployed yet is not an error, and must not stop the
// other logs from being read.
func TestAMissingPathIsSkippedRatherThanFatal(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "wardryx.ndjson"))

	c := Config{EventPaths: []string{filepath.Join(dir, "nowhere"), dir}}
	got := c.ResolveEventFiles()
	if len(got) != 1 || filepath.Base(got[0]) != "wardryx.ndjson" {
		t.Fatalf("%v", got)
	}
}

// Read order has to be stable: the same event log read in a different order
// between restarts would reorder the picture an alert carries.
func TestTheOrderIsStableAndNothingIsCountedTwice(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"c.ndjson", "a.ndjson", "b.ndjson"} {
		touch(t, filepath.Join(dir, n))
	}
	one := filepath.Join(dir, "a.ndjson")

	// The same file named directly and through its directory is one file.
	c := Config{EventPaths: []string{one, dir, dir}}
	got := c.ResolveEventFiles()
	if len(got) != 3 {
		t.Fatalf("want 3 distinct, got %v", got)
	}
	for i, want := range []string{"a.ndjson", "b.ndjson", "c.ndjson"} {
		if filepath.Base(got[i]) != want {
			t.Fatalf("order: %v", got)
		}
	}
}

// Only *.ndjson. The directory is a mount somebody else also writes to, and a
// state file or a lost+found is not an event log.
func TestOnlyNDJSONIsRead(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "events.ndjson"))
	touch(t, filepath.Join(dir, "state.json"))
	if err := os.Mkdir(filepath.Join(dir, "lost+found"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := Config{EventPaths: []string{dir}}.ResolveEventFiles()
	if len(got) != 1 || filepath.Base(got[0]) != "events.ndjson" {
		t.Fatalf("%v", got)
	}
}

// A path that cannot be stat'ed is not a path that is not there.
//
// On a shared network volume the two look identical from one syscall and are
// not remotely the same thing. Measured on a live cluster 2026-08-03: the RWX
// event volume answered `Remote I/O error` for the mount point while listing
// its three logs in the same breath, and treating that as absence made the
// notifier permanently deaf with a fully readable log underneath it.
//
// Absence is skipped, as it always was. Anything else is kept as a candidate,
// because a failed open per poll is cheaper than silence.
func TestAPathThatCannotBeStattedIsKeptRatherThanDropped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root walks through the permission bits this test needs")
	}
	parent := t.TempDir()
	inner := filepath.Join(parent, "events")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	got := Config{EventPaths: []string{inner}}.ResolveEventFiles()
	if len(got) != 1 || got[0] != inner {
		t.Fatalf("an unreadable path was dropped instead of kept: %v", got)
	}
}
