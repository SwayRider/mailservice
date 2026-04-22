// send.go implements the email sending endpoints.
//
// This file provides two types of email sending:
//   - Direct sending (Send): Sends raw HTML and text content
//   - Template-based sending (SendTemplate): Reads templates from the local
//     filesystem and substitutes variables before sending
//
// Each method has an "Internal" variant for service-to-service calls that
// bypasses authentication.

package server

import (
	"bytes"
	"context"
	htmlTemp "html/template"
	"os"
	"path/filepath"
	txtTemp "text/template"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mailv1 "github.com/swayrider/protos/mail/v1"
	log "github.com/swayrider/swlib/logger"
)

// SendTemplate sends an email using templates read from the local filesystem.
//
// The method:
//  1. Reads HTML and text templates from the configured templates directory
//  2. Parses them as Go templates
//  3. Executes templates with the provided data map
//  4. Sends the resulting email via SMTP
//
// Returns:
//   - codes.InvalidArgument: Template not found in object store
//   - codes.Internal: Template parsing, execution, or SMTP error
func (s *MailServer) SendTemplate(
	ctx context.Context,
	req *mailv1.SendTemplateRequest,
) (*mailv1.SendTemplateResponse, error) {
	lg := s.Logger().Derive(log.WithFunction("SendTemplate"))

	htmlBytes, err := os.ReadFile(filepath.Join(s.TemplatesDir(), req.HtmlTemplate))
	if err != nil {
		lg.Errorf("failed to get HTML template %s, error: %v", req.HtmlTemplate, err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	htmlTemplateContent := string(htmlBytes)

	txtBytes, err := os.ReadFile(filepath.Join(s.TemplatesDir(), req.TextTemplate))
	if err != nil {
		lg.Errorf("failed to get Text template %s, error: %v", req.TextTemplate, err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	txtTemplateContent := string(txtBytes)

	htmlTmpl, err := htmlTemp.New("").Parse(htmlTemplateContent)
	if err != nil {
		lg.Errorf("failed to parse HTML template %s, error: %v", req.HtmlTemplate, err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	txtTmpl, err := txtTemp.New("").Parse(txtTemplateContent)
	if err != nil {
		lg.Errorf("failed to parse Text template %s, error: %v", req.TextTemplate, err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	var htmlBuf, txtBuf bytes.Buffer
	if err := htmlTmpl.Execute(&htmlBuf, req.Data); err != nil {
		lg.Errorf("failed to execute template %s, error: %v", req.HtmlTemplate, err)
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := txtTmpl.Execute(&txtBuf, req.Data); err != nil {
		lg.Errorf("failed to execute template %s, error: %v", req.TextTemplate, err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	if err := s.Mailer().Send(
		req.From, req.To, req.Cc, req.Bcc, req.Subject,
		htmlBuf.String(), txtBuf.String(),
	); err != nil {
		lg.Errorf("failed to send email, error: %v", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &mailv1.SendTemplateResponse{
		Message: "email sent",
	}, nil
}

// SendTemplateInternal is a public endpoint for internal service-to-service calls.
// It delegates to SendTemplate without requiring authentication.
func (s *MailServer) SendTemplateInternal(
	ctx context.Context,
	req *mailv1.SendTemplateRequest,
) (*mailv1.SendTemplateResponse, error) {
	return s.SendTemplate(ctx, req)
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

	if err := s.Mailer().Send(
		req.From, req.To, req.Cc, req.Bcc, req.Subject,
		req.HtmlBody, req.TextBody,
	); err != nil {
		lg.Errorf("failed to send email, error: %v", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &mailv1.SendResponse{
		Message: "email sent",
	}, nil
}

// SendInternal is a public endpoint for internal service-to-service calls.
// It delegates to Send without requiring authentication.
func (s *MailServer) SendInternal(
	ctx context.Context,
	req *mailv1.SendRequest,
) (*mailv1.SendResponse, error) {
	return s.Send(ctx, req)
}
