// Package config reads heraldyx's settings from the environment.
//
// Everything has a default that is safe to run with, and the one setting with
// no safe default (where to send mail) makes the process do nothing rather
// than fail. An alerting component that refuses to start because it was
// deployed without an address is a component that takes the rest of the
// deployment down with it.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
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

	SMTPHost string
	SMTPFrom string
	SMTPUser string
	SMTPPass string

	DedupWindow  time.Duration
	MaxPerHour   int
	DigestPeriod time.Duration
	PollInterval time.Duration
}

// FromEnv reads the configuration.
func FromEnv() (Config, error) {
	c := Config{
		EventPaths:   splitList(getenv("HERALDYX_EVENTS", "/var/lib/stack/events")),
		To:           splitList(os.Getenv("HERALDYX_TO")),
		MinSeverity:  getenv("HERALDYX_MIN_SEVERITY", "high"),
		Box:          getenv("HERALDYX_BOX", "agent stack"),
		ConsoleURL:   os.Getenv("HERALDYX_CONSOLE_URL"),
		StatePath:    getenv("HERALDYX_STATE", "/var/lib/stack/heraldyx/state.json"),
		MailFile:     os.Getenv("HERALDYX_MAIL_FILE"),
		SMTPHost:     os.Getenv("HERALDYX_SMTP_HOST"),
		SMTPFrom:     os.Getenv("HERALDYX_SMTP_FROM"),
		SMTPUser:     os.Getenv("HERALDYX_SMTP_USER"),
		SMTPPass:     os.Getenv("HERALDYX_SMTP_PASS"),
		DedupWindow:  seconds("HERALDYX_DEDUP_SECONDS", 600),
		MaxPerHour:   integer("HERALDYX_MAX_PER_HOUR", 20),
		DigestPeriod: hours("HERALDYX_DIGEST_HOURS", 24),
		PollInterval: millis("HERALDYX_POLL_MS", 2000),
	}
	if len(c.EventPaths) == 0 {
		return c, fmt.Errorf("config: HERALDYX_EVENTS is empty, there is nothing to watch")
	}
	if c.SMTPHost != "" && c.SMTPFrom == "" {
		return c, fmt.Errorf("config: HERALDYX_SMTP_HOST is set but HERALDYX_SMTP_FROM is not, so mail would have no sender")
	}
	return c, nil
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
func (c Config) ResolveEventFiles() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range c.EventPaths {
		info, err := os.Stat(p)
		if err != nil {
			// Not there yet. A plane may simply not be deployed, and the path
			// may appear later; the caller re-resolves on every poll.
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
