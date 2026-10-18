// Package deliver puts a rendered message somewhere: a mail server, a file,
// or nowhere at all.
//
// The interface exists so the awkward half of this system is testable without
// a mail server, and so the default build of a box with no address configured
// carries no mail code path at all beyond a no-op.
package deliver

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/TAIPANBOX/heraldyx/internal/render"
)

// Sender delivers one message to a set of recipients.
type Sender interface {
	// Name identifies the sender in logs.
	Name() string
	// Send delivers m. An error is a delivery failure, not a reason to stop
	// the process: the caller retries and eventually gives up loudly.
	Send(to []string, m render.Message) error
}

// Discard drops everything. This is what a box with no address configured
// gets: the process runs, stays healthy, watches the log, and sends nothing.
// A missing address is a choice, not a broken deployment.
type Discard struct{}

func (Discard) Name() string { return "discard" }

func (Discard) Send([]string, render.Message) error { return nil }

// File appends messages to a file instead of sending them. This is how the
// whole chain is proven on a laptop, and how an operator can watch what WOULD
// be sent before pointing this at a real mail server.
type File struct {
	path string
	mu   sync.Mutex
}

// NewFile returns a file sender writing to path.
func NewFile(path string) *File { return &File{path: path} }

func (f *File) Name() string { return "file" }

func (f *File) Send(to []string, m render.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// #nosec G304 -- the path is operator-supplied configuration, the same as
	// every other path this process is told to use.
	fh, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("deliver: open %s: %w", f.path, err)
	}
	defer fh.Close()
	_, err = fmt.Fprintf(fh, "To: %s\nSubject: %s\n\n%s\n----\n",
		strings.Join(to, ", "), m.Subject, m.Body)
	return err
}

// Recording keeps messages in memory. Tests only.
type Recording struct {
	mu       sync.Mutex
	Messages []render.Message
	Fail     error
}

func (r *Recording) Name() string { return "recording" }

func (r *Recording) Send(_ []string, m render.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Fail != nil {
		return r.Fail
	}
	r.Messages = append(r.Messages, m)
	return nil
}

// Sent returns a copy of what was recorded.
func (r *Recording) Sent() []render.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]render.Message, len(r.Messages))
	copy(out, r.Messages)
	return out
}

// TLSMode says how the connection to the mail server is encrypted.
type TLSMode string

const (
	// TLSStartTLS connects in the clear and then upgrades with STARTTLS. The
	// server must offer the upgrade or nothing is sent. This is the default.
	TLSStartTLS TLSMode = "starttls"
	// TLSImplicit speaks TLS from the first byte, as submission on port 465
	// does. A host given with port 465 and no mode gets this.
	TLSImplicit TLSMode = "implicit"
	// TLSPlain sends in the clear. It is refused for any host that is not on
	// the loopback interface, and the operator has to ask for it by name.
	TLSPlain TLSMode = "plain"
)

// The dial deadline covers the TCP connect. The send deadline covers the TLS
// handshake and every line of the session after it. Without them a server that
// stops answering holds the notifier, and the box goes quiet without saying so.
const (
	defaultDialTimeout = 10 * time.Second
	defaultSendTimeout = 60 * time.Second
)

// SMTPConfig is an ordinary mail submission setup: a host, a from address,
// optionally a username and password, and how the connection is encrypted.
type SMTPConfig struct {
	Host     string // host:port, e.g. smtp.example.com:587
	From     string
	Username string
	Password string
	// TLS is the connection mode. Empty means STARTTLS, or implicit TLS when
	// Host names port 465.
	TLS TLSMode
}

// SMTP sends over SMTP submission using the standard library only.
//
// No dependency is taken for this on purpose. A mail client is a small amount
// of well-specified code, and this process is the one component of the box
// that is allowed to open a connection to the outside world: its dependency
// list is a thing an operator reads.
type SMTP struct {
	cfg         SMTPConfig
	mode        TLSMode
	dialTimeout time.Duration
	sendTimeout time.Duration
	// roots is what the server's certificate is verified against. Nil means
	// the system roots. Only tests set it.
	roots *x509.CertPool
}

// NewSMTP validates the configuration and returns a sender.
func NewSMTP(cfg SMTPConfig) (*SMTP, error) {
	if cfg.Host == "" {
		return nil, errors.New("deliver: smtp host is empty")
	}
	if _, _, err := net.SplitHostPort(cfg.Host); err != nil {
		return nil, fmt.Errorf("deliver: smtp host %q needs a port, e.g. %s:587", cfg.Host, cfg.Host)
	}
	if err := checkHeaderSafe("from address", cfg.From); err != nil {
		return nil, err
	}
	mode, err := resolveTLSMode(cfg.TLS, cfg.Host)
	if err != nil {
		return nil, err
	}
	return &SMTP{
		cfg:         cfg,
		mode:        mode,
		dialTimeout: defaultDialTimeout,
		sendTimeout: defaultSendTimeout,
	}, nil
}

// resolveTLSMode turns the operator's setting into the mode a connection uses.
// An empty setting is STARTTLS, or implicit TLS on port 465. Plaintext is never
// the default, and it is refused for any host off the loopback interface.
func resolveTLSMode(mode TLSMode, host string) (TLSMode, error) {
	_, port, err := net.SplitHostPort(host)
	if err != nil {
		return "", fmt.Errorf("deliver: smtp host %q needs a port: %w", host, err)
	}
	switch mode {
	case "":
		if port == "465" {
			return TLSImplicit, nil
		}
		return TLSStartTLS, nil
	case TLSStartTLS, TLSImplicit:
		return mode, nil
	case TLSPlain:
		if !isLoopbackHost(host) {
			return "", fmt.Errorf("deliver: smtp tls mode plain is refused for %s: plaintext is allowed only to the loopback interface (127.0.0.0/8, ::1, localhost)", host)
		}
		return mode, nil
	default:
		return "", fmt.Errorf("deliver: unknown smtp tls mode %q, want starttls, implicit or plain", mode)
	}
}

// isLoopbackHost reports whether a host:port names the loopback interface by
// address or by the name localhost. Other names are not resolved here: a name
// that might point anywhere is not a place plaintext may go.
func isLoopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *SMTP) Name() string { return "smtp" }

func (s *SMTP) Send(to []string, m render.Message) error {
	msg, err := Compose(s.cfg.From, to, m, time.Now())
	if err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(s.cfg.Host) // checked by NewSMTP
	conn, err := s.connect()
	if err != nil {
		return fmt.Errorf("deliver: smtp send: %w", err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("deliver: smtp send: %w", err)
	}
	defer c.Close()
	if err := s.session(c, host, to, msg); err != nil {
		return fmt.Errorf("deliver: smtp send: %w", err)
	}
	return nil
}

// connect opens the TCP connection and, for implicit TLS, completes the
// handshake. The send deadline is set before either, so the send ends at the
// deadline whatever the server does.
func (s *SMTP) connect() (net.Conn, error) {
	d := net.Dialer{Timeout: s.dialTimeout}
	raw, err := d.Dial("tcp", s.cfg.Host)
	if err != nil {
		return nil, err
	}
	if err := raw.SetDeadline(time.Now().Add(s.sendTimeout)); err != nil {
		raw.Close()
		return nil, err
	}
	if s.mode != TLSImplicit {
		return raw, nil
	}
	conn := tls.Client(raw, s.tlsConfig())
	if err := conn.Handshake(); err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

// session runs the SMTP dialogue. In STARTTLS mode the server must offer the
// upgrade, and nothing (not AUTH, not the message) is sent before it is done.
//
// A QUIT that fails is not reported. The server has accepted the message at the
// end of DATA by then, and a retry would send the same alert twice.
func (s *SMTP) session(c *smtp.Client, host string, to []string, msg []byte) error {
	if err := c.Hello("localhost"); err != nil {
		return err
	}
	if s.mode == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("server %s does not offer STARTTLS, refusing to send in the clear", host)
		}
		if err := c.StartTLS(s.tlsConfig()); err != nil {
			return err
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return err
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	_ = c.Quit()
	return nil
}

// tlsConfig verifies the server against the name it was dialled by, and keeps
// TLS 1.2 as the floor.
func (s *SMTP) tlsConfig() *tls.Config {
	host, _, _ := net.SplitHostPort(s.cfg.Host)
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: s.roots}
}

// Compose builds the RFC 5322 message bytes.
//
// Every header value that came from an event passes through
// [checkHeaderSafe] first. A subject line is built from ids a producer chose,
// and a bare newline inside one of them would let that producer add headers of
// its own: a Bcc, a Reply-To pointing somewhere else. This is the one place
// where data from inside the stack turns into protocol, so it is the one place
// that has to refuse.
func Compose(from string, to []string, m render.Message, now time.Time) ([]byte, error) {
	if err := checkHeaderSafe("from address", from); err != nil {
		return nil, err
	}
	for _, r := range to {
		if err := checkHeaderSafe("recipient", r); err != nil {
			return nil, err
		}
	}
	if err := checkHeaderSafe("subject", m.Subject); err != nil {
		return nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(m.Body, "\n", "\r\n"))
	return []byte(b.String()), nil
}

// checkHeaderSafe refuses anything that could break out of a header.
func checkHeaderSafe(what, v string) error {
	if strings.ContainsAny(v, "\r\n") {
		return fmt.Errorf("deliver: %s contains a line break, refusing to build a message from it", what)
	}
	return nil
}
