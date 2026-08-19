// Package server implements the gRPC server for the mail service.
//
// This package provides the MailServer and HealthServer implementations that handle
// email sending operations via SMTP. It supports both direct email sending and
// template-based emails with variable substitution.
//
// # Endpoint Security
//
// Endpoints are registered with different security levels in init():
//   - Admin: Send, SendTemplate (for authenticated admin users)
//   - ServiceClient: Send, SendTemplate with "email:send" scope (for
//     service-to-service calls, e.g. from authservice)

package server

import (
	"context"
	htmlTemp "html/template"
	"net"
	"strconv"
	"sync"
	txtTemp "text/template"
	"time"

	log "github.com/swayrider/swlib/logger"
	"github.com/swayrider/swlib/security"

	healthv1 "github.com/swayrider/protos/health/v1"
	mailv1 "github.com/swayrider/protos/mail/v1"
)

// MailSender is the interface used by MailServer to deliver emails.
// *mail.Mailer satisfies this interface.
type MailSender interface {
	Send(ctx context.Context, from string, to []string, cc []string, bcc []string, subject string, htmlBody string, textBody string) error
}

func init() {
	security.AdminEndpoint("/mail.v1.MailService/Send")
	security.ServiceClientEndpoint("/mail.v1.MailService/Send", []string{
		"email:send",
	})

	security.AdminEndpoint("/mail.v1.MailService/SendTemplate")
	security.ServiceClientEndpoint("/mail.v1.MailService/SendTemplate", []string{
		"email:send",
	})

	security.PublicEndpoint("/health.v1.HealthService/Ping")
	security.PublicEndpoint("/health.v1.HealthService/Check")
}

// MailServer implements the MailService gRPC interface.
// It combines a filesystem templates directory and an SMTP mailer for delivery.
type MailServer struct {
	mailv1.UnimplementedMailServiceServer
	templatesDir       string      // Directory path for email templates
	mailer             MailSender  // SMTP mailer for email delivery
	allowedFromDomains []string    // Domains permitted in the request From address
	l                  *log.Logger // Logger instance

	htmlTemplates *templateCache[*htmlTemp.Template] // Cache of parsed HTML templates, keyed by name+mtime
	txtTemplates  *templateCache[*txtTemp.Template]  // Cache of parsed text templates, keyed by name+mtime
}

// HealthServer implements the HealthService gRPC interface for health checks.
//
// Check() probes SMTP reachability rather than reporting UP unconditionally,
// caching the result for probeTTL to avoid dialing the SMTP server on every
// orchestrator health check.
type HealthServer struct {
	healthv1.UnimplementedHealthServiceServer
	smtpAddr string        // SMTP server host:port
	probeTTL time.Duration // How long a probe result is reused before re-probing
	l        *log.Logger   // Logger instance

	mu        sync.Mutex
	lastCheck time.Time
	lastUp    bool
}

// NewMailServer creates a new MailServer with the given dependencies.
//
// allowedFromDomains restricts the domain of the request-supplied From
// address; a request From outside this list is rejected. It has no effect
// on an empty From, which falls back to the mailer's configured user.
func NewMailServer(
	templatesDir string,
	mailer MailSender,
	allowedFromDomains []string,
	l *log.Logger,
) *MailServer {
	return &MailServer{
		templatesDir:       templatesDir,
		mailer:             mailer,
		allowedFromDomains: allowedFromDomains,
		htmlTemplates: newTemplateCache(templatesDir, func(b []byte) (*htmlTemp.Template, error) {
			return htmlTemp.New("").Parse(string(b))
		}),
		txtTemplates: newTemplateCache(templatesDir, func(b []byte) (*txtTemp.Template, error) {
			return txtTemp.New("").Parse(string(b))
		}),
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

// NewHealthServer creates a new HealthServer that probes the SMTP server at
// smtpHost:smtpPort, caching the result for probeTTL.
func NewHealthServer(smtpHost string, smtpPort int, probeTTL time.Duration, l *log.Logger) *HealthServer {
	return &HealthServer{
		smtpAddr: net.JoinHostPort(smtpHost, strconv.Itoa(smtpPort)),
		probeTTL: probeTTL,
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
