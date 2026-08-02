package deliver

import (
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/heraldyx/internal/render"
)

var now = time.Date(2026, 8, 2, 14, 3, 0, 0, time.UTC)

// A subject line is built from ids a producer chose. A bare newline inside one
// would let that producer write headers of its own: a Bcc to somewhere else, a
// Reply-To that is not us. This is the one place data becomes protocol.
func TestHeaderInjectionIsRefused(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   []string
		msg  render.Message
	}{
		{
			name: "subject",
			from: "box@example.com",
			to:   []string{"ops@example.com"},
			msg:  render.Message{Subject: "alert\r\nBcc: attacker@example.com", Body: "b"},
		},
		{
			name: "recipient",
			from: "box@example.com",
			to:   []string{"ops@example.com\nBcc: attacker@example.com"},
			msg:  render.Message{Subject: "s", Body: "b"},
		},
		{
			name: "from",
			from: "box@example.com\r\nReply-To: attacker@example.com",
			to:   []string{"ops@example.com"},
			msg:  render.Message{Subject: "s", Body: "b"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Compose(c.from, c.to, c.msg, now); err == nil {
				t.Fatal("want a refusal, got a message")
			}
		})
	}
}

func TestComposeShape(t *testing.T) {
	raw, err := Compose("box@example.com", []string{"ops@example.com"},
		render.Message{Subject: "[prod-box] run-42 is approaching its budget", Body: "line one\nline two\n"}, now)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		"From: box@example.com\r\n",
		"To: ops@example.com\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		// Mail loops are a real failure mode when a mailbox has an autoresponder.
		"Auto-Submitted: auto-generated\r\n",
		"\r\n\r\nline one\r\nline two\r\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

// A subject with non-ASCII must not be emitted raw.
func TestNonASCIISubjectIsEncoded(t *testing.T) {
	raw, err := Compose("box@example.com", []string{"ops@example.com"},
		render.Message{Subject: "бюджет вичерпано", Body: "b"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Subject: =?utf-8?") {
		t.Fatalf("want an encoded-word subject, got:\n%s", raw)
	}
}

// A box with nothing configured gets this, and it must be boring.
func TestDiscardIsSilentAndSuccessful(t *testing.T) {
	if err := (Discard{}).Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"}); err != nil {
		t.Fatalf("the no-op sender must not fail: %v", err)
	}
}

func TestFileSenderWritesWhatWouldBeSent(t *testing.T) {
	path := t.TempDir() + "/mail.txt"
	f := NewFile(path)
	if err := f.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Send([]string{"ops@example.com"}, render.Message{Subject: "s2", Body: "b2"}); err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, path)
	if !strings.Contains(raw, "Subject: s\n") || !strings.Contains(raw, "Subject: s2\n") {
		t.Fatalf("both messages should be appended:\n%s", raw)
	}
}

func TestSMTPRefusesAHostWithoutAPort(t *testing.T) {
	if _, err := NewSMTP(SMTPConfig{Host: "smtp.example.com", From: "box@example.com"}); err == nil {
		t.Fatal("want a refusal: a host with no port is the most common way this is misconfigured")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := readAll(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
