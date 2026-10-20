// Package config reads heraldyx's settings from the environment.
//
// Everything has a default that is safe to run with, and the one setting with
// no safe default (where to send mail) makes the process do nothing rather
// than fail. An alerting component that refuses to start because it was
// deployed without an address is a component that takes the rest of the
// deployment down with it.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TAIPANBOX/heraldyx/internal/rule"
	"github.com/TAIPANBOX/heraldyx/internal/stream"
)

// Config is the whole of heraldyx's configuration.
type Config struct {
	// EventPaths are files and/or directories holding NDJSON agent-events.
	// A directory contributes every *.ndjson file in it, re-read on every
	// poll so a plane deployed later is picked up.
	EventPaths []string
	// To is the recipient list. Empty means notifications are off.
	To []string
	// MinSeverity is the floor for an immediate message.
	MinSeverity string
	// Box names this deployment in the subject line.
	Box string
	// ConsoleURL is the base of the operator's own console.
	ConsoleURL string
	// StatePath is where dedup counters and read offsets are kept.
	StatePath string
	// MailFile, when set, writes messages to a file instead of sending them.
	MailFile string
	// PassportsPath is an optional directory of agent-passport JSON, read to
	// answer "who is answerable for this agent". Unset means alerts carry no
	// owner line, which is the honest default: the owner lives in the passport
	// and nothing else in this process knows it.
	PassportsPath string
	// ControlPath is the acknowledgements file. Written only by the command
	// line (--ack, --unack); the loop only reads it. Beside the state file by
	// default, so it lands on the volume the deployment already made writable.
	ControlPath string
	// SentPath is the dispatch journal: one chained agent-event per message
	// sent. Defaults to `sent.ndjson` beside the state file, since both are
	// this process's own writable place. Empty disables recording.
	SentPath string
	// Streams is the operator's declaration of what a stream file may carry,
	// on top of the convention (<source>.ndjson carries <source>) and the
	// measured exceptions built into `internal/stream`. Empty for a box whose
	// files follow the convention.
	Streams map[string][]string

	SMTPHost string
	SMTPFrom string
	SMTPUser string
	SMTPPass string
	// SMTPTLS is HERALDYX_SMTP_TLS as written: starttls, implicit or plain.
	// Empty means STARTTLS, or implicit TLS on port 465. The deliver package
	// decides what it means, and refuses a value it does not know.
	SMTPTLS string

	DedupWindow  time.Duration
	MaxPerHour   int
	DigestPeriod time.Duration
	PulsePeriod  time.Duration
	PollInterval time.Duration

	// Quiet is the recurring quiet window from HERALDYX_QUIET, nil when off.
	// QuietWhy says why it is off when the setting was given but cannot be
	// honoured, or what zone it runs in when HERALDYX_TZ is not set; empty
	// otherwise.
	Quiet    *rule.QuietHours
	QuietWhy string

	// CheckpointGate is HERALDYX_CHECKPOINT_GATE: when on, a stream file is read
	// only while a signed checkpoint covers it (F-018). Off by default. RED STUB:
	// FromEnv does not read it yet.
	CheckpointGate bool
	// CheckpointDir is HERALDYX_CHECKPOINTS, where <stream>.ckpt.ndjson files live.
	CheckpointDir string
	// CheckpointKeys is HERALDYX_CHECKPOINT_KEYS, the pinned public keys.
	CheckpointKeys string
}

// FromEnv reads the configuration.
func FromEnv() (Config, error) {
	c := Config{
		EventPaths:    splitList(getenv("HERALDYX_EVENTS", "/var/lib/stack/events")),
		To:            splitList(os.Getenv("HERALDYX_TO")),
		MinSeverity:   getenv("HERALDYX_MIN_SEVERITY", "high"),
		Box:           getenv("HERALDYX_BOX", "agent stack"),
		ConsoleURL:    os.Getenv("HERALDYX_CONSOLE_URL"),
		StatePath:     getenv("HERALDYX_STATE", "/var/lib/stack/heraldyx/state.json"),
		MailFile:      os.Getenv("HERALDYX_MAIL_FILE"),
		SentPath:      os.Getenv("HERALDYX_SENT"),
		PassportsPath: os.Getenv("HERALDYX_PASSPORTS"),
		SMTPHost:      os.Getenv("HERALDYX_SMTP_HOST"),
		SMTPFrom:      os.Getenv("HERALDYX_SMTP_FROM"),
		SMTPUser:      os.Getenv("HERALDYX_SMTP_USER"),
		SMTPPass:      os.Getenv("HERALDYX_SMTP_PASS"),
		SMTPTLS:       os.Getenv("HERALDYX_SMTP_TLS"),
		DedupWindow:   seconds("HERALDYX_DEDUP_SECONDS", 600),
		MaxPerHour:    integer("HERALDYX_MAX_PER_HOUR", 20),
		DigestPeriod:  hours("HERALDYX_DIGEST_HOURS", 24),
		PulsePeriod:   hours("HERALDYX_PULSE_HOURS", 24),
		PollInterval:  millis("HERALDYX_POLL_MS", 2000),
	}
	// Defaulted here rather than in the struct literal above, because it is
	// derived from another field the operator may have set. An explicitly
	// empty HERALDYX_SENT stays empty: turning recording off is a choice, and
	// a default that quietly reinstates it is not a default.
	c.ControlPath = os.Getenv("HERALDYX_CONTROL")
	if c.ControlPath == "" {
		c.ControlPath = filepath.Join(filepath.Dir(c.StatePath), "control.json")
	}
	if _, set := os.LookupEnv("HERALDYX_SENT"); !set {
		c.SentPath = filepath.Join(filepath.Dir(c.StatePath), "sent.ndjson")
	}
	streams, err := stream.ParseExtra(os.Getenv("HERALDYX_STREAMS"))
	if err != nil {
		return c, fmt.Errorf("config: HERALDYX_STREAMS: %w", err)
	}
	c.Streams = streams
	c.Quiet, c.QuietWhy = parseQuiet(os.Getenv("HERALDYX_QUIET"), os.Getenv("HERALDYX_TZ"))
	if len(c.EventPaths) == 0 {
		return c, fmt.Errorf("config: HERALDYX_EVENTS is empty, there is nothing to watch")
	}
	if c.SMTPHost != "" && c.SMTPFrom == "" {
		return c, fmt.Errorf("config: HERALDYX_SMTP_HOST is set but HERALDYX_SMTP_FROM is not, so mail would have no sender")
	}
	// The checkpoint gate (F-018) is a switch the operator turns on, and it is
	// refused, not read as off, when its value is not one it knows: a typo must
	// not leave the gate off without a word. A gate switched on needs a
	// directory to read checkpoints from and keys to verify them with, and
	// without either it would refuse every stream, so it does not start.
	switch os.Getenv("HERALDYX_CHECKPOINT_GATE") {
	case "", "off":
		c.CheckpointGate = false
	case "on":
		c.CheckpointGate = true
	default:
		return c, fmt.Errorf("config: HERALDYX_CHECKPOINT_GATE is %q, want on or off", os.Getenv("HERALDYX_CHECKPOINT_GATE"))
	}
	c.CheckpointDir = os.Getenv("HERALDYX_CHECKPOINTS")
	c.CheckpointKeys = os.Getenv("HERALDYX_CHECKPOINT_KEYS")
	if c.CheckpointGate && c.CheckpointDir == "" {
		return c, fmt.Errorf("config: HERALDYX_CHECKPOINT_GATE is on but HERALDYX_CHECKPOINTS is not set, so there is no checkpoint to read")
	}
	if c.CheckpointGate && c.CheckpointKeys == "" {
		return c, fmt.Errorf("config: HERALDYX_CHECKPOINT_GATE is on but HERALDYX_CHECKPOINT_KEYS is not set, so no checkpoint could be verified")
	}
	return c, nil
}

// StreamPolicy is the rule that decides which sources a stream file may
// carry: the built-in table plus whatever HERALDYX_STREAMS declared.
func (c Config) StreamPolicy() stream.Policy {
	return stream.Default().Extend(c.Streams)
}

// Enabled reports whether this box will send anything at all.
func (c Config) Enabled() bool {
	return len(c.To) > 0 && (c.SMTPHost != "" || c.MailFile != "")
}

// Why explains, in one sentence an operator can act on, why notifications are
// off. Empty when they are on.
//
// This exists because "configured wrong" and "deliberately not configured"
// look identical in a log that only says nothing.
func (c Config) Why() string {
	switch {
	case len(c.To) == 0 && c.SMTPHost == "" && c.MailFile == "":
		return "no recipients and no mail transport are configured (HERALDYX_TO, HERALDYX_SMTP_HOST)"
	case len(c.To) == 0:
		return "no recipients are configured (HERALDYX_TO)"
	case c.SMTPHost == "" && c.MailFile == "":
		return "no mail transport is configured (HERALDYX_SMTP_HOST, or HERALDYX_MAIL_FILE to write them to a file)"
	default:
		return ""
	}
}

// ResolveEventFiles expands directories in EventPaths into the NDJSON files
// they hold, sorted so the read order is stable.
//
// An entry that names one stream file (`*.ndjson`) is read only while it is a
// regular file, by Lstat: never expanded as a directory and never followed as
// a link (invariant 33). Such a file often sits on a volume another process
// writes, the Keyryx witness's findings for one, and a link or a directory
// there would make this process read a stream it was never told to read under
// the stem of the one it was. An entry that cannot be checked is not read.
func (c Config) ResolveEventFiles() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range c.EventPaths {
		if isStreamEntry(p) {
			if regularStreamFile(p) && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				// Something other than absence: on a shared network volume a
				// stat of the mount point can fail while the files under it
				// read perfectly. Measured on a live cluster 2026-08-03, where
				// the RWX event volume answered `Remote I/O error` for the
				// directory itself and listed its three logs in the same
				// breath. Treating that as "not there yet" made the notifier
				// permanently deaf with a fully readable log under it.
				//
				// So try it as a directory anyway. Glob does its own readdir
				// and does not care what stat thought.
				if matches, gerr := filepath.Glob(filepath.Join(p, "*.ndjson")); gerr == nil && len(matches) > 0 {
					for _, m := range matches {
						if !seen[m] {
							seen[m] = true
							out = append(out, m)
						}
					}
					continue
				}
				// Not a directory we can list. It may still be a readable
				// file, and an unreadable one costs a failed open per poll,
				// which is cheaper than silence.
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
			// Genuinely absent: a plane may simply not be deployed, and the
			// path may appear later; the caller re-resolves on every poll.
			continue
		}
		if !info.IsDir() {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			continue
		}
		matches, err := filepath.Glob(filepath.Join(p, "*.ndjson"))
		if err != nil {
			continue
		}
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

func getenv(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func integer(k string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(k))); err == nil && n >= 0 {
		return n
	}
	return def
}

func seconds(k string, def int) time.Duration {
	return time.Duration(integer(k, def)) * time.Second
}

func hours(k string, def int) time.Duration {
	return time.Duration(integer(k, def)) * time.Hour
}

func millis(k string, def int) time.Duration {
	n := integer(k, def)
	if n < 100 {
		// A poll faster than this buys nothing and costs a syscall storm on a
		// shared volume that other planes are writing to.
		n = 100
	}
	return time.Duration(n) * time.Millisecond
}

// parseQuiet reads HERALDYX_QUIET ("HH:MM-HH:MM") and HERALDYX_TZ. A value
// that cannot be honoured turns quiet hours off and says why, and the process
// still starts: a notifier that refuses to run over a convenience setting
// leaves the operator with no alerts at all (invariant 4's reasoning).
func parseQuiet(spec, zone string) (*rule.QuietHours, string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, ""
	}
	parts := strings.Split(spec, "-")
	if len(parts) != 2 {
		return nil, fmt.Sprintf("HERALDYX_QUIET=%q is not HH:MM-HH:MM, so quiet hours are off", spec)
	}
	start, ok1 := clockMinutes(parts[0])
	end, ok2 := clockMinutes(parts[1])
	if !ok1 || !ok2 {
		return nil, fmt.Sprintf("HERALDYX_QUIET=%q is not HH:MM-HH:MM, so quiet hours are off", spec)
	}
	if start == end {
		return nil, fmt.Sprintf("HERALDYX_QUIET=%q starts and ends at the same minute, so quiet hours are off", spec)
	}
	loc, note := time.Local, ""
	if zone = strings.TrimSpace(zone); zone != "" {
		l, err := time.LoadLocation(zone)
		if err != nil {
			return nil, fmt.Sprintf("HERALDYX_TZ=%q is not a time zone (%v), so quiet hours are off", zone, err)
		}
		loc = l
	} else {
		abbrev, _ := time.Now().In(loc).Zone()
		note = fmt.Sprintf("HERALDYX_TZ is not set, so quiet hours %s run in this process's local zone (%s): set HERALDYX_TZ", spec, abbrev)
	}
	return &rule.QuietHours{Start: start, End: end, Loc: loc}, note
}

// clockMinutes turns "HH:MM" into minutes after midnight.
func clockMinutes(s string) (int, bool) {
	hh, mm, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || len(hh) != 2 || len(mm) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(hh)
	m, err2 := strconv.Atoi(mm)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// isStreamEntry reports whether a configured entry names one stream file
// rather than a directory of them: it ends in `.ndjson`, as every stream file
// this process reads does.
func isStreamEntry(p string) bool {
	return strings.HasSuffix(p, ".ndjson")
}

// regularStreamFile reports whether p is a regular file, read with Lstat so a
// link at that name is never followed.
func regularStreamFile(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.Mode().IsRegular()
}

// UnreadEntry is a configured stream file entry ResolveEventFiles does not
// read, and why, in a few words for the log.
type UnreadEntry struct {
	Path string
	Why  string
}

// UnreadStreamEntries names the configured entries that name a stream file
// (`*.ndjson`) and are not read: a link, a directory, anything but a regular
// file, or a path Lstat cannot check. ResolveEventFiles reads none of them,
// and the startup line says so, because a stream nobody hears from and a
// stream that is quiet look the same afterwards. An entry that is not there
// at all is not named: a plane may simply not be running yet.
func (c Config) UnreadStreamEntries() []UnreadEntry {
	var out []UnreadEntry
	for _, p := range c.EventPaths {
		if !isStreamEntry(p) {
			continue
		}
		info, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			out = append(out, UnreadEntry{p, "it cannot be checked (" + err.Error() + ")"})
		case info.Mode()&fs.ModeSymlink != 0:
			out = append(out, UnreadEntry{p, "it is a link, and a link is never followed"})
		case info.IsDir():
			out = append(out, UnreadEntry{p, "it is a directory, and a stream file entry is never expanded"})
		case !info.Mode().IsRegular():
			out = append(out, UnreadEntry{p, "it is not a regular file"})
		}
	}
	return out
}
