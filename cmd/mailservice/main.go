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
// The service fetches JWT public keys from the authservice to verify
// tokens, keeping them refreshed in the background via a swlib/jwtkeys.Cache
// (see JWT_KEYS_REFRESH_INTERVAL_SECS / JWT_KEYS_FETCH_TIMEOUT_SECS).
//
// # Endpoints
//
// Email endpoints require authentication:
//   - Send/SendTemplate: Requires admin or service client with "email:send" scope
package main

import (
	"context"
	"strings"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/swayrider/grpcclients"
	"github.com/swayrider/grpcclients/authclient"
	"github.com/swayrider/mailservice/internal/mail"
	"github.com/swayrider/mailservice/internal/server"
	healthv1 "github.com/swayrider/protos/health/v1"
	mailv1 "github.com/swayrider/protos/mail/v1"
	"github.com/swayrider/swlib/app"
	"github.com/swayrider/swlib/jwtkeys"
	log "github.com/swayrider/swlib/logger"
	"google.golang.org/grpc"
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

	MAIL_ALLOWED_FROM_DOMAINS
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

	FldSmtpDialTimeoutSecs = "smtp-dial-timeout-secs"
	EnvSmtpDialTimeoutSecs = "SMTP_DIAL_TIMEOUT_SECS"
	DefSmtpDialTimeoutSecs = 5

	FldSmtpTimeoutSecs = "smtp-timeout-secs"
	EnvSmtpTimeoutSecs = "SMTP_TIMEOUT_SECS"
	DefSmtpTimeoutSecs = 30

	FldHealthProbeTtlSecs = "health-probe-ttl-secs"
	EnvHealthProbeTTLSecs = "HEALTH_PROBE_TTL_SECS"
	DefHealthProbeTtlSecs = 15

	FldMailAllowedFromDomains = "mail-allowed-from-domains"
	EnvMailAllowedFromDomains = "MAIL_ALLOWED_FROM_DOMAINS"
	DefMailAllowedFromDomains = ""
)

func main() {
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
			app.NewIntConfigField(
				FldSmtpDialTimeoutSecs, EnvSmtpDialTimeoutSecs,
				"Timeout in seconds for establishing the SMTP connection", DefSmtpDialTimeoutSecs),
			app.NewIntConfigField(
				FldSmtpTimeoutSecs, EnvSmtpTimeoutSecs,
				"Timeout in seconds for the entire SMTP transaction", DefSmtpTimeoutSecs),
			app.NewIntConfigField(
				FldHealthProbeTtlSecs, EnvHealthProbeTTLSecs,
				"How long in seconds a health probe result is cached before re-probing SMTP",
				DefHealthProbeTtlSecs),
			app.NewStringConfigField(
				FldMailAllowedFromDomains, EnvMailAllowedFromDomains,
				"Comma-separated list of domains allowed in the request From address "+
					"(defaults to the SMTP user's domain if unset)",
				DefMailAllowedFromDomains),
		).
		WithConfigFields(app.RateLimitConfigFields()...).
		WithConfigFields(app.JWTKeysConfigFields()...)

	jwtKeyCache := jwtkeys.New(application.Logger())

	grpcConfig := app.NewGrpcConfig(
		app.AuthInterceptor|app.RateLimitInterceptor,
		jwtKeyCache.GetPublicKeys,
		app.GrpcServiceHooks{
			ServiceRegistrar:   grpcMailRegistrar,
			ServiceHTTPHandler: grpcMailGateway(application),
		},
		app.GrpcServiceHooks{
			ServiceRegistrar:   grpcHealthRegistrar,
			ServiceHTTPHandler: grpcHealthGateway(application),
		},
	)
	// mailservice has no attachment support (text/html body + template data
	// only), so a cap well below gRPC's ~4MB implicit default is safe.
	grpcConfig.SetMaxRecvMsgSize(2 << 20)

	application = application.
		WithBackgroundRoutines(
			app.JWTKeysFetcher(jwtKeyCache),
			app.RateLimitEvictor(grpcConfig),
		).
		WithInitializers(app.JWTKeysInitializer(jwtKeyCache), app.RateLimiterInitializer(grpcConfig)).
		WithGrpc(grpcConfig)
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

// grpcMailRegistrar registers the MailService gRPC server with the registrar.
func grpcMailRegistrar(r grpc.ServiceRegistrar, a app.App) {
	templatesDir := app.GetConfigField[string](a.Config(), FldMailTemplatesDir)
	user := app.GetConfigField[string](a.Config(), FldSmtpUser)
	password := app.GetConfigField[string](a.Config(), FldSmtpPassword)
	host := app.GetConfigField[string](a.Config(), FldSmtpHost)
	port := app.GetConfigField[int](a.Config(), FldSmtpPort)
	dialTimeoutSecs := app.GetConfigField[int](a.Config(), FldSmtpDialTimeoutSecs)
	timeoutSecs := app.GetConfigField[int](a.Config(), FldSmtpTimeoutSecs)
	allowedFromDomains := app.GetConfigField[string](a.Config(), FldMailAllowedFromDomains)

	srv := server.NewMailServer(
		templatesDir,
		mail.NewMailer(
			user, password, host, port,
			time.Duration(dialTimeoutSecs)*time.Second,
			time.Duration(timeoutSecs)*time.Second),
		resolveAllowedFromDomains(allowedFromDomains, user, a.Logger()),
		a.Logger())
	mailv1.RegisterMailServiceServer(r, srv)
}

// resolveAllowedFromDomains parses the configured comma-separated domain
// list. If it is unset, the allowlist is derived from the SMTP user's
// domain, so the service is secure-by-default without extra configuration.
// If the SMTP user has no domain (e.g. a bare local-dev username), every
// domain is allowed and a warning is logged.
func resolveAllowedFromDomains(configured string, smtpUser string, l *log.Logger) []string {
	if configured != "" {
		domains := strings.Split(configured, ",")
		for i, d := range domains {
			domains[i] = strings.TrimSpace(d)
		}
		return domains
	}

	if i := strings.LastIndex(smtpUser, "@"); i >= 0 {
		return []string{smtpUser[i+1:]}
	}

	l.Derive(log.WithFunction("resolveAllowedFromDomains")).Warnf(
		"MAIL_ALLOWED_FROM_DOMAINS is unset and SMTP_USER %q has no domain to derive a default from; "+
			"allowing any From domain", smtpUser)
	return nil
}

// grpcHealthRegistrar registers the HealthService gRPC server with the registrar.
func grpcHealthRegistrar(r grpc.ServiceRegistrar, a app.App) {
	host := app.GetConfigField[string](a.Config(), FldSmtpHost)
	port := app.GetConfigField[int](a.Config(), FldSmtpPort)
	probeTTLSecs := app.GetConfigField[int](a.Config(), FldHealthProbeTtlSecs)

	srv := server.NewHealthServer(host, port, time.Duration(probeTTLSecs)*time.Second, a.Logger())
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
