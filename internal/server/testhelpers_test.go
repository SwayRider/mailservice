package server

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	log "github.com/swayrider/swlib/logger"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

func newTestMailServer(mailer MailSender, templatesDir string) *MailServer {
	return NewMailServer(templatesDir, mailer, []string{"example.com"}, log.New())
}

func newTestHealthServer() *HealthServer {
	return NewHealthServer(log.New())
}

// =============================================================================
// mockMailer — implements MailSender with a configurable function field
// =============================================================================

type mockMailer struct {
	sendFn func(from string, to []string, cc []string, bcc []string, subject string, htmlBody string, textBody string) error
}

func (m *mockMailer) Send(from string, to []string, cc []string, bcc []string, subject string, htmlBody string, textBody string) error {
	if m.sendFn != nil {
		return m.sendFn(from, to, cc, bcc, subject, htmlBody, textBody)
	}
	return nil
}

// writeTemplates creates named template files inside a temporary directory and
// returns the directory path. Passing an empty name skips that file.
func writeTemplates(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("writeTemplates: %v", err)
		}
	}
	return dir
}
