// heraldyx watches the agent stack's shared event log and mails the operator
// when one of their agents is heading somewhere they would want to know about.
//
// It reads a file and sends mail. It holds no credential for any plane, has no
// API of its own, and can take no action on any agent: the mail it sends
// carries a link into the operator's own console, never a control. That
// narrowness is deliberate. This is the one component of the box that talks to
// something outside it, so it is the one component whose blast radius has to
// be small enough to state in a sentence.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
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

// version is stamped at build time (see the Makefile).
var version = "dev"

// aroundLimit is how many other agents an alert names. Long enough to show a
// pattern, short enough that the alert is still about the thing it is about:
// an operator who has to scroll a mail at 3am reads none of it.
const aroundLimit = 6

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "heraldyx: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("heraldyx", flag.ContinueOnError)
	var (
		testMail  = fs.Bool("test-mail", false, "send one test message and exit (used by the installer, so a wrong mail setup is found while the operator is still at the keyboard)")
		once      = fs.Bool("once", false, "poll once and exit, instead of running until stopped")
		fromNow   = fs.Bool("from-now", true, "on a first run, start at the end of the event log rather than mailing its history")
		showVer   = fs.Bool("version", false, "print the version and exit")
		journalOn = fs.Bool("journal", false, "print what has been sent, verify the record chain, and exit non-zero if it is broken")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "heraldyx: mail the operator when an agent needs them\n\nflags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), "\nenvironment:\n%s\n", envHelp)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVer {
		fmt.Println(version)
		return nil
	}

	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	sender, err := senderFor(cfg)
	if err != nil {
		return err
	}
	rcfg := rule.Config{
		DedupWindow: cfg.DedupWindow,
		MaxPerHour:  cfg.MaxPerHour,
	}
	if rcfg.MinRank, err = rule.ParseSeverity(cfg.MinSeverity); err != nil {
		return err
	}
	rendercfg := render.Config{Box: cfg.Box, ConsoleURL: cfg.ConsoleURL}

	if *journalOn {
		return showJournal(cfg)
	}

	if *testMail {
		return sendTest(cfg, rendercfg, sender)
	}

	journal, err := record.Open(cfg.SentPath)
	if err != nil {
		// Not fatal, and the ordering matters: mail that goes out unrecorded is
		// worse than mail that does not go out only in an argument nobody is
		// having yet, while an alert that never left because an audit file
		// could not be opened is a failure happening right now.
		log.Printf("record: %v", err)
	}
	defer closeJournal(journal, cfg.SentPath)

	snap, err := state.Load(cfg.StatePath)
	if err != nil {
		// Reported, not fatal: see state.Load.
		log.Printf("state: %v", err)
	}
	// Who is answerable, when a passport directory says so. Optional by
	// design: without it an alert simply carries no owner line rather than a
	// guessed one.
	passports := passport.Open(cfg.PassportsPath)
	if passports.Enabled() {
		log.Printf("passports: %d owner(s) known from %s", passports.Count(time.Now()), cfg.PassportsPath)
	}
	// What else is going on. In memory only: this is what this process has seen
	// since it started, and a fresh process says less rather than describing a
	// fleet from before a rollout.
	picture := fleet.New()

	w := watch.New(cfg.ResolveEventFiles(), snap.Offsets)
	if *fromNow && len(snap.Offsets) == 0 {
		if err := w.SkipToEnd(); err != nil {
			log.Printf("watch: %v", err)
		}
	}

	// What this process READS and whether it can SEND are two different facts,
	// and this used to print only one of them: with no mail configured the
	// "watching" line was skipped entirely, so an operator asking why no mail
	// arrived could not tell a notifier that is deliberately off from one that
	// is blind. Measured on a live cluster 2026-08-03, where a check that
	// looked for that line concluded the notifier saw none of three event logs
	// it was in fact watching.
	//
	// A notifier with no recipients still reads the log: it keeps its offsets,
	// counts the digest, and is one Secret away from mailing what it saw. The
	// input line therefore belongs in both cases.
	//
	// The RESOLVED files, not the configured paths. A configured path is
	// usually a directory, and counting those says "watching 1 path(s)"
	// whether that directory holds two event logs or none at all: an empty log
	// and a quiet fleet look identical from here afterwards.
	files := cfg.ResolveEventFiles()
	log.Printf("watching %d file(s) under %d path(s), floor %s, dedup %s, ceiling %d/hour",
		len(files), len(cfg.EventPaths), cfg.MinSeverity, cfg.DedupWindow, cfg.MaxPerHour)
	if len(files) == 0 {
		log.Printf("no event log exists yet under %s: this process will keep looking, and until one appears there is nothing to notify about",
			strings.Join(cfg.EventPaths, ", "))
	}

	if reason := cfg.Why(); reason != "" {
		log.Printf("notifications are OFF: %s. This process will watch and stay healthy, and send nothing.", reason)
	} else {
		log.Printf("sending via %s to %d recipient(s)", sender.Name(), len(cfg.To))
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	tick := time.NewTicker(cfg.PollInterval)
	defer tick.Stop()

	// How many unwritten records have already been logged. See sayUnrecorded.
	said := 0

	for {
		cycle(cfg, rcfg, rendercfg, w, snap, sender, journal, passports, picture, time.Now())
		said = sayUnrecorded(journal.Failures, said)
		snap.Offsets = w.Offsets()
		if err := state.Save(cfg.StatePath, snap); err != nil {
			log.Printf("state: %v", err)
		}
		if *once {
			return nil
		}
		select {
		case <-stop:
			log.Printf("stopping")
			return nil
		case <-tick.C:
		}
	}
}

// closeJournal closes the journal and logs a failure, in the same voice as
// the record.Open and state.Load errors a few lines above and below it in
// run(). Split out, the way cycle already is, so a test can drive it
// directly: forcing record.Open's own file to fail closing from outside the
// package is not practical, and a double close is.
//
// The error is logged rather than folded into journal.Failures. Failures
// counts per-dispatch write failures, and its growth is reported by
// sayUnrecorded once per poll cycle from inside the loop in run(); this
// deferred close runs exactly once, after that loop has already returned, so
// an increment here would never reach sayUnrecorded and would be exactly the
// kind of silent counter this defect is about. A close failure is also a
// different fact from a per-message write failure: it is about the journal
// file as a whole, at the moment this process is shutting down.
func closeJournal(journal *record.Journal, path string) {
	if err := journal.Close(); err != nil {
		log.Printf("record: close %s: %v", path, err)
	}
}

// sayUnrecorded reports messages that went out with no record behind them, and
// returns the count that has now been said.
//
// It exists because the two things that cause a missing record are invisible
// from everywhere else. A dispatch with no agent id to file it under is not
// recorded, because the envelope requires one and this stack does not invent
// one (invariant 11); a chained write that failed is a journal that has stopped
// recording. The first is a known gap in the trail and the second is a fault,
// and an operator can act on neither one they are never told about. The mail
// itself is unaffected in both cases, which is the whole reason this can go
// unnoticed.
//
// It reports the GROWTH, not the standing count, because the journal's counter
// is cumulative for the life of the process and this runs once per poll. A line
// every two seconds about a gap from an hour ago is how an operator learns to
// stop reading this process's log, which would cost more than the silence it
// replaced.
func sayUnrecorded(failures, said int) int {
	if failures <= said {
		return said
	}
	log.Printf("record: %d message(s) sent without a record just now, %d since this process started: no agent id to file them under, or the write failed. The mail went out either way, and the journal is short by that many.",
		failures-said, failures)
	return failures
}

// cycle is one pass: read what is new, decide, send. Split out so a test can
// drive it with a fixed clock and no timers.
func cycle(
	cfg config.Config,
	rcfg rule.Config,
	rendercfg render.Config,
	w *watch.Watcher,
	snap *state.Snapshot,
	sender deliver.Sender,
	journal *record.Journal,
	passports *passport.Directory,
	picture *fleet.Picture,
	now time.Time,
) {
	w.SetPaths(cfg.ResolveEventFiles())
	// The agent the digest is attributed to. A digest is not about one agent,
	// but the envelope requires an id and this stack never invents one, so it
	// is attributed to the last agent that actually caused a message this
	// cycle. When nothing did, it is not recorded and the gap is counted,
	// which is the honest outcome rather than a record filed under a made-up
	// identity.
	var lastAgent, lastRun string
	// And the last agent the ceiling actually HELD, which is a different
	// question. A notice about alerts that were held back must not be recorded
	// against an agent whose alert went out.
	var heldAgent, heldRun string
	for _, e := range w.Poll() {
		// Every event feeds the picture, including the ones nobody is mailed
		// about: an agent quietly at 80% of its budget is exactly the context
		// that makes a different agent's alert worth reading.
		picture.Note(e, now)
		switch rule.Decide(rcfg, snap.Rule, e, now) {
		case rule.Notify:
			lastAgent, lastRun = e.AgentID, e.RunID
			around := make([]render.Around, 0, aroundLimit)
			for _, a := range picture.Around(e.AgentID, now, aroundLimit) {
				around = append(around, render.Around{Label: a.Kind.Label(), AgentID: fleet.Short(a.AgentID), What: a.What})
			}
			owner := passports.OwnerOf(e.AgentID, now)
			deliver_(cfg, sender, journal, render.Event(rendercfg, e, now, owner, around), record.Dispatch{
				Kind:    record.KindAlert,
				AgentID: e.AgentID,
				RunID:   e.RunID,
				About:   rule.Key(e),
			}, now)
		case rule.Suppressed:
			// Counted by rule.Decide, told about below. The notice used to go
			// from HERE, which meant it went on the FIRST event the ceiling
			// refused: the counter stood at exactly 1 at that instant, and
			// taking the notice stamped the one-per-hour window that then
			// blocked every later event of the same burst. Measured
			// 2026-08-03 on 30 events against a ceiling of 20: the mail said
			// one alert had been held back, ten had, and nine sat in the state
			// file. Understating a flood during the exact event the ceiling
			// exists for.
			heldAgent, heldRun = e.AgentID, e.RunID
		case rule.Digest, rule.Drop:
			// Nothing now. The digest goes out on its own schedule below.
		}
	}

	// The ceiling's own notice: one per window, carrying everything held back
	// since the last one. At the end of the cycle for the same reason the
	// digest is, so it counts what this poll actually did rather than what its
	// first refused event did.
	//
	// Taken unconditionally, not only when this cycle held something. What
	// releases the count is the CLOCK, and a remainder that can only leave on
	// the next suppression is a remainder nobody hears about in the ordinary
	// ending, where the flood stops. This way it leaves on the first poll after
	// the window opens, about two seconds later by default.
	//
	// When nothing was held this cycle there is no agent to file it under, and
	// this stack does not invent one (invariant 11). The mail goes, and the
	// journal counts the record it did not write, exactly as the digest below
	// behaves when nothing caused a message.
	if n, due := snap.Rule.TakeSuppressionNotice(time.Hour, now); due {
		if heldAgent != "" {
			lastAgent, lastRun = heldAgent, heldRun
		}
		deliver_(cfg, sender, journal, render.Suppression(rendercfg, n, now), record.Dispatch{
			Kind:    record.KindSuppression,
			AgentID: heldAgent,
			RunID:   heldRun,
			About:   fmt.Sprintf("suppressed:%d", n),
		}, now)
	}

	if cfg.DigestPeriod > 0 && snap.Rule.DigestDue(cfg.DigestPeriod, now) {
		since := time.UnixMilli(snap.Rule.DigestSince)
		entries := snap.Rule.TakeDigest(now)
		deliver_(cfg, sender, journal, render.Digest(rendercfg, entries, since, now), record.Dispatch{
			Kind:    record.KindDigest,
			AgentID: lastAgent,
			RunID:   lastRun,
			About:   fmt.Sprintf("digest:%d", len(entries)),
		}, now)
	}
}

// deliver_ sends one message and records that it did, or does neither when
// this box has no recipients.
//
// Both halves live behind ONE condition on purpose. The first version of this
// had them apart, and on a box with no address configured it sent nothing and
// then wrote an audit record saying a message had been accepted. A trail that
// claims a notification nobody received is worse than no trail: it is the
// exact thing an operator would later hold up as proof.
//
// A delivery failure is logged, recorded as a refusal, and dropped. The
// alternative is a retry queue inside a process whose whole value is being
// simple, and the event that caused this is already in a log that outlives it.
func deliver_(
	cfg config.Config,
	sender deliver.Sender,
	journal *record.Journal,
	m render.Message,
	d record.Dispatch,
	now time.Time,
) {
	if len(cfg.To) == 0 {
		return
	}
	err := sender.Send(cfg.To, m)
	if err != nil {
		log.Printf("delivery failed (%s): %v", sender.Name(), err)
	}
	d.To = cfg.To
	d.Transport = sender.Name()
	d.Err = err
	journal.Sent(d, now)
}

func sendTest(cfg config.Config, rendercfg render.Config, sender deliver.Sender) error {
	if reason := cfg.Why(); reason != "" {
		return fmt.Errorf("cannot send a test message: %s", reason)
	}
	m := render.Test(rendercfg, time.Now())
	if err := sender.Send(cfg.To, m); err != nil {
		return fmt.Errorf("test message: %w", err)
	}
	fmt.Printf("test message sent to %s via %s\n", strings.Join(cfg.To, ", "), sender.Name())
	return nil
}

// showJournal prints the dispatch record and fails when it is not intact.
//
// The image has no shell, deliberately, so an operator cannot read this file
// themselves without copying a volume out. This is how they ask. It exits
// non-zero on a broken chain so a deployment check can use it directly rather
// than parsing prose.
func showJournal(cfg config.Config) error {
	st, err := record.ReadStatus(cfg.SentPath)
	if err != nil {
		return err
	}
	fmt.Print(st)
	if !st.Ok() {
		return fmt.Errorf("the record at %s is not intact: %d chain break(s), %d malformed line(s)",
			st.Path, st.Breaks, st.Malformed)
	}
	return nil
}

func senderFor(cfg config.Config) (deliver.Sender, error) {
	switch {
	case cfg.MailFile != "":
		// Deliberately checked BEFORE SMTP: if an operator set both, the one
		// that cannot leave the machine wins. Surprising in the safe
		// direction is the only acceptable kind of surprising here.
		return deliver.NewFile(cfg.MailFile), nil
	case cfg.SMTPHost != "":
		return deliver.NewSMTP(deliver.SMTPConfig{
			Host:     cfg.SMTPHost,
			From:     cfg.SMTPFrom,
			Username: cfg.SMTPUser,
			Password: cfg.SMTPPass,
		})
	default:
		return deliver.Discard{}, nil
	}
}

const envHelp = `  HERALDYX_EVENTS         files and/or directories of NDJSON agent-events
                          (default /var/lib/stack/events)
  HERALDYX_TO             comma-separated recipients; empty = send nothing
  HERALDYX_MIN_SEVERITY   info|low|medium|high|critical (default high)
  HERALDYX_BOX            name of this deployment, used in the subject line
  HERALDYX_CONSOLE_URL    base URL of the operator's console, for deep links
  HERALDYX_STATE          state file (default /var/lib/stack/heraldyx/state.json)
  HERALDYX_MAIL_FILE      write messages to this file instead of sending them
  HERALDYX_SMTP_HOST      host:port of the mail server, e.g. smtp.example.com:587
  HERALDYX_SMTP_FROM      envelope sender address
  HERALDYX_SMTP_USER      username, if the server wants one
  HERALDYX_SMTP_PASS      password, if the server wants one
  HERALDYX_DEDUP_SECONDS  quiet window per condition (default 600)
  HERALDYX_MAX_PER_HOUR   ceiling on immediate messages (default 20, 0 = none)
  HERALDYX_DIGEST_HOURS   how often the below-threshold summary goes out
                          (default 24, 0 = never)
  HERALDYX_POLL_MS        how often to read the log (default 2000)`
