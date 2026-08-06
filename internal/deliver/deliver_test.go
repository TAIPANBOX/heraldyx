package deliver

import (
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
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

// The other half of the same defect, from the side that refuses.
//
// `Compose` is right to refuse a subject with a line break: that is what stops
// a producer writing headers of its own, and TestHeaderInjectionIsRefused above
// holds it. What was wrong until 2026-08-05 is that a producer could REACH that
// refusal with a value it chose. `agent_id` in tokenfuse comes from the
// caller's own `x-fuse-agent-id` header, so an agent could put a newline in its
// own name and every alert about it died here instead of arriving. Silence is
// the worst failure this component has, and an agent able to cause it is worse
// than the injection that was already closed.
//
// So this is the reachability test, not another injection test: nothing
// `internal/render` produces may make Compose refuse. The guard stays; it is
// now the last line rather than the only one.
func TestNoProducerSuppliedFieldCanStopDelivery(t *testing.T) {
	hostile := map[string]string{
		"a line feed":       "x\nBcc: attacker@example.com",
		"a crlf":            "x\r\nReply-To: attacker@example.com",
		"a nul":             "x\x00y",
		"a hundred k of it": strings.Repeat("a", 100_000),
	}
	fields := map[string]func(*event.Event, string){
		"type":     func(e *event.Event, v string) { e.Type = v },
		"agent_id": func(e *event.Event, v string) { e.AgentID = v },
		"run_id":   func(e *event.Event, v string) { e.RunID = v },
		"source":   func(e *event.Event, v string) { e.Source = v },
	}

	// Both shapes of event, because `rule.Subject` prefers the run id: an event
	// with a run id never puts the agent id in the subject, and most of the
	// money plane's events have one. The event that carries the defect is the
	// ordinary one about an agent rather than a run, which is exactly the event
	// an agent-scoped alert is.
	for _, withRun := range []bool{true, false} {
		shape := "with a run id"
		if !withRun {
			shape = "with no run id"
		}
		for field, set := range fields {
			for name, value := range hostile {
				t.Run(shape+", "+field+" with "+name, func(t *testing.T) {
					e := event.Event{
						Schema:   event.SchemaV02,
						TS:       "2026-08-02T14:03:00Z",
						Source:   "tokenfuse",
						Type:     "breaker_tripped",
						AgentID:  "agent://acme.example/biller",
						Severity: event.SeverityHigh,
					}
					if withRun {
						e.RunID = "run-42"
					}
					set(&e, value)
					m := render.Event(render.Config{Box: "prod-box", ConsoleURL: "https://box.example.com"},
						e, now, "", nil)
					raw, err := Compose("box@example.com", []string{"ops@example.com"}, m, now)
					if err != nil {
						t.Fatalf("a producer stopped its own alert by writing %s into %s: %v", name, field, err)
					}
					if !strings.Contains(string(raw), "Subject: ") {
						t.Fatalf("composed no subject at all:\n%s", raw)
					}
				})
			}
		}
	}
}
