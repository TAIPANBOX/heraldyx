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
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	// The zone database is compiled in so a distroless image can load a zone
	// such as Europe/London for HERALDYX_QUIET, with no /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/TAIPANBOX/heraldyx/internal/config"
	"github.com/TAIPANBOX/heraldyx/internal/control"
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
		testMail   = fs.Bool("test-mail", false, "send one test message and exit (used by the installer, so a wrong mail setup is found while the operator is still at the keyboard)")
		once       = fs.Bool("once", false, "poll once and exit, instead of running until stopped")
		fromNow    = fs.Bool("from-now", true, "on a first run, start at the end of the event log rather than mailing its history")
		showVer    = fs.Bool("version", false, "print the version and exit")
		journalOn  = fs.Bool("journal", false, "print what has been sent, verify the record chain, and exit non-zero if it is broken")
		ackKey     = fs.String("ack", "", "acknowledge the condition with this key (the Key line of a mail): alerts below critical about it are held until --for passes and reported when it ends; a critical is always sent")
		ackFor     = fs.Duration("for", rule.DefaultAck, "how long --ack holds, at most 168h")
		ackBy      = fs.String("by", "", "who acknowledges; recorded as reported, not verified")
		ackNote    = fs.String("note", "", "a short note kept with the acknowledgement")
		unackKey   = fs.String("unack", "", "remove the acknowledgement of this key now")
		listAcks   = fs.Bool("acks", false, "list the acknowledgements in force and exit")
		maintFor   = fs.Duration("maintenance", 0, "open a maintenance window for this long, at most 24h: alerts below critical are held and reported when it closes; a critical is always sent")
		maintScope = fs.String("scope", "", "with --maintenance: hold only alerts from this source")
		maintEnd   = fs.Bool("maintenance-end", false, "close the maintenance window now")
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

	// Whether --maintenance was given at all, so that --maintenance 0 is
	// refused as a window and does not fall through to running the loop.
	maintGiven := false
	fs.Visit(func(f *flag.Flag) {
		maintGiven = maintGiven || f.Name == "maintenance"
	})

	// The acknowledgement and maintenance commands come before senderFor: they
	// only touch the control file, and they must work on a box whose mail is
	// not set up.
	switch {
	case *ackKey != "":
		return ackCommand(cfg, *ackKey, *ackFor, *ackBy, *ackNote, time.Now())
	case *unackKey != "":
		return unackCommand(cfg, *unackKey, time.Now())
	case *listAcks:
		return listAcksCommand(cfg, time.Now())
	case maintGiven:
		return maintenanceCommand(cfg, *maintFor, *maintScope, *ackBy, *ackNote, time.Now())
	case *maintEnd:
		return endMaintenanceCommand(cfg, time.Now())
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
	w.SetHeads(snap.Heads)
	w.SetPolicy(cfg.StreamPolicy())
	// The checkpoint gate (F-018) is off unless the operator switched it on, and
	// then a stream is read only while a signed checkpoint covers it.
	gate, err := checkpointGate(cfg)
	if err != nil {
		return err
	}
	if gate != nil {
		w.SetCoverage(gate.Covered)
	}
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
	for _, u := range cfg.UnreadStreamEntries() {
		log.Printf("not reading %s: it is named as one stream file and %s", u.Path, u.Why)
	}
	if cfg.QuietWhy != "" {
		log.Printf("quiet hours: %s", cfg.QuietWhy)
	}
	if q := cfg.Quiet; q != nil {
		log.Printf("quiet hours: %02d:%02d to %02d:%02d in %s; alerts below critical are held and reported when the hours end, a critical is always sent",
			q.Start/60, q.Start%60, q.End/60, q.End%60, q.Loc)
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
	saidVolume := 0
	saidForeign := 0
	saidUnknown := 0
	saidUncovered := 0
	saidHolds := watch.Holds{}

	for {
		cycle(cfg, rcfg, rendercfg, w, snap, sender, journal, passports, picture, time.Now())
		said = sayUnrecorded(journal.Failures, said)
		saidVolume = sayVolume(w.Capped, w.Oversized, saidVolume)
		saidForeign = sayForeign(w.ForeignSource, saidForeign)
		saidUnknown = sayUnknown(w.UnknownStream, saidUnknown)
		saidUncovered = sayUncovered(w.NotCovered, saidUncovered)
		saidHolds = sayHolds(w.Holds(), saidHolds)
		snap.Offsets = w.Offsets()
		snap.Heads = w.Heads()
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

// sayVolume reports a plane producing more than a poll can read, and returns
// the count that has now been said.
//
// A counter nobody prints is a field an operator cannot act on, which is the
// shape invariant 13 records as worse than an absent field. Same discipline as
// sayUnrecorded: growth, not the standing count, once per poll. Two facts in
// one line because they are the same fact at two sizes: a poll that hit the
// cap means the plane wrote more than a cap between two polls and the rest is
// delayed to the next one; an oversized line means one event alone did not
// fit and was skipped rather than delivered.
func sayVolume(capped, oversized, said int) int {
	now := capped + oversized
	if now <= said {
		return said
	}
	log.Printf("watch: a plane is producing more than one poll reads: %d capped poll(s) and %d oversized line(s) since this process started (%d new). A capped poll is read on the next one; an oversized line is skipped and counted, never delivered.",
		capped, oversized, now-said)
	return now
}

// sayHolds reports reads held, given up on, or started again, and returns
// what has now been said.
//
// Each counter is a different fact and gets its own line, once per growth,
// for the reason sayVolume gives. Two of them are said because they cost the
// operator something: a file read again from its start may mail alerts a
// second time, and a skipped line with a zero byte in it may hold events this
// process will never see. The other two are said because a hold nobody can
// see reads, from outside, exactly like a plane that went quiet.
func sayHolds(now, said watch.Holds) watch.Holds {
	if n := now.Shorter - said.Shorter; n > 0 {
		log.Printf("watch: %d file(s) looked shorter than what had already been read (%d since this process started), and nothing shows a different file, so the read position is kept and nothing is read twice. A shared volume can report an old size, and a writer's node can hold bytes the volume has not had yet; reading resumes when the file reaches the position again.",
			n, now.Shorter)
	}
	if n := now.ShorterExpired - said.ShorterExpired; n > 0 {
		log.Printf("watch: %d file(s) stayed shorter than what had been read for %s and are read again from the start (%d since this process started). Alerts already mailed from them may be mailed again, within the dedup window and the hourly ceiling.",
			n, watch.HoldWindow, now.ShorterExpired)
	}
	if n := now.Truncations - said.Truncations; n > 0 {
		log.Printf("watch: %d file(s) are a different file from the one being read (its first line changed) and are read from the start (%d since this process started).",
			n, now.Truncations)
	}
	if n := now.ZeroHeld - said.ZeroHeld; n > 0 {
		log.Printf("watch: %d line(s) with a zero byte in them were met (%d since this process started): a span the volume has not filled in yet. Reading that file stops before it and looks again each poll, for up to %s.",
			n, now.ZeroHeld, watch.HoldWindow)
	}
	if n := now.ZeroSkipped - said.ZeroSkipped; n > 0 {
		log.Printf("watch: %d line(s) with a zero byte in them were still there after %s and were skipped (%d since this process started). Any event in that span is lost to this process: a defect of the volume or of a writer, and the file itself is not changed.",
			n, watch.HoldWindow, now.ZeroSkipped)
	}
	return now
}

// sayUncovered reports events refused because their file has no signed
// checkpoint, once per growth, the way sayForeign reports the foreign ones. The
// mail says it once per file; this line is how a flood from a file already
// raised stays visible.
func sayUncovered(n, said int) int {
	if n <= said {
		return said
	}
	log.Printf("watch: %d event(s) refused because no signed checkpoint covers their file, %d since this process started. They were not processed or mailed. One notice goes per file; HERALDYX_CHECKPOINT_GATE=off reads them as before.",
		n-said, n)
	return n
}

// sayForeign reports events refused because the file they sat in may not carry
// the source they claimed, and returns the count that has now been said.
//
// The counter is the same kind of fact as Malformed and Capped: process
// lifetime, cumulative, and useless to an operator unless something prints it.
// The growth, once per poll, for the reason sayVolume gives. The mail is
// separate and goes once per file and claimed source; this line is how the
// rest of a flood from an already-raised pair stays visible without a mail.
func sayForeign(n, said int) int {
	if n <= said {
		return said
	}
	log.Printf("watch: %d event(s) refused because the file they were read from may not carry the source they claim, %d since this process started. They were not processed as that source: not mailed, not rendered, not in the digest. One alert goes per file and claimed source; HERALDYX_STREAMS declares a file that is meant to carry more.",
		n-said, n)
	return n
}

// sayUnknown reports events read from a stream nothing declares, and returns
// the count that has now been said. These ARE processed, so the line says so.
func sayUnknown(n, said int) int {
	if n <= said {
		return said
	}
	log.Printf("watch: %d event(s) read from a stream this box has no declaration for, %d since this process started. They claim the file's own name as their source and were processed; one alert goes per file. HERALDYX_STREAMS declares a stream that is expected.",
		n-said, n)
	return n
}

// cadence is how often the ceiling's summary goes out while alerts are being
// held. A package value rather than a flag or a variable: the ceiling is the
// operator's to set, and how often they are told it is holding the line is
// this process's own promise, the same one in every deployment.
var cadence = rule.DefaultCadence()

// sayCeiling prints the line at the ceiling: that it is holding alerts back
// from here, which one was the first, and what happens next. Called once per
// summary period, on the first alert held since the previous summary, by the
// caller that knows that.
//
// It names the three things an operator reading the log needs in order to
// stop wondering: a summary follows within the interval and repeats while
// anything is held, a critical is not held at all, and the events themselves
// are all still in the log. Issue #71 measured the version of this that said
// nothing: twenty alerts, then silence, for 28 minutes.
func sayCeiling(maxPerHour int, first string, c rule.Cadence) {
	log.Printf("ceiling: %d messages went out in the last hour, so alerts below critical are held back from here (%s first). What happens next: a summary naming the count and the worst severity goes out within %s and again every %s while any are held, sooner for a burst of %d; a critical is still sent at once, one message per condition; every event stays in the log and the console.",
		maxPerHour, first, c.Every, c.Every, c.Burst)
}

// ackCommand writes one acknowledgement to the control file. It is the only
// writer of that file besides --unack: the loop only reads it.
func ackCommand(cfg config.Config, key string, d time.Duration, by, note string, now time.Time) error {
	f, err := control.Load(cfg.ControlPath)
	if err != nil {
		return fmt.Errorf("nothing was changed, because the control file cannot be read: %w", err)
	}
	f.Prune(now)
	a := rule.Ack{Key: key, At: now.UnixMilli(), Until: now.Add(d).UnixMilli(), By: by, Note: note}
	if err := f.Put(a); err != nil {
		return err
	}
	if err := control.Save(cfg.ControlPath, f); err != nil {
		return err
	}
	fmt.Printf("acknowledged %s until %s: alerts below critical about it are held and reported when this ends; a critical is still sent\n",
		key, now.Add(d).UTC().Format(time.RFC3339))
	return nil
}

func unackCommand(cfg config.Config, key string, now time.Time) error {
	f, err := control.Load(cfg.ControlPath)
	if err != nil {
		return fmt.Errorf("nothing was changed, because the control file cannot be read: %w", err)
	}
	f.Prune(now)
	if !f.Remove(key) {
		return fmt.Errorf("no acknowledgement of %s is in force", key)
	}
	if err := control.Save(cfg.ControlPath, f); err != nil {
		return err
	}
	fmt.Printf("acknowledgement of %s removed; what it held is reported on the next poll\n", key)
	return nil
}

// maintenanceCommand opens a maintenance window. It replaces a window already
// open, and with --maintenance-end it is the only writer of the window besides
// that command: the loop only reads the control file.
func maintenanceCommand(cfg config.Config, d time.Duration, scope, by, note string, now time.Time) error {
	f, err := control.Load(cfg.ControlPath)
	if err != nil {
		return fmt.Errorf("nothing was changed, because the control file cannot be read: %w", err)
	}
	f.Prune(now)
	m := rule.Maintenance{At: now.UnixMilli(), Until: now.Add(d).UnixMilli(), Scope: scope, By: by, Note: note}
	if err := f.SetMaintenance(m); err != nil {
		return err
	}
	if err := control.Save(cfg.ControlPath, f); err != nil {
		return err
	}
	fmt.Printf("maintenance window open until %s: alerts below critical are held and reported when it closes; a critical is still sent\n",
		time.UnixMilli(m.Until).UTC().Format(time.RFC3339))
	return nil
}

// endMaintenanceCommand closes the maintenance window now. What it held is
// reported by the loop on its next poll, as an unacknowledgement's is.
func endMaintenanceCommand(cfg config.Config, now time.Time) error {
	f, err := control.Load(cfg.ControlPath)
	if err != nil {
		return fmt.Errorf("nothing was changed, because the control file cannot be read: %w", err)
	}
	f.Prune(now)
	if !f.EndMaintenance() {
		return errors.New("no maintenance window is open")
	}
	if err := control.Save(cfg.ControlPath, f); err != nil {
		return err
	}
	fmt.Println("maintenance window closed; what it held is reported on the next poll")
	return nil
}

func listAcksCommand(cfg config.Config, now time.Time) error {
	f, err := control.Load(cfg.ControlPath)
	if err != nil {
		return err
	}
	active := f.Active(now)
	if len(active) == 0 {
		fmt.Println("no acknowledgement is in force")
		return nil
	}
	for _, a := range sortedAcks(active) {
		fmt.Printf("%s until %s by %q note %q\n", a.Key, time.UnixMilli(a.Until).UTC().Format(time.RFC3339), a.By, a.Note)
	}
	return nil
}

// controlMemory is what the loop last logged about the control file: the error
// text, the set of keys in force and the maintenance window in force. Package
// level because cycle is called with a fresh argument list each poll and this
// is a log de-duplicator, not state; nothing decides on it. newBench resets it.
type controlMemory struct{ err, keys, maint string }

var controlSaid controlMemory

// loadControl reads what the operator has set on the box for this poll: the
// acknowledgements in force and the maintenance window, if one is. A file that
// cannot be read holds nothing, so neither is honoured, and says so once per
// distinct error.
func loadControl(path string, now time.Time) (map[string]rule.Ack, *rule.Maintenance) {
	f, err := control.Load(path)
	if err != nil {
		if err.Error() != controlSaid.err {
			log.Printf("control: %v; no acknowledgement is honoured and no maintenance window is in force until it is fixed", err)
			controlSaid.err = err.Error()
		}
		return nil, nil
	}
	controlSaid.err = ""
	active := f.Active(now)
	keys := make([]string, 0, len(active))
	for k := range active {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if joined := strings.Join(keys, ","); joined != controlSaid.keys {
		log.Printf("control: %d acknowledgement(s) in force: %s", len(keys), joined)
		controlSaid.keys = joined
	}
	m := f.ActiveMaintenance(now)
	label := ""
	if m != nil {
		label = fmt.Sprintf("%d:%d:%s", m.At, m.Until, m.Scope)
	}
	if label != controlSaid.maint {
		switch {
		case m != nil:
			scope := "every source"
			if m.Scope != "" {
				scope = m.Scope
			}
			log.Printf("control: maintenance window in force until %s, scope %s; alerts below critical are held and a critical is still sent",
				time.UnixMilli(m.Until).UTC().Format(time.RFC3339), scope)
		case controlSaid.maint != "":
			log.Printf("control: no maintenance window is in force")
		}
		controlSaid.maint = label
	}
	return active, m
}

// sortedAcks returns the acknowledgements ordered by key, so the pulse lists
// them the same way on every poll.
func sortedAcks(acks map[string]rule.Ack) []rule.Ack {
	keys := make([]string, 0, len(acks))
	for k := range acks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]rule.Ack, 0, len(keys))
	for _, k := range keys {
		out = append(out, acks[k])
	}
	return out
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
	files := cfg.ResolveEventFiles()
	w.SetPaths(files)
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
	events := w.Poll()
	snap.Rule.NotePoll(len(events), now)
	// The acknowledgements in force this poll. rcfg is this call's own copy,
	// so setting Acks here reaches only the decisions made in this cycle.
	acks, maint := loadControl(cfg.ControlPath, now)
	rcfg.Acks, rcfg.Maintenance, rcfg.Quiet = acks, maint, cfg.Quiet
	// Announce maintenance transitions before anything is decided, so held
	// alerts are counted into the window they belong to. A close is always
	// mailed, even with nothing held, because that mail is how the operator
	// learns that alerting is back.
	if step := snap.Rule.TakeMaintenanceStep(maint, now); step.Closed || step.Open != nil {
		if step.Closed {
			deliver_(cfg, sender, journal, render.MaintenanceClosed(rendercfg, step.ClosedWindow, step.Held, now), record.Dispatch{
				Kind:  record.KindMaintenance,
				About: fmt.Sprintf("maintenance_closed:%d", step.Held.Count),
			}, now)
		}
		if step.Open != nil {
			deliver_(cfg, sender, journal, render.MaintenanceOpened(rendercfg, *step.Open, now), record.Dispatch{
				Kind:  record.KindMaintenance,
				About: "maintenance_opened",
			}, now)
		}
	}
	// What the watcher refused or did not recognise is raised BEFORE the events
	// are decided, so a poll full of ordinary alerts cannot spend the hour's
	// ceiling ahead of the one notice that says a plane's name is being used.
	raiseStreamNotices(cfg, rcfg, rendercfg, w, snap, sender, journal, now)
	for _, e := range events {
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
			m := render.Event(rendercfg, e, now, owner, around)
			if a, ok := acks[rule.Key(e)]; ok && rule.IsCritical(e.Severity) {
				m.Body += render.AckedCritical(a)
			}
			if rule.IsCritical(e.Severity) {
				if maint != nil && maint.Covers(e.Source, now) {
					m.Body += render.WindowCritical(rule.WindowMaintenance, maint, snap.Rule.MaintHeld.Count)
				} else if cfg.Quiet != nil && cfg.Quiet.Contains(now) {
					m.Body += render.WindowCritical(rule.WindowQuiet, nil, snap.Rule.QuietHeld.Count)
				}
			}
			deliver_(cfg, sender, journal, m, record.Dispatch{
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
			// The log line at the ceiling, and what happens next. Once per
			// summary period rather than once per held event: the first hold
			// since the previous summary is the moment the ceiling starts
			// mattering again, and a line on every held event would repeat
			// itself every poll of a flood, which is a log an operator stops
			// reading. Issue #71 found the log silent here, twenty alerts and
			// then nothing, with no way to tell a quiet fleet from a held one.
			if snap.Rule.SuppressedSince == 1 {
				sayCeiling(rcfg.MaxPerHour, rule.Key(e), cadence)
			}
		case rule.Held:
			// Counted by rule.Decide, reported when the acknowledgement or the
			// window ends. Not a Suppressed: an acknowledgement or a window is
			// the operator's own choice and does not spend the hourly ceiling.
		case rule.Digest, rule.Drop:
			// Nothing now. The digest goes out on its own schedule below.
		}
	}

	// An acknowledgement that ended reports what it held. Filed under no
	// agent: it is about a key, and this stack never invents an id
	// (invariants 11 and 13).
	for _, rel := range snap.Rule.TakeAckReleases(acks, now) {
		deliver_(cfg, sender, journal, render.AckReleased(rendercfg, rel, now), record.Dispatch{
			Kind:  record.KindAckRelease,
			About: fmt.Sprintf("ack_release:%d", rel.Count),
		}, now)
	}

	// Quiet hours that ended with something held report it, as an ended
	// acknowledgement does. Filed under no agent, for the same reason.
	if h, due := snap.Rule.TakeQuietRelease(cfg.Quiet, now); due {
		deliver_(cfg, sender, journal, render.QuietEnded(rendercfg, h, now), record.Dispatch{
			Kind:  record.KindQuietRelease,
			About: fmt.Sprintf("quiet_release:%d", h.Count),
		}, now)
	}

	// The ceiling's own summary: under the cadence, carrying everything held
	// back since the previous one, the time the first of them was held and
	// the worst severity among them. At the end of the cycle for the same
	// reason the digest is, so it counts what this poll actually did rather
	// than what its first refused event did.
	//
	// Taken unconditionally, not only when this cycle held something. What
	// releases the count is the CLOCK, and a remainder that can only leave on
	// the next suppression is a remainder nobody hears about in the ordinary
	// ending, where the flood stops. This way it leaves on the first poll after
	// the interval passes, about two seconds later by default.
	//
	// The interval was an hour until issue #71, the same width as the ceiling
	// itself, and that width measured as one summary at the moment the
	// ceiling was reached followed by 28 minutes of held alerts nobody was
	// told about. It is ten minutes now, sooner for a burst, never inside a
	// minute (rule.DefaultCadence), and the summary that goes says all three.
	//
	// When nothing was held this cycle there is no agent to file it under, and
	// this stack does not invent one (invariant 11). The mail goes, and the
	// journal counts the record it did not write, exactly as the digest below
	// behaves when nothing caused a message.
	if n, due := snap.Rule.TakeSuppressionNotice(cadence, now); due {
		if heldAgent != "" {
			lastAgent, lastRun = heldAgent, heldRun
		}
		deliver_(cfg, sender, journal, render.Suppression(rendercfg, n, now), record.Dispatch{
			Kind:    record.KindSuppression,
			AgentID: heldAgent,
			RunID:   heldRun,
			About:   fmt.Sprintf("suppressed:%d", n.Count),
		}, now)
	}

	switch {
	case cfg.DigestPeriod > 0 && snap.Rule.DigestDue(cfg.DigestPeriod, now):
		since := time.UnixMilli(snap.Rule.DigestSince)
		entries := snap.Rule.TakeDigest(now)
		m := render.Digest(rendercfg, entries, since, now)
		if cfg.PulsePeriod > 0 {
			// The digest is itself a mail that proves the path works, so it
			// carries the pulse and resets its window: one mail, never two.
			m.Body += "\n" + render.PulseLines(snap.Rule.TakePulse(now), len(files), cfg.PulsePeriod, sortedAcks(acks), maint)
		}
		deliver_(cfg, sender, journal, m, record.Dispatch{
			Kind:    record.KindDigest,
			AgentID: lastAgent,
			RunID:   lastRun,
			About:   fmt.Sprintf("digest:%d", len(entries)),
		}, now)
	case snap.Rule.PulseDue(cfg.PulsePeriod, now):
		// No digest carried the pulse in a whole period. Filed like the
		// digest: under the last agent that caused a message this cycle, or
		// counted as a gap when none did (invariants 11 and 13).
		deliver_(cfg, sender, journal, render.Pulse(rendercfg, snap.Rule.TakePulse(now), len(files), cfg.PulsePeriod, now, sortedAcks(acks), maint), record.Dispatch{
			Kind:    record.KindPulse,
			AgentID: lastAgent,
			RunID:   lastRun,
			About:   "pulse",
		}, now)
	}
}

// raiseStreamNotices raises what the watcher found wrong with its input: one
// notice per (file, claimed source) pair that was refused, and one per file of
// a stream nothing declares. Each key is raised once, ever, across polls and
// restarts (snap.Raise), and then goes through the same floor, dedup window
// and ceiling as any alert (rule.DecideKey): both are `high`. Below the floor it lands in the digest rather
// than being lost.
//
// Neither is filed under an agent. The notice is about a file, not about an
// agent, and this process never invents a subject (invariant 11), so the
// journal counts the missing record as it does for any dispatch with no agent
// to file it under.
func raiseStreamNotices(
	cfg config.Config,
	rcfg rule.Config,
	rendercfg render.Config,
	w *watch.Watcher,
	snap *state.Snapshot,
	sender deliver.Sender,
	journal *record.Journal,
	now time.Time,
) {
	for _, f := range w.TakeForeign() {
		raised := "foreign_source:" + f.File + ":" + f.Claimed
		key := "foreign_source:" + filepath.Base(f.File) + ":" + clip(f.Claimed, 64)
		raiseOnce(rcfg, snap, raised, key, "high", now, func() {
			deliver_(cfg, sender, journal, render.ForeignSource(rendercfg, render.Foreign{
				File: f.File, Claimed: f.Claimed, Count: f.Count, Allowed: f.Allowed,
			}, now), record.Dispatch{Kind: record.KindForeignSource, About: key}, now)
		})
	}
	for _, u := range w.TakeUnknown() {
		raised := "unknown_stream:" + u.File
		key := "unknown_stream:" + filepath.Base(u.File)
		raiseOnce(rcfg, snap, raised, key, "high", now, func() {
			deliver_(cfg, sender, journal, render.UnknownStream(rendercfg, render.Unknown{
				File: u.File, Count: u.Count,
			}, now), record.Dispatch{Kind: record.KindUnknownStream, About: key}, now)
		})
	}
	// A file the checkpoint gate refused: one notice per file, ever, because the
	// refusal is a fact about the file and repeating it adds nothing. It is
	// high, since the file's events are not reaching anyone.
	for _, u := range w.TakeUncovered() {
		raised := "uncovered_stream:" + u.File
		key := "uncovered_stream:" + filepath.Base(u.File)
		raiseOnce(rcfg, snap, raised, key, "high", now, func() {
			deliver_(cfg, sender, journal, render.UncoveredStream(rendercfg, render.Uncovered{
				File: u.File, Reason: u.Reason, Count: u.Count,
			}, now), record.Dispatch{Kind: record.KindUncovered, About: key}, now)
		})
	}
}

// raiseOnce lets a notice through at most once per `raised` key, and then
// only as far as the operator's own limits allow.
func raiseOnce(rcfg rule.Config, snap *state.Snapshot, raised, key, severity string, now time.Time, send func()) {
	if !snap.Raise(raised, now) {
		return
	}
	switch rule.DecideKey(rcfg, snap.Rule, key, severity, now) {
	case rule.Notify:
		send()
	case rule.Suppressed:
		if snap.Rule.SuppressedSince == 1 {
			sayCeiling(rcfg.MaxPerHour, key, cadence)
		}
	case rule.Held:
		// Counted by DecideKey, reported when the acknowledgement or window ends.
	case rule.Digest, rule.Drop:
		// Counted in the digest by DecideKey when below the floor; a dropped
		// duplicate inside the dedup window needs nothing more.
	}
}

// clip bounds producer-written text used inside a dedup key.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
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
			TLS:      deliver.TLSMode(cfg.SMTPTLS),
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
  HERALDYX_CONTROL        acknowledgements file, written by --ack (default:
                          control.json beside HERALDYX_STATE)
  HERALDYX_MAIL_FILE      write messages to this file instead of sending them
  HERALDYX_SMTP_HOST      host:port of the mail server, e.g. smtp.example.com:587
  HERALDYX_SMTP_FROM      envelope sender address
  HERALDYX_SMTP_USER      username, if the server wants one
  HERALDYX_SMTP_PASS      password, if the server wants one
  HERALDYX_SMTP_TLS       starttls (default), implicit (port 465 default), or
                          plain (loopback only, for a local relay)
  HERALDYX_DEDUP_SECONDS  quiet window per condition (default 600)
  HERALDYX_MAX_PER_HOUR   ceiling on immediate messages (default 20, 0 = none)
  HERALDYX_DIGEST_HOURS   how often the below-threshold summary goes out
                          (default 24, 0 = never)
  HERALDYX_PULSE_HOURS    how often a box says the notifier is alive, even
                          when nothing happened (default 24, 0 = never)
  HERALDYX_QUIET          daily quiet hours, HH:MM-HH:MM; alerts below
                          critical are held and summarised when they end
  HERALDYX_TZ             IANA zone for HERALDYX_QUIET, e.g. Europe/London
  HERALDYX_POLL_MS        how often to read the log (default 2000)
  HERALDYX_STREAMS        what a stream file may carry beyond the built-in
                          table, as stem=source|source,... (default none)`
