package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// A configured entry that names one stream file (`*.ndjson`) is read only
// while it is a regular file. The file is usually on a volume another
// process can write, such as the Keyryx witness's findings file: a link there
// would make this process read a stream it was never told to read, under the
// stem of the file it was told to.
func TestANamedStreamFileThatIsASymlinkIsNotRead(t *testing.T) {
	bus, alerts := t.TempDir(), t.TempDir()
	touch(t, filepath.Join(bus, "wardryx.ndjson"))
	entry := filepath.Join(alerts, "keyryx.ndjson")
	if err := os.Symlink(filepath.Join(bus, "wardryx.ndjson"), entry); err != nil {
		t.Fatal(err)
	}

	c := Config{EventPaths: []string{entry}}
	if got := c.ResolveEventFiles(); len(got) != 0 {
		t.Fatalf("a link named as a stream file was read: %v", got)
	}
	if got := c.UnreadStreamEntries(); len(got) != 1 || got[0].Path != entry || !strings.Contains(got[0].Why, "link") {
		t.Fatalf("the link is not named as unread: %v", got)
	}
}

// An entry that names a stream file is never expanded as a directory, even
// when something put a directory at that name.
func TestANamedStreamFileThatIsADirectoryIsNotExpanded(t *testing.T) {
	alerts := t.TempDir()
	entry := filepath.Join(alerts, "keyryx.ndjson")
	if err := os.Mkdir(entry, 0o700); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(entry, "wardryx.ndjson"))

	c := Config{EventPaths: []string{entry}}
	if got := c.ResolveEventFiles(); len(got) != 0 {
		t.Fatalf("a directory named as a stream file was expanded: %v", got)
	}
	if got := c.UnreadStreamEntries(); len(got) != 1 || got[0].Path != entry || !strings.Contains(got[0].Why, "directory") {
		t.Fatalf("the directory is not named as unread: %v", got)
	}
}

// The control: a regular stream file named directly is read, beside a
// directory entry that is still expanded, and nothing is named as unread. A
// file that is not there yet is not unread either: it may appear later.
func TestANamedRegularStreamFileIsReadBesideADirectory(t *testing.T) {
	bus, alerts := t.TempDir(), t.TempDir()
	touch(t, filepath.Join(bus, "tokenfuse.ndjson"))
	entry := filepath.Join(alerts, "keyryx.ndjson")
	touch(t, entry)

	c := Config{EventPaths: []string{bus, entry, filepath.Join(alerts, "later.ndjson")}}
	want := []string{filepath.Join(bus, "tokenfuse.ndjson"), entry}
	if got := c.ResolveEventFiles(); !reflect.DeepEqual(got, sortedCopy(want)) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := c.UnreadStreamEntries(); len(got) != 0 {
		t.Fatalf("nothing should be unread, got %v", got)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
