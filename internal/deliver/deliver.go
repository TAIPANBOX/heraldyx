// Package deliver puts a rendered message somewhere: a mail server, a file,
// or nowhere at all.
//
// The interface exists so the awkward half of this system is testable without
// a mail server, and so the default build of a box with no address configured
// carries no mail code path at all beyond a no-op.
package deliver

import (
	"errors"
	"fmt"
	"mime"
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

// SMTPConfig is an ordinary mail submission setup: a host, a from address, and
// optionally a username and password.
type SMTPConfig struct {
	Host     string // host:port, e.g. smtp.example.com:587
	From     string
	Username string
	Password string
}

// SMTP sends over SMTP submission using the standard library only.
//
// No dependency is taken for this on purpose. A mail client is a small amount
// of well-specified code, and this process is the one component of the box
// that is allowed to open a connection to the outside world: its dependency
// list is a thing an operator reads.
type SMTP struct {
	cfg SMTPConfig
}

// NewSMTP validates the configuration and returns a sender.
func NewSMTP(cfg SMTPConfig) (*SMTP, error) {
	if cfg.Host == "" {
		return nil, errors.New("deliver: smtp host is empty")
	}
	if !strings.Contains(cfg.Host, ":") {
		return nil, fmt.Errorf("deliver: smtp host %q needs a port, e.g. %s:587", cfg.Host, cfg.Host)
	}
	if err := checkHeaderSafe("from address", cfg.From); err != nil {
		return nil, err
	}
	return &SMTP{cfg: cfg}, nil
}

func (s *SMTP) Name() string { return "smtp" }

func (s *SMTP) Send(to []string, m render.Message) error {
	msg, err := Compose(s.cfg.From, to, m, time.Now())
	if err != nil {
		return err
	}
	var auth smtp.Auth
	if s.cfg.Username != "" {
		host := s.cfg.Host[:strings.LastIndex(s.cfg.Host, ":")]
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, host)
	}
	if err := smtp.SendMail(s.cfg.Host, auth, s.cfg.From, to, msg); err != nil {
		return fmt.Errorf("deliver: smtp send: %w", err)
	}
	return nil
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
