package deliver

import (
	"testing"

	"github.com/TAIPANBOX/heraldyx/internal/render"
)

// A remote relay that does not offer STARTTLS gets no mail. The unfixed
// transport handed the message to such a server in the clear, so an alert's
// content crossed the network unencrypted whenever the server simply did not
// say it could encrypt.
func TestAPlainRelayThatOffersNoSTARTTLSIsRefused(t *testing.T) {
	relay := newFakeRelay(t, relayOpts{})
	s, err := NewSMTP(SMTPConfig{Host: relay.addr(), From: "box@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"}); err == nil {
		t.Fatal("want a refusal: the relay offers no STARTTLS, so nothing should be sent")
	}
	if got := relay.received(); len(got) != 0 {
		t.Fatalf("the relay received %d message(s) without encryption", len(got))
	}
}

// A STARTTLS server whose certificate is not trusted gets no mail. Verification
// is on in the unfixed code too, so this one is a guard, not a red test: it
// pins a property the fix must keep.
func TestAStarttlsRelayIsReachedOnlyThroughAVerifiedCertificate(t *testing.T) {
	relay := newFakeRelay(t, relayOpts{starttls: true})
	s, err := NewSMTP(SMTPConfig{Host: relay.addr(), From: "box@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"}); err == nil {
		t.Fatal("want a refusal: the relay's certificate is not in the system roots")
	}
	if got := relay.received(); len(got) != 0 {
		t.Fatalf("the relay received %d message(s) over an unverified connection", len(got))
	}
}

// The server accepts the message at the end of DATA and then drops the
// connection instead of answering QUIT. The message is delivered, so the send
// must report success: an error here would make the caller send it again.
func TestAMessageAcceptedBeforeAFailedQuitIsNotResent(t *testing.T) {
	relay := newFakeRelay(t, relayOpts{dropOnQuit: true})
	s, err := NewSMTP(SMTPConfig{Host: relay.addr(), From: "box@example.com", TLS: TLSPlain})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"}); err != nil {
		t.Fatalf("a failed QUIT after acceptance was reported as a failed send: %v", err)
	}
	if got := relay.received(); len(got) != 1 {
		t.Fatalf("relay received %d message(s), want 1", len(got))
	}
}
