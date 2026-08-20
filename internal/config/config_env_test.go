package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole configuration surface, which was at 0%.
//
// A notifier that reads its configuration wrong does not crash. It runs, sees
// events, and sends nothing, which is indistinguishable from a quiet week. That
// is why `Why()` exists at all, and why none of this being read back was worse
// than the line count suggests.

func TestAnEmptyRecipientListMeansNothingIsSentAndTheReasonNamesTheVariable(t *testing.T) {
	t.Parallel()

	// "configured wrong" and "deliberately not configured" look identical in a
	// log that only says nothing, so each case has to name what to set.
	for _, tc := range []struct {
		name      string
		c         Config
		enabled   bool
		mustNames []string
	}{
		{
			name:      "nothing configured at all",
			c:         Config{},
			enabled:   false,
			mustNames: []string{"HERALDYX_TO", "HERALDYX_SMTP_HOST"},
		},
		{
			name:      "a transport but nobody to send to",
			c:         Config{SMTPHost: "smtp.example"},
			enabled:   false,
			mustNames: []string{"HERALDYX_TO"},
		},
		{
			name:      "recipients but no way to reach them",
			c:         Config{To: []string{"ops@example"}},
			enabled:   false,
			mustNames: []string{"HERALDYX_SMTP_HOST", "HERALDYX_MAIL_FILE"},
		},
		{
			name:    "recipients and smtp",
			c:       Config{To: []string{"ops@example"}, SMTPHost: "smtp.example"},
			enabled: true,
		},
		{
			name:    "recipients and a mail file, which is a transport too",
			c:       Config{To: []string{"ops@example"}, MailFile: "/tmp/mail.txt"},
			enabled: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.c.Enabled(); got != tc.enabled {
				t.Errorf("Enabled() = %v, want %v", got, tc.enabled)
			}
			why := tc.c.Why()
			if tc.enabled {
				if why != "" {
					t.Errorf("an enabled config must explain nothing, got %q", why)
				}
				return
			}
			if why == "" {
				t.Fatal("a disabled config must say why, or an operator cannot tell it " +
					"from a quiet week")
			}
			for _, name := range tc.mustNames {
				if !strings.Contains(why, name) {
					t.Errorf("the reason must name %s so it can be acted on, got: %s", name, why)
				}
			}
		})
	}
}

func TestTurningTheSentRecordOffStaysOffRatherThanBeingDefaultedBack(t *testing.T) {
	// The distinction between LookupEnv and Getenv, and it is one character to
	// break. An operator who sets HERALDYX_SENT to empty has turned recording
	// off deliberately; a default that quietly reinstates it is not a default,
	// it is an override of a decision.
	t.Setenv("HERALDYX_EVENTS", "/tmp/events")
	t.Setenv("HERALDYX_STATE", "/tmp/state/state.json")
	t.Setenv("HERALDYX_SENT", "")

	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if c.SentPath != "" {
		t.Errorf("HERALDYX_SENT was set to empty on purpose and came back as %q", c.SentPath)
	}
}

func TestAnUnsetSentRecordIsDerivedFromWhereTheStateLives(t *testing.T) {
	t.Setenv("HERALDYX_EVENTS", "/tmp/events")
	t.Setenv("HERALDYX_STATE", "/var/lib/stack/heraldyx/state.json")

	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	want := filepath.Join("/var/lib/stack/heraldyx", "sent.ndjson")
	if c.SentPath != want {
		t.Errorf("SentPath = %q, want %q", c.SentPath, want)
	}
}

func TestAConfigThatCouldNeverWorkIsRefusedAtStartupWithTheReason(t *testing.T) {
	t.Run("nothing to watch", func(t *testing.T) {
		t.Setenv("HERALDYX_EVENTS", "   ,  , ")
		if _, err := FromEnv(); err == nil {
			t.Error("an empty event list must be refused: there is nothing to watch")
		}
	})

	t.Run("mail with no sender", func(t *testing.T) {
		t.Setenv("HERALDYX_EVENTS", "/tmp/events")
		t.Setenv("HERALDYX_SMTP_HOST", "smtp.example")
		t.Setenv("HERALDYX_SMTP_FROM", "")
		_, err := FromEnv()
		if err == nil {
			t.Fatal("SMTP with no sender must be refused at startup rather than at the " +
				"first alert, which is the worst possible moment to discover it")
		}
		if !strings.Contains(err.Error(), "HERALDYX_SMTP_FROM") {
			t.Errorf("the error must name the missing variable, got: %v", err)
		}
	})
}

func TestAMalformedNumberFallsBackRatherThanBecomingZero(t *testing.T) {
	// Zero would be a real setting with a real meaning here: zero per hour is
	// "send nothing", and a typo silently becoming that is a notifier switched
	// off by a stray character.
	t.Setenv("HERALDYX_EVENTS", "/tmp/events")
	for _, bad := range []string{"twenty", "20/hour", "-5", ""} {
		t.Setenv("HERALDYX_MAX_PER_HOUR", bad)
		c, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		if c.MaxPerHour != 20 {
			t.Errorf("HERALDYX_MAX_PER_HOUR=%q gave %d, want the default of 20", bad, c.MaxPerHour)
		}
	}

	t.Setenv("HERALDYX_MAX_PER_HOUR", "0")
	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if c.MaxPerHour != 0 {
		t.Errorf("an explicit 0 is a real setting and must survive, got %d", c.MaxPerHour)
	}
}

func TestThePollIntervalIsNeverFastEnoughToStormASharedVolume(t *testing.T) {
	t.Setenv("HERALDYX_EVENTS", "/tmp/events")
	for _, tooFast := range []string{"1", "10", "99"} {
		t.Setenv("HERALDYX_POLL_MS", tooFast)
		c, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		if c.PollInterval < 100*time.Millisecond {
			t.Errorf("HERALDYX_POLL_MS=%s gave %v; other planes write to that volume",
				tooFast, c.PollInterval)
		}
	}
}

func TestARecipientListIsSplitAndTrimmedRatherThanTakenWhole(t *testing.T) {
	t.Setenv("HERALDYX_EVENTS", "/tmp/events")
	t.Setenv("HERALDYX_TO", " ops@example.com , , security@example.com ,")

	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if len(c.To) != 2 {
		t.Fatalf("got %d recipients from a list with two real entries: %v", len(c.To), c.To)
	}
	for _, want := range []string{"ops@example.com", "security@example.com"} {
		var found bool
		for _, got := range c.To {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q did not survive splitting: %v", want, c.To)
		}
	}
}
