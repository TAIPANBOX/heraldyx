package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole chain, end to end, with no mail server: an event lands in the log,
// and a message an operator can read comes out the other side.
//
// This is the test that would have caught every integration mistake the unit
// tests cannot see: a config key read under the wrong name, a sender wired to
// the wrong mode, offsets that do not persist, a first run that mails history.
func TestOneEventBecomesOneMessage(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	mail := filepath.Join(dir, "mail.txt")

	write(t, events, ndjson("budget_threshold", "medium", "run-42", `,"data":{"org":"acme","budget_micros":2000000,"spent_micros":1600000}`))

	env(t, map[string]string{
		"HERALDYX_EVENTS":       events,
		"HERALDYX_TO":           "ops@example.com",
		"HERALDYX_MAIL_FILE":    mail,
		"HERALDYX_MIN_SEVERITY": "medium",
		"HERALDYX_CONSOLE_URL":  "https://box.example.com",
		"HERALDYX_BOX":          "prod-box",
		"HERALDYX_STATE":        filepath.Join(dir, "state.json"),
	})

	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatal(err)
	}

	got := read(t, mail)
	if !strings.Contains(got, "To: ops@example.com") {
		t.Fatalf("no message was delivered:\n%s", got)
	}
	if !strings.Contains(got, "[prod-box] run-42 is approaching its budget") {
		t.Fatalf("subject is not what an operator would want to see:\n%s", got)
	}
	if !strings.Contains(got, "$1.60 of $2.00 (80%)") {
		t.Fatalf("the numbers did not survive the chain:\n%s", got)
	}
	if !strings.Contains(got, "https://box.example.com/i/budget_threshold:run-42") {
		t.Fatalf("the deep link did not survive the chain:\n%s", got)
	}

	// A second pass with nothing appended must produce nothing. This is the
	// offset half of the state file: without it, every poll re-reads the log
	// and the operator gets the same alert every two seconds.
	before := len(got)
	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatal(err)
	}
	if after := len(read(t, mail)); after != before {
		t.Fatalf("a second poll re-sent something: file grew from %d to %d", before, after)
	}
}

// The same condition tripping again inside the window is one message, across
// process restarts. Each run() call here is a separate process as far as the
// state file is concerned.
func TestDedupSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	mail := filepath.Join(dir, "mail.txt")
	env(t, map[string]string{
		"HERALDYX_EVENTS":       events,
		"HERALDYX_TO":           "ops@example.com",
		"HERALDYX_MAIL_FILE":    mail,
		"HERALDYX_MIN_SEVERITY": "high",
		"HERALDYX_STATE":        filepath.Join(dir, "state.json"),
	})

	for range 3 {
		write(t, events, ndjson("budget_exhausted", "critical", "run-7", ""))
		if err := run([]string{"--once", "--from-now=false"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(read(t, mail), "Subject:"); n != 1 {
		t.Fatalf("want 1 message across 3 restarts of the same condition, got %d", n)
	}
}

// A box deployed with no address must run, stay healthy, and send nothing.
// This is the default for anyone who does not want mail, and it must not be a
// broken deployment.
func TestNoRecipientsIsHealthyAndSilent(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	write(t, events, ndjson("budget_exhausted", "critical", "run-1", ""))
	env(t, map[string]string{
		"HERALDYX_EVENTS": events,
		"HERALDYX_TO":     "",
		"HERALDYX_STATE":  filepath.Join(dir, "state.json"),
	})
	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatalf("a box with no address configured must still run: %v", err)
	}
}

// A first run starts at the end of the log. A month of history mailed at once
// is how an operator learns to filter this sender to trash.
func TestAFirstRunDoesNotMailHistory(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	mail := filepath.Join(dir, "mail.txt")
	for i := range 20 {
		write(t, events, ndjson("budget_exhausted", "critical", "old-"+string(rune('a'+i)), ""))
	}
	env(t, map[string]string{
		"HERALDYX_EVENTS":    events,
		"HERALDYX_TO":        "ops@example.com",
		"HERALDYX_MAIL_FILE": mail,
		"HERALDYX_STATE":     filepath.Join(dir, "state.json"),
	})
	if err := run([]string{"--once"}); err != nil { // --from-now defaults true
		t.Fatal(err)
	}
	if _, err := os.Stat(mail); err == nil {
		t.Fatalf("history was mailed:\n%s", read(t, mail))
	}
}

// The installer's check: one message, sent while the operator is still at the
// keyboard, so a wrong mail setup is found now rather than during an incident.
func TestTestMailSendsOneMessage(t *testing.T) {
	dir := t.TempDir()
	mail := filepath.Join(dir, "mail.txt")
	env(t, map[string]string{
		"HERALDYX_EVENTS":    filepath.Join(dir, "events.ndjson"),
		"HERALDYX_TO":        "ops@example.com",
		"HERALDYX_MAIL_FILE": mail,
		"HERALDYX_BOX":       "prod-box",
		"HERALDYX_STATE":     filepath.Join(dir, "state.json"),
	})
	if err := run([]string{"--test-mail"}); err != nil {
		t.Fatal(err)
	}
	got := read(t, mail)
	if !strings.Contains(got, "[prod-box] notifications are working") {
		t.Fatalf("the test message is not recognisable:\n%s", got)
	}
}

// And it must FAIL loudly when mail is not configured, rather than reporting
// success for a message it never sent.
func TestTestMailFailsWhenNothingIsConfigured(t *testing.T) {
	dir := t.TempDir()
	env(t, map[string]string{
		"HERALDYX_EVENTS": filepath.Join(dir, "events.ndjson"),
		"HERALDYX_TO":     "",
		"HERALDYX_STATE":  filepath.Join(dir, "state.json"),
	})
	if err := run([]string{"--test-mail"}); err == nil {
		t.Fatal("want an error naming what is missing")
	}
}

// The record half of stage 4, end to end: a message that goes out leaves one
// chained agent-event behind it.
func TestASentMessageLeavesARecord(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	sent := filepath.Join(dir, "sent.ndjson")
	write(t, events, ndjson("budget_exhausted", "critical", "run-7", ""))
	env(t, map[string]string{
		"HERALDYX_EVENTS":    events,
		"HERALDYX_TO":        "ops@example.com",
		"HERALDYX_MAIL_FILE": filepath.Join(dir, "mail.txt"),
		"HERALDYX_STATE":     filepath.Join(dir, "state.json"),
		"HERALDYX_SENT":      sent,
	})
	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatal(err)
	}

	got := read(t, sent)
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("want exactly one record:\n%s", got)
	}
	for _, want := range []string{
		`"source":"heraldyx"`,
		`"type":"alert_sent"`,
		`"agent_id":"agent://acme/biller"`,
		`"about":"budget_exhausted:run-7"`,
		`"outcome":"accepted"`,
		`"ops@example.com"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

// The bug this test exists for: a box with no address sends nothing, so it
// must record nothing. An audit trail claiming a notification nobody received
// is worse than no trail at all.
func TestNoRecipientsMeansNoRecord(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	sent := filepath.Join(dir, "sent.ndjson")
	write(t, events, ndjson("budget_exhausted", "critical", "run-7", ""))
	env(t, map[string]string{
		"HERALDYX_EVENTS": events,
		"HERALDYX_TO":     "",
		"HERALDYX_STATE":  filepath.Join(dir, "state.json"),
		"HERALDYX_SENT":   sent,
	})
	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(sent); err == nil && len(strings.TrimSpace(string(b))) != 0 {
		t.Fatalf("a record was written for a message that was never sent:\n%s", b)
	}
}

// Recording defaults on, beside the state file, without being asked for.
func TestTheJournalDefaultsToBesideTheState(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	write(t, events, ndjson("budget_exhausted", "critical", "run-7", ""))
	env(t, map[string]string{
		"HERALDYX_EVENTS":    events,
		"HERALDYX_TO":        "ops@example.com",
		"HERALDYX_MAIL_FILE": filepath.Join(dir, "mail.txt"),
		"HERALDYX_STATE":     filepath.Join(dir, "sub", "state.json"),
	})
	// HERALDYX_SENT deliberately unset above.
	os.Unsetenv("HERALDYX_SENT")
	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "sent.ndjson")); err != nil {
		t.Fatalf("no journal beside the state file: %v", err)
	}
}

func ndjson(kind, severity, run, extra string) string {
	return `{"schema":"taipanbox.dev/agent-event/v0.2","ts":"2026-08-02T14:00:00Z",` +
		`"source":"tokenfuse","type":"` + kind + `","agent_id":"agent://acme/biller",` +
		`"run_id":"` + run + `","severity":"` + severity + `"` + extra + "}\n"
}

func env(t *testing.T, kv map[string]string) {
	t.Helper()
	// Every variable this binary reads is set explicitly, including the empty
	// ones: a developer's own HERALDYX_SMTP_HOST must not reach into a test.
	for _, k := range []string{
		"HERALDYX_EVENTS", "HERALDYX_TO", "HERALDYX_MIN_SEVERITY", "HERALDYX_BOX",
		"HERALDYX_CONSOLE_URL", "HERALDYX_STATE", "HERALDYX_MAIL_FILE",
		"HERALDYX_SMTP_HOST", "HERALDYX_SMTP_FROM", "HERALDYX_SMTP_USER", "HERALDYX_SMTP_PASS",
		"HERALDYX_DEDUP_SECONDS", "HERALDYX_MAX_PER_HOUR", "HERALDYX_DIGEST_HOURS", "HERALDYX_POLL_MS",
		"HERALDYX_SENT",
	} {
		t.Setenv(k, kv[k])
	}
}

func write(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
