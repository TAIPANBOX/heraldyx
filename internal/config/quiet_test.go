package config

import "testing"

func TestQuietHoursParseWithAZone(t *testing.T) {
	q, why := parseQuiet("22:00-07:00", "Europe/London")
	if q == nil || q.Start != 22*60 || q.End != 7*60 || q.Loc.String() != "Europe/London" || why != "" {
		t.Fatalf("got %+v, %q", q, why)
	}
}

func TestAMalformedQuietSettingIsOffAndSaysWhy(t *testing.T) {
	for _, spec := range []string{"25:00-07:00", "22:00", "22:00-22:00", "ten-seven"} {
		if q, why := parseQuiet(spec, "Europe/London"); q != nil || why == "" {
			t.Errorf("%q: want off with a reason, have %+v, %q", spec, q, why)
		}
	}
	if q, why := parseQuiet("22:00-07:00", "Mars/Olympus"); q != nil || why == "" {
		t.Errorf("an unknown zone: want off with a reason, have %+v, %q", q, why)
	}
}

func TestQuietHoursWithoutAZoneSayWhichZoneTheyUse(t *testing.T) {
	q, why := parseQuiet("22:00-07:00", "")
	if q == nil || why == "" {
		t.Fatalf("want on in the local zone with a note, have %+v, %q", q, why)
	}
}

func TestNoQuietSettingIsOffAndSilent(t *testing.T) {
	if q, why := parseQuiet("", ""); q != nil || why != "" {
		t.Fatalf("got %+v, %q", q, why)
	}
}
