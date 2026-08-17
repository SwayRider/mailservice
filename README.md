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

The service runs two background routines:

1. **Public Key Fetcher**: Periodically fetches JWT public keys from authservice
2. **Public Key Listener**: Caches received public keys for JWT verification

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

### Template Configuration

| Environment Variable | CLI Flag | Default | Description |
| -------------------- | -------- | ------- | ----------- |
| `MAIL_TEMPLATES_DIR` | `-mail-templates-dir` | `assets/mail/templates` | Directory containing email templates (bundled in container) |

### Service Dependencies

| Environment Variable | CLI Flag | Default | Description |
| -------------------- | -------- | ------- | ----------- |
| `AUTHSERVICE_HOST` | `-authservice-host` | | Auth service host |
| `AUTHSERVICE_PORT` | `-authservice-port` | | Auth service port |

## API Reference

The API is defined in the Protocol Buffer files at `backend/protos/mail/v1/` and `backend/protos/health/v1/`.

### Endpoint Access Levels

| Level | Description |
| ----- | ----------- |
| **Public** | No authentication required (health endpoints only) |
| **Admin** | Requires valid JWT with admin privileges |
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

Email templates are bundled in the container under `assets/mail/templates` and use Go's standard `html/template` and `text/template` packages. The directory can be overridden via `MAIL_TEMPLATES_DIR`.

### Template Storage

```
assets/templates/
├── verify_user.html
├── verify_user.txt
├── reset_password.html
├── reset_password.txt
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

## Security Considerations

- **Service-to-Service Calls**: Callers such as authservice must obtain an `email:send`-scoped service-client token and pass it as `Authorization: Bearer <token>` — there is no unauthenticated path to any send endpoint.
- **SMTP Credentials**: Store SMTP credentials securely using environment variables or a secrets manager.
- **Rate Limiting**: Consider implementing rate limiting at the infrastructure level to prevent email abuse.

## Building

```bash
# Generate protobuf code (run from repo root)
make proto

# Build the service
cd backend
go build ./services/mailservice/cmd/mailservice

# Run the service
go run ./services/mailservice/cmd/mailservice
```

## Docker

```bash
# Build container (from mailservice/ directory)
make container-build
```

### FORCE_DEV_LATEST

By default, a release build on a version-tagged commit (e.g., `v1.2.3`) pushes two tags: the version tag and `latest`. Set `FORCE_DEV_LATEST=1` to additionally push the `dev-latest` floating tag:

```bash
FORCE_DEV_LATEST=1 make container-build
```

Use this when a release should also advance environments that track `dev-latest`.

## Development

For local development with Docker Compose infrastructure:

1. Start base infrastructure: `cd infra/dev/layer-00 && docker-compose up -d`
2. Start SwayRider services: `cd infra/dev/layer-20 && docker-compose up -d`

Development ports:
- REST API: 34002
- gRPC: 34102
