// limits_test.go verifies that an oversized request is rejected at the gRPC
// transport layer when the server is configured with a max receive message
// size (mailservice sets this via grpc.MaxRecvMsgSize in cmd/mailservice;
// internal/server itself has no application-level body-size check).

package server

import (
	"context"
	"net"
	"strings"
	"testing"

	mailv1 "github.com/swayrider/protos/mail/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// newSizeLimitedTestServer boots an in-memory gRPC server (no auth
// interceptor) with the given max receive message size in front of a
// MailServer, and returns a client for it.
func newSizeLimitedTestServer(t *testing.T, maxRecvMsgSize int) mailv1.MailServiceClient {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)

	gs := grpc.NewServer(grpc.MaxRecvMsgSize(maxRecvMsgSize))
	mailv1.RegisterMailServiceServer(gs, newTestMailServer(&mockMailer{}, t.TempDir()))

	go func() {
		_ = gs.Serve(lis)
	}()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return mailv1.NewMailServiceClient(conn)
}

func TestSend_RejectsOversizedMessage(t *testing.T) {
	const maxRecvMsgSize = 4 * 1024 // 4 KiB, well below the oversized body below

	mailClient := newSizeLimitedTestServer(t, maxRecvMsgSize)

	_, err := mailClient.Send(context.Background(), &mailv1.SendRequest{
		From:     "sender@example.com",
		To:       []string{"recipient@example.com"},
		Subject:  "Hello",
		HtmlBody: strings.Repeat("a", maxRecvMsgSize*2),
		TextBody: "Hello",
	})
	if err == nil {
		t.Fatal("expected error for oversized message, got nil")
	}
	if code := status.Code(err); code != codes.ResourceExhausted {
		t.Errorf("error code = %v, want %v", code, codes.ResourceExhausted)
	}
}

func TestSend_AllowsMessageWithinLimit(t *testing.T) {
	const maxRecvMsgSize = 64 * 1024 // 64 KiB

	mailClient := newSizeLimitedTestServer(t, maxRecvMsgSize)

	_, err := mailClient.Send(context.Background(), &mailv1.SendRequest{
		From:     "sender@example.com",
		To:       []string{"recipient@example.com"},
		Subject:  "Hello",
		HtmlBody: "<p>Hello</p>",
		TextBody: "Hello",
	})
	if err != nil {
		t.Fatalf("Send returned unexpected error for a message within the size limit: %v", err)
	}
}
