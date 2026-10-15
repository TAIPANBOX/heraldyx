package control

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TAIPANBOX/heraldyx/internal/rule"
)

var t0 = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

func ack(key string, d time.Duration) rule.Ack {
	return rule.Ack{Key: key, At: t0.UnixMilli(), Until: t0.Add(d).UnixMilli()}
}

func TestAMissingControlFileIsNoAcks(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "control.json"))
	if err != nil || len(f.Acks) != 0 {
		t.Fatalf("want empty and no error, have %+v, %v", f, err)
	}
}

func TestAMalformedControlFileIsNoAcksAndAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "control.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(p)
	if err == nil || len(f.Acks) != 0 {
		t.Fatalf("want empty and an error, have %+v, %v", f, err)
	}
}

func TestSaveThenLoadKeepsEveryAck(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "control.json")
	var f File
	if err := f.Put(ack("policy_deny:run-1", time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := Save(p, f); err != nil {
		t.Fatal(err)
	}
	back, err := Load(p)
	if err != nil || len(back.Acks) != 1 || back.Acks[0].Key != "policy_deny:run-1" {
		t.Fatalf("round trip lost the ack: %+v, %v", back, err)
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
		t.Fatalf("control file mode %v, want 0600", info.Mode().Perm())
	}
}

func TestPutReplacesTheSameKeyAndRefusesABadAck(t *testing.T) {
	var f File
	_ = f.Put(ack("policy_deny:run-1", time.Hour))
	_ = f.Put(ack("policy_deny:run-1", 2*time.Hour))
	if len(f.Acks) != 1 || f.Acks[0].Until != t0.Add(2*time.Hour).UnixMilli() {
		t.Fatalf("the second ack of a key did not replace the first: %+v", f.Acks)
	}
	if err := f.Put(ack("policy_deny:run-1", rule.MaxAck+time.Hour)); err == nil {
		t.Fatal("an ack over the maximum was accepted")
	}
}

func TestActiveLeavesOutWhatHasEnded(t *testing.T) {
	var f File
	_ = f.Put(ack("policy_deny:run-1", time.Hour))
	_ = f.Put(ack("policy_deny:run-2", 3*time.Hour))
	got := f.Active(t0.Add(2 * time.Hour))
	if _, ok := got["policy_deny:run-1"]; ok || len(got) != 1 {
		t.Fatalf("an ended ack is still active: %+v", got)
	}
}

func TestRemoveReportsWhetherThereWasAnAck(t *testing.T) {
	var f File
	_ = f.Put(ack("policy_deny:run-1", time.Hour))
	if f.Remove("policy_deny:run-9") {
		t.Fatal("removed an ack that was not there")
	}
	if !f.Remove("policy_deny:run-1") || len(f.Acks) != 0 {
		t.Fatalf("the ack was not removed: %+v", f.Acks)
	}
}

func TestPruneDropsWhatEndedAndKeepsWhatIsInForce(t *testing.T) {
	var f File
	_ = f.Put(ack("policy_deny:run-1", time.Hour))
	_ = f.Put(ack("policy_deny:run-2", 3*time.Hour))
	f.Prune(t0.Add(2 * time.Hour))
	if len(f.Acks) != 1 || f.Acks[0].Key != "policy_deny:run-2" {
		t.Fatalf("prune kept the wrong acks: %+v", f.Acks)
	}
}

// A path that exists but is not a readable file is an error, not an empty file
// that a command would then overwrite.
func TestAnUnreadableControlFileIsAnErrorNotAnEmptyOne(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("a directory was read as an empty control file")
	}
}

func TestASaveThatCannotCreateItsDirectoryIsAnError(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(filepath.Join(parent, "control.json"), File{}); err == nil {
		t.Fatal("a save under a regular file reported success")
	}
}
