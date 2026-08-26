# mailservice

Email delivery service for the SwayRider platform. Sends transactional emails via SMTP with support for both raw content and template-based emails. Templates are bundled in the container and rendered with Go's template engine.

> **Security Note:** The mailservice should not be directly exposed to the internet. All send endpoints require a valid admin JWT or service-client token with the `email:send` scope — there are no unauthenticated endpoints.

## Architecture

The mailservice exposes two server interfaces:

| Interface | Port | Purpose |
| --------- | ---- | ------- |
| REST/HTTP | 8080 | HTTP API via gRPC-gateway |
| gRPC | 8081 | Internal service-to-service communication |

### Dependencies

- **authservice**: JWT public key retrieval for authentication
- **SMTP Server**: Email delivery (e.g., SendGrid, Mailgun, or self-hosted)

### Background Processes

The service runs a `swlib/jwtkeys.Cache`-backed background routine that periodically fetches JWT public keys from authservice and refreshes the in-memory cache used for token verification (see `JWT_KEYS_REFRESH_INTERVAL_SECS` / `JWT_KEYS_FETCH_TIMEOUT_SECS` below).

## Configuration

Configuration is provided via environment variables or CLI flags.

### Server Configuration

| Environment Variable | CLI Flag | Default | Description |
| -------------------- | -------- | ------- | ----------- |
| `HTTP_PORT` | `-http-port` | 8080 | REST API port |
| `GRPC_PORT` | `-grpc-port` | 8081 | gRPC port |

### SMTP Configuration

| Environment Variable | CLI Flag | Default | Description |
| -------------------- | -------- | ------- | ----------- |
| `SMTP_HOST` | `-smtp-host` | 127.0.0.1 | SMTP server hostname |
| `SMTP_PORT` | `-smtp-port` | 587 | SMTP server port |
| `SMTP_USER` | `-smtp-user` | | SMTP authentication username |
| `SMTP_PASSWORD` | `-smtp-password` | | SMTP authentication password |
| `MAIL_ALLOWED_FROM_DOMAINS` | `-mail-allowed-from-domains` | (derived from `SMTP_USER`) | Comma-separated list of domains allowed in the request `From` address. If unset, defaults to `SMTP_USER`'s domain |
| `SMTP_DIAL_TIMEOUT_SECS` | `-smtp-dial-timeout-secs` | `5` | Timeout for establishing the SMTP connection |
| `SMTP_TIMEOUT_SECS` | `-smtp-timeout-secs` | `30` | Timeout for the entire SMTP transaction |

### Health

| Environment Variable | CLI Flag | Default | Description |
| -------------------- | -------- | ------- | ----------- |
| `HEALTH_PROBE_TTL_SECS` | `-health-probe-ttl-secs` | `15` | How long a health probe result is cached before re-probing SMTP |

### Rate Limiting

| Environment Variable | CLI Flag | Default | Description |
| -------------------- | -------- | ------- | ----------- |
| `RATE_LIMIT_RPS` | `-rate-limit-rps` | `50` | Requests per second allowed per source IP |
| `RATE_LIMIT_BURST` | `-rate-limit-burst` | `100` | Burst allowance per source IP |
| `RATE_LIMIT_IDLE_TTL_SECS` | `-rate-limit-idle-ttl-secs` | `300` | How long an idle per-IP rate limiter entry is kept |

### JWT Key Cache

| Environment Variable | CLI Flag | Default | Description |
| -------------------- | -------- | ------- | ----------- |
| `JWT_KEYS_REFRESH_INTERVAL_SECS` | `-jwt-keys-refresh-interval-secs` | `300` | How often the JWT public-key cache is refreshed from authservice |
| `JWT_KEYS_FETCH_TIMEOUT_SECS` | `-jwt-keys-fetch-timeout-secs` | `15` | Timeout for a single public-key fetch |

### Template Configuration

| Environment Variable | CLI Flag | Default | Description |
| -------------------- | -------- | ------- | ----------- |
| `MAIL_TEMPLATES_DIR` | `-mail-templates-dir` | `assets/templates` | Directory containing email templates (bundled in container) |

### Service Dependencies

| Environment Variable | CLI Flag | Default | Description |
| -------------------- | -------- | ------- | ----------- |
| `AUTHSERVICE_HOST` | `-authservice-host` | | Auth service host |
| `AUTHSERVICE_PORT` | `-authservice-port` | | Auth service port |

## API Reference

The API is defined in the Protocol Buffer files at `protos/mail/v1/` and `protos/health/v1/`.

### Endpoint Access Levels

| Level | Description |
| ----- | ----------- |
| **Public** | No authentication required (health endpoints only) |
| **Admin** | Requires valid JWT with admin privileges (a valid JWT for a non-admin user is rejected) |
| **Service Client** | Requires service client token with `email:send` scope |

Both send endpoints (`Send`, `SendTemplate`) require admin or service client authentication.
Service-to-service callers (e.g. authservice) obtain an `email:send`-scoped token and pass it as
`Authorization: Bearer <token>`.

---

### Health Endpoints

#### Check Health

Returns the health status of the service.

- **Endpoint:** `GET /api/v1/health`
- **Access:** Public

```bash
curl --request GET \
  --url http://localhost:8080/api/v1/health
```

Response:
```json
{
  "status": "UP"
}
```

#### Ping

Simple health check that returns HTTP 200.

- **Endpoint:** `GET /api/v1/health/ping`
- **Access:** Public

---

### Email Sending Endpoints

Both endpoints validate the request before sending: `from`, if set, must be a syntactically valid
address whose domain is in `MAIL_ALLOWED_FROM_DOMAINS` (an empty `from` falls back to `SMTP_USER`
and is not checked); `to`/`cc`/`bcc` addresses must be syntactically valid and `to` must be
non-empty.

#### Send (Protected)

Sends an email with raw HTML and text content.

- **Endpoint:** `POST /api/v1/mail/send`
- **Access:** Admin or Service Client (with `email:send` scope)

```bash
curl --request POST \
  --url http://localhost:8080/api/v1/mail/send \
  --header 'Authorization: Bearer <admin_or_service_token>' \
  --header 'content-type: application/json' \
  --data '{
    "from": "noreply@example.com",
    "to": ["user@example.com"],
    "cc": ["cc@example.com"],
    "bcc": ["bcc@example.com"],
    "subject": "Welcome to SwayRider",
    "htmlBody": "<html><body><h1>Welcome!</h1><p>Thank you for joining.</p></body></html>",
    "textBody": "Welcome!\n\nThank you for joining."
  }'
```

Response:
```json
{
  "message": "email sent"
}
```

#### Send Template (Protected)

Sends an email using templates read from the local filesystem. Templates are rendered with the provided data.

- **Endpoint:** `POST /api/v1/mail/send-template`
- **Access:** Admin or Service Client (with `email:send` scope)

```bash
curl --request POST \
  --url http://localhost:8080/api/v1/mail/send-template \
  --header 'Authorization: Bearer <admin_or_service_token>' \
  --header 'content-type: application/json' \
  --data '{
    "from": "noreply@example.com",
    "to": ["user@example.com"],
    "subject": "Welcome to SwayRider",
    "htmlTemplate": "welcome.html",
    "textTemplate": "welcome.txt",
    "data": {
      "Name": "John Doe",
      "ActivationUrl": "https://app.example.com/activate?token=abc123"
    }
  }'
```

Response:
```json
{
  "message": "email sent"
}
```

## Email Templates

Email templates are bundled in the container under `assets/templates` and use Go's standard `html/template` and `text/template` packages. The directory can be overridden via `MAIL_TEMPLATES_DIR`.

### Template Storage

```
assets/templates/
├── verify_user.html
├── verify_user.txt
├── reset_password.html
├── reset_password.txt
├── invite_user.html
├── invite_user.txt
├── test.html
└── test.txt
```

### Template Syntax

Templates use Go template syntax with data provided in the API request:

**HTML Template (`verify_user.html`):**
```html
<!DOCTYPE html>
<html>
<head><title>Verify Email</title></head>
<body>
  <h1>Hello!</h1>
  <p>Please verify your email address: {{.Email}}</p>
  <p><a href="{{.VerificationURL}}">Click here to verify</a></p>
  <footer>&copy; {{.Year}} SwayRider</footer>
</body>
</html>
```

**Text Template (`verify_user.txt`):**
```text
Hello!

Please verify your email address: {{.Email}}

Click the following link to verify:
{{.VerificationURL}}

© {{.Year}} SwayRider
```

### SwayRider Default Templates

The authservice uses the following templates (bundled in the container):

| Template | Purpose |
| -------- | ------- |
| `verify_user.html` / `verify_user.txt` | Email verification |
| `reset_password.html` / `reset_password.txt` | Password reset |
| `invite_user.html` / `invite_user.txt` | Invite-only registration email |

## Security Considerations

- **Service-to-Service Calls**: Callers such as authservice must obtain an `email:send`-scoped service-client token and pass it as `Authorization: Bearer <token>` — there is no unauthenticated path to any send endpoint.
- **SMTP Credentials**: Store SMTP credentials securely using environment variables or a secrets manager.
- **Rate Limiting**: Per-source-IP rate limiting is built in (`RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` / `RATE_LIMIT_IDLE_TTL_SECS`, see Configuration above).

## Building

```bash
# Generate protobuf code
cd protos && make

# Build the service
go build ./cmd/mailservice

# Run the service
go run ./cmd/mailservice
```

## Docker

```bash
# Build container (from mailservice/ directory)
make container-build
```

### Tagging

Tags are derived from the git state of the checkout:

| Branch / state | Tags applied |
|----------------|--------------|
| Version-tagged commit (`v1.2.3`) | `v1.2.3`, `latest` |
| `main` (untagged) | `v{last}-{date}-dev-b{N}`, `dev-latest` |
| Other branch | `v{last}-{branch}-b{N}` |
| Detached HEAD | `v{last}-{sha}-b{N}` |

Non-release builds get an incrementing build number (`-b{N}`) so repeated builds of the same branch don't overwrite each other. The number comes from querying the registry for the highest existing `-b{N}` tag on the same base tag and adding 1; the build fails if the registry can't be reached. Release builds are immutable and never get a build number.

### FORCE_DEV_LATEST

Only `main` (untagged HEAD) pushes the `dev-latest` floating tag automatically. Set `FORCE_DEV_LATEST=1` to also push `dev-latest` from a release build (a version-tagged commit, e.g. `v1.2.3`) or from any other branch:

```bash
FORCE_DEV_LATEST=1 make container-build
```

Or, across all services at once, `tools/containerbuild.py --dev-latest`. Use this when a release — or a branch build — should also advance environments that track `dev-latest`.

## Development

For local development with Docker Compose infrastructure:

1. Start base infrastructure: `cd infra/dev/layer-00 && docker-compose up -d`
2. Start SwayRider services: `cd infra/dev/layer-20 && docker-compose up -d`

Development ports:
- REST API: 34002
- gRPC: 34102
