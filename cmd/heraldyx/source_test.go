package main

import (
	"bytes"
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

// These tests are the behaviour of invariant 22: an event is processed as the
// source it claims only when the file it was read from may carry that source.
// They drive cycle() and run() the way the other end-to-end tests do and read
// only the mail and the state, so each one states what an operator would see.

// lineFrom is one valid envelope claiming the given source.
func lineFrom(source, kind, severity, run string) string {
	return `{"schema":"taipanbox.dev/agent-event/v0.2","ts":"2026-10-04T10:00:00Z",` +
		`"source":"` + source + `","type":"` + kind + `","agent_id":"agent://acme/biller",` +
		`"run_id":"` + run + `","severity":"` + severity + `"}` + "\n"
}

// streamBench is a bench over a DIRECTORY of named stream files, which is how
// every launcher points heraldyx at the bus.
type streamBench struct {
	dir, mail string
	snap      *state.Snapshot
	poll      func(now time.Time)
}

func newStreamBench(t *testing.T, minSeverity string) *streamBench {
	t.Helper()
	return newStreamBenchWith(t, minSeverity, "")
}

// newStreamBenchWith is a bench whose operator declared what some files may
// carry (HERALDYX_STREAMS), applied the way run() applies it.
func newStreamBenchWith(t *testing.T, minSeverity, streams string) *streamBench {
	t.Helper()
	dir := t.TempDir()
	mail := filepath.Join(dir, "mail.out")
	events := filepath.Join(dir, "bus")
	if err := os.MkdirAll(events, 0o700); err != nil {
		t.Fatal(err)
	}
	env(t, map[string]string{
		"HERALDYX_EVENTS":       events,
		"HERALDYX_TO":           "ops@example.com",
		"HERALDYX_MAIL_FILE":    mail,
		"HERALDYX_MIN_SEVERITY": minSeverity,
		"HERALDYX_BOX":          "prod-box",
		"HERALDYX_STATE":        filepath.Join(dir, "state.json"),
		"HERALDYX_STREAMS":      streams,
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
	w.SetPolicy(cfg.StreamPolicy())
	b := &streamBench{dir: events, mail: mail, snap: snap}
	b.poll = func(now time.Time) {
		cycle(cfg, rcfg, render.Config{Box: cfg.Box}, w, snap, deliver.NewFile(mail),
			journal, passport.Open(""), fleet.New(), now)
	}
	return b
}

// put appends lines to <stem>.ndjson on the bus.
func (b *streamBench) put(t *testing.T, stem string, lines ...string) {
	t.Helper()
	write(t, filepath.Join(b.dir, stem+".ndjson"), strings.Join(lines, ""))
}

// out is everything mailed so far, or "" when nothing was.
func (b *streamBench) out(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(b.mail)
	if err != nil {
		return ""
	}
	return string(raw)
}

// messages counts the mails sent so far.
func (b *streamBench) messages(t *testing.T) int {
	t.Helper()
	return strings.Count(b.out(t), "\nSubject: ") + boolToInt(strings.HasPrefix(b.out(t), "Subject: "))
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

var t0 = time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)

// a poll the way a later one would be: well past the dedup window, so that
// once-per-pair is shown to be the pair's own memory and not dedup's.
func later(n int) time.Time { return t0.Add(time.Duration(n) * 30 * time.Minute) }

// The defect, as it stood: a line claiming `source: wardryx` inside
// tokenfuse.ndjson was rendered and mailed as a policy decision from wardryx.
// Nothing checked that the file a line sits in is the file its claimed source
// writes, and the shared bus is a directory every writer can append to.
func TestAForeignSourceIsNeverMailedAsThatSource(t *testing.T) {
	b := newStreamBench(t, "medium")
	b.put(t, "tokenfuse", lineFrom("wardryx", "policy_deny", "high", "run-forged-1"))
	b.poll(t0)

	got := b.out(t)
	if strings.Contains(got, "run-forged-1") {
		t.Fatalf("a line claiming wardryx inside tokenfuse.ndjson was processed as wardryx; subjects:\n%s", subjects(got))
	}
	if n := b.messages(t); n != 1 {
		t.Fatalf("want exactly the one alert about the refused source, got %d:\n%s", n, subjects(got))
	}
	sub := subjects(got)
	if !strings.Contains(sub, "tokenfuse.ndjson") || !strings.Contains(sub, "wardryx") {
		t.Fatalf("the alert must name the file and the claimed source:\n%s", sub)
	}
}

// The alert says what was refused and where, in the box's own words, and
// never carries what the refused line itself said.
func TestTheForeignSourceAlertNamesTheFileTheClaimAndTheCount(t *testing.T) {
	b := newStreamBench(t, "medium")
	b.put(t, "tokenfuse",
		lineFrom("wardryx", "policy_deny", "high", "run-forged-1"),
		lineFrom("wardryx", "approval_granted", "info", "run-forged-2"),
		lineFrom("wardryx", "policy_deny", "high", "run-forged-3"))
	b.poll(t0)

	got := b.out(t)
	for _, want := range []string{
		"3 event(s)",
		"tokenfuse.ndjson",
		"wardryx",
		"not processed as that source",
		"HERALDYX_STREAMS",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the alert does not say %q:\n%s", want, got)
		}
	}
	for _, leaked := range []string{"run-forged-1", "run-forged-2", "agent://acme/biller"} {
		if strings.Contains(got, leaked) {
			t.Errorf("the alert carries %q off a refused line:\n%s", leaked, got)
		}
	}
}

// One alert per (file, claimed source) pair, across polls, and a different
// claim or a different file is a new pair. The polls are 30 minutes apart so
// the dedup window (10 minutes) cannot be what holds the second one back.
func TestAForeignSourceIsRaisedOncePerFileAndClaimedSourceAcrossPolls(t *testing.T) {
	b := newStreamBench(t, "medium")

	b.put(t, "tokenfuse", lineFrom("wardryx", "policy_deny", "high", "run-1"), lineFrom("wardryx", "policy_deny", "high", "run-2"))
	b.poll(later(0))
	if n := b.messages(t); n != 1 {
		t.Fatalf("first sighting of (tokenfuse.ndjson, wardryx): want 1 message, got %d", n)
	}

	b.put(t, "tokenfuse", lineFrom("wardryx", "policy_deny", "high", "run-3"))
	b.poll(later(1))
	b.put(t, "tokenfuse", lineFrom("wardryx", "policy_deny", "high", "run-4"))
	b.poll(later(2))
	if n := b.messages(t); n != 1 {
		t.Fatalf("the same pair seen again must not be raised again: %d messages\n%s", n, subjects(b.out(t)))
	}

	// A different claimed source in the same file is a new pair.
	b.put(t, "tokenfuse", lineFrom("engram", "memory_written", "info", "run-5"))
	b.poll(later(3))
	if n := b.messages(t); n != 2 {
		t.Fatalf("a different claimed source is a different pair: want 2, got %d", n)
	}

	// The same claimed source in a different file is a new pair.
	b.put(t, "typryx", lineFrom("wardryx", "policy_deny", "high", "run-6"))
	b.poll(later(4))
	if n := b.messages(t); n != 3 {
		t.Fatalf("the same claim in a different file is a different pair: want 3, got %d", n)
	}

	// And none of the three pairs repeats after that.
	b.put(t, "tokenfuse", lineFrom("wardryx", "policy_deny", "high", "run-7"), lineFrom("engram", "memory_written", "info", "run-8"))
	b.put(t, "typryx", lineFrom("wardryx", "policy_deny", "high", "run-9"))
	b.poll(later(5))
	if n := b.messages(t); n != 3 {
		t.Fatalf("every known pair was raised again: %d messages\n%s", n, subjects(b.out(t)))
	}
}

// What was raised is remembered across a restart: a process that forgot it
// would mail the same pair once per rollout, which is how an operator learns
// to filter this sender.
func TestAForeignSourceIsNotRaisedAgainAfterARestart(t *testing.T) {
	dir := t.TempDir()
	bus := filepath.Join(dir, "bus")
	if err := os.MkdirAll(bus, 0o700); err != nil {
		t.Fatal(err)
	}
	mail := filepath.Join(dir, "mail.out")
	env(t, map[string]string{
		"HERALDYX_EVENTS":        bus,
		"HERALDYX_TO":            "ops@example.com",
		"HERALDYX_MAIL_FILE":     mail,
		"HERALDYX_MIN_SEVERITY":  "medium",
		"HERALDYX_BOX":           "prod-box",
		"HERALDYX_STATE":         filepath.Join(dir, "state.json"),
		"HERALDYX_DEDUP_SECONDS": "0",
	})
	tf := filepath.Join(bus, "tokenfuse.ndjson")

	write(t, tf, lineFrom("wardryx", "policy_deny", "high", "run-1"))
	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatal(err)
	}
	write(t, tf, lineFrom("wardryx", "policy_deny", "high", "run-2"))
	if err := run([]string{"--once", "--from-now=false"}); err != nil {
		t.Fatal(err)
	}
	got := read(t, mail)
	if n := strings.Count(got, "Subject: "); n != 1 {
		t.Fatalf("a restart re-raised a pair already raised: %d messages\n%s", n, subjects(got))
	}
	if strings.Contains(got, "run-1") || strings.Contains(got, "run-2") {
		t.Fatalf("a refused line was processed:\n%s", subjects(got))
	}
}

// A real line beside a forged one is still read: the rule refuses lines, not
// files.
func TestALegitimateLineBesideAForeignOneIsStillMailed(t *testing.T) {
	b := newStreamBench(t, "medium")
	b.put(t, "tokenfuse",
		lineFrom("tokenfuse", "budget_exhausted", "critical", "run-legit"),
		lineFrom("wardryx", "policy_deny", "high", "run-forged"))
	b.poll(t0)

	got := b.out(t)
	if !strings.Contains(subjects(got), "run-legit") {
		t.Fatalf("the legitimate line was dropped:\n%s", subjects(got))
	}
	if strings.Contains(got, "run-forged") {
		t.Fatalf("the forged line was processed:\n%s", subjects(got))
	}
	if n := b.messages(t); n != 2 {
		t.Fatalf("want the legitimate alert and one alert about the refusal, got %d:\n%s", n, subjects(got))
	}
}

// `taipan demo` writes events attributed to six planes into one demo.ndjson,
// but the events directory is writable by every co-tenant until the launchers
// give each writer its own file, so a built-in exception for demo.ndjson would
// let any of them create it and speak as wardryx. It is therefore NOT in the
// default table: undeclared, it is an unknown stream and only `source: demo`
// would be read from it.
func TestACoTenantsDemoFileIsRefusedByDefault(t *testing.T) {
	b := newStreamBench(t, "medium")
	b.put(t, "demo", lineFrom("wardryx", "policy_deny", "high", "run-cotenant"))
	b.poll(t0)

	got := b.out(t)
	if strings.Contains(got, "run-cotenant") {
		t.Fatalf("a co-tenant's demo.ndjson was processed as wardryx:\n%s", subjects(got))
	}
	if n := b.messages(t); n != 1 || !strings.Contains(subjects(got), "demo.ndjson") {
		t.Fatalf("want one alert naming demo.ndjson, got %d:\n%s", n, subjects(got))
	}
}

// An operator who runs `taipan demo` against this box declares the file, and
// then it is processed whole and nothing is raised.
func TestADeclaredMultiSourceFileIsProcessedWholeAndRaisesNothing(t *testing.T) {
	b := newStreamBenchWith(t, "medium", "demo=tokenfuse|wardryx|mockryx")
	b.put(t, "demo",
		lineFrom("tokenfuse", "budget_exhausted", "critical", "run-a"),
		lineFrom("wardryx", "policy_deny", "high", "run-b"),
		lineFrom("mockryx", "sim_finding", "high", "run-c"))
	b.poll(t0)

	got := b.out(t)
	for _, run := range []string{"run-a", "run-b", "run-c"} {
		if !strings.Contains(subjects(got), run) {
			t.Errorf("a line in the declared multi-source file was refused (%s):\n%s", run, subjects(got))
		}
	}
	if n := b.messages(t); n != 3 {
		t.Errorf("want exactly the three alerts and nothing about the file, got %d:\n%s", n, subjects(got))
	}
}

// The renamed files the money plane writes: tokenfuse's control plane and its
// MCP broker each append to their own file, and both stamp source
// `tokenfuse`, because they share the crate that builds the envelope.
func TestARenamedFileOfTheSameProducerIsProcessed(t *testing.T) {
	b := newStreamBench(t, "medium")
	b.put(t, "tokenfuse-cloud", lineFrom("tokenfuse", "budget_exhausted", "critical", "run-cloud"))
	b.put(t, "tokenfuse-mcp", lineFrom("tokenfuse", "dlp_block", "high", "run-mcp"))
	b.poll(t0)

	got := b.out(t)
	if !strings.Contains(subjects(got), "run-cloud") || !strings.Contains(subjects(got), "run-mcp") {
		t.Fatalf("a renamed tokenfuse file was refused:\n%s", subjects(got))
	}
	if n := b.messages(t); n != 2 {
		t.Fatalf("want two alerts and no notice, got %d:\n%s", n, subjects(got))
	}
}

// A file whose stem nothing declares is not silently trusted: a line whose
// source equals the stem is still read (a new plane must not go deaf the day
// it is deployed), and the operator is told once that the box does not know
// the stream.
func TestAnUnknownStreamIsReadAndSaidOnceNotTrustedInSilence(t *testing.T) {
	b := newStreamBench(t, "medium")
	b.put(t, "newplane", lineFrom("newplane", "budget_exhausted", "critical", "run-n1"))
	b.poll(later(0))

	got := b.out(t)
	if !strings.Contains(subjects(got), "run-n1") {
		t.Fatalf("a line from a stream this build has not heard of was dropped:\n%s", subjects(got))
	}
	if !strings.Contains(got, "newplane.ndjson") || !strings.Contains(got, "not a stream this box knows") {
		t.Fatalf("an unknown stream must be said out loud:\n%s", got)
	}
	if n := b.messages(t); n != 2 {
		t.Fatalf("want the event and one notice about the stream, got %d:\n%s", n, subjects(got))
	}

	b.put(t, "newplane", lineFrom("newplane", "budget_exhausted", "critical", "run-n2"))
	b.poll(later(1))
	if n := b.messages(t); n != 3 {
		t.Fatalf("the second event is mailed and the notice is not repeated: got %d:\n%s", n, subjects(b.out(t)))
	}
}

// An unknown stem claiming somebody else's source is the forged case, not the
// new-plane case.
func TestAnUnknownStreamClaimingAnotherSourceIsRefused(t *testing.T) {
	b := newStreamBench(t, "medium")
	b.put(t, "newplane", lineFrom("tokenfuse", "budget_exhausted", "critical", "run-x"))
	b.poll(t0)

	got := b.out(t)
	if strings.Contains(got, "run-x") {
		t.Fatalf("a line claiming tokenfuse inside newplane.ndjson was processed as tokenfuse:\n%s", subjects(got))
	}
	if n := b.messages(t); n != 1 {
		t.Fatalf("want one alert about the refusal, got %d:\n%s", n, subjects(got))
	}
}

// A refusal below the operator's floor is not lost: it goes to the digest
// like any other high that did not clear the floor.
func TestARefusalBelowTheFloorGoesToTheDigest(t *testing.T) {
	b := newStreamBench(t, "critical")
	b.put(t, "tokenfuse", lineFrom("wardryx", "policy_deny", "high", "run-forged"))
	b.poll(t0)

	if n := b.messages(t); n != 0 {
		t.Fatalf("a high below a critical floor must not be mailed now:\n%s", subjects(b.out(t)))
	}
	found := false
	for key := range b.snap.Rule.Digest {
		if strings.HasPrefix(key, "foreign_source:") {
			found = true
		}
		if strings.Contains(key, "run-forged") {
			t.Errorf("the digest counted the refused line as an event of its claimed source: %q", key)
		}
	}
	if !found {
		t.Fatalf("the refusal was lost below the floor, digest holds: %v", b.snap.Rule.Digest)
	}
}

// Hostile lines are unchanged by the rule: garbage and truncated envelopes are
// counted as malformed exactly as before and never reach the source check, and
// a claimed source that tries to break the message cannot.
func TestHostileLinesAreUnchangedAndAHostileClaimCannotBreakTheMail(t *testing.T) {
	b := newStreamBench(t, "medium")
	hostile := `{"schema":"taipanbox.dev/agent-event/v0.2","ts":"2026-10-04T10:00:00Z",` +
		`"source":"wardryx\r\nBcc: attacker@example.com\r\n\r\nclick here","type":"policy_deny",` +
		`"agent_id":"agent://acme/biller","run_id":"run-h","severity":"high"}` + "\n"
	b.put(t, "tokenfuse",
		"not json at all\n",
		`{"source":"tokenfuse"}`+"\n",
		hostile,
		lineFrom("tokenfuse", "budget_exhausted", "critical", "run-legit"))
	b.poll(t0)

	got := b.out(t)
	if !strings.Contains(subjects(got), "run-legit") {
		t.Fatalf("the legitimate line was lost among hostile ones:\n%s", got)
	}
	if strings.Contains(got, "\nBcc:") || strings.Contains(got, "\r\nBcc:") {
		t.Fatalf("a claimed source injected a header into the mail:\n%s", got)
	}
	if strings.Contains(got, "run-h") {
		t.Fatalf("the hostile line was processed as the source it claimed:\n%s", subjects(got))
	}
	if n := b.messages(t); n != 2 {
		t.Fatalf("want the legitimate alert and one alert about the hostile claim, got %d:\n%s", n, subjects(got))
	}
}

// The recipe in the README pointed heraldyx at one file named events.ndjson
// holding several planes' lines. That file's stem is nobody's source, so the
// rule refuses its lines until the operator declares what it may carry.
func TestAFileNamedEventsIsRefusedUntilItsSourcesAreDeclared(t *testing.T) {
	for _, tc := range []struct {
		name, streams string
		wantMailed    bool
	}{
		{"undeclared", "", false},
		{"declared", "events=tokenfuse|wardryx", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			events := filepath.Join(dir, "events.ndjson")
			mail := filepath.Join(dir, "mail.out")
			env(t, map[string]string{
				"HERALDYX_EVENTS":       events,
				"HERALDYX_TO":           "ops@example.com",
				"HERALDYX_MAIL_FILE":    mail,
				"HERALDYX_MIN_SEVERITY": "medium",
				"HERALDYX_STATE":        filepath.Join(dir, "state.json"),
				"HERALDYX_STREAMS":      tc.streams,
			})
			write(t, events, lineFrom("tokenfuse", "budget_exhausted", "critical", "run-t")+
				lineFrom("wardryx", "policy_deny", "high", "run-w"))
			if err := run([]string{"--once", "--from-now=false"}); err != nil {
				t.Fatal(err)
			}
			got := ""
			if raw, err := os.ReadFile(mail); err == nil {
				got = string(raw)
			}
			mailed := strings.Contains(got, "run-t") && strings.Contains(got, "run-w")
			if mailed != tc.wantMailed {
				t.Fatalf("streams=%q: lines mailed = %v, want %v\n%s", tc.streams, mailed, tc.wantMailed, subjects(got))
			}
			if tc.wantMailed && strings.Count(got, "Subject: ") != 2 {
				t.Fatalf("a declared file must raise nothing, subjects:\n%s", subjects(got))
			}
		})
	}
}

// The two counters are said once per growth, like the byte cap's: a counter
// nobody prints is a field an operator cannot act on, and a line on every poll
// of a flood is a log nobody reads.
func TestForeignAndUnknownCountsAreSaidOncePerGrowthNotEveryPoll(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	sf, su := 0, 0
	for _, c := range []struct{ foreign, unknown int }{{0, 0}, {2, 0}, {2, 0}, {5, 1}, {5, 1}} {
		sf = sayForeign(c.foreign, sf)
		su = sayUnknown(c.unknown, su)
	}
	if sf != 5 || su != 1 {
		t.Fatalf("high-water marks: foreign %d, unknown %d, want 5 and 1", sf, su)
	}
	got := out.String()
	if n := strings.Count(got, "refused because the file they were read from may not carry"); n != 2 {
		t.Fatalf("want one line per growth of the foreign count, 2 in all, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "3 event(s) refused") || !strings.Contains(got, "5 since this process started") {
		t.Errorf("the second line must say what it added and the total:\n%s", got)
	}
	if n := strings.Count(got, "read from a stream this box has no declaration for"); n != 1 {
		t.Fatalf("want one line for the unknown count, got %d:\n%s", n, got)
	}
}
