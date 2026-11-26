package deliver

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/heraldyx/internal/render"
)

// plainSender is a sender to a loopback relay in plaintext, the one mode that
// lets a test speak to the fake relay without a certificate.
func plainSender(t *testing.T, relay *fakeRelay, user, pass string) *SMTP {
	t.Helper()
	s, err := NewSMTP(SMTPConfig{Host: relay.addr(), From: "box@example.com", Username: user, Password: pass, TLS: TLSPlain})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Every step of the dialogue the server can refuse is a failed send. A step
// that ignored the refusal would report success for a message the server never
// took, and the caller would never retry it: the alert would be lost quietly.
func TestEveryRefusedStepOfTheDialogueFailsTheSend(t *testing.T) {
	cases := []struct {
		name string
		opts relayOpts
	}{
		{"greeting refused", relayOpts{reject: []string{"EHLO", "HELO"}}},
		{"sender refused", relayOpts{reject: []string{"MAIL FROM"}}},
		{"recipient refused", relayOpts{reject: []string{"RCPT TO"}}},
		{"data refused", relayOpts{reject: []string{"DATA"}}},
		{"message refused at the end of the data", relayOpts{refuseMessage: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			relay := newFakeRelay(t, c.opts)
			s := plainSender(t, relay, "", "")
			err := s.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"})
			if err == nil {
				t.Fatal("the relay refused a step and the send reported success")
			}
			if !strings.Contains(err.Error(), "relay.test") || !strings.HasPrefix(err.Error(), "deliver: smtp send: ") {
				t.Fatalf("the error does not carry the relay's refusal: %v", err)
			}
			if got := relay.received(); len(got) != 0 {
				t.Fatalf("the relay stored %d message(s) it refused", len(got))
			}
		})
	}
}

// The second recipient refused fails the send even though the first was taken:
// a message that reaches part of the list is not a delivered alert.
func TestASecondRecipientRefusedFailsTheSend(t *testing.T) {
	relay := newFakeRelay(t, relayOpts{reject: []string{"RCPT TO:<NOBODY@"}})
	s := plainSender(t, relay, "", "")
	err := s.Send([]string{"ops@example.com", "nobody@example.com"}, render.Message{Subject: "s", Body: "b"})
	if err == nil {
		t.Fatal("a refused recipient was reported as delivered")
	}
	if got := relay.received(); len(got) != 0 {
		t.Fatalf("relay received %d message(s), want none", len(got))
	}
}

// With a username configured the credentials go to the server as AUTH PLAIN,
// and the message follows only once they are accepted.
func TestCredentialsAreSentAsAuthPlainBeforeTheMessage(t *testing.T) {
	relay := newFakeRelay(t, relayOpts{auth: true})
	s := plainSender(t, relay, "box-user", "s3cret")
	if err := s.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"}); err != nil {
		t.Fatalf("send with credentials: %v", err)
	}
	lines := relay.authLines()
	if len(lines) != 1 {
		t.Fatalf("relay saw %d AUTH command(s), want 1: %q", len(lines), lines)
	}
	f := strings.Fields(lines[0])
	if len(f) != 3 || f[1] != "PLAIN" {
		t.Fatalf("AUTH line %q, want AUTH PLAIN <initial response>", lines[0])
	}
	raw, err := base64.StdEncoding.DecodeString(f[2])
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "\x00box-user\x00s3cret" {
		t.Fatalf("AUTH PLAIN carried %q, want the configured username and password", raw)
	}
	if got := relay.received(); len(got) != 1 {
		t.Fatalf("relay received %d message(s), want 1", len(got))
	}
}

// Credentials the server refuses stop the send before MAIL FROM. So does a
// server that does not implement AUTH when a username is configured: the
// client still tries it, and the refusal is not skipped past into sending
// unauthenticated through a relay the operator configured credentials for.
func TestRefusedOrUnofferedAuthenticationSendsNothing(t *testing.T) {
	cases := []struct {
		name string
		opts relayOpts
	}{
		{"credentials refused", relayOpts{auth: true, reject: []string{"AUTH"}}},
		{"AUTH not implemented", relayOpts{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			relay := newFakeRelay(t, c.opts)
			s := plainSender(t, relay, "box-user", "wrong")
			err := s.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"})
			if err == nil {
				t.Fatal("the send succeeded without accepted credentials")
			}
			if !strings.HasPrefix(err.Error(), "deliver: smtp send: ") {
				t.Fatalf("error: %v", err)
			}
			if got := relay.received(); len(got) != 0 {
				t.Fatalf("relay received %d message(s) without authentication", len(got))
			}
		})
	}
}

// A message with a header a producer could break out of is refused before any
// connection is made: the relay never even sees a greeting answered.
func TestTheMailPathRefusesABadHeaderBeforeConnecting(t *testing.T) {
	relay := newFakeRelay(t, relayOpts{})
	s := plainSender(t, relay, "", "")
	err := s.Send([]string{"ops@example.com"}, render.Message{Subject: "s\r\nBcc: thief@example.net", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "subject contains a line break") {
		t.Fatalf("want the header refusal, got %v", err)
	}
	if strings.HasPrefix(err.Error(), "deliver: smtp send") {
		t.Fatalf("the refusal came from the connection, not from the header check: %v", err)
	}
}

// NewSMTP refuses what it cannot send with, at startup rather than at the
// first alert.
func TestNewSMTPRefusesAnUnusableConfiguration(t *testing.T) {
	cases := []struct {
		name string
		cfg  SMTPConfig
		want string
	}{
		{"empty host", SMTPConfig{From: "box@example.com"}, "smtp host is empty"},
		{"from address with a line break", SMTPConfig{Host: "127.0.0.1:25", From: "box@example.com\nBcc: x@example.net"}, "from address contains a line break"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := NewSMTP(c.cfg)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("NewSMTP(%+v) = %v, %v; want an error containing %q", c.cfg, s, err, c.want)
			}
			if s != nil {
				t.Fatal("a refused configuration still returned a sender")
			}
		})
	}
}

// The mode resolver and the loopback test refuse a host with no port on their
// own, not only behind NewSMTP's check: plaintext never goes to a host whose
// name could not even be split.
func TestAHostWithoutAPortIsNeitherResolvedNorLoopback(t *testing.T) {
	if _, err := resolveTLSMode(TLSPlain, "127.0.0.1"); err == nil || !strings.Contains(err.Error(), "needs a port") {
		t.Fatalf("resolveTLSMode on a host without a port: %v", err)
	}
	if isLoopbackHost("127.0.0.1") {
		t.Fatal("a host without a port was taken for loopback")
	}
	if !isLoopbackHost("[::1]:25") || !isLoopbackHost("localhost:25") || isLoopbackHost("10.0.0.1:25") {
		t.Fatal("the loopback test misjudges a host:port")
	}
}

// The file sender reports a path it cannot open, naming the path, rather than
// dropping the message.
func TestTheFileSenderReportsAPathItCannotOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "out.txt")
	err := NewFile(path).Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "deliver: open "+path) {
		t.Fatalf("want an open error naming %s, got %v", path, err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the cause was lost: %v", err)
	}
}

// The recording sender other packages test against: a set failure is returned
// and nothing is recorded, and what it hands back is a copy a caller cannot
// use to change the record.
func TestTheRecordingSenderFailsOnDemandAndHandsOutACopy(t *testing.T) {
	boom := errors.New("relay down")
	r := &Recording{Fail: boom}
	if err := r.Send(nil, render.Message{Subject: "lost"}); !errors.Is(err, boom) {
		t.Fatalf("Send with Fail set = %v, want %v", err, boom)
	}
	if got := r.Sent(); len(got) != 0 {
		t.Fatalf("a failed send was recorded: %+v", got)
	}
	r.Fail = nil
	if err := r.Send(nil, render.Message{Subject: "kept"}); err != nil {
		t.Fatal(err)
	}
	got := r.Sent()
	if len(got) != 1 || got[0].Subject != "kept" {
		t.Fatalf("recorded %+v", got)
	}
	got[0].Subject = "changed by the caller"
	if again := r.Sent(); again[0].Subject != "kept" {
		t.Fatalf("Sent handed out the record itself: %q", again[0].Subject)
	}
}

// Each sender names itself distinctly: the name is how a log line says which
// transport an alert went through.
func TestEverySenderNamesItselfDistinctly(t *testing.T) {
	smtpSender, err := NewSMTP(SMTPConfig{Host: "127.0.0.1:25", From: "box@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Sender{"discard": Discard{}, "file": NewFile("x"), "recording": &Recording{}, "smtp": smtpSender}
	for name, s := range want {
		if s.Name() != name {
			t.Fatalf("%T names itself %q, want %q", s, s.Name(), name)
		}
	}
}
