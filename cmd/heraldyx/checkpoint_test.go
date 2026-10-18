package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/heraldyx/internal/checkpoint/checkpointtest"
	"github.com/TAIPANBOX/heraldyx/internal/config"
)

// The checkpoint gate (F-018) end to end: what an operator would see in the
// mail when the gate is off, when it is on and a stream has no checkpoint,
// and how a refusal is raised. Each test drives cycle() through a bench, as
// source_test.go does.

// gateFixture makes a checkpoints directory and a keys file pinning one fresh
// key, and returns both paths.
func gateFixture(t *testing.T) (ckptDir, keysPath string) {
	t.Helper()
	key, err := checkpointtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ckptDir = filepath.Join(root, "checkpoints")
	if err := os.MkdirAll(ckptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keysPath = filepath.Join(root, "keys.json")
	write(t, keysPath, `{"`+key.Kid+`":"`+key.Public()+`"}`)
	return ckptDir, keysPath
}

// gatedBench is a bench with the gate switched on over that fixture.
func gatedBench(t *testing.T, minSeverity, ckptDir, keysPath string) *streamBench {
	t.Helper()
	return newStreamBenchEnv(t, minSeverity, "", map[string]string{
		"HERALDYX_CHECKPOINT_GATE": "on",
		"HERALDYX_CHECKPOINTS":     ckptDir,
		"HERALDYX_CHECKPOINT_KEYS": keysPath,
	})
}

// With the gate off, which is the default, a stream with no checkpoint is read
// and mailed exactly as it was before the gate existed.
func TestGateOffLeavesAnUncheckedStreamAsItWas(t *testing.T) {
	b := newStreamBench(t, "medium")
	b.put(t, "wardryx", lineFrom("wardryx", "policy_deny", "high", "run-plain-1"))
	b.poll(t0)

	if n := b.messages(t); n != 1 {
		t.Fatalf("with the gate off the line must be mailed once, got %d:\n%s", n, subjects(b.out(t)))
	}
	if strings.Contains(b.out(t), "checkpoint") {
		t.Fatalf("with the gate off nothing may mention checkpoints:\n%s", b.out(t))
	}
}

// With the gate on and no checkpoint for the file, the line is refused, and
// the refusal is said in the mail: it is not silently dropped.
func TestAStreamWithNoCheckpointIsRefusedAndSaidInAMail(t *testing.T) {
	ckpt, keys := gateFixture(t)
	b := gatedBench(t, "medium", ckpt, keys)
	b.put(t, "wardryx", lineFrom("wardryx", "policy_deny", "high", "run-refused-1"))
	b.poll(t0)

	got := b.out(t)
	if strings.Contains(got, "run-refused-1") {
		t.Fatalf("a line from a stream with no signed checkpoint was mailed:\n%s", got)
	}
	if sub := subjects(got); !strings.Contains(sub, "wardryx.ndjson") {
		t.Fatalf("the refusal must name the stream in its subject:\n%s", sub)
	}
	if !strings.Contains(got, "signed checkpoint") {
		t.Fatalf("the refusal must say why in its body:\n%s", got)
	}
}

// One refusal mail goes per file, however many polls refuse its lines.
func TestARefusalIsRaisedOncePerFile(t *testing.T) {
	ckpt, keys := gateFixture(t)
	b := gatedBench(t, "medium", ckpt, keys)
	// put appends, so each poll adds only its own new line.
	b.put(t, "wardryx", lineFrom("wardryx", "policy_deny", "high", "run-a"))
	b.poll(t0)
	b.put(t, "wardryx", lineFrom("wardryx", "policy_deny", "high", "run-b"))
	b.poll(later(1))
	b.put(t, "wardryx", lineFrom("wardryx", "policy_deny", "high", "run-c"))
	b.poll(later(2))

	if n := b.messages(t); n != 1 {
		t.Fatalf("want one refusal mail for wardryx.ndjson across three polls, got %d:\n%s", n, subjects(b.out(t)))
	}
}

// keyryx.ndjson is a known stream now, so a keyryx line in it is not called an
// unknown stream, and a wardryx claim inside it is still refused.
func TestKeyryxStreamMayCarryKeyryxOnly(t *testing.T) {
	b := newStreamBench(t, "medium")
	b.put(t, "keyryx",
		lineFrom("keyryx", "witness_stream_rewritten", "high", "run-keyryx-1")+
			lineFrom("wardryx", "policy_deny", "high", "run-forged-keyryx"))
	b.poll(t0)

	got := b.out(t)
	if strings.Contains(got, "run-forged-keyryx") {
		t.Fatalf("a wardryx claim inside keyryx.ndjson was processed as wardryx:\n%s", got)
	}
	if strings.Contains(got, "is not a stream this box knows") {
		t.Fatalf("keyryx.ndjson must be a stream this box knows, not an unknown one:\n%s", subjects(got))
	}
	if sub := subjects(got); !strings.Contains(sub, "keyryx.ndjson") || !strings.Contains(sub, "wardryx") {
		t.Fatalf("the refused wardryx claim must still be said:\n%s", sub)
	}
}

// The gate is built from the configuration: nil when off, and an error that
// names the keys file when it is on and the keys cannot load, so the process
// does not start with a gate that could verify nothing.
func TestAGateWhoseKeysCannotLoadDoesNotStart(t *testing.T) {
	gate, err := checkpointGate(config.Config{CheckpointGate: false})
	if gate != nil || err != nil {
		t.Fatalf("a gate that is off must build nothing and fail nothing, got %v, %v", gate, err)
	}
	_, err = checkpointGate(config.Config{
		CheckpointGate: true,
		CheckpointDir:  t.TempDir(),
		CheckpointKeys: filepath.Join(t.TempDir(), "missing.json"),
	})
	if err == nil {
		t.Fatal("a gate on with keys that cannot load must refuse to start")
	}
}
