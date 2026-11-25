package config

import (
	"os"
	"testing"
)

// The checkpoint gate (F-018) is a switch the operator must turn on, and it
// fails closed: a gate switched on without a directory and keys cannot start.

func TestTheCheckpointGateIsOffUnlessSwitchedOn(t *testing.T) {
	t.Setenv("HERALDYX_EVENTS", "/tmp/events")
	t.Setenv("HERALDYX_CHECKPOINT_GATE", "")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.CheckpointGate {
		t.Fatal("the gate must be off when nothing switches it on")
	}

	t.Setenv("HERALDYX_CHECKPOINT_GATE", "on")
	t.Setenv("HERALDYX_CHECKPOINTS", "/tmp/ckpt")
	t.Setenv("HERALDYX_CHECKPOINT_KEYS", "/tmp/keys.json")
	c, err = FromEnv()
	if err != nil {
		t.Fatalf("a gate on with a directory and keys must start: %v", err)
	}
	if !c.CheckpointGate || c.CheckpointDir != "/tmp/ckpt" || c.CheckpointKeys != "/tmp/keys.json" {
		t.Fatalf("the gate settings were not read: %+v", c)
	}

	t.Setenv("HERALDYX_CHECKPOINT_GATE", "maybe")
	if _, err := FromEnv(); err == nil {
		t.Fatal("a value that is neither on nor off must be refused, not read as off")
	}
}

func TestAGateWithNoCheckpointDirRefusesStartup(t *testing.T) {
	t.Setenv("HERALDYX_EVENTS", "/tmp/events")
	t.Setenv("HERALDYX_CHECKPOINT_GATE", "on")
	t.Setenv("HERALDYX_CHECKPOINTS", "")
	t.Setenv("HERALDYX_CHECKPOINT_KEYS", "/tmp/keys.json")
	if _, err := FromEnv(); err == nil {
		t.Fatal("a gate switched on with no checkpoint directory must refuse to start")
	}
}

// The custody mode (HERALDYX_CHECKPOINT_STATEMENTS): keys come from the key
// statements keyryx custody publishes into a directory this process mounts read
// only, under a delegation root pinned here, never the pinned file at the same
// time and never a root taken on first use.
func TestTheCustodyModeIsReadWholeOrRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the directory must belong to another uid than this process's, and root's own check is skipped")
	}
	es := "NaOa2mSo5zSHvOsRk6pvNF7uRP9POnReHOSys-KZ6K8"
	ml := "mldsa65:SbmWOGTDsmMCN9stqj3HzZwoLR0dwUFHb40ct-hbHhE"
	// A directory another uid owns, as custody's is: the root of the file
	// system is one on every machine this runs on.
	other := "/"
	base := map[string]string{
		"HERALDYX_EVENTS":                 "/tmp/events",
		"HERALDYX_CHECKPOINT_GATE":        "on",
		"HERALDYX_CHECKPOINTS":            "/tmp/checkpoints",
		"HERALDYX_CHECKPOINT_KEYS":        "",
		"HERALDYX_CHECKPOINT_STATEMENTS":  other,
		"HERALDYX_CHECKPOINT_ROOT_PIN":    es + "," + ml,
		"HERALDYX_CHECKPOINT_STREAM_KEYS": "tokenfuse.ndjson|tokenfuse-checkpoint",
	}
	set := func(edit map[string]string) (Config, error) {
		for k, v := range base {
			t.Setenv(k, v)
		}
		for k, v := range edit {
			t.Setenv(k, v)
		}
		return FromEnv()
	}
	c, err := set(nil)
	if err != nil {
		t.Fatalf("a whole custody configuration: %v", err)
	}
	if c.CheckpointStatements != other || len(c.CheckpointRootPin) != 2 || c.CheckpointStreamKeys["tokenfuse.ndjson"] != "tokenfuse-checkpoint" {
		t.Fatalf("read as %+v", c)
	}
	own := t.TempDir()
	for name, edit := range map[string]map[string]string{
		"both the pinned file and custody": {"HERALDYX_CHECKPOINT_KEYS": "/tmp/keys.json"},
		"neither":                          {"HERALDYX_CHECKPOINT_STATEMENTS": ""},
		"no root pin":                      {"HERALDYX_CHECKPOINT_ROOT_PIN": ""},
		"a pin with one half":              {"HERALDYX_CHECKPOINT_ROOT_PIN": es},
		"a pin that is not kids":           {"HERALDYX_CHECKPOINT_ROOT_PIN": "a,b"},
		"no stream bound":                  {"HERALDYX_CHECKPOINT_STREAM_KEYS": ""},
		"a binding that is not one":        {"HERALDYX_CHECKPOINT_STREAM_KEYS": "tokenfuse"},
		"a relative directory":             {"HERALDYX_CHECKPOINT_STATEMENTS": "statements"},
		"a directory this process owns":    {"HERALDYX_CHECKPOINT_STATEMENTS": own},
		"a directory that is not there":    {"HERALDYX_CHECKPOINT_STATEMENTS": "/nonexistent-statements"},
	} {
		if _, err := set(edit); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if c, err := set(map[string]string{"HERALDYX_CHECKPOINT_GATE": "off", "HERALDYX_CHECKPOINT_ROOT_PIN": ""}); err != nil || c.CheckpointStatements != "" {
		t.Fatalf("the gate off: %v %q", err, c.CheckpointStatements)
	}
}
