// send.go implements the email sending endpoints.
//
// This file provides two types of email sending:
//   - Direct sending (Send): Sends raw HTML and text content
//   - Template-based sending (SendTemplate): Reads templates from the local
//     filesystem and substitutes variables before sending
//
// Both RPCs require a service-client JWT with the "email:send" scope; see
// security.ServiceClientEndpoint registration in server.go.

package server

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"

	mailv1 "github.com/swayrider/protos/mail/v1"
	log "github.com/swayrider/swlib/logger"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// templateNameRe restricts template names to flat basenames: an alphanumeric
// first character followed by alphanumerics, underscore, dot, or hyphen. This
// blocks path separators, leading dots, and empty names.
var templateNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// validTemplateName reports whether name is safe to join onto the templates
// directory. Real template names are always flat basenames (e.g.
// "verify_user.html"), so anything containing a path separator or ".." is
// rejected outright to prevent path traversal.
func validTemplateName(name string) bool {
	return templateNameRe.MatchString(name) && !strings.Contains(name, "..")
}

// SendTemplate sends an email using templates read from the local filesystem.
//
// The method:
//  1. Reads HTML and text templates from the configured templates directory
//  2. Parses them as Go templates
//  3. Executes templates with the provided data map
//  4. Sends the resulting email via SMTP
//
// Returns:
//   - codes.InvalidArgument: Invalid template name, from address, or recipients
//   - codes.NotFound: Template not found
//   - codes.Internal: Template parsing, execution, or SMTP error
func (s *MailServer) SendTemplate(
	ctx context.Context,
	req *mailv1.SendTemplateRequest,
) (*mailv1.SendTemplateResponse, error) {
	lg := s.Logger().Derive(log.WithFunction("SendTemplate"))

	if !validTemplateName(req.HtmlTemplate) {
		lg.Errorf("invalid HTML template name: %q", req.HtmlTemplate)
		return nil, status.Error(codes.InvalidArgument, "invalid template name")
	}
	if !validTemplateName(req.TextTemplate) {
		lg.Errorf("invalid text template name: %q", req.TextTemplate)
		return nil, status.Error(codes.InvalidArgument, "invalid template name")
	}

	if err := validateFrom(req.From, s.allowedFromDomains); err != nil {
		lg.Errorf("rejected from address: %v", err)
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	if err := validateRecipients(req.To, req.Cc, req.Bcc); err != nil {
		lg.Errorf("rejected recipients: %v", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	htmlTmpl, err := s.htmlTemplates.get(req.HtmlTemplate)
	if err != nil {
		lg.Errorf("failed to load HTML template %s, error: %v", req.HtmlTemplate, err)
		if _, ok := errors.AsType[*templateAccessError](err); ok {
			return nil, status.Error(codes.NotFound, "template not found")
		}
		return nil, status.Error(codes.Internal, "failed to parse template")
	}

	txtTmpl, err := s.txtTemplates.get(req.TextTemplate)
	if err != nil {
		lg.Errorf("failed to load text template %s, error: %v", req.TextTemplate, err)
		if _, ok := errors.AsType[*templateAccessError](err); ok {
			return nil, status.Error(codes.NotFound, "template not found")
		}
		return nil, status.Error(codes.Internal, "failed to parse template")
	}

	var htmlBuf, txtBuf bytes.Buffer
	if err := htmlTmpl.Execute(&htmlBuf, req.Data); err != nil {
		lg.Errorf("failed to execute template %s, error: %v", req.HtmlTemplate, err)
		return nil, status.Error(codes.Internal, "failed to render template")
	}
	if err := txtTmpl.Execute(&txtBuf, req.Data); err != nil {
		lg.Errorf("failed to execute template %s, error: %v", req.TextTemplate, err)
		return nil, status.Error(codes.Internal, "failed to render template")
	}

	if err := s.Mailer().Send(
		ctx, req.From, req.To, req.Cc, req.Bcc, req.Subject,
		htmlBuf.String(), txtBuf.String(),
	); err != nil {
		lg.Errorf("failed to send email, error: %v", err)
		return nil, status.Error(codes.Internal, "failed to send email")
	}

	return &mailv1.SendTemplateResponse{
		Message: "email sent",
	}, nil
}

// Send sends an email with the provided HTML and text content directly.
//
// Returns:
//   - codes.Internal: SMTP error during sending
func (s *MailServer) Send(
	ctx context.Context,
	req *mailv1.SendRequest,
) (*mailv1.SendResponse, error) {
	lg := s.Logger().Derive(log.WithFunction("Send"))

	if err := validateFrom(req.From, s.allowedFromDomains); err != nil {
		lg.Errorf("rejected from address: %v", err)
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	if err := validateRecipients(req.To, req.Cc, req.Bcc); err != nil {
		lg.Errorf("rejected recipients: %v", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err := s.Mailer().Send(
		ctx, req.From, req.To, req.Cc, req.Bcc, req.Subject,
		req.HtmlBody, req.TextBody,
	); err != nil {
		lg.Errorf("failed to send email, error: %v", err)
		return nil, status.Error(codes.Internal, "failed to send email")
	}

	return &mailv1.SendResponse{
		Message: "email sent",
	}, nil
}
