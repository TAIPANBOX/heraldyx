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
	// SuppressedSince counts events dropped by the hourly ceiling since the
	// last suppression notice was sent.
	SuppressedSince int `json:"suppressed_since"`
	// SuppressNoticeAt is the unix millis of the last suppression notice, so
	// the notice itself cannot become the flood it warns about.
	SuppressNoticeAt int64 `json:"suppress_notice_at"`
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

// NoteSuppressed records one event the ceiling refused.
func (s *State) NoteSuppressed(now time.Time) {
	s.SuppressedSince++
}

// TakeSuppressionNotice reports whether a "messages suppressed" notice is due,
// and if so returns how many were suppressed and resets the counter.
//
// The notice is itself rate limited to one per window: the whole point is that
// the operator's mailbox stays usable while something is on fire.
func (s *State) TakeSuppressionNotice(window time.Duration, now time.Time) (int, bool) {
	if s.SuppressedSince == 0 {
		return 0, false
	}
	if s.SuppressNoticeAt > 0 && now.UnixMilli()-s.SuppressNoticeAt < window.Milliseconds() {
		return 0, false
	}
	n := s.SuppressedSince
	s.SuppressedSince = 0
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
