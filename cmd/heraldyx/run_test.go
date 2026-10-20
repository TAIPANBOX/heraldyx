package main

import (
	"bytes"
	"errors"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// capture runs fn with stdout and stderr sent to files, and returns both.
func capture(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	errf, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	was, wasErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = out, errf
	defer func() { os.Stdout, os.Stderr = was, wasErr }()
	fn()
	out.Close()
	errf.Close()
	return read(t, out.Name()), read(t, errf.Name())
}

// The two flags that answer without reading anything do exactly that.
func TestVersionAndHelpAnswerWithoutReadingAnything(t *testing.T) {
	dir := t.TempDir()
	env(t, map[string]string{"HERALDYX_STATE": filepath.Join(dir, "state.json")})

	var err error
	stdout, _ := capture(t, func() { err = run([]string{"--version"}) })
	if err != nil || strings.TrimSpace(stdout) != version {
		t.Fatalf("--version: err %v, printed %q, want %q", err, stdout, version)
	}
	_, stderr := capture(t, func() { err = run([]string{"--help"}) })
	if !errors.Is(err, flag.ErrHelp) || !strings.Contains(stderr, "HERALDYX_EVENTS") {
		t.Fatalf("--help: err %v, printed %q", err, stderr)
	}
	if _, serr := os.Stat(filepath.Join(dir, "state.json")); serr == nil {
		t.Fatal("a flag that only answers wrote a state file")
	}
}

// A configuration this process cannot use stops it at startup, naming why.
func TestAConfigurationThatCannotWorkStopsItAtStartup(t *testing.T) {
	for _, c := range []struct {
		name string
		kv   map[string]string
		want string
	}{
		{"a mail server and no sender", map[string]string{"HERALDYX_SMTP_HOST": "smtp.example.com:587"}, "HERALDYX_SMTP_FROM"},
		{"a mail server with no port", map[string]string{"HERALDYX_SMTP_HOST": "smtp.example.com", "HERALDYX_SMTP_FROM": "h@example.com"}, "needs a port"},
		{"a severity floor that does not exist", map[string]string{"HERALDYX_MIN_SEVERITY": "urgent"}, "unknown severity"},
	} {
		t.Run(c.name, func(t *testing.T) {
			kv := map[string]string{"HERALDYX_STATE": filepath.Join(t.TempDir(), "state.json")}
			for k, v := range c.kv {
				kv[k] = v
			}
			env(t, kv)
			err := run([]string{"--once"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error naming %q, got %v", c.want, err)
			}
		})
	}
}

// --journal prints the record, and fails when the record is not intact or
// cannot be read at all.
func TestTheJournalFlagFailsOnARecordThatIsNotIntact(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	sent := filepath.Join(dir, "sent.ndjson")
	env(t, map[string]string{
		"HERALDYX_EVENTS":    events,
		"HERALDYX_TO":        "ops@example.com",
		"HERALDYX_MAIL_FILE": filepath.Join(dir, "mail.txt"),
		"HERALDYX_STATE":     filepath.Join(dir, "state.json"),
		"HERALDYX_SENT":      sent,
	})
	write(t, events, ndjson("budget_exhausted", "critical", "run-1", "")+ndjson("budget_exhausted", "critical", "run-2", ""))
	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatal(err)
	}

	var err error
	stdout, _ := capture(t, func() { err = run([]string{"--journal"}) })
	if err != nil || stdout == "" {
		t.Fatalf("an intact record: err %v, printed %q", err, stdout)
	}

	write(t, sent, "not a record\n")
	capture(t, func() { err = run([]string{"--journal"}) })
	if err == nil || !strings.Contains(err.Error(), "not intact") {
		t.Fatalf("a record with a malformed line must fail: %v", err)
	}

	t.Setenv("HERALDYX_SENT", dir) // a directory is not a record
	capture(t, func() { err = run([]string{"--journal"}) })
	if err == nil {
		t.Fatal("a record that cannot be read must fail")
	}
}

// The installer's test message reports a transport that cannot deliver.
func TestTestMailReportsATransportThatFails(t *testing.T) {
	dir := t.TempDir()
	env(t, map[string]string{
		"HERALDYX_TO":        "ops@example.com",
		"HERALDYX_MAIL_FILE": filepath.Join(dir, "missing", "mail.txt"),
		"HERALDYX_STATE":     filepath.Join(dir, "state.json"),
	})
	if err := run([]string{"--test-mail"}); err == nil || !strings.Contains(err.Error(), "test message") {
		t.Fatalf("want the test message to fail, got %v", err)
	}
}

// Its own files failing, a passport directory, an empty event directory, a
// message that cannot be delivered: each is said in the log, and the process
// keeps going rather than exiting, because a notifier that stops is worse
// than one that says it is degraded.
func TestWhatGoesWrongAroundItIsSaidAndItKeepsGoing(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir string) map[string]string
		want  string
	}{
		{"the journal cannot be opened", func(t *testing.T, dir string) map[string]string {
			blocker := filepath.Join(dir, "blocker")
			write(t, blocker, "")
			return map[string]string{"HERALDYX_SENT": filepath.Join(blocker, "sent.ndjson")}
		}, "record: "},
		{"the state cannot be read or saved", func(t *testing.T, dir string) map[string]string {
			blocker := filepath.Join(dir, "blocker")
			write(t, blocker, "")
			return map[string]string{"HERALDYX_STATE": filepath.Join(blocker, "state.json")}
		}, "state: "},
		{"a passport directory is configured", func(t *testing.T, dir string) map[string]string {
			t.Setenv("HERALDYX_PASSPORTS", dir)
			return nil
		}, "passports: 0 owner(s) known from"},
		{"no event log exists yet", func(t *testing.T, dir string) map[string]string {
			empty := filepath.Join(dir, "events")
			if err := os.Mkdir(empty, 0o700); err != nil {
				t.Fatal(err)
			}
			return map[string]string{"HERALDYX_EVENTS": empty}
		}, "no event log exists yet under"},
		{"a message cannot be delivered", func(t *testing.T, dir string) map[string]string {
			return map[string]string{"HERALDYX_MAIL_FILE": filepath.Join(dir, "missing", "mail.txt")}
		}, "delivery failed (file)"},
		{"a file cannot be stated on a first run", func(t *testing.T, dir string) map[string]string {
			if os.Geteuid() == 0 {
				t.Skip("root reads a directory with no permissions")
			}
			locked := filepath.Join(dir, "locked")
			if err := os.Mkdir(locked, 0o700); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(locked, "tokenfuse.ndjson"), ndjson("budget_exhausted", "critical", "run-1", ""))
			if err := os.Chmod(locked, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(locked, 0o700) })
			// A stream file entry Lstat cannot check is not read (invariant
			// 33), and the startup line says so with the error, where the
			// watcher used to say it on every poll.
			return map[string]string{"HERALDYX_EVENTS": filepath.Join(locked, "tokenfuse.ndjson")}
		}, "it cannot be checked (lstat"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			events := filepath.Join(dir, "tokenfuse.ndjson")
			write(t, events, ndjson("budget_exhausted", "critical", "run-1", ""))
			kv := map[string]string{
				"HERALDYX_EVENTS":    events,
				"HERALDYX_TO":        "ops@example.com",
				"HERALDYX_MAIL_FILE": filepath.Join(dir, "mail.txt"),
				"HERALDYX_STATE":     filepath.Join(dir, "state.json"),
			}
			t.Setenv("HERALDYX_PASSPORTS", "")
			for k, v := range c.setup(t, dir) {
				kv[k] = v
			}
			env(t, kv)
			var out bytes.Buffer
			log.SetOutput(&out)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })

			args := []string{"--once", "--from-now=false"}
			if c.name == "a file cannot be stated on a first run" {
				args = []string{"--once"}
			}
			if err := run(args); err != nil {
				t.Fatalf("it must keep going: %v", err)
			}
			if !strings.Contains(out.String(), c.want) {
				t.Fatalf("the log does not say %q:\n%s", c.want, out.String())
			}
		})
	}
}

// Run as it runs on a box, it polls until told to stop, and stops cleanly.
func TestItPollsUntilToldToStopAndThenStops(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	events := filepath.Join(dir, "tokenfuse.ndjson")
	write(t, events, ndjson("budget_exhausted", "critical", "run-1", ""))
	env(t, map[string]string{
		"HERALDYX_EVENTS":  events,
		"HERALDYX_STATE":   statePath,
		"HERALDYX_POLL_MS": "100",
	})
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	done := make(chan error, 1)
	go func() { done <- run([]string{"--from-now=false"}) }()

	// The state file is written after the signal handler is in place, so
	// once it exists a SIGTERM reaches run rather than ending the test binary.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(statePath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first poll never saved its state")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(350 * time.Millisecond) // a few ticks
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("it must stop cleanly: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("it did not stop on SIGTERM")
	}
}
