package mail

import (
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// NewMailer Tests
// =============================================================================

func TestNewMailer(t *testing.T) {
	m := NewMailer("user@example.com", "secret", "smtp.example.com", 587)

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

	m := NewMailer("user@example.com", "password", "127.0.0.1", port)
	err = m.Send("", []string{"to@example.com"}, nil, nil, "subject", "<b>hello</b>", "hello")
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

	m := NewMailer("default@example.com", "password", "127.0.0.1", port)
	// from="" triggers the fallback to m.User inside Send.
	err = m.Send("", []string{"to@example.com"}, nil, nil, "subject", "<b>hello</b>", "hello")
	if err == nil {
		t.Error("expected SMTP error, got nil")
	}
}

func TestSend_RefusesWhenServerDoesNotAdvertiseSTARTTLS(t *testing.T) {
	host, port, authSeen := startFakeSMTPServer(t, false /* advertiseStartTLS */)

	m := NewMailer("user@example.com", "password", host, port)
	err := m.Send("", []string{"to@example.com"}, nil, nil, "subject", "<b>hello</b>", "hello")
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
