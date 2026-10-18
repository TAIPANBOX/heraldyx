package deliver

import (
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/heraldyx/internal/render"
)

// Port 465 is submission over implicit TLS. An operator who gives that port and
// no mode gets implicit TLS, so the launchers that prompt for "host:465" keep
// working without a new setting. Anything else defaults to STARTTLS.
func TestPort465IsImplicitTLSByDefault(t *testing.T) {
	cases := []struct {
		name string
		mode TLSMode
		host string
		want TLSMode
	}{
		{"unset on 587 is STARTTLS", "", "smtp.example.com:587", TLSStartTLS},
		{"unset on 25 is STARTTLS", "", "smtp.example.com:25", TLSStartTLS},
		{"unset on 465 is implicit TLS", "", "smtp.example.com:465", TLSImplicit},
		{"an explicit STARTTLS on 465 is kept", TLSStartTLS, "smtp.example.com:465", TLSStartTLS},
		{"plaintext to loopback is kept", TLSPlain, "127.0.0.1:2525", TLSPlain},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveTLSMode(c.mode, c.host)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("resolveTLSMode(%q, %q) = %q, want %q", c.mode, c.host, got, c.want)
			}
		})
	}
}

// The STARTTLS path, end to end: a server that offers the upgrade and presents
// a certificate the client trusts takes the message after the upgrade.
func TestAStarttlsRelayReceivesTheMailOverVerifiedTLS(t *testing.T) {
	relay := newFakeRelay(t, relayOpts{starttls: true})
	s, err := NewSMTP(SMTPConfig{Host: relay.addr(), From: "box@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	s.roots = relay.trust()
	if err := s.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"}); err != nil {
		t.Fatalf("send after STARTTLS: %v", err)
	}
	if got := relay.received(); len(got) != 1 {
		t.Fatalf("relay received %d message(s), want 1", len(got))
	}
}

// The implicit path, end to end: a relay that speaks TLS from the first byte and
// presents a certificate the client trusts receives the message.
func TestAnImplicitTLSRelayReceivesTheMail(t *testing.T) {
	relay := newFakeRelay(t, relayOpts{implicit: true})
	s, err := NewSMTP(SMTPConfig{Host: relay.addr(), From: "box@example.com", TLS: TLSImplicit})
	if err != nil {
		t.Fatal(err)
	}
	s.roots = relay.trust()
	if err := s.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"}); err != nil {
		t.Fatalf("send over implicit TLS: %v", err)
	}
	if got := relay.received(); len(got) != 1 {
		t.Fatalf("relay received %d message(s), want 1", len(got))
	}
}

// The same relay with a certificate nobody trusts gets nothing. This is the
// verification the implicit path must not skip.
func TestAnImplicitTLSRelayWithAnUntrustedCertificateGetsNoMail(t *testing.T) {
	relay := newFakeRelay(t, relayOpts{implicit: true})
	s, err := NewSMTP(SMTPConfig{Host: relay.addr(), From: "box@example.com", TLS: TLSImplicit})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"}); err == nil {
		t.Fatal("want a refusal: the certificate is not in the system roots")
	}
	if got := relay.received(); len(got) != 0 {
		t.Fatalf("relay received %d message(s) over an unverified connection", len(got))
	}
}

// Plaintext is used only when the operator names it, and only to the loopback
// interface. A relay on 127.0.0.1 that offers no STARTTLS takes the message.
func TestAnExplicitPlainRelayOnLoopbackIsUsed(t *testing.T) {
	relay := newFakeRelay(t, relayOpts{})
	s, err := NewSMTP(SMTPConfig{Host: relay.addr(), From: "box@example.com", TLS: TLSPlain})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"}); err != nil {
		t.Fatalf("send in the clear to loopback: %v", err)
	}
	if got := relay.received(); len(got) != 1 {
		t.Fatalf("relay received %d message(s), want 1", len(got))
	}
}

// Plaintext toward any other host is refused when the transport is built, so
// the process does not start with a setting that would send in the clear.
func TestPlaintextToANonLoopbackHostIsRefusedAtStartup(t *testing.T) {
	for _, host := range []string{"mail.example.com:25", "192.0.2.10:25", "10.0.0.5:2525"} {
		_, err := NewSMTP(SMTPConfig{Host: host, From: "box@example.com", TLS: TLSPlain})
		if err == nil {
			t.Errorf("%s: want a refusal, plaintext is for the loopback interface only", host)
			continue
		}
		if !strings.Contains(err.Error(), "loopback") {
			t.Errorf("%s: the refusal does not say why: %v", host, err)
		}
	}
	for _, host := range []string{"127.0.0.1:2525", "localhost:2525", "[::1]:2525"} {
		if _, err := NewSMTP(SMTPConfig{Host: host, From: "box@example.com", TLS: TLSPlain}); err != nil {
			t.Errorf("%s: loopback plaintext refused: %v", host, err)
		}
	}
}

// A misspelt mode must not fall back to something weaker or to the default
// without saying so. The refusal lists the three values it accepts.
func TestAnUnknownTLSModeIsRefused(t *testing.T) {
	_, err := NewSMTP(SMTPConfig{Host: "127.0.0.1:2525", From: "box@example.com", TLS: TLSMode("tls-ish")})
	if err == nil {
		t.Fatal("want a refusal for an unknown TLS mode")
	}
	for _, want := range []string{"starttls", "implicit", "plain"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not list %q: %v", want, err)
		}
	}
}

// A server that accepts the connection and never speaks must not hold the
// notifier. The session deadline cuts it off.
func TestAStalledSessionIsCutOffAtTheDeadline(t *testing.T) {
	relay := newFakeRelay(t, relayOpts{silent: true})
	s, err := NewSMTP(SMTPConfig{Host: relay.addr(), From: "box@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	s.sendTimeout = 300 * time.Millisecond
	errc := make(chan error, 1)
	go func() {
		errc <- s.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"})
	}()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("want an error from a server that never speaks")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("send still running after 5s with a 300ms deadline: no deadline is set")
	}
}

// A host that does not accept the connection is given up on at the dial
// deadline. 192.0.2.1 is TEST-NET-1 and is not routed, so the dial either stalls
// until the deadline or fails at once; both must return.
func TestAHostThatNeverAcceptsIsGivenUpOnAtTheDialDeadline(t *testing.T) {
	s, err := NewSMTP(SMTPConfig{Host: "192.0.2.1:587", From: "box@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	s.dialTimeout = 300 * time.Millisecond
	errc := make(chan error, 1)
	go func() {
		errc <- s.Send([]string{"ops@example.com"}, render.Message{Subject: "s", Body: "b"})
	}()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("want an error from an unreachable host")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dial still running after 5s with a 300ms deadline: no dial deadline is set")
	}
}

// A transport built with defaults has both deadlines set. Zero would mean no
// deadline at all, which is the defect this closes.
func TestTheDefaultDeadlinesAreSet(t *testing.T) {
	s, err := NewSMTP(SMTPConfig{Host: "smtp.example.com:587", From: "box@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if s.dialTimeout <= 0 || s.sendTimeout <= 0 {
		t.Fatalf("dial %v and send %v: both deadlines must be set", s.dialTimeout, s.sendTimeout)
	}
}
