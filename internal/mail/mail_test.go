package mail

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	netmail "net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// NewMailer Tests
// =============================================================================

func TestNewMailer(t *testing.T) {
	m := NewMailer("user@example.com", "secret", "smtp.example.com", 587, 2*time.Second, 10*time.Second)

	if m.User != "user@example.com" {
		t.Errorf("User = %q, want %q", m.User, "user@example.com")
	}
	if m.Password != "secret" {
		t.Errorf("Password = %q, want %q", m.Password, "secret")
	}
	if m.Host != "smtp.example.com" {
		t.Errorf("Host = %q, want %q", m.Host, "smtp.example.com")
	}
	if m.Port != 587 {
		t.Errorf("Port = %d, want %d", m.Port, 587)
	}
	if m.DialTimeout != 2*time.Second {
		t.Errorf("DialTimeout = %v, want %v", m.DialTimeout, 2*time.Second)
	}
	if m.OverallTimeout != 10*time.Second {
		t.Errorf("OverallTimeout = %v, want %v", m.OverallTimeout, 10*time.Second)
	}
}

func TestNewMailer_DefaultsWhenZero(t *testing.T) {
	m := NewMailer("user@example.com", "secret", "smtp.example.com", 587, 0, 0)

	if m.DialTimeout != defaultDialTimeout {
		t.Errorf("DialTimeout = %v, want default %v", m.DialTimeout, defaultDialTimeout)
	}
	if m.OverallTimeout != defaultOverallTimeout {
		t.Errorf("OverallTimeout = %v, want default %v", m.OverallTimeout, defaultOverallTimeout)
	}
}

// =============================================================================
// Send Tests
// =============================================================================

func TestSend_SMTPUnavailable(t *testing.T) {
	// Allocate a free port, then close the listener to guarantee connection refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate test port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("failed to close test listener: %v", err)
	}

	m := NewMailer("user@example.com", "password", "127.0.0.1", port, 0, 0)
	err = m.Send(context.Background(), "", []string{"to@example.com"}, nil, nil, "subject", "<b>hello</b>", "hello")
	if err == nil {
		t.Error("expected error sending to unavailable SMTP server, got nil")
	}
}

func TestSend_EmptyFromFallsBackToUser(t *testing.T) {
	// When from is empty, the Mailer.User should be used as the sender.
	// We verify the code path executes without panic even when SMTP is unavailable.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate test port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("failed to close test listener: %v", err)
	}

	m := NewMailer("default@example.com", "password", "127.0.0.1", port, 0, 0)
	// from="" triggers the fallback to m.User inside Send.
	err = m.Send(context.Background(), "", []string{"to@example.com"}, nil, nil, "subject", "<b>hello</b>", "hello")
	if err == nil {
		t.Error("expected SMTP error, got nil")
	}
}

func TestSend_RefusesWhenServerDoesNotAdvertiseSTARTTLS(t *testing.T) {
	host, port, authSeen := startFakeSMTPServer(t, false /* advertiseStartTLS */)

	m := NewMailer("user@example.com", "password", host, port, 0, 0)
	err := m.Send(context.Background(), "", []string{"to@example.com"}, nil, nil, "subject", "<b>hello</b>", "hello")
	if err == nil {
		t.Fatal("expected error when server does not advertise STARTTLS, got nil")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("expected error to mention STARTTLS, got: %v", err)
	}

	select {
	case cmd := <-authSeen:
		t.Errorf("AUTH command sent despite missing STARTTLS support: %q", cmd)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSend_RespectsCanceledContext(t *testing.T) {
	host, port, _ := startFakeSMTPServer(t, true /* advertiseStartTLS */)

	m := NewMailer("user@example.com", "password", host, port, 0, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := m.Send(ctx, "", []string{"to@example.com"}, nil, nil, "subject", "<b>hello</b>", "hello")
	if err == nil {
		t.Fatal("expected error when context is already canceled, got nil")
	}
}

func TestSend_RespectsOverallTimeout(t *testing.T) {
	// A server that accepts the connection but never writes a greeting, so
	// the client blocks reading it -- the overall connection deadline is
	// the only thing that can unblock Send.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-make(chan struct{}) // block forever, holding the connection open
	}()

	addr := l.Addr().(*net.TCPAddr)
	m := NewMailer("user@example.com", "password", "127.0.0.1", addr.Port, time.Second, 50*time.Millisecond)

	start := time.Now()
	err = m.Send(context.Background(), "", []string{"to@example.com"}, nil, nil, "subject", "<b>hello</b>", "hello")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Send took %v, expected it to be bounded by the overall timeout", elapsed)
	}
}

// =============================================================================
// buildEmail Tests -- message construction is pure and network-free, so it
// can be verified directly without a live (or fake) SMTP server.
// =============================================================================

func TestBuildEmail_DefaultsFromToUserWhenEmpty(t *testing.T) {
	m := NewMailer("default@example.com", "secret", "smtp.example.com", 587, 0, 0)

	e := m.buildEmail("", []string{"to@example.com"}, nil, nil, "subject", "<b>hi</b>", "hi")
	if e.From != "default@example.com" {
		t.Errorf("From = %q, want %q", e.From, "default@example.com")
	}
}

func TestBuildEmail_ProducesWellFormedMessage(t *testing.T) {
	m := NewMailer("user@example.com", "secret", "smtp.example.com", 587, 0, 0)

	e := m.buildEmail(
		"sender@example.com",
		[]string{"to@example.com"},
		[]string{"cc@example.com"},
		[]string{"bcc@example.com"},
		"Hello Wörld",
		"<p>hi</p>",
		"hi",
	)

	raw, err := e.Bytes()
	if err != nil {
		t.Fatalf("Bytes() returned error: %v", err)
	}

	parsed, err := netmail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("constructed message is not a valid RFC 5322 message: %v", err)
	}
	requireHeaderAddress(t, parsed.Header, "From", "sender@example.com")
	requireHeaderAddress(t, parsed.Header, "To", "to@example.com")
	requireHeaderAddress(t, parsed.Header, "Cc", "cc@example.com")
	if parsed.Header.Get("Bcc") != "" {
		t.Errorf("Bcc header must be absent from the message, got %q", parsed.Header.Get("Bcc"))
	}
	if raw := string(raw); strings.Contains(raw, "bcc@example.com") {
		t.Errorf("raw message must not reveal the Bcc recipient anywhere: %s", raw)
	}
	// Non-ASCII subjects must be RFC 2047 encoded rather than sent raw.
	if got := parsed.Header.Get("Subject"); !strings.HasPrefix(got, "=?") {
		t.Errorf("Subject header = %q, want RFC 2047 encoded (starting with \"=?\")", got)
	}
	if !strings.Contains(string(raw), "Content-Type: multipart/alternative") {
		t.Error("expected a multipart/alternative message for HTML+text bodies")
	}
}

// requireHeaderAddress asserts that header contains exactly one address
// whose address part (ignoring any display-name/angle-bracket formatting the
// library applies) equals want.
func requireHeaderAddress(t *testing.T, header netmail.Header, headerName, want string) {
	t.Helper()
	got := header.Get(headerName)
	addr, err := netmail.ParseAddress(got)
	if err != nil {
		t.Errorf("%s header = %q is not a parseable address: %v", headerName, got, err)
		return
	}
	if addr.Address != want {
		t.Errorf("%s header address = %q, want %q", headerName, addr.Address, want)
	}
}

// =============================================================================
// Full successful STARTTLS transaction test
// =============================================================================

// smtpTransaction captures what a fake SMTP server observed during one
// client session.
type smtpTransaction struct {
	authLine string
	mailFrom string
	rcptTo   []string
	data     []byte
}

func TestSend_SuccessfulSTARTTLSTransaction(t *testing.T) {
	host, port, pool, txCh := startFakeTLSSMTPServer(t)

	m := NewMailer("user@example.com", "secret", host, port, 2*time.Second, 5*time.Second)
	m.rootCAs = pool

	err := m.Send(
		context.Background(),
		"sender@example.com",
		[]string{"to@example.com"},
		[]string{"cc@example.com"},
		[]string{"bcc@example.com"},
		"Hello",
		"<b>hello</b>",
		"hello",
	)
	if err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	select {
	case tx := <-txCh:
		if tx.authLine == "" {
			t.Error("expected an AUTH command after a successful STARTTLS upgrade")
		}
		if !strings.Contains(tx.mailFrom, "sender@example.com") {
			t.Errorf("MAIL FROM command = %q, want to contain %q", tx.mailFrom, "sender@example.com")
		}
		joinedRcpt := strings.Join(tx.rcptTo, " ")
		for _, want := range []string{"to@example.com", "cc@example.com", "bcc@example.com"} {
			if !strings.Contains(joinedRcpt, want) {
				t.Errorf("RCPT TO commands %v missing %q (Bcc must still be in the envelope even though not in headers)", tx.rcptTo, want)
			}
		}
		data := string(tx.data)
		if !strings.Contains(data, "To: <to@example.com>") {
			t.Errorf("message body missing To header, got: %s", data)
		}
		if strings.Contains(data, "bcc@example.com") {
			t.Errorf("message body must not reveal the Bcc recipient: %s", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the fake SMTP server to observe the transaction")
	}
}

// startFakeTLSSMTPServer starts a minimal SMTP server on 127.0.0.1 that
// advertises and performs a real STARTTLS upgrade (using an ephemeral
// self-signed certificate), accepts AUTH/MAIL/RCPT/DATA/QUIT over the
// resulting TLS connection, and reports the observed transaction on the
// returned channel once the client disconnects.
func startFakeTLSSMTPServer(t *testing.T) (host string, port int, pool *x509.CertPool, txCh <-chan smtpTransaction) {
	t.Helper()

	cert := generateTestTLSCert(t)
	pool = x509.NewCertPool()
	pool.AddCert(cert.Leaf)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	ch := make(chan smtpTransaction, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		var cur net.Conn = conn
		tp := textproto.NewConn(conn)
		if err := tp.PrintfLine("220 fake.smtp ESMTP"); err != nil {
			return
		}

		var tx smtpTransaction
		for {
			line, err := tp.ReadLine()
			if err != nil {
				return
			}
			upper := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(upper, "EHLO"):
				tp.PrintfLine("250-fake.smtp greets you")
				if _, isTLS := cur.(*tls.Conn); !isTLS {
					tp.PrintfLine("250-STARTTLS")
				}
				tp.PrintfLine("250 AUTH PLAIN")
			case strings.HasPrefix(upper, "STARTTLS"):
				if err := tp.PrintfLine("220 2.0.0 Ready to start TLS"); err != nil {
					return
				}
				tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
				if err := tlsConn.Handshake(); err != nil {
					return
				}
				cur = tlsConn
				tp = textproto.NewConn(tlsConn)
			case strings.HasPrefix(upper, "AUTH"):
				tx.authLine = line
				tp.PrintfLine("235 Authentication successful")
			case strings.HasPrefix(upper, "MAIL FROM"):
				tx.mailFrom = line
				tp.PrintfLine("250 OK")
			case strings.HasPrefix(upper, "RCPT TO"):
				tx.rcptTo = append(tx.rcptTo, line)
				tp.PrintfLine("250 OK")
			case strings.HasPrefix(upper, "DATA"):
				if err := tp.PrintfLine("354 Start mail input; end with <CRLF>.<CRLF>"); err != nil {
					return
				}
				data, err := io.ReadAll(tp.DotReader())
				if err != nil {
					return
				}
				tx.data = data
				tp.PrintfLine("250 OK: queued")
			case strings.HasPrefix(upper, "QUIT"):
				tp.PrintfLine("221 Bye")
				select {
				case ch <- tx:
				default:
				}
				return
			default:
				tp.PrintfLine("500 unrecognized command")
			}
		}
	}()

	addr := l.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, pool, ch
}

// generateTestTLSCert creates an ephemeral, self-signed certificate valid
// for 127.0.0.1, used by startFakeTLSSMTPServer to perform a real STARTTLS
// handshake in-process.
func generateTestTLSCert(t *testing.T) tls.Certificate {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("failed to parse generated certificate: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
		Leaf:        leaf,
	}
}

// startFakeSMTPServer starts a minimal SMTP server on 127.0.0.1 that greets
// the client and answers EHLO, optionally advertising STARTTLS. It reports
// any AUTH command it receives on the returned channel, letting tests assert
// that credentials were never sent. The server is stopped via t.Cleanup.
func startFakeSMTPServer(t *testing.T, advertiseStartTLS bool) (host string, port int, authSeen <-chan string) {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	seen := make(chan string, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		tp := textproto.NewConn(conn)
		if err := tp.PrintfLine("220 fake.smtp ESMTP"); err != nil {
			return
		}
		for {
			line, err := tp.ReadLine()
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(strings.ToUpper(line), "EHLO"):
				tp.PrintfLine("250-fake.smtp greets you")
				if advertiseStartTLS {
					tp.PrintfLine("250-STARTTLS")
				}
				tp.PrintfLine("250 AUTH PLAIN")
			case strings.HasPrefix(strings.ToUpper(line), "AUTH"):
				select {
				case seen <- line:
				default:
				}
				tp.PrintfLine("235 Authentication successful")
			case strings.HasPrefix(strings.ToUpper(line), "QUIT"):
				tp.PrintfLine("221 Bye")
				return
			default:
				tp.PrintfLine("500 unrecognized command")
			}
		}
	}()

	addr := l.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, seen
}
