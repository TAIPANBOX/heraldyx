package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/heraldyx/internal/config"
	"github.com/TAIPANBOX/heraldyx/internal/deliver"
	"github.com/TAIPANBOX/heraldyx/internal/fleet"
	"github.com/TAIPANBOX/heraldyx/internal/passport"
	"github.com/TAIPANBOX/heraldyx/internal/record"
	"github.com/TAIPANBOX/heraldyx/internal/render"
	"github.com/TAIPANBOX/heraldyx/internal/rule"
	"github.com/TAIPANBOX/heraldyx/internal/state"
	"github.com/TAIPANBOX/heraldyx/internal/watch"
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

// Silent is not the same as blind, and the startup log has to say which.
//
// With no mail configured this used to print the "notifications are OFF" line
// INSTEAD of the line naming what it reads, so an operator asking why no mail
// arrived could not tell a notifier that is deliberately off from one that
// cannot see its input. Measured on a live cluster 2026-08-03: a check looking
// for that line concluded the notifier saw none of three logs it was watching.
func TestItSaysWhatItReadsEvenWhenItCannotSend(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	write(t, events, ndjson("budget_exhausted", "critical", "run-1", ""))

	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	env(t, map[string]string{
		"HERALDYX_EVENTS": events,
		"HERALDYX_TO":     "",
		"HERALDYX_STATE":  filepath.Join(dir, "state.json"),
	})
	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if !strings.Contains(got, "watching 1 file(s)") {
		t.Errorf("a notifier with no address still reads a log and must say so:\n%s", got)
	}
	if !strings.Contains(got, "notifications are OFF") {
		t.Errorf("and it must still say it cannot send:\n%s", got)
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

// The ceiling holds ten alerts back, and the notice about it says ten.
//
// It said one. The notice was sent from INSIDE the event loop, on the first
// event the ceiling refused: `rule.Decide` had just counted that one event, so
// the counter stood at exactly 1 when the notice took it, and taking it
// stamped the one-per-hour window. Every later event of the same burst was
// counted and then found that window closed, so the rest sat in the state file
// waiting for a further suppression more than an hour later.
//
// The failure lands during the exact event the ceiling exists for: an operator
// watching a flood is told one alert was held back when ten were. It
// understates, which is invariant 8's rule about never claiming the stronger
// fact, inverted.
//
// Measured against the unfixed binary on 2026-08-03 with this exact input: 20
// alerts sent, notice "1 alerts suppressed this hour", `suppressed_since: 9`
// left in state.json.
func TestTheSuppressionNoticeCountsTheWholeBurst(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	mail := filepath.Join(dir, "mail.txt")
	statePath := filepath.Join(dir, "state.json")

	// Thirty distinct conditions, so dedup never fires and the ceiling is the
	// only thing that can hold the line. The default ceiling is 20 an hour.
	for i := range 30 {
		write(t, events, ndjson("policy_deny", "high", fmt.Sprintf("run-%d", i), ""))
	}

	env(t, map[string]string{
		"HERALDYX_EVENTS":    events,
		"HERALDYX_TO":        "ops@example.com",
		"HERALDYX_MAIL_FILE": mail,
		"HERALDYX_BOX":       "prod-box",
		"HERALDYX_STATE":     statePath,
	})

	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatal(err)
	}

	got := read(t, mail)
	if !strings.Contains(got, "[prod-box] 10 alerts suppressed this hour") {
		t.Fatalf("the notice does not carry the whole burst, subjects were:\n%s", subjects(got))
	}
	// And nothing is stranded behind it. A count that can only leave on the
	// next suppression is a count the operator may never be told.
	if n := suppressedSince(t, statePath); n != 0 {
		t.Fatalf("%d suppressed events were left behind in the state file", n)
	}
}

// A count the ceiling stranded leaves on the next cycle, not on the next flood.
//
// The notice is rate limited to one an hour on purpose, so the notice cannot
// become the flood it warns about. But an arriving event used to be the only
// thing that could ever release the count, so anything held back after a
// notice had gone out waited for a further suppression an hour or more later.
// When the fleet calms down instead, which is the ordinary ending, nobody is
// told about the tail at all.
//
// This drives `cycle` with a fixed clock rather than `run`, because an hour
// wide window cannot be tested by waiting an hour.
func TestAStrandedSuppressionCountLeavesOnTheNextCycle(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	mail := filepath.Join(dir, "mail.txt")
	env(t, map[string]string{
		"HERALDYX_EVENTS":    events,
		"HERALDYX_TO":        "ops@example.com",
		"HERALDYX_MAIL_FILE": mail,
		"HERALDYX_BOX":       "prod-box",
		"HERALDYX_STATE":     filepath.Join(dir, "state.json"),
	})
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	minRank, err := rule.ParseSeverity(cfg.MinSeverity)
	if err != nil {
		t.Fatal(err)
	}
	rcfg := rule.Config{MinRank: minRank, DedupWindow: cfg.DedupWindow, MaxPerHour: cfg.MaxPerHour}
	snap := state.New()
	journal, err := record.Open(cfg.SentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	w := watch.New(cfg.ResolveEventFiles(), snap.Offsets)
	poll := func(now time.Time) {
		cycle(cfg, rcfg, render.Config{Box: cfg.Box}, w, snap, deliver.NewFile(mail),
			journal, passport.Open(""), fleet.New(), now)
	}

	t0 := time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)

	// Twenty-five distinct conditions at once: 20 go out, 5 are held, and the
	// notice for those 5 goes at the end of this cycle.
	for i := range 25 {
		write(t, events, ndjson("policy_deny", "high", fmt.Sprintf("run-%d", i), ""))
	}
	poll(t0)
	if got := read(t, mail); !strings.Contains(got, "5 alerts suppressed this hour") {
		t.Fatalf("the first notice does not carry the burst, subjects were:\n%s", subjects(got))
	}

	// A minute later, three more are held. The window is closed, so no notice
	// goes now, and that is the rate limit working rather than a fault.
	for i := 25; i < 28; i++ {
		write(t, events, ndjson("policy_deny", "high", fmt.Sprintf("run-%d", i), ""))
	}
	poll(t0.Add(time.Minute))
	if n := snap.Rule.SuppressedSince; n != 3 {
		t.Fatalf("want the 3 held events waiting, have %d", n)
	}
	if strings.Count(read(t, mail), "alerts suppressed this hour") != 1 {
		t.Fatal("the notice itself became the flood it warns about")
	}

	// An hour on, with nothing new in the log at all. The three must leave
	// here: waiting for another suppression is waiting for a flood that may
	// never come.
	poll(t0.Add(61 * time.Minute))
	if got := read(t, mail); !strings.Contains(got, "3 alerts suppressed this hour") {
		t.Fatalf("the stranded count never left, subjects were:\n%s", subjects(got))
	}
	if n := snap.Rule.SuppressedSince; n != 0 {
		t.Fatalf("%d events are still stranded after the flush", n)
	}
}

// A message that goes out with no record behind it is said out loud.
//
// `internal/record` describes its failure counter as "surfaced rather than
// hidden", and until this test it was neither: `@measured` by grep on
// 2026-08-03, nothing outside that package's own tests read `Journal.Failures`,
// so every dispatch skipped for want of an agent id left the mail sent, the
// trail short, and the log silent about the difference.
//
// The scenario is the ordinary one rather than a contrived error. A digest is
// due in a cycle where nothing else caused a message, so there is no agent to
// attribute it to; the envelope requires one and this stack does not invent one
// (invariant 11), so the mail goes and the record does not. The state file is
// seeded with a window opened more than a day ago because the alternative is a
// test that waits twenty-four hours.
func TestAMessageSentWithoutARecordIsReported(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	mail := filepath.Join(dir, "mail.txt")
	sent := filepath.Join(dir, "sent.ndjson")
	statePath := filepath.Join(dir, "state.json")

	// A quiet log: the file exists, and nothing new is in it.
	write(t, events, "")
	// One condition waiting in a digest window that opened 25 hours ago, which
	// is past the 24 hour default.
	seedDigest(t, statePath, "policy_deny:run-1", 3, time.Now().Add(-25*time.Hour))

	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	env(t, map[string]string{
		"HERALDYX_EVENTS":    events,
		"HERALDYX_TO":        "ops@example.com",
		"HERALDYX_MAIL_FILE": mail,
		"HERALDYX_BOX":       "prod-box",
		"HERALDYX_STATE":     statePath,
		"HERALDYX_SENT":      sent,
	})
	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatal(err)
	}

	// The premise, asserted rather than assumed: the mail really did go, and
	// the journal really is short by it. Without both, the log line below could
	// pass on a run where nothing was sent at all.
	if got := read(t, mail); !strings.Contains(got, "daily summary") {
		t.Fatalf("the digest never went, so this test is not exercising the gap:\n%s", subjects(got))
	}
	if got := strings.TrimSpace(read(t, sent)); got != "" {
		t.Fatalf("this test needs a dispatch that was NOT recorded, and one was:\n%s", got)
	}

	got := out.String()
	if !strings.Contains(got, "1 message(s) sent without a record") {
		t.Fatalf("a message went out unrecorded and the log never said so:\n%s", got)
	}
}

// A gap already reported is not reported again on every poll.
//
// The journal's counter is cumulative for the life of the process and the
// report runs once per cycle, which by default is every two seconds. Saying the
// standing count rather than its growth would turn one missed record into a
// line in the log forever, and a log that repeats itself is one an operator
// stops reading: the same failure mode this whole component is built to avoid
// in a mailbox.
func TestAGapAlreadyReportedIsNotReportedAgainEveryPoll(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	said := 0
	// Two quiet cycles, one that misses a record, then two quiet ones again,
	// then a cycle that misses two more.
	for _, failures := range []int{0, 0, 1, 1, 1, 3} {
		said = sayUnrecorded(failures, said)
	}
	if said != 3 {
		t.Fatalf("the high-water mark is wrong: want 3, got %d", said)
	}

	got := out.String()
	if n := strings.Count(got, "sent without a record"); n != 2 {
		t.Fatalf("want one line per cycle that actually missed a record, 2 in all, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "1 message(s) sent without a record just now, 1 since") {
		t.Errorf("the first gap is not reported as one message:\n%s", got)
	}
	if !strings.Contains(got, "2 message(s) sent without a record just now, 3 since") {
		t.Errorf("the later gap must say what it added and what the total is:\n%s", got)
	}
}

// seedDigest writes a state file whose digest window opened at since, so a
// digest is due on the next cycle without anything having to wait for one.
func seedDigest(t *testing.T, path, key string, count int, since time.Time) {
	t.Helper()
	snap := state.New()
	snap.Rule.Digest[key] = count
	snap.Rule.DigestSince = since.UnixMilli()
	if err := state.Save(path, snap); err != nil {
		t.Fatal(err)
	}
}

// subjects reduces a mail file to its subject lines, so a failure above prints
// what was sent rather than several kilobytes of body.
func subjects(mail string) string {
	var out []string
	for _, line := range strings.Split(mail, "\n") {
		if strings.HasPrefix(line, "Subject:") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return "(no messages at all)"
	}
	return strings.Join(out, "\n")
}

// suppressedSince reads the counter the ceiling keeps out of the state file,
// which is the half of this that an assertion on the mail cannot see.
func suppressedSince(t *testing.T, path string) int {
	t.Helper()
	var snap struct {
		Rule struct {
			SuppressedSince int `json:"suppressed_since"`
		} `json:"rule"`
	}
	if err := json.Unmarshal([]byte(read(t, path)), &snap); err != nil {
		t.Fatalf("state file at %s did not parse: %v", path, err)
	}
	return snap.Rule.SuppressedSince
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
