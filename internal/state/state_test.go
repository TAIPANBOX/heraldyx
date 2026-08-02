package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)

// The whole reason this package exists: a restart must not re-send what was
// already sent, and must not re-read what was already read.
func TestARestartRemembersBothHalves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	s := New()
	s.Rule.NoteSent("budget_exhausted:run-1", t0)
	s.Offsets["/var/lib/stack/events/tokenfuse.ndjson"] = 4096
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}

	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Rule.SentWithin("budget_exhausted:run-1", 10*time.Minute, t0.Add(time.Minute)) {
		t.Fatal("the dedup window did not survive the restart, so every alert would be re-sent")
	}
	if back.Offsets["/var/lib/stack/events/tokenfuse.ndjson"] != 4096 {
		t.Fatalf("the read offset did not survive: %v", back.Offsets)
	}
}

func TestAFirstRunIsNotAnError(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "not-there.json"))
	if err != nil {
		t.Fatalf("a missing state file is a first run, not a failure: %v", err)
	}
	if s.Rule == nil || s.Offsets == nil {
		t.Fatal("a fresh snapshot must be usable immediately")
	}
}

// A corrupt cache must not take the alerting path down with it: the operator
// would be left with no mail at all, which is worse than a window of possible
// duplicates.
func TestACorruptStateFileStartsFreshAndSaysSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err == nil {
		t.Fatal("the operator must be told the state was unreadable")
	}
	if s == nil || s.Rule == nil {
		t.Fatal("and it must still return something usable")
	}
}

// A crash during a write must leave the previous state, not half of the new.
func TestSaveIsAtomicAndLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for range 3 {
		if err := Save(path, New()); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want only the state file, found %d entries", len(entries))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The file holds who has been mailed about what. It is not world-readable.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("want 0600, got %o", perm)
	}
}

func TestSaveCreatesItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "state.json")
	if err := Save(path, New()); err != nil {
		t.Fatalf("a fresh volume has no directory yet: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
