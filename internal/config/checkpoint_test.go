package config

import "testing"

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
