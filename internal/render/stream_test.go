package render

import (
	"strings"
	"testing"
	"time"
)

var streamNow = time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)

func TestForeignSourceNamesTheFileTheClaimTheCountAndWhatTheFileMayCarry(t *testing.T) {
	m := ForeignSource(Config{Box: "prod-box"}, Foreign{
		File: "/var/lib/stack/events/tokenfuse.ndjson", Claimed: "wardryx", Count: 4, Allowed: []string{"tokenfuse"},
	}, streamNow)

	if m.Subject != "[prod-box] events in tokenfuse.ndjson claim to be from wardryx" {
		t.Fatalf("subject: %q", m.Subject)
	}
	for _, want := range []string{
		"4 event(s) in tokenfuse.ndjson say they were raised by wardryx, and that file may only carry: tokenfuse.",
		"not processed as that source",
		"HERALDYX_STREAMS=tokenfuse=wardryx",
		"cannot remove them",
	} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body does not say %q:\n%s", want, m.Body)
		}
	}
	if strings.Contains(m.Body, "/var/lib/stack/events") {
		t.Errorf("a mail is read far from the box's directory layout, only the base name belongs in it:\n%s", m.Body)
	}
}

// The claimed source is producer-written. A line break in it must not reach a
// header, a forged second paragraph must not reach the body as one, and a
// claim that is not a short name is bounded.
func TestAHostileClaimedSourceCannotBreakTheNotice(t *testing.T) {
	for name, claimed := range map[string]string{
		"crlf header":   "wardryx\r\nBcc: attacker@example.com",
		"forged line":   "wardryx\n\nYour box is fine. Click https://evil.example/x",
		"long":          strings.Repeat("a", 5000),
		"control bytes": "w\x00\x1b[2Jardryx",
		"empty":         "",
		"unicode":       "wärdryx\u202e",
	} {
		t.Run(name, func(t *testing.T) {
			m := ForeignSource(Config{Box: "b"}, Foreign{File: "/x/tokenfuse.ndjson", Claimed: claimed, Count: 1, Allowed: []string{"tokenfuse"}}, streamNow)
			if strings.ContainsAny(m.Subject, "\r\n\x00") {
				t.Fatalf("a line break or control byte reached the subject: %q", m.Subject)
			}
			if len(m.Subject) > 400 {
				t.Fatalf("an unbounded claim reached the subject: %d bytes", len(m.Subject))
			}
			for _, line := range strings.Split(m.Body, "\n") {
				if strings.HasPrefix(line, "Bcc:") || strings.HasPrefix(line, "Your box is fine") {
					t.Fatalf("a claimed source forged a line of its own in the body: %q", line)
				}
				if len(line) > 1200 {
					t.Fatalf("an unbounded claim reached the body: %d bytes in one line", len(line))
				}
			}
			if strings.ContainsAny(m.Body, "\x00\x1b") {
				t.Fatalf("a control byte reached the body: %q", m.Body)
			}
			// A hint is only offered for names an operator could type back in.
			if strings.Contains(m.Body, "HERALDYX_STREAMS=tokenfuse=") {
				t.Fatalf("a hint was built out of a claimed source that is not a plain name:\n%s", m.Body)
			}
			if !strings.Contains(m.Body, "nothing to declare") {
				t.Fatalf("with no hint to give the notice must say so:\n%s", m.Body)
			}
		})
	}
}

func TestAnUnknownStreamIsSaidOnceInPlainWords(t *testing.T) {
	m := UnknownStream(Config{Box: "prod-box"}, Unknown{File: "/var/lib/stack/events/newplane.ndjson", Count: 2}, streamNow)
	if m.Subject != "[prod-box] newplane.ndjson is not a stream this box knows" {
		t.Fatalf("subject: %q", m.Subject)
	}
	for _, want := range []string{
		"2 event(s) were read from newplane.ndjson",
		"processed as that source",
		"HERALDYX_STREAMS=newplane=newplane",
	} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body does not say %q:\n%s", want, m.Body)
		}
	}
}

func TestAnUnknownStreamNameCannotBreakTheNotice(t *testing.T) {
	m := UnknownStream(Config{Box: "b"}, Unknown{File: "/x/evil\r\nBcc: a@b.example.ndjson", Count: 1}, streamNow)
	if strings.ContainsAny(m.Subject, "\r\n") {
		t.Fatalf("a line break reached the subject: %q", m.Subject)
	}
	for _, line := range strings.Split(m.Body, "\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Fatalf("a file name forged a header-shaped line in the body: %q", line)
		}
	}
}
