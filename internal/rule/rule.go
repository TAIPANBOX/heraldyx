// Package rule decides which events are worth a human's attention, and how
// often. It holds three independent limits, and every one of them exists
// because of a way an alerting system fails in production rather than in a
// design document:
//
//   - a severity floor, so an audit signal never pages like an incident;
//   - a dedup window, so one condition that keeps tripping is one message;
//   - an hourly ceiling, so a misbehaving fleet cannot turn this process into
//     a mail flood aimed at its own operator.
//
// Nothing here sends anything or touches the network. It is a pure decision
// over an event plus the counters in [State], which makes the awkward cases
// (a burst, a restart, a clock that moved) testable without a mail server.
package rule

import (
	"fmt"
	"strings"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// Severity ranks, low to high. The envelope's own vocabulary (agent-passport
// SPEC.md 6.1) is a set of strings with no ordering attached, so the ordering
// lives here, once, rather than as a comparison open-coded at each use.
const (
	rankUnknown = -1
	rankInfo    = 0
	rankLow     = 1
	rankMedium  = 2
	rankHigh    = 3
	rankCrit    = 4
)

// Rank returns the severity's position in the ladder, or -1 for a value this
// build does not know.
//
// An unknown severity is deliberately NOT treated as critical or as info. A
// future producer that invents a level must not be able to either page
// everyone or go silent by accident; see [Decide], which routes it to the
// digest and says so.
func Rank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case event.SeverityInfo:
		return rankInfo
	case event.SeverityLow:
		return rankLow
	case event.SeverityMedium:
		return rankMedium
	case event.SeverityHigh:
		return rankHigh
	case event.SeverityCritical:
		return rankCrit
	default:
		return rankUnknown
	}
}

// ParseSeverity turns an operator-supplied floor ("high") into a rank.
func ParseSeverity(s string) (int, error) {
	r := Rank(s)
	if r == rankUnknown {
		return 0, fmt.Errorf("rule: unknown severity %q (want info|low|medium|high|critical)", s)
	}
	return r, nil
}

// Verdict is what to do with one event.
type Verdict int

const (
	// Drop: below the floor and not worth recording either.
	Drop Verdict = iota
	// Digest: worth telling the operator once a day, not now.
	Digest
	// Notify: send it now.
	Notify
	// Suppressed: it would have been a Notify, but the hourly ceiling is
	// reached. The caller sends ONE notice per window, never the events
	// themselves, and [State.SuppressedSince] carries the count.
	Suppressed
)

func (v Verdict) String() string {
	switch v {
	case Drop:
		return "drop"
	case Digest:
		return "digest"
	case Notify:
		return "notify"
	case Suppressed:
		return "suppressed"
	default:
		return "unknown"
	}
}

// Config is the operator-facing shape of the three limits.
type Config struct {
	// MinRank is the severity floor for an immediate message. Anything below
	// it that is still a known severity goes to the digest.
	MinRank int
	// DedupWindow is how long one (type, subject) pair stays quiet after it
	// has been sent. Default 10 minutes, matching the window the money
	// plane's own alert pipeline has used since it was written.
	DedupWindow time.Duration
	// MaxPerHour caps immediate messages. Zero means no ceiling, which is a
	// choice an operator can make and not a default.
	MaxPerHour int
}

// DefaultConfig is a floor of `high`, a 10 minute dedup window, and 20
// messages an hour.
func DefaultConfig() Config {
	return Config{
		MinRank:     rankHigh,
		DedupWindow: 10 * time.Minute,
		MaxPerHour:  20,
	}
}

// Subject is the thing an event is about: the run when there is one, else the
// agent. It is the second half of the dedup key, and it is also what a human
// reads first in the subject line.
//
// Never empty for a well-formed event: the envelope requires `agent_id`.
func Subject(e event.Event) string {
	if e.RunID != "" {
		return e.RunID
	}
	return e.AgentID
}

// Key is the dedup key: one condition about one subject.
func Key(e event.Event) string {
	return e.Type + ":" + Subject(e)
}

// Decide returns the verdict for one event at time now, and mutates st to
// record what it decided. The mutation is the point: dedup and the ceiling are
// both memory, and a decision that does not remember itself is not a limit.
func Decide(cfg Config, st *State, e event.Event, now time.Time) Verdict {
	r := Rank(e.Severity)

	// An unknown severity is not silently dropped and not silently escalated.
	// It goes to the digest, where a human sees it once and can decide
	// whether this build needs to learn a new level.
	if r == rankUnknown {
		st.NoteDigest(e, now)
		return Digest
	}
	if r < cfg.MinRank {
		st.NoteDigest(e, now)
		return Digest
	}

	// Dedup before the ceiling, on purpose: a condition that trips two hundred
	// times must not eat two hundred slots of an operator's hourly budget and
	// crowd out the one different thing that happened.
	if st.SentWithin(Key(e), cfg.DedupWindow, now) {
		return Drop
	}

	if cfg.MaxPerHour > 0 && st.SentInLastHour(now) >= cfg.MaxPerHour {
		st.NoteSuppressed(now)
		return Suppressed
	}

	st.NoteSent(Key(e), now)
	return Notify
}
