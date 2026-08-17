// Package main implements the mailservice binary.
//
// The mailservice provides email sending capabilities for the SwayRider platform.
// It supports both direct email sending and template-based emails with data substitution.
//
// # Service Components
//
// The service initializes several components on startup:
//   - SMTP mailer for sending emails
//   - Auth service client for fetching JWT public keys
//
// # Authentication
//
// The service fetches JWT public keys from the authservice to verify tokens.
// Two background routines maintain the key cache:
//   - publicKeyFetcher: Periodically fetches keys from authservice
//   - publicKeyListener: Updates the local cache when new keys arrive
//
// # Endpoints
//
// Email endpoints require authentication:
//   - Send/SendTemplate: Requires admin or service client with "email:send" scope
package main

import (
	"context"
	"fmt"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"github.com/swayrider/grpcclients"
	"github.com/swayrider/grpcclients/authclient"
	healthv1 "github.com/swayrider/protos/health/v1"
	mailv1 "github.com/swayrider/protos/mail/v1"
	"github.com/swayrider/mailservice/internal/mail"
	"github.com/swayrider/mailservice/internal/repository"
	"github.com/swayrider/mailservice/internal/server"
	"github.com/swayrider/swlib/app"
	"github.com/swayrider/swlib/cache"
	log "github.com/swayrider/swlib/logger"
)

/*
Flags:
	-http-port			(default: 8080)
	-grpc-port			(default: 8081)

	-authservice-host
	-authservice-port

	-mail-templates-dir		(default: assets/mail/templates)

	-smtp-host			(default: 127.0.0.1)
	-smtp-port			(default: 587)
	-smtp-user
	-smtp-password

Environment variables:
	HTTP_PORT
	GRPC_PORT

	AUTHSERVICE_HOST
	AUTHSERVICE_PORT

	MAIL_TEMPLATES_DIR

	SMTP_HOST
	SMTP_PORT
	SMTP_USER
	SMTP_PASSWORD
*/

const (
	FldMailTemplatesDir = "mail-templates-dir"
	EnvMailTemplatesDir = "MAIL_TEMPLATES_DIR"
	DefMailTemplatesDir = "assets/templates"

	FldSmtpHost     = "smtp-host"
	FldSmtpPort     = "smtp-port"
	FldSmtpUser     = "smtp-user"
	FldSmtpPassword = "smtp-password"

	EnvSmtpHost     = "SMTP_HOST"
	EnvSmtpPort     = "SMTP_PORT"
	EnvSmtpUser     = "SMTP_USER"
	EnvSmtpPassword = "SMTP_PASSWORD"

	DefSmtpHost     = "127.0.0.1"
	DefSmtpPort     = 587
	DefSmtpUser     = ""
	DefSmtpPassword = ""
)

func main() {
	keyChan := make(chan []string)

	application := app.New("mailservice").
		WithDefaultConfigFields(app.BackendServiceFields, app.FlagGroupOverrides{}).
		WithServiceClients(
			app.NewServiceClient("authservice", authServiceClientCtor),
		).
		WithConfigFields(
			app.NewStringConfigField(
				FldMailTemplatesDir, EnvMailTemplatesDir, "Mail templates directory", DefMailTemplatesDir),
			app.NewStringConfigField(
				FldSmtpHost, EnvSmtpHost, "SMTP server", DefSmtpHost),
			app.NewIntConfigField(
				FldSmtpPort, EnvSmtpPort, "SMTP port", DefSmtpPort),
			app.NewStringConfigField(
				FldSmtpUser, EnvSmtpUser, "SMTP user", DefSmtpUser),
			app.NewStringConfigField(
				FldSmtpPassword, EnvSmtpPassword, "SMTP password", DefSmtpPassword),
		).
		WithBackgroundRoutines(
			publicKeyListener(keyChan),
			publicKeyFetcher(keyChan),
		)

	grpcConfig := app.NewGrpcConfig(
		app.AuthInterceptor,
		getPublicKeys,
		app.GrpcServiceHooks{
			ServiceRegistrar:   grpcMailRegistrar,
			ServiceHTTPHandler: grpcMailGateway(application),
		},
		app.GrpcServiceHooks{
			ServiceRegistrar:   grpcHealthRegistrar,
			ServiceHTTPHandler: grpcHealthGateway(application),
		},
	)
	application = application.WithGrpc(grpcConfig)
	application.Run()
}

// authServiceClientCtor creates a new auth service gRPC client.
// This client is used to fetch JWT public keys for token verification.
func authServiceClientCtor(a app.App) grpcclients.Client {
	lg := a.Logger().Derive(log.WithComponent("authServiceClientCtor"))
	clnt, err := authclient.New(
		app.ServiceClientHostAndPort(a, "authservice"))
	if err != nil {
		lg.Fatalf("failed to create authservice client: %v", err)
	}
	return clnt
}

// publicKeyListener is a background routine that listens for JWT public key updates.
// When new keys are received on the channel, they are stored in the local cache.
func publicKeyListener(keyChan chan []string) func(app.App) {
	return func(a app.App) {
		ctx := a.BackgroundContext()
		defer func() {
			a.BackgroundWaitGroup().Done()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case keys := <-keyChan:
				cache.LCSet(repository.JwtPublicKeys, keys)
			}
		}
	}
}

// publicKeyFetcher is a background routine that periodically fetches JWT public keys
// from the authservice and sends them to the listener via the channel.
func publicKeyFetcher(keyChan chan []string) func(app.App) {
	return func(a app.App) {
		ctx := a.BackgroundContext()
		defer func() {
			a.BackgroundWaitGroup().Done()
		}()
		clnt := app.GetServiceClient[*authclient.Client](a, "authservice")
		authclient.PublicKeyFetcher(ctx, clnt, keyChan)
	}
}

// getPublicKeys retrieves JWT public keys from the local cache.
// This function is called by the gRPC auth interceptor to verify tokens.
func getPublicKeys() ([]string, error) {
	keysIface, ok := cache.LCGet(repository.JwtPublicKeys)
	if !ok {
		return nil, fmt.Errorf("no public keys found")
	}

	keys, ok := keysIface.([]string)
	if !ok {
		return nil, fmt.Errorf("invalid public keys")
	}
	return keys, nil
}

// grpcMailRegistrar registers the MailService gRPC server with the registrar.
func grpcMailRegistrar(r grpc.ServiceRegistrar, a app.App) {
	templatesDir := app.GetConfigField[string](a.Config(), FldMailTemplatesDir)
	user := app.GetConfigField[string](a.Config(), FldSmtpUser)
	password := app.GetConfigField[string](a.Config(), FldSmtpPassword)
	host := app.GetConfigField[string](a.Config(), FldSmtpHost)
	port := app.GetConfigField[int](a.Config(), FldSmtpPort)

	srv := server.NewMailServer(
		templatesDir,
		mail.NewMailer(user, password, host, port), a.Logger())
	mailv1.RegisterMailServiceServer(r, srv)
}

// grpcHealthRegistrar registers the HealthService gRPC server with the registrar.
func grpcHealthRegistrar(r grpc.ServiceRegistrar, a app.App) {
	srv := server.NewHealthServer(a.Logger())
	healthv1.RegisterHealthServiceServer(r, srv)
}

// grpcMailGateway returns an HTTP handler that proxies REST requests to gRPC.
func grpcMailGateway(a app.App) app.ServiceHTTPHandler {
	return func(
		ctx context.Context,
		mux *runtime.ServeMux,
		endpoint string,
		opts []grpc.DialOption,
	) error {
		lg := a.Logger().Derive(log.WithFunction("MailServiceHTTPHandler"))
		if err := mailv1.RegisterMailServiceHandlerFromEndpoint(
			ctx, mux, endpoint, opts,
		); err != nil {
			lg.Fatalf("failed to register mail gRPC gateway: %v", err)
		}
		return nil
	}
}

// grpcHealthGateway returns an HTTP handler that proxies health check requests to gRPC.
func grpcHealthGateway(a app.App) app.ServiceHTTPHandler {
	return func(
		ctx context.Context,
		mux *runtime.ServeMux,
		endpoint string,
		opts []grpc.DialOption,
	) error {
		lg := a.Logger().Derive(log.WithFunction("HealthServiceHTTPHandler"))
		if err := healthv1.RegisterHealthServiceHandlerFromEndpoint(
			ctx, mux, endpoint, opts,
		); err != nil {
			lg.Fatalf("failed to register health gRPC gateway: %v", err)
		}
		return nil
	}
}
