// Package server implements the gRPC server for the mail service.
//
// This package provides the MailServer and HealthServer implementations that handle
// email sending operations via SMTP. It supports both direct email sending and
// template-based emails with variable substitution.
//
// # Endpoint Security
//
// Endpoints are registered with different security levels in init():
//   - Public: SendInternal, SendTemplateInternal (for service-to-service calls)
//   - Admin: Send, SendTemplate (for authenticated admin users)
//   - ServiceClient: Send, SendTemplate with "email:send" scope

package server

import (
	log "github.com/swayrider/swlib/logger"
	"github.com/swayrider/swlib/security"

	healthv1 "github.com/swayrider/protos/health/v1"
	mailv1 "github.com/swayrider/protos/mail/v1"
)

// MailSender is the interface used by MailServer to deliver emails.
// *mail.Mailer satisfies this interface.
type MailSender interface {
	Send(from string, to []string, cc []string, bcc []string, subject string, htmlBody string, textBody string) error
}

func init() {
	security.AdminEndpoint("/mail.v1.MailService/Send")
	security.ServiceClientEndpoint("/mail.v1.MailService/Send", []string{
		"email:send",
	})
	security.PublicEndpoint("/mail.v1.MailService/SendInternal")

	security.AdminEndpoint("/mail.v1.MailService/SendTemplate")
	security.ServiceClientEndpoint("/mail.v1.MailService/SendTemplate", []string{
		"email:send",
	})
	security.PublicEndpoint("/mail.v1.MailService/SendTemplateInternal")

	security.PublicEndpoint("/health.v1.HealthService/Ping")
	security.PublicEndpoint("/health.v1.HealthService/Check")
}

// MailServer implements the MailService gRPC interface.
// It combines a filesystem templates directory and an SMTP mailer for delivery.
type MailServer struct {
	mailv1.UnimplementedMailServiceServer
	templatesDir string     // Directory path for email templates
	mailer       MailSender // SMTP mailer for email delivery
	l            *log.Logger // Logger instance
}

// HealthServer implements the HealthService gRPC interface for health checks.
type HealthServer struct {
	healthv1.UnimplementedHealthServiceServer
	l *log.Logger // Logger instance
}

// NewMailServer creates a new MailServer with the given dependencies.
func NewMailServer(
	templatesDir string,
	mailer MailSender,
	l *log.Logger,
) *MailServer {
	return &MailServer{
		templatesDir: templatesDir,
		mailer:       mailer,
		l: l.Derive(
			log.WithComponent("MailServer"),
			log.WithFunction("NewMailServer"),
		),
	}
}

// TemplatesDir returns the directory path for email templates.
func (s *MailServer) TemplatesDir() string {
	return s.templatesDir
}

// Mailer returns the SMTP mailer instance.
func (s *MailServer) Mailer() MailSender {
	return s.mailer
}

// Logger returns the logger instance.
func (s *MailServer) Logger() *log.Logger {
	return s.l
}

// NewHealthServer creates a new HealthServer with the given logger.
func NewHealthServer(l *log.Logger) *HealthServer {
	return &HealthServer{
		l: l.Derive(
			log.WithComponent("HealthServer"),
			log.WithFunction("NewHealthServer"),
		),
	}
}

// Logger returns the logger instance.
func (s *HealthServer) Logger() *log.Logger {
	return s.l
}
