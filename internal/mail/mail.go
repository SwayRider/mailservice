// Package mail provides SMTP email sending functionality.
//
// This package wraps the jordan-wright/email library to provide a simple
// interface for sending multipart emails (HTML + text) via SMTP with
// PlainAuth authentication. The connection is always upgraded via STARTTLS
// before authenticating; servers that don't advertise STARTTLS are refused
// rather than falling back to a plaintext channel.

package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	netmail "net/mail"
	"net/smtp"
	"time"

	"github.com/jordan-wright/email"
)

// defaultDialTimeout bounds how long connecting to the SMTP server may take.
const defaultDialTimeout = 5 * time.Second

// defaultOverallTimeout bounds the entire SMTP transaction (EHLO, STARTTLS,
// AUTH, RCPT, DATA) once connected, since net/smtp has no per-step context
// support and a hung server could otherwise block a goroutine indefinitely.
const defaultOverallTimeout = 30 * time.Second

// Mailer holds SMTP connection configuration for sending emails.
type Mailer struct {
	User           string        // SMTP username (also used as default From address)
	Password       string        // SMTP password
	Host           string        // SMTP server hostname
	Port           int           // SMTP server port (typically 587 for TLS)
	DialTimeout    time.Duration // Timeout for establishing the TCP connection
	OverallTimeout time.Duration // Timeout for the entire SMTP transaction once connected
}

// NewMailer creates a new Mailer with the given SMTP configuration.
//
// dialTimeout and overallTimeout fall back to sane defaults when zero.
func NewMailer(
	user string,
	password string,
	host string,
	port int,
	dialTimeout time.Duration,
	overallTimeout time.Duration,
) *Mailer {
	if dialTimeout == 0 {
		dialTimeout = defaultDialTimeout
	}
	if overallTimeout == 0 {
		overallTimeout = defaultOverallTimeout
	}
	return &Mailer{
		User:           user,
		Password:       password,
		Host:           host,
		Port:           port,
		DialTimeout:    dialTimeout,
		OverallTimeout: overallTimeout,
	}
}

// Send delivers an email via SMTP.
//
// If the from address is empty, the SMTP user is used as the sender.
// Both HTML and text bodies are included for multipart email support.
//
// The connection is upgraded via STARTTLS before the SMTP credentials are
// sent; if the server doesn't advertise STARTTLS, Send fails rather than
// authenticating over a plaintext channel. Connecting honors ctx; the
// transaction as a whole is bounded by m.OverallTimeout.
func (m *Mailer) Send(
	ctx context.Context,
	from string,
	to []string,
	cc []string,
	bcc []string,
	subject string,
	htmlBody string,
	textBody string,
) error {
	e := email.NewEmail()
	if from != "" {
		e.From = from
	} else {
		e.From = m.User
	}
	e.To = to
	e.Cc = cc
	e.Bcc = bcc
	e.Subject = subject
	e.HTML = []byte(htmlBody)
	e.Text = []byte(textBody)

	return m.sendWithMandatorySTARTTLS(ctx, e)
}

// sendWithMandatorySTARTTLS delivers e over a connection that has been
// upgraded via STARTTLS.
//
// email.Email.SendWithStartTLS silently falls back to a plaintext channel
// when the server doesn't advertise STARTTLS, which would leak the SMTP
// credentials to an active on-path attacker. This performs the equivalent
// SMTP transaction (adapted from email.Email.SendWithStartTLS, itself
// adapted from net/smtp.SendMail) but refuses to authenticate or send
// unless the STARTTLS upgrade succeeds first.
//
// Connecting honors ctx (bounded additionally by m.DialTimeout); once
// connected, the whole transaction is bounded by m.OverallTimeout via a
// deadline on the raw connection, since net/smtp has no per-step context
// support and a hung server could otherwise block indefinitely.
func (m *Mailer) sendWithMandatorySTARTTLS(ctx context.Context, e *email.Email) error {
	sender, err := netmail.ParseAddress(e.From)
	if err != nil {
		return fmt.Errorf("parse from address: %w", err)
	}
	recipients, err := mergeAndValidateRecipients(e)
	if err != nil {
		return err
	}
	raw, err := e.Bytes()
	if err != nil {
		return fmt.Errorf("build message: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", m.Host, m.Port)
	dialer := &net.Dialer{Timeout: m.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smtp server: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(m.OverallTimeout)); err != nil {
		conn.Close()
		return fmt.Errorf("set smtp connection deadline: %w", err)
	}

	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("init smtp client: %w", err)
	}
	defer c.Close()

	if err := c.Hello("localhost"); err != nil {
		return fmt.Errorf("smtp hello: %w", err)
	}
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return fmt.Errorf(
			"smtp server %s does not advertise STARTTLS; refusing to send credentials over a plaintext channel",
			m.Host)
	}
	if err := c.StartTLS(&tls.Config{ServerName: m.Host}); err != nil {
		return fmt.Errorf("smtp starttls: %w", err)
	}

	auth := smtp.PlainAuth("", m.User, m.Password, m.Host)
	if ok, _ := c.Extension("AUTH"); ok {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := c.Mail(sender.Address); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, rcpt := range recipients {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt to %s: %w", rcpt, err)
		}
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data close: %w", err)
	}
	return c.Quit()
}

// mergeAndValidateRecipients merges the To, Cc, and Bcc address lists and
// validates each address, matching the address handling in email.Email.Send.
func mergeAndValidateRecipients(e *email.Email) ([]string, error) {
	to := make([]string, 0, len(e.To)+len(e.Cc)+len(e.Bcc))
	to = append(append(append(to, e.To...), e.Cc...), e.Bcc...)
	for i, addr := range to {
		parsed, err := netmail.ParseAddress(addr)
		if err != nil {
			return nil, err
		}
		to[i] = parsed.Address
	}
	if len(to) == 0 {
		return nil, fmt.Errorf("must specify at least one recipient")
	}
	return to, nil
}
