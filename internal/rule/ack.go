package rule

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// MaxAck is the longest an acknowledgement may hold a condition. There is no
// forever: a mute nobody remembers is how a real incident goes unheard.
const MaxAck = 7 * 24 * time.Hour

// DefaultAck is how long an acknowledgement holds when no duration is named.
const DefaultAck = 4 * time.Hour

// maxAckHeld bounds State.AckHeld, the same way maxDigestKeys bounds the digest.
const maxAckHeld = 500

// Ack is one acknowledgement: an operator saying "I know about this condition,
// stop mailing me about it until Until". It holds alerts below critical only.
// The JSON shape is the control file's.
type Ack struct {
	Key   string `json:"key"`
	At    int64  `json:"at"`
	Until int64  `json:"until"`
	By    string `json:"by,omitempty"`
	Note  string `json:"note,omitempty"`
}

// HeldCount is what one hold has kept back: how many, the worst, since when.
type HeldCount struct {
	Count int    `json:"count"`
	Worst string `json:"worst,omitempty"`
	First int64  `json:"first"`
}

// AckRelease is the report owed when an acknowledgement ends with alerts held
// under it. A hold never goes quiet: what it held is told, never dropped.
type AckRelease struct {
	Key string
	HeldCount
}

// ValidateAck refuses an acknowledgement this process must not honour.
func ValidateAck(a Ack) error {
	if a.Key == "" || strings.ContainsAny(a.Key, " ") || !oneLine(a.Key, 256) {
		return fmt.Errorf("rule: ack key %q must be one line, no spaces, at most 256 bytes", a.Key)
	}
	if !strings.Contains(a.Key, ":") {
		return fmt.Errorf("rule: ack key %q is not type:subject; copy it from the Key line of the mail", a.Key)
	}
	if a.Until <= a.At {
		return errors.New("rule: an acknowledgement needs an end after its start")
	}
	// Compared in milliseconds, the unit the fields are stored in. Converting
	// the span to a Duration first overflows for a hand-edited end far in the
	// future, and an overflowed span can pass the check: a mute with no end.
	if a.Until-a.At > MaxAck.Milliseconds() {
		return fmt.Errorf("rule: an acknowledgement may hold at most %s", MaxAck)
	}
	if !oneLine(a.By, 64) || !oneLine(a.Note, 200) {
		return errors.New("rule: --by (at most 64 bytes) and --note (at most 200) must be one line of printable text")
	}
	return nil
}

// oneLine is true for "" or printable text with no line break, at most n bytes.
func oneLine(s string, n int) bool {
	if len(s) > n {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// NoteHeldByAck counts one alert an acknowledgement held.
func (s *State) NoteHeldByAck(key, severity string, now time.Time) {
	if s.AckHeld == nil {
		s.AckHeld = map[string]HeldCount{}
	}
	if _, known := s.AckHeld[key]; !known && len(s.AckHeld) >= maxAckHeld {
		key = "(more, not listed)"
	}
	h := s.AckHeld[key]
	if h.Count == 0 {
		h.First = now.UnixMilli()
	}
	h.Count++
	if r := Rank(severity); r > Rank(h.Worst) {
		h.Worst = word(r)
	}
	s.AckHeld[key] = h
	s.PulseAckHeld++
}

// TakeAckReleases returns, and forgets, the held count of every key that no
// acknowledgement in active covers at now any more.
func (s *State) TakeAckReleases(active map[string]Ack, now time.Time) []AckRelease {
	var out []AckRelease
	for k, h := range s.AckHeld {
		if a, ok := active[k]; ok && now.UnixMilli() < a.Until {
			continue
		}
		out = append(out, AckRelease{Key: k, HeldCount: h})
		delete(s.AckHeld, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
