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

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/heraldyx/internal/config"
	"github.com/TAIPANBOX/heraldyx/internal/deliver"
	"github.com/TAIPANBOX/heraldyx/internal/record"
	"github.com/TAIPANBOX/heraldyx/internal/render"
	"github.com/TAIPANBOX/heraldyx/internal/rule"
	"github.com/TAIPANBOX/heraldyx/internal/state"
	"github.com/TAIPANBOX/heraldyx/internal/watch"
)

// version is stamped at build time (see the Makefile).
var version = "dev"

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
	defer journal.Close()

	snap, err := state.Load(cfg.StatePath)
	if err != nil {
		// Reported, not fatal: see state.Load.
		log.Printf("state: %v", err)
	}
	w := watch.New(cfg.ResolveEventFiles(), snap.Offsets)
	if *fromNow && len(snap.Offsets) == 0 {
		if err := w.SkipToEnd(); err != nil {
			log.Printf("watch: %v", err)
		}
	}

	if reason := cfg.Why(); reason != "" {
		log.Printf("notifications are OFF: %s. This process will watch and stay healthy, and send nothing.", reason)
	} else {
		log.Printf("watching %d path(s), floor %s, dedup %s, ceiling %d/hour, sending via %s to %d recipient(s)",
			len(cfg.EventPaths), cfg.MinSeverity, cfg.DedupWindow, cfg.MaxPerHour, sender.Name(), len(cfg.To))
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	tick := time.NewTicker(cfg.PollInterval)
	defer tick.Stop()

	for {
		cycle(cfg, rcfg, rendercfg, w, snap, sender, journal, time.Now())
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
	now time.Time,
) {
	w.SetPaths(cfg.ResolveEventFiles())
	// The agent a digest or a suppression notice is attributed to. Neither is
	// about one agent, but the envelope requires an id and this stack never
	// invents one, so they are attributed to the last agent that actually
	// caused a message this cycle. When nothing did, they are not recorded and
	// the gap is counted, which is the honest outcome rather than a record
	// filed under a made-up identity.
	var lastAgent, lastRun string
	for _, e := range w.Poll() {
		switch rule.Decide(rcfg, snap.Rule, e, now) {
		case rule.Notify:
			lastAgent, lastRun = e.AgentID, e.RunID
			deliver_(cfg, sender, journal, render.Event(rendercfg, e, now), record.Dispatch{
				Kind:    record.KindAlert,
				AgentID: e.AgentID,
				RunID:   e.RunID,
				About:   rule.Key(e),
			}, now)
		case rule.Suppressed:
			if n, due := snap.Rule.TakeSuppressionNotice(time.Hour, now); due {
				lastAgent, lastRun = e.AgentID, e.RunID
				deliver_(cfg, sender, journal, render.Suppression(rendercfg, n, now), record.Dispatch{
					Kind:    record.KindSuppression,
					AgentID: e.AgentID,
					RunID:   e.RunID,
					About:   fmt.Sprintf("suppressed:%d", n),
				}, now)
			}
		case rule.Digest, rule.Drop:
			// Nothing now. The digest goes out on its own schedule below.
		}
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
	now := time.Now()
	m := render.Event(rendercfg, event.Event{
		Schema:   event.SchemaV02,
		TS:       now.UTC().Format(time.RFC3339),
		Source:   "heraldyx",
		Type:     "install_check",
		AgentID:  "agent://example/installer",
		Severity: event.SeverityInfo,
	}, now)
	m.Subject = fmt.Sprintf("[%s] notifications are working", cfg.Box)
	m.Body = "This box can send you mail.\n\n" + m.Body
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
