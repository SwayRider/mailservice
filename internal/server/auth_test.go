// auth_test.go verifies that the gRPC AuthInterceptor enforces the endpoint
// profiles declared in server.go: Send/SendTemplate require an admin user
// JWT or a service token with the "email:send" scope, while health Ping and
// Check remain public.

package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"testing"
	"time"

	jwt5 "github.com/golang-jwt/jwt/v5"
	healthv1 "github.com/swayrider/protos/health/v1"
	mailv1 "github.com/swayrider/protos/mail/v1"
	"github.com/swayrider/swlib/grpc/interceptors"
	"github.com/swayrider/swlib/jwt"
	log "github.com/swayrider/swlib/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// newAuthTestServer boots an in-memory gRPC server with the real
// AuthInterceptor in front of a MailServer (backed by mailer) and a
// HealthServer, returning clients for both. When publicKeyPEM is empty, the
// key resolver fails token verification.
func newAuthTestServer(t *testing.T, mailer MailSender, publicKeyPEM string) (
	mailv1.MailServiceClient, healthv1.HealthServiceClient,
) {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)

	getKeys := func() ([]string, error) {
		if publicKeyPEM == "" {
			return nil, fmt.Errorf("no public keys")
		}
		return []string{publicKeyPEM}, nil
	}

	gs := grpc.NewServer(grpc.UnaryInterceptor(
		interceptors.AuthInterceptor(getKeys, log.New())))

	mailv1.RegisterMailServiceServer(gs, newTestMailServer(mailer, t.TempDir()))
	healthv1.RegisterHealthServiceServer(gs, newTestHealthServer("127.0.0.1", 1, time.Minute))

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

	return mailv1.NewMailServiceClient(conn),
		healthv1.NewHealthServiceClient(conn)
}

func sendRequest() *mailv1.SendRequest {
	return &mailv1.SendRequest{
		From:     "noreply@example.com",
		To:       []string{"user@example.com"},
		Subject:  "subject",
		HtmlBody: "<b>hello</b>",
		TextBody: "hello",
	}
}

func bearerContext(token string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+token)
}

func requireUnauthenticated(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Errorf("code = %v, want %v (message: %v)", st.Code(), codes.Unauthenticated, err)
	}
}

func TestAuthInterceptorRejectsUnauthenticatedSend(t *testing.T) {
	mailClient, _ := newAuthTestServer(t, &mockMailer{}, "")

	_, err := mailClient.Send(context.Background(), sendRequest())
	requireUnauthenticated(t, err)
}

func TestAuthInterceptorRejectsUnauthenticatedSendTemplate(t *testing.T) {
	mailClient, _ := newAuthTestServer(t, &mockMailer{}, "")

	_, err := mailClient.SendTemplate(context.Background(), &mailv1.SendTemplateRequest{
		From:         "noreply@example.com",
		To:           []string{"user@example.com"},
		HtmlTemplate: "welcome.html",
		TextTemplate: "welcome.txt",
	})
	requireUnauthenticated(t, err)
}

func TestAuthInterceptorAllowsPublicPing(t *testing.T) {
	_, healthClient := newAuthTestServer(t, &mockMailer{}, "")

	resp, err := healthClient.Ping(context.Background(), &healthv1.PingRequest{})
	if err != nil {
		t.Fatalf("public Ping failed: %v", err)
	}
	if resp == nil {
		t.Error("expected non-nil PingResponse")
	}
}

func TestAuthInterceptorRejectsNonAdminVerifiedUser(t *testing.T) {
	privateKeyPEM, publicKeyPEM := testRSAKeyPair(t)
	verified := true

	_, token, _, err := jwt.GenerateToken(
		"test-user",
		&jwt.OpenIDClaims{EmailVerified: &verified},
		jwt.NewSwayRiderUserClaims(false /* isAdmin */, "standard"),
		privateKeyPEM,
		time.Hour,
	)
	if err != nil {
		t.Fatalf("failed to generate user token: %v", err)
	}

	mailClient, _ := newAuthTestServer(t, &mockMailer{}, publicKeyPEM)

	_, err = mailClient.Send(bearerContext(string(token)), sendRequest())
	requireUnauthenticated(t, err)
}

func TestAuthInterceptorRejectsUnverifiedAdminUser(t *testing.T) {
	privateKeyPEM, publicKeyPEM := testRSAKeyPair(t)
	unverified := false

	_, token, _, err := jwt.GenerateToken(
		"test-admin",
		&jwt.OpenIDClaims{EmailVerified: &unverified},
		jwt.NewSwayRiderUserClaims(true /* isAdmin */, "standard"),
		privateKeyPEM,
		time.Hour,
	)
	if err != nil {
		t.Fatalf("failed to generate user token: %v", err)
	}

	mailClient, _ := newAuthTestServer(t, &mockMailer{}, publicKeyPEM)

	_, err = mailClient.Send(bearerContext(string(token)), sendRequest())
	requireUnauthenticated(t, err)
}

func TestAuthInterceptorAllowsAdminVerifiedUser(t *testing.T) {
	privateKeyPEM, publicKeyPEM := testRSAKeyPair(t)
	verified := true

	_, token, _, err := jwt.GenerateToken(
		"test-admin",
		&jwt.OpenIDClaims{EmailVerified: &verified},
		jwt.NewSwayRiderUserClaims(true /* isAdmin */, "standard"),
		privateKeyPEM,
		time.Hour,
	)
	if err != nil {
		t.Fatalf("failed to generate user token: %v", err)
	}

	mailClient, _ := newAuthTestServer(t, &mockMailer{}, publicKeyPEM)

	_, err = mailClient.Send(bearerContext(string(token)), sendRequest())
	if err != nil {
		t.Fatalf("authorized admin call failed: %v", err)
	}
}

func TestAuthInterceptorAllowsServiceTokenWithEmailSendScope(t *testing.T) {
	privateKeyPEM, publicKeyPEM := testRSAKeyPair(t)

	_, token, _, err := jwt.GenerateToken(
		"test-service",
		nil,
		jwt.NewSwayRiderServiceClaims(jwt5.ClaimStrings{"email:send"}),
		privateKeyPEM,
		time.Hour,
	)
	if err != nil {
		t.Fatalf("failed to generate service token: %v", err)
	}

	mailClient, _ := newAuthTestServer(t, &mockMailer{}, publicKeyPEM)

	_, err = mailClient.Send(bearerContext(string(token)), sendRequest())
	if err != nil {
		t.Fatalf("authorized service call failed: %v", err)
	}
}

func TestAuthInterceptorRejectsServiceTokenWithoutEmailSendScope(t *testing.T) {
	privateKeyPEM, publicKeyPEM := testRSAKeyPair(t)

	_, token, _, err := jwt.GenerateToken(
		"test-service",
		nil,
		jwt.NewSwayRiderServiceClaims(jwt5.ClaimStrings{"some:other-scope"}),
		privateKeyPEM,
		time.Hour,
	)
	if err != nil {
		t.Fatalf("failed to generate service token: %v", err)
	}

	mailClient, _ := newAuthTestServer(t, &mockMailer{}, publicKeyPEM)

	_, err = mailClient.Send(bearerContext(string(token)), sendRequest())
	requireUnauthenticated(t, err)
}

// testRSAKeyPair generates an ephemeral RSA keypair and returns the private
// and public keys in PEM encoding, matching the authservice key format.
func testRSAKeyPair(t *testing.T) (privateKeyPEM, publicKeyPEM string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	privateKeyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))

	publicKeyBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("failed to marshal public key: %v", err)
	}
	publicKeyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyBytes,
	}))

	return privateKeyPEM, publicKeyPEM
}
