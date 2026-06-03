package mail

import (
	"net"
	"testing"
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
	l.Close()

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
	l.Close()

	m := NewMailer("default@example.com", "password", "127.0.0.1", port)
	// from="" triggers the fallback to m.User inside Send.
	err = m.Send("", []string{"to@example.com"}, nil, nil, "subject", "<b>hello</b>", "hello")
	if err == nil {
		t.Error("expected SMTP error, got nil")
	}
}
