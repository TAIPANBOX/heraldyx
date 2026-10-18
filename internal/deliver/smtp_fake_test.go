package deliver

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRelay is a mail server small enough to read. It speaks the dialogue a
// submission client needs and records the message bodies it is given, so the
// transport is tested against something that really speaks SMTP. The unit
// tests of Compose cannot tell us whether a server receives anything at all.
type fakeRelay struct {
	ln   net.Listener
	opts relayOpts
	cert tls.Certificate

	mu       sync.Mutex
	messages []string
}

type relayOpts struct {
	implicit   bool // TLS from the first byte, as port 465 is spoken
	starttls   bool // offers STARTTLS and performs it
	silent     bool // accepts the connection and never speaks
	dropOnQuit bool // takes the message, then closes the connection instead of answering QUIT
}

func newFakeRelay(t *testing.T, o relayOpts) *fakeRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	r := &fakeRelay{ln: ln, opts: o, cert: testCert(t)}
	go r.serve()
	return r
}

func (r *fakeRelay) addr() string { return r.ln.Addr().String() }

// trust returns a pool holding only this relay's certificate, for the
// Verify path of a client that is told to trust it.
func (r *fakeRelay) trust() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(r.cert.Leaf)
	return pool
}

// received returns the message bodies the relay accepted, in arrival order.
func (r *fakeRelay) received() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.messages...)
}

func (r *fakeRelay) serve() {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return
		}
		go r.session(conn)
	}
}

func (r *fakeRelay) session(conn net.Conn) {
	defer conn.Close()
	if r.opts.silent {
		// Hold the connection and say nothing until the client gives up.
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	if r.opts.implicit {
		tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{r.cert}})
		if err := tc.Handshake(); err != nil {
			return
		}
		conn = tc
	}
	r.dialogue(conn)
}

func (r *fakeRelay) dialogue(conn net.Conn) {
	br := bufio.NewReader(conn)
	tlsUp := false
	// Write errors are not reported: a client that has gone away is the
	// client's failure, and the test asserts on what the client did.
	say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	say("220 relay.test ESMTP")
	for {
		raw, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line := strings.TrimRight(raw, "\r\n")
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
			if r.opts.starttls && !tlsUp {
				say("250-relay.test")
				say("250 STARTTLS")
			} else {
				say("250 relay.test")
			}
		case up == "STARTTLS":
			if !r.opts.starttls || tlsUp {
				say("502 5.5.1 command not implemented")
				continue
			}
			say("220 2.0.0 ready to start TLS")
			tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{r.cert}})
			if err := tc.Handshake(); err != nil {
				return
			}
			conn = tc
			br = bufio.NewReader(conn)
			tlsUp = true
		case strings.HasPrefix(up, "MAIL FROM"), strings.HasPrefix(up, "RCPT TO"):
			say("250 2.1.0 ok")
		case up == "DATA":
			say("354 end data with <CR><LF>.<CR><LF>")
			var body strings.Builder
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(l, "\r\n") == "." {
					break
				}
				body.WriteString(l)
			}
			r.mu.Lock()
			r.messages = append(r.messages, body.String())
			r.mu.Unlock()
			say("250 2.0.0 ok: queued")
		case up == "QUIT":
			if r.opts.dropOnQuit {
				return
			}
			say("221 2.0.0 bye")
			return
		default:
			say("250 2.0.0 ok")
		}
	}
}

// testCert makes a self-signed certificate for 127.0.0.1 and localhost. It is
// never trusted by the system roots, so a client that verifies refuses it
// unless the test hands it the pool from trust().
func testCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "relay.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}
