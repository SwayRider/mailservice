// Package mail provides SMTP email sending functionality.
//
// This package wraps the jordan-wright/email library to provide a simple
// interface for sending multipart emails (HTML + text) via SMTP with
// PlainAuth authentication. The connection is always upgraded via STARTTLS
// before authenticating; servers that don't advertise STARTTLS are refused
// rather than falling back to a plaintext channel.

package mail

import (
	"crypto/tls"
	"fmt"
	netmail "net/mail"
	"net/smtp"

	"github.com/jordan-wright/email"
)

// Mailer holds SMTP connection configuration for sending emails.
type Mailer struct {
	User     string // SMTP username (also used as default From address)
	Password string // SMTP password
	Host     string // SMTP server hostname
	Port     int    // SMTP server port (typically 587 for TLS)
}

// NewMailer creates a new Mailer with the given SMTP configuration.
func NewMailer(
	user string,
	password string,
	host string,
	port int,
) *Mailer {
	return &Mailer{
		User:     user,
		Password: password,
		Host:     host,
		Port:     port,
	}
}

// Send delivers an email via SMTP.
//
// If the from address is empty, the SMTP user is used as the sender.
// Both HTML and text bodies are included for multipart email support.
//
// The connection is upgraded via STARTTLS before the SMTP credentials are
// sent; if the server doesn't advertise STARTTLS, Send fails rather than
// authenticating over a plaintext channel.
func (m *Mailer) Send(
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

	return m.sendWithMandatorySTARTTLS(e)
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
func (m *Mailer) sendWithMandatorySTARTTLS(e *email.Email) error {
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
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial smtp server: %w", err)
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
