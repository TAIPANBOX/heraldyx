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

	"github.com/TAIPANBOX/agent-stack-go/v2/event"
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

// IsCritical reports whether severity is the top of the ladder.
func IsCritical(severity string) bool { return Rank(severity) == rankCrit }

// word is the canonical spelling of a known rank, the inverse of [Rank] for
// everything Rank knows. An unknown rank has no word and gets an empty one.
func word(rank int) string {
	switch rank {
	case rankInfo:
		return event.SeverityInfo
	case rankLow:
		return event.SeverityLow
	case rankMedium:
		return event.SeverityMedium
	case rankHigh:
		return event.SeverityHigh
	case rankCrit:
		return event.SeverityCritical
	default:
		return ""
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
	// reached and it is not a critical. The caller sends a summary under a
	// [Cadence], never the events themselves, and [State.SuppressedSince]
	// carries the count.
	Suppressed
	// Held: it would have been a Notify, but an acknowledgement covers its key
	// or a hold window (quiet hours, a maintenance window) is open, and it is
	// not a critical. Counted in [State.AckHeld], [State.QuietHeld] or
	// [State.MaintHeld], and reported when the acknowledgement or window ends.
	Held
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
	case Held:
		return "held"
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
	// Acks are the acknowledgements in force, by key, as the loop read them
	// from the control file this poll. Read only here; nil means none.
	Acks map[string]Ack
	// Quiet is the recurring quiet window, nil when off. Loaded by config.
	Quiet *QuietHours
	// Maintenance is the maintenance window in force this poll, nil when none.
	Maintenance *Maintenance
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
	return DecideFrom(cfg, st, Key(e), e.Severity, e.Source, now)
}

// DecideKey is [Decide] for something that is not an event line but has to
// pass the same three limits: a notice this process raises about its own
// input, under a key it chose and a severity it chose. It exists so such a
// notice is held to the floor, the dedup window and the ceiling like every
// other alert, instead of being a second path around them.
func DecideKey(cfg Config, st *State, key, severity string, now time.Time) Verdict {
	return DecideFrom(cfg, st, key, severity, "", now)
}

// DecideFrom is the one decision. source is the event's source when there is
// one, empty for a notice this process raised itself; a maintenance window
// scoped to a source holds only alerts from that source.
//
// The order is the design's (section 5): floor, then dedup, then below
// critical only, acknowledgement, then hold windows, then the ceiling. An
// acknowledgement is the more specific reason, so it comes first.
func DecideFrom(cfg Config, st *State, key, severity, source string, now time.Time) Verdict {
	r := Rank(severity)

	// An unknown severity is not silently dropped and not silently escalated.
	// It goes to the digest, where a human sees it once and can decide
	// whether this build needs to learn a new level.
	if r == rankUnknown {
		st.NoteDigestKey(key, now)
		return Digest
	}
	if r < cfg.MinRank {
		st.NoteDigestKey(key, now)
		return Digest
	}

	// Dedup before the ceiling, on purpose: a condition that trips two hundred
	// times must not eat two hundred slots of an operator's hourly budget and
	// crowd out the one different thing that happened.
	if st.SentWithin(key, cfg.DedupWindow, now) {
		return Drop
	}

	// An acknowledgement holds its own key, below critical only, until it
	// ends. Before the ceiling: an alert the operator already acknowledged
	// must not spend the hour's budget, and it is counted, never lost.
	if r < rankCrit {
		if a, ok := cfg.Acks[key]; ok && now.UnixMilli() < a.Until {
			st.NoteHeldByAck(key, severity, now)
			return Held
		}
		// A maintenance window the operator opened on purpose is the more
		// specific reason, then quiet hours. Below critical only, counted,
		// reported when the window ends. Neither touches LastSent or the
		// hourly ceiling: nothing was sent.
		if cfg.Maintenance != nil && cfg.Maintenance.Covers(source, now) {
			st.NoteHeldByWindow(WindowMaintenance, severity, now)
			return Held
		}
		if cfg.Quiet != nil && cfg.Quiet.Contains(now) {
			st.NoteHeldByWindow(WindowQuiet, severity, now)
			return Held
		}
	}

	// The ceiling holds everything below critical. A critical goes through
	// it: the whole meaning of that severity is "now", and the one limit that
	// exists to keep the mailbox usable is not a reason to sit on it. It is
	// still one message per condition, because dedup ran first, and it still
	// counts toward the hour, because the ceiling is a count of what went out
	// and not of what deserved to. Issue #71 measured the alternative: a
	// `budget_exhausted` at critical held with twenty mediums, and an operator
	// who heard nothing for 28 minutes.
	if cfg.MaxPerHour > 0 && r < rankCrit && st.SentInLastHour(now) >= cfg.MaxPerHour {
		st.NoteSuppressed(severity, now)
		return Suppressed
	}

	st.NoteSent(key, now)
	return Notify
}
