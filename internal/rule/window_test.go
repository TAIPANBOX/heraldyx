package rule

import (
	"testing"
	"time"
)

func london(t *testing.T) *time.Location {
	t.Helper()
	l, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestQuietHoursContainAcrossMidnightInLondon(t *testing.T) {
	l := london(t)
	q := QuietHours{Start: 22 * 60, End: 7 * 60, Loc: l}
	cases := map[string]bool{
		"2026-10-16T21:59:00": false,
		"2026-10-16T22:00:00": true,
		"2026-10-16T23:30:00": true,
		"2026-10-17T06:59:00": true,
		"2026-10-17T07:00:00": false,
		"2026-10-17T12:00:00": false,
	}
	for wall, want := range cases {
		at, err := time.ParseInLocation("2006-01-02T15:04:05", wall, l)
		if err != nil {
			t.Fatal(err)
		}
		if got := q.Contains(at); got != want {
			t.Errorf("%s London: Contains = %v, want %v", wall, got, want)
		}
	}
}

// The clocks go back at 02:00 BST on 2026-10-25. The window is wall-clock
// time in London on both sides of the change, never a fixed UTC span.
func TestQuietHoursFollowTheClockChange(t *testing.T) {
	q := QuietHours{Start: 22 * 60, End: 7 * 60, Loc: london(t)}
	if !q.Contains(time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)) { // 01:30 BST
		t.Error("01:30 BST is inside 22:00-07:00")
	}
	if !q.Contains(time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC)) { // 01:30 GMT
		t.Error("01:30 GMT is inside 22:00-07:00")
	}
	if q.Contains(time.Date(2026, 10, 25, 7, 0, 0, 0, time.UTC)) { // 07:00 GMT
		t.Error("07:00 GMT is outside 22:00-07:00")
	}
	if !q.Contains(time.Date(2026, 10, 25, 6, 30, 0, 0, time.UTC)) { // 06:30 GMT
		t.Error("06:30 GMT is inside 22:00-07:00, a UTC reading would miss the hour")
	}
}

var winT0 = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

func maint(scope string, d time.Duration) *Maintenance {
	return &Maintenance{At: winT0.UnixMilli(), Until: winT0.Add(d).UnixMilli(), Scope: scope, Note: "upgrade"}
}

func TestAWindowHoldsBelowCriticalAndNeverACritical(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Maintenance = maint("", time.Hour)
	st := NewState()
	if v := DecideFrom(cfg, st, "policy_deny:run-1", "high", "wardryx", winT0.Add(time.Minute)); v != Held {
		t.Fatalf("a high in a maintenance window: want held, have %s", v)
	}
	if v := DecideFrom(cfg, st, "budget_exhausted:run-9", "critical", "tokenfuse", winT0.Add(time.Minute)); v != Notify {
		t.Fatalf("a window held a critical: %s", v)
	}
	cfg.Maintenance = nil
	cfg.Quiet = &QuietHours{Start: 0, End: 23*60 + 59, Loc: time.UTC}
	if v := DecideFrom(cfg, st, "budget_exhausted:run-10", "critical", "tokenfuse", winT0.Add(time.Minute)); v != Notify {
		t.Fatalf("quiet hours held a critical: %s", v)
	}
	if v := DecideFrom(cfg, st, "policy_deny:run-2", "high", "wardryx", winT0.Add(time.Minute)); v != Held {
		t.Fatalf("a high in quiet hours: want held, have %s", v)
	}
	if st.MaintHeld.Count != 1 || st.QuietHeld.Count != 1 {
		t.Fatalf("held counts: maintenance %d, quiet %d; want 1 and 1", st.MaintHeld.Count, st.QuietHeld.Count)
	}
}

func TestAScopedMaintenanceHoldsOnlyItsSource(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Maintenance = maint("tokenfuse", time.Hour)
	st := NewState()
	if v := DecideFrom(cfg, st, "policy_deny:run-1", "high", "wardryx", winT0.Add(time.Minute)); v != Notify {
		t.Fatalf("a window scoped to tokenfuse held a wardryx alert: %s", v)
	}
	if v := DecideFrom(cfg, st, "breaker_tripped:run-2", "high", "tokenfuse", winT0.Add(time.Minute)); v != Held {
		t.Fatalf("a window scoped to tokenfuse did not hold a tokenfuse alert: %s", v)
	}
}

func TestWindowHeldAlertsDoNotCountTowardTheCeiling(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Maintenance = maint("", time.Hour)
	st := NewState()
	for i := 0; i < 25; i++ {
		DecideFrom(cfg, st, "policy_deny:run-1", "high", "wardryx", winT0.Add(time.Duration(i)*time.Second))
	}
	if n := st.SentInLastHour(winT0.Add(time.Minute)); n != 0 {
		t.Fatalf("held alerts spent the hourly budget: %d", n)
	}
}

func TestAQuietHoldIsReleasedWhenTheWindowEnds(t *testing.T) {
	q := &QuietHours{Start: 22 * 60, End: 7 * 60, Loc: time.UTC}
	st := NewState()
	st.NoteHeldByWindow(WindowQuiet, "high", time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC))
	if _, due := st.TakeQuietRelease(q, time.Date(2026, 10, 17, 6, 0, 0, 0, time.UTC)); due {
		t.Fatal("released while quiet hours are still on")
	}
	h, due := st.TakeQuietRelease(q, time.Date(2026, 10, 17, 7, 0, 0, 0, time.UTC))
	if !due || h.Count != 1 || h.Worst != "high" {
		t.Fatalf("want a release of 1, worst high; have %+v, %v", h, due)
	}
	if _, again := st.TakeQuietRelease(q, time.Date(2026, 10, 17, 7, 1, 0, 0, time.UTC)); again {
		t.Fatal("the same release went twice")
	}
}

func TestAMaintenanceWindowOpensOnceAndAlwaysCloses(t *testing.T) {
	st := NewState()
	m := maint("", 30*time.Minute)
	if step := st.TakeMaintenanceStep(m, winT0.Add(time.Minute)); step.Open == nil || step.Closed {
		t.Fatalf("first poll in a window: want open, have %+v", step)
	}
	if step := st.TakeMaintenanceStep(m, winT0.Add(2*time.Minute)); step.Open != nil || step.Closed {
		t.Fatalf("second poll in the same window: want nothing, have %+v", step)
	}
	step := st.TakeMaintenanceStep(m, winT0.Add(30*time.Minute))
	if !step.Closed || step.Held.Count != 0 || step.ClosedWindow.Note != "upgrade" {
		t.Fatalf("a window that held nothing must still close, with its own note: %+v", step)
	}
	if again := st.TakeMaintenanceStep(m, winT0.Add(31*time.Minute)); again.Closed || again.Open != nil {
		t.Fatalf("a closed window closed twice: %+v", again)
	}
}

func TestValidateMaintenanceRefusesWhatMustNotBeHonoured(t *testing.T) {
	ok := Maintenance{At: winT0.UnixMilli(), Until: winT0.Add(time.Hour).UnixMilli()}
	if err := ValidateMaintenance(ok); err != nil {
		t.Fatalf("a good window was refused: %v", err)
	}
	bad := map[string]Maintenance{
		"no end":       {At: ok.At, Until: ok.At},
		"over a day":   {At: ok.At, Until: winT0.Add(MaxMaintenance + time.Minute).UnixMilli()},
		"no start":     {At: 0, Until: ok.Until},
		"space scope":  {At: ok.At, Until: ok.Until, Scope: "token fuse"},
		"newline note": {At: ok.At, Until: ok.Until, Note: "a\nb"},
	}
	for name, m := range bad {
		if err := ValidateMaintenance(m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A window holds nothing before it opens or after it closes, and a quiet
// window with no zone, or with equal start and end, holds nothing at all.
func TestAWindowHoldsOnlyItsOwnSpan(t *testing.T) {
	m := maint("", time.Hour)
	if m.Covers("wardryx", winT0.Add(-time.Second)) {
		t.Error("a window held an alert before it opened")
	}
	if m.Covers("wardryx", winT0.Add(time.Hour)) {
		t.Error("a window held an alert at the moment it closed")
	}
	if !m.Covers("wardryx", winT0.Add(time.Hour-time.Millisecond)) {
		t.Error("a window did not hold an alert a millisecond before it closed")
	}
	if (QuietHours{Start: 60, End: 60, Loc: time.UTC}).Contains(winT0) {
		t.Error("quiet hours with equal start and end held an alert")
	}
	if (QuietHours{Start: 0, End: 23 * 60, Loc: nil}).Contains(winT0) {
		t.Error("quiet hours with no zone held an alert")
	}
}
