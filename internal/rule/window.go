package rule

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxMaintenance is the longest a maintenance window may run. A window
// nobody closed is a mute nobody remembers.
const MaxMaintenance = 24 * time.Hour

// The two kinds of hold window, as NoteHeldByWindow names them.
const (
	WindowQuiet       = "quiet"
	WindowMaintenance = "maintenance"
)

// QuietHours is a recurring daily window on the operator's own wall clock.
// Start and End are minutes after local midnight; Start after End crosses
// midnight. Loc is loaded by the caller: this package does no I/O.
type QuietHours struct {
	Start, End int
	Loc        *time.Location
}

// Contains reports whether now is inside the window, read on Loc's wall clock,
// so a clock change moves the window with the operator's day.
func (q QuietHours) Contains(now time.Time) bool {
	if q.Loc == nil || q.Start == q.End {
		return false
	}
	t := now.In(q.Loc)
	m := t.Hour()*60 + t.Minute()
	if q.Start < q.End {
		return m >= q.Start && m < q.End
	}
	return m >= q.Start || m < q.End
}

// Maintenance is one window an operator opened with a command. It holds
// alerts below critical from Scope (a source name, or every source when
// empty) until Until. The JSON shape is the control file's.
type Maintenance struct {
	At    int64  `json:"at"`
	Until int64  `json:"until"`
	Scope string `json:"scope,omitempty"`
	By    string `json:"by,omitempty"`
	Note  string `json:"note,omitempty"`
}

// Covers reports whether the window holds an alert from source at now. An
// alert with no source (a notice this process raises about its own input)
// is held only by a window that covers every source.
func (m Maintenance) Covers(source string, now time.Time) bool {
	n := now.UnixMilli()
	if n < m.At || n >= m.Until {
		return false
	}
	return m.Scope == "" || m.Scope == source
}

// ValidateMaintenance refuses a window this process must not honour.
func ValidateMaintenance(m Maintenance) error {
	if m.At <= 0 || m.Until <= m.At {
		return errors.New("rule: a maintenance window needs a start and an end after it")
	}
	if m.Until-m.At > MaxMaintenance.Milliseconds() {
		return fmt.Errorf("rule: a maintenance window may run at most %s", MaxMaintenance)
	}
	if m.Scope != "" && (strings.ContainsAny(m.Scope, " :") || !oneLine(m.Scope, 64)) {
		return fmt.Errorf("rule: scope %q must be one source name", m.Scope)
	}
	if !oneLine(m.By, 64) || !oneLine(m.Note, 200) {
		return errors.New("rule: --by (at most 64 bytes) and --note (at most 200) must be one line of printable text")
	}
	return nil
}

// NoteHeldByWindow counts one alert a window held.
func (s *State) NoteHeldByWindow(window, severity string, now time.Time) {
	h := &s.QuietHeld
	if window == WindowMaintenance {
		h = &s.MaintHeld
	}
	if h.Count == 0 {
		h.First = now.UnixMilli()
	}
	h.Count++
	if r := Rank(severity); r > Rank(h.Worst) {
		h.Worst = word(r)
	}
	s.PulseWindowHeld++
}

// TakeQuietRelease returns what quiet hours held, once they are over (or no
// longer configured), and resets it.
func (s *State) TakeQuietRelease(q *QuietHours, now time.Time) (HeldCount, bool) {
	if s.QuietHeld.Count == 0 || (q != nil && q.Contains(now)) {
		return HeldCount{}, false
	}
	h := s.QuietHeld
	s.QuietHeld = HeldCount{}
	return h, true
}

// MaintenanceStep is what one poll owes about maintenance: a window to
// announce, a window to close, or both when one replaced another.
type MaintenanceStep struct {
	Open         *Maintenance
	Closed       bool
	ClosedWindow Maintenance
	Held         HeldCount
}

// TakeMaintenanceStep compares the window in force (nil for none) with the
// one this process last announced. A window that closes is always reported,
// with what it held, even nothing: that mail is how the operator knows
// alerting is back.
func (s *State) TakeMaintenanceStep(m *Maintenance, now time.Time) MaintenanceStep {
	var step MaintenanceStep
	n := now.UnixMilli()
	active := m != nil && n >= m.At && n < m.Until
	if s.MaintAnnounced != nil && (!active || m.At != s.MaintAnnounced.At) {
		step.Closed, step.ClosedWindow, step.Held = true, *s.MaintAnnounced, s.MaintHeld
		s.MaintAnnounced, s.MaintHeld = nil, HeldCount{}
	}
	if active && s.MaintAnnounced == nil {
		c := *m
		step.Open = &c
		s.MaintAnnounced = &c
	}
	return step
}
