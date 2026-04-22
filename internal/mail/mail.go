// Package mail provides SMTP email sending functionality.
//
// This package wraps the jordan-wright/email library to provide a simple
// interface for sending multipart emails (HTML + text) via SMTP with
// PlainAuth authentication.

package mail

import (
	"fmt"
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

	err := e.Send(
		fmt.Sprintf("%s:%d", m.Host, m.Port),
		smtp.PlainAuth("", m.User, m.Password, m.Host))
	return err
}
