package server

import (
	"context"
	"errors"
	"testing"

	mailv1 "github.com/swayrider/protos/mail/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// =============================================================================
// Send Tests
// =============================================================================

func TestSend_Success(t *testing.T) {
	mailer := &mockMailer{}
	s := newTestMailServer(mailer, t.TempDir())

	resp, err := s.Send(context.Background(), &mailv1.SendRequest{
		From:     "sender@example.com",
		To:       []string{"recipient@example.com"},
		Subject:  "Hello",
		HtmlBody: "<p>Hello</p>",
		TextBody: "Hello",
	})
	if err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}
	if resp.Message != "email sent" {
		t.Errorf("resp.Message = %q, want %q", resp.Message, "email sent")
	}
}

func TestSend_MailerError(t *testing.T) {
	smtpErr := errors.New("smtp: connection refused")
	mailer := &mockMailer{
		sendFn: func(from string, to []string, cc []string, bcc []string, subject string, htmlBody string, textBody string) error {
			return smtpErr
		},
	}
	s := newTestMailServer(mailer, t.TempDir())

	_, err := s.Send(context.Background(), &mailv1.SendRequest{
		To:      []string{"recipient@example.com"},
		Subject: "Hello",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("error code = %v, want %v", code, codes.Internal)
	}
}

// =============================================================================
// SendTemplate Tests
// =============================================================================

func TestSendTemplate_Success(t *testing.T) {
	var gotHTML, gotText string
	mailer := &mockMailer{
		sendFn: func(from string, to []string, cc []string, bcc []string, subject string, htmlBody string, textBody string) error {
			gotHTML = htmlBody
			gotText = textBody
			return nil
		},
	}
	dir := writeTemplates(t, map[string]string{
		"test.html": `<p>Hello {{index . "Name"}}</p>`,
		"test.txt":  `Hello {{index . "Name"}}`,
	})
	s := newTestMailServer(mailer, dir)

	resp, err := s.SendTemplate(context.Background(), &mailv1.SendTemplateRequest{
		From:         "sender@example.com",
		To:           []string{"recipient@example.com"},
		Subject:      "Welcome",
		HtmlTemplate: "test.html",
		TextTemplate: "test.txt",
		Data:         map[string]string{"Name": "Alice"},
	})
	if err != nil {
		t.Fatalf("SendTemplate returned unexpected error: %v", err)
	}
	if resp.Message != "email sent" {
		t.Errorf("resp.Message = %q, want %q", resp.Message, "email sent")
	}
	if gotHTML != "<p>Hello Alice</p>" {
		t.Errorf("HTML body = %q, want %q", gotHTML, "<p>Hello Alice</p>")
	}
	if gotText != "Hello Alice" {
		t.Errorf("text body = %q, want %q", gotText, "Hello Alice")
	}
}

func TestSendTemplate_MissingHTMLTemplate(t *testing.T) {
	dir := writeTemplates(t, map[string]string{
		"test.txt": `Hello`,
	})
	s := newTestMailServer(&mockMailer{}, dir)

	_, err := s.SendTemplate(context.Background(), &mailv1.SendTemplateRequest{
		HtmlTemplate: "missing.html",
		TextTemplate: "test.txt",
	})
	if err == nil {
		t.Fatal("expected error for missing HTML template, got nil")
	}
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Errorf("error code = %v, want %v", code, codes.InvalidArgument)
	}
}

func TestSendTemplate_MissingTextTemplate(t *testing.T) {
	dir := writeTemplates(t, map[string]string{
		"test.html": `<p>Hello</p>`,
	})
	s := newTestMailServer(&mockMailer{}, dir)

	_, err := s.SendTemplate(context.Background(), &mailv1.SendTemplateRequest{
		HtmlTemplate: "test.html",
		TextTemplate: "missing.txt",
	})
	if err == nil {
		t.Fatal("expected error for missing text template, got nil")
	}
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Errorf("error code = %v, want %v", code, codes.InvalidArgument)
	}
}

func TestSendTemplate_InvalidHTMLTemplateSyntax(t *testing.T) {
	// {{range}} without a pipeline argument is a parse error.
	dir := writeTemplates(t, map[string]string{
		"bad.html": `{{range}}{{end}}`,
		"ok.txt":   `Hello`,
	})
	s := newTestMailServer(&mockMailer{}, dir)

	_, err := s.SendTemplate(context.Background(), &mailv1.SendTemplateRequest{
		HtmlTemplate: "bad.html",
		TextTemplate: "ok.txt",
	})
	if err == nil {
		t.Fatal("expected error for invalid HTML template syntax, got nil")
	}
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("error code = %v, want %v", code, codes.Internal)
	}
}

func TestSendTemplate_InvalidTextTemplateSyntax(t *testing.T) {
	dir := writeTemplates(t, map[string]string{
		"ok.html": `<p>Hello</p>`,
		"bad.txt": `{{range}}{{end}}`,
	})
	s := newTestMailServer(&mockMailer{}, dir)

	_, err := s.SendTemplate(context.Background(), &mailv1.SendTemplateRequest{
		HtmlTemplate: "ok.html",
		TextTemplate: "bad.txt",
	})
	if err == nil {
		t.Fatal("expected error for invalid text template syntax, got nil")
	}
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("error code = %v, want %v", code, codes.Internal)
	}
}

func TestSendTemplate_MailerError(t *testing.T) {
	mailer := &mockMailer{
		sendFn: func(from string, to []string, cc []string, bcc []string, subject string, htmlBody string, textBody string) error {
			return errors.New("smtp: connection refused")
		},
	}
	dir := writeTemplates(t, map[string]string{
		"test.html": `<p>Hello</p>`,
		"test.txt":  `Hello`,
	})
	s := newTestMailServer(mailer, dir)

	_, err := s.SendTemplate(context.Background(), &mailv1.SendTemplateRequest{
		To:           []string{"recipient@example.com"},
		HtmlTemplate: "test.html",
		TextTemplate: "test.txt",
	})
	if err == nil {
		t.Fatal("expected error from mailer, got nil")
	}
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("error code = %v, want %v", code, codes.Internal)
	}
}

func TestSendTemplate_PathTraversalHTMLTemplate(t *testing.T) {
	var called bool
	mailer := &mockMailer{
		sendFn: func(from string, to []string, cc []string, bcc []string, subject string, htmlBody string, textBody string) error {
			called = true
			return nil
		},
	}
	dir := writeTemplates(t, map[string]string{
		"test.txt": `Hello`,
	})
	s := newTestMailServer(mailer, dir)

	_, err := s.SendTemplate(context.Background(), &mailv1.SendTemplateRequest{
		HtmlTemplate: "../../../../etc/passwd",
		TextTemplate: "test.txt",
	})
	if err == nil {
		t.Fatal("expected error for path traversal in HTML template, got nil")
	}
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Errorf("error code = %v, want %v", code, codes.InvalidArgument)
	}
	if called {
		t.Error("mailer.Send should not be called for a rejected template name")
	}
}

func TestSendTemplate_PathTraversalTextTemplate(t *testing.T) {
	var called bool
	mailer := &mockMailer{
		sendFn: func(from string, to []string, cc []string, bcc []string, subject string, htmlBody string, textBody string) error {
			called = true
			return nil
		},
	}
	dir := writeTemplates(t, map[string]string{
		"test.html": `<p>Hello</p>`,
	})
	s := newTestMailServer(mailer, dir)

	_, err := s.SendTemplate(context.Background(), &mailv1.SendTemplateRequest{
		HtmlTemplate: "test.html",
		TextTemplate: "../../../../etc/passwd",
	})
	if err == nil {
		t.Fatal("expected error for path traversal in text template, got nil")
	}
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Errorf("error code = %v, want %v", code, codes.InvalidArgument)
	}
	if called {
		t.Error("mailer.Send should not be called for a rejected template name")
	}
}

func TestSendTemplate_AbsolutePathTemplate(t *testing.T) {
	dir := writeTemplates(t, map[string]string{
		"test.txt": `Hello`,
	})
	s := newTestMailServer(&mockMailer{}, dir)

	_, err := s.SendTemplate(context.Background(), &mailv1.SendTemplateRequest{
		HtmlTemplate: "/etc/passwd",
		TextTemplate: "test.txt",
	})
	if err == nil {
		t.Fatal("expected error for absolute path template, got nil")
	}
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Errorf("error code = %v, want %v", code, codes.InvalidArgument)
	}
}

func TestSendTemplate_HiddenOrEmptyTemplateName(t *testing.T) {
	dir := writeTemplates(t, map[string]string{
		"test.txt": `Hello`,
	})
	s := newTestMailServer(&mockMailer{}, dir)

	for _, name := range []string{"", ".hidden"} {
		_, err := s.SendTemplate(context.Background(), &mailv1.SendTemplateRequest{
			HtmlTemplate: name,
			TextTemplate: "test.txt",
		})
		if err == nil {
			t.Fatalf("expected error for HTML template name %q, got nil", name)
		}
		if code := status.Code(err); code != codes.InvalidArgument {
			t.Errorf("template name %q: error code = %v, want %v", name, code, codes.InvalidArgument)
		}
	}
}
