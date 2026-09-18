package rule

import (
	"sort"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// maxDigestKeys bounds the digest map. A daily summary that lists ten thousand
// distinct conditions is not a summary, and an unbounded map in a process that
// runs for months is a leak with a friendly name.
const maxDigestKeys = 500

// State is what the rules remember: when each condition was last sent, when
// messages went out this hour, and what has piled up for the digest.
//
// Every field is exported and JSON-tagged because this is exactly what gets
// written to disk between runs. A dedup window kept only in memory is not a
// dedup window: a rollout, an eviction or a crash would re-send everything
// that was already sent, and a restart loop would turn one incident into a
// mailbox full of the same incident.
type State struct {
	// LastSent is dedup key -> unix millis of the last message sent for it.
	LastSent map[string]int64 `json:"last_sent"`
	// SentTimes is the unix millis of recent sends, used for the hourly
	// ceiling. Trimmed to the last hour on every write, so it stays small.
	SentTimes []int64 `json:"sent_times"`
	// Digest is dedup key -> how many times it has been seen since the last
	// digest went out.
	Digest map[string]int `json:"digest"`
	// DigestSince is the unix millis the current digest window opened.
	DigestSince int64 `json:"digest_since"`
	// SuppressedSince counts events held back by the hourly ceiling since the
	// last summary of them was sent.
	SuppressedSince int `json:"suppressed_since"`
	// SuppressedWorst is the worst severity among them, as the canonical word
	// (`high`, never `HIGH ` off the wire), so the summary can say how bad the
	// worst held alert was rather than only how many there were. Empty in a
	// state file written before it existed, and the summary says nothing
	// about a worst it does not know.
	SuppressedWorst string `json:"suppressed_worst,omitempty"`
	// SuppressedFirstAt is the unix millis the first of them was held, so the
	// summary can say since when. Zero in an older state file.
	SuppressedFirstAt int64 `json:"suppressed_first_at,omitempty"`
	// SuppressNoticeAt is the unix millis of the last summary, so the summary
	// itself cannot become the flood it warns about.
	SuppressNoticeAt int64 `json:"suppress_notice_at"`
}

// Cadence bounds how often the ceiling's own summary goes out while alerts
// are being held back. Three numbers rather than one, and each closes a way
// the summary failed in production:
//
//   - Every: a summary is due once this long has passed since the previous one
//     and anything has been held since. It was an hour, the same width as the
//     ceiling itself, and issue #71 measured what that means: one summary at
//     the moment the ceiling was reached, then 28 minutes of held alerts the
//     operator heard nothing about.
//   - Burst: a summary is due sooner once this many have been held since the
//     previous one, so a flood is reported as a flood rather than ten minutes
//     later as a number.
//   - Floor: but never sooner than this after the previous one, whatever the
//     count. Without it the burst bound turns the summary into one mail per
//     fifty events, which is the flood the ceiling exists to prevent, sent by
//     the ceiling.
//
// This is policy about the summary, not a limit an operator sets: the
// ceiling is theirs (`HERALDYX_MAX_PER_HOUR`), and how often they are told it
// is holding the line is this process's own promise.
type Cadence struct {
	Every time.Duration
	Burst int
	Floor time.Duration
}

// DefaultCadence is a summary every 10 minutes while anything is held, sooner
// for fifty held since the previous one, and never inside a minute of it.
func DefaultCadence() Cadence {
	return Cadence{Every: 10 * time.Minute, Burst: 50, Floor: time.Minute}
}

// Notice is what one summary of held alerts carries.
type Notice struct {
	// Count is how many were held since the previous summary.
	Count int
	// Worst is the worst severity among them as a canonical word, or empty
	// when the state that held them predates the field.
	Worst string
	// Since is when the first of them was held, or the zero time when the
	// state that held them predates the field.
	Since time.Time
	// Every is how long, at most, until the next summary while alerts keep
	// being held: the policy that produced this one, carried so the mail can
	// say it without a second source of truth.
	Every time.Duration
}

// NewState returns an empty state.
func NewState() *State {
	return &State{
		LastSent: map[string]int64{},
		Digest:   map[string]int{},
	}
}

// ensure initialises maps a zero-value or partially-loaded State may be
// missing, so a hand-edited or truncated state file cannot panic this process.
func (s *State) ensure() {
	if s.LastSent == nil {
		s.LastSent = map[string]int64{}
	}
	if s.Digest == nil {
		s.Digest = map[string]int{}
	}
}

// SentWithin reports whether key was sent inside window before now.
//
// A last-sent stamp in the FUTURE (a state file copied from another machine, a
// clock that moved backwards) counts as within the window. The alternative is
// to treat it as ancient and re-send, and of the two ways to be wrong about a
// clock, being quiet once is the recoverable one.
func (s *State) SentWithin(key string, window time.Duration, now time.Time) bool {
	s.ensure()
	last, ok := s.LastSent[key]
	if !ok {
		return false
	}
	if last > now.UnixMilli() {
		return true
	}
	return now.UnixMilli()-last < window.Milliseconds()
}

// NoteSent records a message for key at now.
func (s *State) NoteSent(key string, now time.Time) {
	s.ensure()
	ms := now.UnixMilli()
	s.LastSent[key] = ms
	s.SentTimes = append(s.SentTimes, ms)
	s.trimSentTimes(now)
	s.trimLastSent(now)
}

// SentInLastHour counts messages sent in the hour ending at now.
func (s *State) SentInLastHour(now time.Time) int {
	cutoff := now.Add(-time.Hour).UnixMilli()
	n := 0
	for _, ms := range s.SentTimes {
		if ms >= cutoff {
			n++
		}
	}
	return n
}

// NoteSuppressed records one event the ceiling refused, with its severity, so
// the summary can say how bad the worst of them was and since when.
//
// The word stored is the canonical one for the rank, never the wire's own
// spelling: a summary subject is a mail header, and what goes into it is
// chosen here rather than by a producer.
func (s *State) NoteSuppressed(severity string, now time.Time) {
	if s.SuppressedSince == 0 {
		s.SuppressedFirstAt = now.UnixMilli()
		s.SuppressedWorst = ""
	}
	s.SuppressedSince++
	if r := Rank(severity); r > Rank(s.SuppressedWorst) {
		s.SuppressedWorst = word(r)
	}
}

// TakeSuppressionNotice reports whether a summary of held alerts is due under
// c, and if so returns what it carries and resets the count behind it.
//
// Due means: something has been held since the previous summary, and either
// there was no previous summary, or it is at least c.Every old, or at least
// c.Burst have been held and it is at least c.Floor old. A previous summary
// stamped in the future (a clock that moved, a state file from another
// machine) counts as recent, for the same reason SentWithin treats a future
// stamp that way: of the two ways to be wrong about a clock, quiet once is
// the recoverable one.
func (s *State) TakeSuppressionNotice(c Cadence, now time.Time) (Notice, bool) {
	if s.SuppressedSince == 0 {
		return Notice{}, false
	}
	if s.SuppressNoticeAt > 0 {
		age := now.UnixMilli() - s.SuppressNoticeAt
		burst := c.Burst > 0 && s.SuppressedSince >= c.Burst && age >= c.Floor.Milliseconds()
		if age < c.Every.Milliseconds() && !burst {
			return Notice{}, false
		}
	}
	n := Notice{Count: s.SuppressedSince, Worst: s.SuppressedWorst, Every: c.Every}
	if s.SuppressedFirstAt > 0 {
		n.Since = time.UnixMilli(s.SuppressedFirstAt)
	}
	s.SuppressedSince = 0
	s.SuppressedWorst = ""
	s.SuppressedFirstAt = 0
	s.SuppressNoticeAt = now.UnixMilli()
	return n, true
}

// NoteDigest records one event for the daily summary.
func (s *State) NoteDigest(e event.Event, now time.Time) {
	s.ensure()
	if s.DigestSince == 0 {
		s.DigestSince = now.UnixMilli()
	}
	key := Key(e)
	if _, known := s.Digest[key]; !known && len(s.Digest) >= maxDigestKeys {
		// Full. Count the overflow under one honest label rather than
		// silently dropping it or growing without bound.
		s.Digest["(more, not listed)"]++
		return
	}
	s.Digest[key]++
}

// DigestDue reports whether the digest window has been open at least period.
func (s *State) DigestDue(period time.Duration, now time.Time) bool {
	if len(s.Digest) == 0 || s.DigestSince == 0 {
		return false
	}
	return now.UnixMilli()-s.DigestSince >= period.Milliseconds()
}

// DigestEntry is one line of the daily summary.
type DigestEntry struct {
	Key   string
	Count int
}

// TakeDigest returns the summary sorted by count (descending, then by key so
// the order is stable) and clears it.
func (s *State) TakeDigest(now time.Time) []DigestEntry {
	s.ensure()
	out := make([]DigestEntry, 0, len(s.Digest))
	for k, n := range s.Digest {
		out = append(out, DigestEntry{Key: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	s.Digest = map[string]int{}
	s.DigestSince = now.UnixMilli()
	return out
}

// trimSentTimes drops stamps older than an hour: the ceiling never looks
// further back, so keeping them is a slow leak.
func (s *State) trimSentTimes(now time.Time) {
	cutoff := now.Add(-time.Hour).UnixMilli()
	keep := s.SentTimes[:0]
	for _, ms := range s.SentTimes {
		if ms >= cutoff {
			keep = append(keep, ms)
		}
	}
	s.SentTimes = keep
}

// trimLastSent drops dedup entries far older than any window an operator would
// configure. Without it, a long-lived process accumulates one entry per run id
// it has ever alerted on, forever.
func (s *State) trimLastSent(now time.Time) {
	const keepFor = 24 * time.Hour
	cutoff := now.Add(-keepFor).UnixMilli()
	for k, ms := range s.LastSent {
		if ms < cutoff {
			delete(s.LastSent, k)
		}
	}
}
