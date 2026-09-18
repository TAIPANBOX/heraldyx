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
	// The subject's start time is the wall clock here, since this drives run().
	if !strings.Contains(got, "[prod-box] 10 alerts suppressed since ") || !strings.Contains(got, ", worst high") {
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
	if got := read(t, mail); !strings.Contains(got, "5 alerts suppressed since 14:00 UTC, worst high") {
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
	if strings.Count(read(t, mail), "alerts suppressed") != 1 {
		t.Fatal("the notice itself became the flood it warns about")
	}

	// An hour on, with nothing new in the log at all. The three must leave
	// here: waiting for another suppression is waiting for a flood that may
	// never come.
	poll(t0.Add(61 * time.Minute))
	if got := read(t, mail); !strings.Contains(got, "3 alerts suppressed since 14:01 UTC, worst high") {
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

// A plane producing more than a poll reads is said once per growth, not once
// per poll and not never: the same discipline as sayUnrecorded, for the two
// counters the byte cap added.
func TestAbnormalVolumeIsReportedOncePerGrowthNotEveryPoll(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	said := 0
	// Quiet, quiet, one capped poll, quiet (same standing counts), then a
	// poll that both hits the cap again and skips an oversized line.
	for _, c := range []struct{ capped, oversized int }{
		{0, 0}, {0, 0}, {1, 0}, {1, 0}, {2, 1},
	} {
		said = sayVolume(c.capped, c.oversized, said)
	}
	if said != 3 {
		t.Fatalf("the high-water mark is wrong: want 3, got %d", said)
	}
	got := out.String()
	if n := strings.Count(got, "producing more than one poll reads"); n != 2 {
		t.Fatalf("want one line per cycle that actually grew, 2 in all, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "1 capped poll(s) and 0 oversized line(s) since this process started (1 new)") {
		t.Errorf("the first growth is not reported as one capped poll:\n%s", got)
	}
	if !strings.Contains(got, "2 capped poll(s) and 1 oversized line(s) since this process started (2 new)") {
		t.Errorf("the later growth must say both counts and what it added:\n%s", got)
	}
}

// A journal that fails to close is said out loud, not silently dropped.
//
// defer journal.Close() at the end of run() used to discard its return value
// entirely: neither counted, unlike a per-dispatch write failure (which
// increments record.Journal.Failures), nor logged, unlike every other error
// path in run() (record.Open and state.Load both log and continue). This is
// the one write-adjacent failure invariant 13 did not surface.
//
// closeJournal is exercised directly rather than through run(), because
// forcing record.Open's own file to fail closing from outside the package
// would need platform-specific fd tricks; a double close on the same journal
// is a real, deterministic way to make Close return a non-nil error without
// one, and it drives the exact function run()'s defer calls.
func TestAJournalCloseFailureIsLogged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sent.ndjson")
	journal, err := record.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Premise: the first close must succeed, so the second one failing is
	// really about a double close and not some other setup problem.
	if err := journal.Close(); err != nil {
		t.Fatalf("premise: the first close must succeed: %v", err)
	}

	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	closeJournal(journal, path)

	if !strings.Contains(out.String(), "record: close") {
		t.Fatalf("a journal close failure must be logged, got:\n%s", out.String())
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

// bench drives cycle() with a fixed clock, the way the stranded-count test
// above does, so a ten-minute cadence can be tested without waiting ten
// minutes. Each poll is one pass over whatever has been appended to events.
type bench struct {
	events, mail string
	snap         *state.Snapshot
	poll         func(now time.Time)
}

func newBench(t *testing.T, minSeverity string) *bench {
	t.Helper()
	dir := t.TempDir()
	events := filepath.Join(dir, "tokenfuse.ndjson")
	mail := filepath.Join(dir, "mail.txt")
	env(t, map[string]string{
		"HERALDYX_EVENTS":       events,
		"HERALDYX_TO":           "ops@example.com",
		"HERALDYX_MAIL_FILE":    mail,
		"HERALDYX_MIN_SEVERITY": minSeverity,
		"HERALDYX_BOX":          "prod-box",
		"HERALDYX_STATE":        filepath.Join(dir, "state.json"),
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
	t.Cleanup(func() { journal.Close() })
	w := watch.New(cfg.ResolveEventFiles(), snap.Offsets)
	// The file exists before the first poll, so the watcher has something to
	// hold an offset for even when the first poll's events are appended later.
	write(t, events, "")
	b := &bench{events: events, mail: mail, snap: snap}
	b.poll = func(now time.Time) {
		cycle(cfg, rcfg, render.Config{Box: cfg.Box}, w, snap, deliver.NewFile(mail),
			journal, passport.Open(""), fleet.New(), now)
	}
	return b
}

// held appends n distinct alerts of one type and severity, numbered from
// start, so dedup never fires on them and only the ceiling can hold them.
func (b *bench) held(t *testing.T, kind, severity string, start, n int) {
	t.Helper()
	for i := start; i < start+n; i++ {
		write(t, b.events, ndjson(kind, severity, fmt.Sprintf("run-%d", i), ""))
	}
}

// summaries counts the ceiling's own notices in the mail file.
func (b *bench) summaries(t *testing.T) int {
	t.Helper()
	if _, err := os.Stat(b.mail); err != nil {
		return 0
	}
	return strings.Count(read(t, b.mail), "alerts suppressed")
}

// Issue #71, the second ask. A critical that arrived after the ceiling was
// reached was held with everything else: the whole meaning of that severity
// is "now", and the one limit that exists to keep the mailbox usable is not
// a reason to sit on it. It goes through the ceiling, one message per
// condition, and the dedup window still applies to it.
func TestACriticalIsSentThroughTheCeiling(t *testing.T) {
	b := newBench(t, "medium")
	t0 := time.Date(2026, 9, 17, 12, 11, 0, 0, time.UTC)

	// The ceiling is reached: 25 distinct highs, 20 go out, 5 are held.
	b.held(t, "policy_deny", "high", 0, 25)
	b.poll(t0)
	if got := b.snap.Rule.SentInLastHour(t0); got != 20 {
		t.Fatalf("premise: the ceiling must be reached, %d sent", got)
	}
	if n := b.summaries(t); n != 1 {
		t.Fatalf("premise: want the first summary, have %d", n)
	}

	// A critical a minute later, about a run nothing has been mailed about.
	write(t, b.events, ndjson("budget_exhausted", "critical", "run-900", ""))
	b.poll(t0.Add(time.Minute))
	if got := read(t, b.mail); !strings.Contains(subjects(got), "run-900") {
		t.Fatalf("a critical was held back by the ceiling, subjects were:\n%s", subjects(got))
	}

	// The same critical again inside the dedup window: one message, not two.
	write(t, b.events, ndjson("budget_exhausted", "critical", "run-900", ""))
	b.poll(t0.Add(2 * time.Minute))
	if n := strings.Count(subjects(read(t, b.mail)), "run-900"); n != 1 {
		t.Fatalf("dedup must still apply to a critical: %d messages about run-900", n)
	}

	// A different critical is a different condition, and it goes too.
	write(t, b.events, ndjson("budget_exhausted", "critical", "run-901", ""))
	b.poll(t0.Add(3 * time.Minute))
	if got := read(t, b.mail); !strings.Contains(subjects(got), "run-901") {
		t.Fatalf("a second, different critical was held back, subjects were:\n%s", subjects(got))
	}

	// And none of the three was counted as held: with nothing else held since
	// the first summary, no further summary is due when the interval passes.
	b.poll(t0.Add(15 * time.Minute))
	if n := b.summaries(t); n != 1 {
		t.Fatalf("a critical that went out was still counted as held: %d summaries", n)
	}
}

// Issue #71, the first ask. After the first summary the operator heard
// nothing for 28 minutes while events kept being held, because the summary
// was rate limited to one an hour. A summary is due again ten minutes after
// the previous one whenever anything has been held since, and it carries the
// true count, the time the first of them was held, and the worst severity.
func TestHeldAlertsAreSummarisedAgainWithinABoundedInterval(t *testing.T) {
	b := newBench(t, "medium")
	t0 := time.Date(2026, 9, 17, 12, 11, 0, 0, time.UTC)

	b.held(t, "policy_deny", "high", 0, 25)
	b.poll(t0)
	if n := b.summaries(t); n != 1 {
		t.Fatalf("premise: want the first summary, have %d", n)
	}

	// Three more a minute later. The summary is rate limited, so none goes
	// now, and that is the limit working rather than the defect.
	b.held(t, "dependency_failed", "high", 25, 3)
	b.poll(t0.Add(time.Minute))
	if n := b.summaries(t); n != 1 {
		t.Fatalf("the summary became the flood it warns about: %d summaries after one minute", n)
	}
	if n := b.snap.Rule.SuppressedSince; n != 3 {
		t.Fatalf("want the 3 held events waiting, have %d", n)
	}

	// Just under the interval: still nothing.
	b.poll(t0.Add(10*time.Minute - time.Second))
	if n := b.summaries(t); n != 1 {
		t.Fatalf("a summary went out before the interval had passed: %d summaries", n)
	}

	// The interval passes with nothing new in the log at all. The three
	// leave here, and the mail says when the first of them was held and how
	// bad the worst of them was.
	b.poll(t0.Add(10 * time.Minute))
	got := read(t, b.mail)
	if n := b.summaries(t); n != 2 {
		t.Fatalf("no second summary ten minutes after the first, subjects were:\n%s", subjects(got))
	}
	if !strings.Contains(got, "[prod-box] 3 alerts suppressed since 12:12 UTC, worst high") {
		t.Fatalf("the second summary does not carry the count, the start and the worst, subjects were:\n%s", subjects(got))
	}
	if n := b.snap.Rule.SuppressedSince; n != 0 {
		t.Fatalf("%d events are still stranded after the second summary", n)
	}

	// And again, for as long as it goes on.
	b.held(t, "unit_cap_exceeded", "high", 28, 4)
	b.poll(t0.Add(12 * time.Minute))
	b.poll(t0.Add(20 * time.Minute))
	if got := read(t, b.mail); !strings.Contains(got, "4 alerts suppressed since 12:23 UTC, worst high") {
		t.Fatalf("no third summary, subjects were:\n%s", subjects(got))
	}
}

// The summary says how bad the worst held alert was, so an operator reading
// "31 held" can tell thirty-one mediums from thirty mediums and one high.
func TestTheSummaryNamesTheWorstSeverityHeld(t *testing.T) {
	b := newBench(t, "medium")
	t0 := time.Date(2026, 9, 17, 12, 11, 0, 0, time.UTC)

	// The ceiling is filled by mediums, none held yet.
	b.held(t, "breaker_tripped", "medium", 0, 20)
	b.poll(t0)
	if n := b.summaries(t); n != 0 {
		t.Fatalf("premise: nothing should be held yet, have %d summaries", n)
	}

	// Five more mediums and one high are held.
	b.held(t, "breaker_tripped", "medium", 20, 3)
	b.held(t, "dependency_failed", "high", 23, 1)
	b.held(t, "breaker_tripped", "medium", 24, 2)
	b.poll(t0.Add(time.Minute))
	got := read(t, b.mail)
	if !strings.Contains(got, "[prod-box] 6 alerts suppressed since 12:12 UTC, worst high") {
		t.Fatalf("the summary does not name the worst severity held, subjects were:\n%s", subjects(got))
	}
	if !strings.Contains(got, "the worst of them was high") {
		t.Fatalf("the body does not say how bad the worst one was:\n%s", got)
	}
}

// A burst is summarised sooner than the interval, and the summary still
// cannot become the flood it warns about: fifty held since the previous
// summary bring the next one forward, but never to less than a minute after
// the previous one.
func TestALargeBurstIsSummarisedSoonerThanTheInterval(t *testing.T) {
	b := newBench(t, "medium")
	t0 := time.Date(2026, 9, 17, 12, 11, 0, 0, time.UTC)

	b.held(t, "policy_deny", "high", 0, 25)
	b.poll(t0)
	if n := b.summaries(t); n != 1 {
		t.Fatalf("premise: want the first summary, have %d", n)
	}

	// Fifty held two minutes after the first summary: a summary now.
	b.held(t, "policy_deny", "high", 25, 50)
	b.poll(t0.Add(2 * time.Minute))
	got := read(t, b.mail)
	if n := b.summaries(t); n != 2 {
		t.Fatalf("a burst of 50 waited for the interval, subjects were:\n%s", subjects(got))
	}
	if !strings.Contains(got, "50 alerts suppressed since 12:13 UTC, worst high") {
		t.Fatalf("the burst summary does not carry the burst, subjects were:\n%s", subjects(got))
	}

	// Fifty more thirty seconds later: the floor holds them.
	b.held(t, "policy_deny", "high", 75, 50)
	b.poll(t0.Add(2*time.Minute + 30*time.Second))
	if n := b.summaries(t); n != 2 {
		t.Fatalf("the burst rule let the summary become a flood: %d summaries", n)
	}
	if n := b.snap.Rule.SuppressedSince; n != 50 {
		t.Fatalf("want the 50 held events waiting behind the floor, have %d", n)
	}

	// A minute after the previous summary they leave, together with what
	// arrived in the meantime.
	b.held(t, "policy_deny", "high", 125, 10)
	b.poll(t0.Add(3 * time.Minute))
	if got := read(t, b.mail); !strings.Contains(got, "60 alerts suppressed since 12:13 UTC") {
		t.Fatalf("the floor never released the burst, subjects were:\n%s", subjects(got))
	}
}

// Issue #71, the third ask. The log said nothing at the ceiling, so a person
// reading it saw twenty alerts and then silence, with no way to tell a quiet
// fleet from a held one. The line at the ceiling says what happens next, and
// it is printed once per summary period rather than once per held event.
func TestTheLogSaysWhatHappensNextAtTheCeiling(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	b := newBench(t, "medium")
	t0 := time.Date(2026, 9, 17, 12, 11, 0, 0, time.UTC)

	b.held(t, "policy_deny", "high", 0, 25)
	b.poll(t0)
	got := out.String()
	if !strings.Contains(got, "ceiling:") {
		t.Fatalf("nothing in the log says the ceiling is holding alerts back:\n%s", got)
	}
	for _, want := range []string{"held back", "summary", "critical"} {
		if !strings.Contains(got, want) {
			t.Errorf("the line at the ceiling does not say what happens next (%q missing):\n%s", want, got)
		}
	}
	if n := strings.Count(got, "ceiling:"); n != 1 {
		t.Fatalf("five alerts were held and the line was printed %d times, want once", n)
	}

	// The first summary went at the end of that cycle, so the next hold opens
	// a new period and gets its line; two more held thirty seconds after it,
	// inside the same period, do not.
	b.held(t, "policy_deny", "high", 25, 3)
	b.poll(t0.Add(time.Minute))
	if n := strings.Count(out.String(), "ceiling:"); n != 2 {
		t.Fatalf("the first hold after a summary must get its line, got %d lines", n)
	}
	b.held(t, "policy_deny", "high", 28, 2)
	b.poll(t0.Add(time.Minute + 30*time.Second))
	if n := strings.Count(out.String(), "ceiling:"); n != 2 {
		t.Fatalf("the line repeats inside one summary period: %d lines", n)
	}

	// The next summary goes, then one more is held: a new period, a new line.
	b.poll(t0.Add(10 * time.Minute))
	b.held(t, "policy_deny", "high", 30, 1)
	b.poll(t0.Add(11 * time.Minute))
	if n := strings.Count(out.String(), "ceiling:"); n != 3 {
		t.Fatalf("want one line per summary period, 3 in all, got %d:\n%s", n, out.String())
	}
}
