# Code Review — `mailservice`

**Date:** 2026-08
**Scope:** Full review of `mailservice/` — SMTP delivery service for the SwayRider platform.
**Reviewed:** `cmd/mailservice/main.go`, `internal/server/*` (server, send, health, ping), `internal/mail/mail.go`, `internal/repository/repository.go`, `assets/templates/*`, `protos/mail/v1/mail.proto`, `Dockerfile`, `Makefile`, `env.example`, `.github/workflows/ci.yml`, the shared `swlib` auth interceptor/profile machinery, and the `jordan-wright/email` dependency source.
**No code changes were made.**

---

## Summary

The service is small, readable, and well-structured: a clean handler/mailer split behind a `MailSender` interface, centralized endpoint security profiles (`init()` in `internal/server/server.go`), templates rendered with Go's `html/template` (auto-escaping), and a sensible dual API (`Send`/`SendTemplate` protected, `SendInternal`/`SendTemplateInternal` public). The `jordan-wright/email` library mitigates header injection and validates addresses before the SMTP envelope is built, which the code inherits for free.

However, the service's own documentation states the internal endpoints **"should not be directly exposed to the internet"** — and that is the crux of every serious finding below. The `*Internal` endpoints are **completely unauthenticated**, the service binds **all interfaces** (`[::]:8081` and `:8080`), and the dev stack publishes both ports to the host. There is no shared secret, no mTLS, no network-policy check, and no rate limiting at the application layer. An attacker who can reach the service at all can:

1. **Read arbitrary files off the container** and email them anywhere (path traversal in `SendTemplateInternal`), and
2. **Send unlimited spoofed/phishing email** through the platform's own SMTP infrastructure (open relay via `SendInternal`/`SendTemplateInternal`).

These two are critical. The rest are hardening items.

---

## Critical

### ~~1. Path traversal → arbitrary file read → data exfiltration (unauthenticated)~~ - FIXED 2026-08-17
`internal/server/send.go`, `SendTemplate()` / `SendTemplateInternal()`

`SendTemplate` joins the request-supplied template name straight onto the templates directory and reads it:

```go
htmlBytes, err := os.ReadFile(filepath.Join(s.TemplatesDir(), req.HtmlTemplate))   // line 44
...
txtBytes, err := os.ReadFile(filepath.Join(s.TemplatesDir(), req.TextTemplate))    // line 51
```

`filepath.Join` performs no containment check — it just cleans the result. An attacker sends:

```json
{
  "to": ["attacker@example.com"],
  "html_template": "../../../../etc/hostname",
  "text_template": "../../../../etc/passwd",
  "data": {}
}
```

`filepath.Join("/app/assets/templates", "../../../../etc/passwd")` resolves to `/etc/passwd`, which is read, parsed as a template (plain files with no `{{` parse fine), rendered, and **mailed to the attacker-controlled recipient**. Because `SendTemplateInternal` is registered as `security.PublicEndpoint` and the gRPC/REST servers bind all interfaces, this is exploitable by anyone who can reach the service — no token required.

Impact: arbitrary read of every file the container user can read — templates, config, environment, `/etc/shadow`-adjacent secrets, mounted volumes — exfiltrated via SMTP. This is the worst finding in the review.

Fix direction: validate template names against a strict allowlist (e.g. `^[A-Za-z0-9_.-]+$`, no path separators, no leading `.`), and/or resolve the path and verify `filepath.Rel(templatesDir, resolved)` stays inside `templatesDir` before reading.

### ~~2. Public `*Internal` endpoints are an unauthenticated open relay / phishing vector~~ - FIXED 2026-08-18
`internal/server/server.go` (`init()`), `internal/server/send.go`, `internal/mail/mail.go`

`SendInternal` and `SendTemplateInternal` are registered `PublicEndpoint` and simply delegate to `Send`/`SendTemplate`:

```go
security.PublicEndpoint("/mail.v1.MailService/SendInternal")             // server.go
security.PublicEndpoint("/mail.v1.MailService/SendTemplateInternal")
...
func (s *MailServer) SendInternal(ctx context.Context, req *mailv1.SendRequest) (*mailv1.SendResponse, error) {
    return s.Send(ctx, req)                                              // send.go
}
```

There is **no authentication, no shared secret, no source-IP check, and no rate limit**. The service binds `[::]:8081` (gRPC) and `:8080` (HTTP gateway) on all interfaces (`swlib/app/grpc.go`), and `infra/dev/layer-20/compose.yml` publishes `34002:8080` / `34102:8081` to the host. The README's own "Security Note" says the service "should not be directly exposed to the internet" and defers rate limiting to "the infrastructure level" — but nothing enforces either.

Combined with finding #3 (arbitrary `From`), any caller who can reach the service can:
- Send unlimited email to arbitrary recipients (spam / email-bombing) through SwayRider's SMTP infrastructure → sender-reputation and IP-blacklist damage, provider suspension.
- Render the platform's **own branded templates** (`verify_user`, `reset_password`, `invite_user`) with attacker-controlled `VerificationURL`/`RegistrationURL`/`PasswordResetURL` data → convincing phishing that appears to originate from the legitimate SwayRider mail server and passes SPF/DKIM if those are configured.

Fix direction: require a shared secret / service token / mTLS on the internal endpoints, or bind the service to the internal network only and terminate the public routes at the gateway. At minimum, remove the internal routes from any externally routed gateway and enforce network policy.

---

## High

### ~~3. No validation of `From` / recipients — full email spoofing~~ - FIXED 2026-08-18
`internal/server/send.go`, `internal/mail/mail.go`

`Send` passes everything through untouched:

```go
if err := s.Mailer().Send(req.From, req.To, req.Cc, req.Bcc, req.Subject, req.HtmlBody, req.TextBody); err != nil {
```

There is no validation that `From` is an allowed/owned domain, that recipients are non-empty (the library catches empty at send time, but only as a late error), or that the body/subject are sane. The caller controls `From` completely, so they can send as `noreply@swayrider.com` or any other address. On the protected endpoint this is limited to admins/service clients (acceptable), but on the public internal endpoint it is a spoofing and phishing enabler for unauthenticated attackers. Even on the protected path, an admin/service client can spoof arbitrary senders.

Fix direction: constrain `From` to a configured allowlist of domains (or derive it server-side and ignore the request value), validate recipient addresses, and consider rejecting addresses outside the platform's expected recipient set for internal flows.

### ~~4. No rate limiting / abuse controls~~ - FIXED 2026-08-18
`internal/server/send.go`, `cmd/mailservice/main.go`

There is no rate limiting, no per-IP throttling, no per-caller quota, and no concurrency cap anywhere in the service. The only implicit bound is gRPC's default 4 MB max message size. The README explicitly punts on this ("Consider implementing rate limiting at the infrastructure level"), but the public internal endpoints mean an unauthenticated attacker can drive unlimited SMTP traffic. Even legitimate service clients can accidentally flood the SMTP provider. This should be enforced in the service (or by an authenticated proxy) rather than assumed.

Fixed by adding a coarse per-source-IP token-bucket rate limiter (`swlib/ratelimit`), enforced on **both** the raw gRPC port (`swlib/grpc/interceptors/ratelimitinterceptor.go`, keyed on the real transport peer address) and the REST/HTTP gateway (new `swlib/http/middlewares/ratelimit.go`, keyed on `http.Request.RemoteAddr`). The two-layer approach matters: the gRPC-side limiter alone can't see individual REST callers, since grpc-gateway proxies REST traffic to the gRPC server in-process over loopback. Tunable via `RATE_LIMIT_RPS`/`RATE_LIMIT_BURST`/`RATE_LIMIT_IDLE_TTL_SECS`. mailservice also now caps incoming gRPC message size at 2 MiB (`grpcConfig.SetMaxRecvMsgSize`), well below gRPC's ~4MB implicit default, since it has no attachment support. Rolled out as shared `swlib/app` infrastructure to every backend service (authservice, mailservice, regionservice, routerservice, searchservice), not just mailservice.

### ~~5. SMTP credentials can be transmitted in plaintext~~ - FIXED 2026-08-18
`internal/mail/mail.go`

```go
err := e.Send(
    fmt.Sprintf("%s:%d", m.Host, m.Port),
    smtp.PlainAuth("", m.User, m.Password, m.Host))
```

`email.Send` calls stdlib `smtp.SendMail`, which upgrades to TLS **only if the server advertises STARTTLS** (`c.Extension("STARTTLS")`). If the SMTP server does not advertise STARTTLS (misconfiguration, plain port 25, or a non-compliant relay), `PlainAuth` credentials are sent base64-encoded **in the clear**. There is no enforcement that the channel is encrypted before authenticating, and no implicit-TLS (port 465) support — so the deployment is constrained to 587/STARTTLS and silently insecure if the server doesn't cooperate. (On the plus side, `smtp.SendMail` verifies the certificate chain by default — `InsecureSkipVerify` is not set.)

Fixed by replacing the library's `Send`/`SendWithStartTLS` call (both of which silently fall back to a plaintext channel when `STARTTLS` isn't advertised) with a hand-rolled SMTP transaction in `Mailer.sendWithMandatorySTARTTLS`, structurally the same as `email.SendWithStartTLS` but with a hard check inserted: after `EHLO`, if the server doesn't advertise `STARTTLS`, `Send` returns an error immediately and never issues `AUTH`. A preflight-connection approach was considered and rejected — it doesn't close the hole, since an active on-path attacker could strip `STARTTLS` on the second (real) connection while the library call still authenticates in plaintext regardless. The check now lives on the same connection used for authentication. Covered by `TestSend_RefusesWhenServerDoesNotAdvertiseSTARTTLS`, which spins up a fake SMTP server without `STARTTLS` and asserts both that `Send` fails with a `STARTTLS`-mentioning error and that no `AUTH` command is ever sent.

---

## Medium

### ~~6. Internal error details leak to the caller~~ - FIXED 2026-08-18
`internal/server/send.go`

Every failure path returns the raw error to the client:

```go
return nil, status.Error(codes.InvalidArgument, err.Error())   // lines 47, 54
return nil, status.Error(codes.Internal, err.Error())          // lines 61, 67, 73, 77, 85, 117
```

`os.ReadFile` errors include the resolved absolute path (useful to an attacker mapping the filesystem, especially combined with finding #1), template parse errors can echo template fragments, and SMTP errors can reveal the SMTP host, auth state, and server internals. Errors should be logged in full server-side and mapped to generic client-facing messages (`codes.NotFound` for missing templates, `codes.Internal` with a sanitized message for delivery failures).

Fixed by replacing every `err.Error()` passed to `status.Error` with a static, generic message (`"template not found"`, `"failed to parse template"`, `"failed to render template"`, `"failed to send email"`), matching the pattern already used in `authservice`'s handlers: log the full error server-side via `lg.Errorf`, return a fixed message to the caller. Missing templates now return `codes.NotFound` instead of `codes.InvalidArgument` (also fixes finding #11, folded into this change since it's the exact same lines). Covered by sanitization assertions added to the existing `send_test.go` cases (missing template, mailer error, invalid template syntax) that check the returned gRPC status message never contains the raw path/error text.

### ~~7. Health check always reports UP for "mail"~~ - FIXED 2026-08-18
`internal/server/health.go`

```go
case "mail", "health", "":
    return &healthv1.HealthResponse{Status: healthv1.HealthResponse_UP}, nil
```

`Check("mail")` never actually probes SMTP connectivity — it returns UP unconditionally. Orchestrators and load balancers will route traffic to a service whose SMTP backend is down, and every send fails. The health endpoint should at least attempt a TCP/SMTP handshake to the configured relay (with a short timeout) before reporting UP.

Fixed by having `Check("mail"/"health"/"")` attempt a `net.DialTimeout` to the configured SMTP host:port (3s timeout) and report `UP`/`DOWN` based on the result, rather than a lightweight TCP reachability check only — no EHLO/AUTH, so the probe doesn't consume a real SMTP session or risk tripping provider abuse limits. The result is cached for a configurable TTL (`HEALTH_PROBE_TTL_SECS`, default 15s) so orchestrator health checks (which may poll every few seconds) don't dial the SMTP server on every request. Covered by new tests in `health_test.go` exercising reachable/unreachable addresses and TTL caching/expiry. The same always-UP gap existed in `authservice`'s health check for its Postgres dependency; fixed there too with the equivalent `PingContext`-based probe (see `authservice`'s own code review / this repo's commit history).

### ~~8. No SMTP timeout or context propagation~~ - FIXED 2026-08-18
`internal/mail/mail.go`

`email.Send` → `smtp.SendMail` → `net.Dial` (no dial timeout), and the handler ignores `ctx` entirely. A hung or black-holed SMTP server can tie up a gRPC goroutine indefinitely, and there is no deadline on the request. A connection/read/write timeout (and ideally honoring `ctx` cancellation) is needed for availability.

Fixed by swapping `smtp.Dial` for `net.Dialer.DialContext`, so connecting now honors both the caller's `ctx` and a configurable dial timeout (`SMTP_DIAL_TIMEOUT_SECS`, default 5s). Once connected, `conn.SetDeadline` bounds the entire SMTP transaction (EHLO/STARTTLS/AUTH/RCPT/DATA) via a configurable overall timeout (`SMTP_TIMEOUT_SECS`, default 30s) — a deadline on the raw connection is the only way to guarantee this, since `net/smtp` has no per-step context support and a hung server could otherwise block a goroutine indefinitely even with `ctx` alone. `ctx` is now threaded from the gRPC handlers (`Send`/`SendTemplate`) through `MailServer.Mailer().Send` down to the dial call. Covered by new tests in `mail_test.go` asserting a canceled context aborts the send and that a server which accepts the connection but never responds is bounded by the overall timeout rather than hanging.

### ~~9. Templates read and re-parsed from disk on every request~~ - FIXED 2026-08-18
`internal/server/send.go`

Each `SendTemplate` call does two `os.ReadFile` calls plus two template parses. There is no caching, so high-volume flows pay disk I/O and parse cost per email. This is a performance/reliability concern (and means any local file write to the templates dir changes behavior live, which is surprising in production). Cache parsed templates keyed by name+mtime.

Fixed by adding a generic `templateCache[T]` (new `internal/server/templatecache.go`) that caches parsed templates keyed by name, invalidated by comparing the file's mtime on each `get()` — a cheap `os.Stat` on every request, but the read+parse is skipped whenever the file hasn't changed since it was cached. This preserves the "edit the file on disk, next request picks it up" behavior the review called out as worth keeping, while eliminating the read+parse cost in the steady state. `MailServer` now holds one `templateCache[*html/template.Template]` and one `templateCache[*text/template.Template]`, both keyed off the same `templatesDir` and constructed once in `NewMailServer` (no new config surface). A `templateAccessError` type distinguishes filesystem errors (stat/read → `codes.NotFound`) from parse errors (→ `codes.Internal`) so the caller-visible status codes are unchanged from before the caching was added. Covered by `templatecache_test.go` (parse-once, cache-reuse, reparse-on-change, missing-file, parse-error-propagation, and a concurrent-access case run under `go test -race`) and two new `send_test.go` cases exercising the same behavior end-to-end through `SendTemplate`.

### ~~10. JWT key-rotation propagation lag (startup + hourly)~~ - FIXED 2026-08-18
`cmd/mailservice/main.go`, `grpcclients/authclient/routines.go`

`publicKeyFetcher` refreshes keys on a **1-hour ticker** (`time.NewTicker(1 * time.Hour)`), and `getPublicKeys` returns an error when the cache is empty. Consequences:
- After authservice rotates its signing keys, mailservice will reject JWTs signed with the new key for up to an hour (and accept tokens signed with a *revoked* key for up to an hour).
- On startup, protected endpoints fail with `ErrNoKeys` until the first fetch completes (the fetcher retries every second, so this window is short, but it exists).

The refresh interval should be much shorter (or key-rotation events pushed), so rotated keys propagate promptly.

Fixed by replacing the hand-rolled `keyChan`/`publicKeyListener`/`publicKeyFetcher`/`getPublicKeys` quintet with a new, reusable `swlib/jwtkeys.Cache` (new `swlib/jwtkeys/cache.go`), wired in via matching `swlib/app` bootstrap helpers (new `swlib/app/jwtkeys.go`) that mirror the existing `swlib/ratelimit` rollout shape. The refresh interval is now configurable (`JWT_KEYS_REFRESH_INTERVAL_SECS`, default 5 minutes — down from the hardcoded hour, a 12x cut to worst-case propagation lag) and each fetch is bounded by its own timeout (`JWT_KEYS_FETCH_TIMEOUT_SECS`, default 15s) so a stuck downstream call can never wedge the loop. The cache also recovers from panics in both the fetch and the refresh loop, retains the last known-good keys across a transient failure instead of clearing them, and logs once a stale cache has gone unrefreshed for longer than its interval (previously silent). This wasn't mailservice-specific — the identical duplicated pattern existed in regionservice, routerservice, and searchservice, and a separately-written, still-hourly private copy existed in swayrider-api (`internal/jwtkeys`) — so all four backend services and swayrider-api were migrated onto the same shared cache in this pass, and the old `authclient.PublicKeyFetcher` (superseded, zero remaining callers) was deleted. Covered by `swlib/jwtkeys/cache_test.go` (retain-keys-on-failure, fetch-timeout bounding, panic recovery, stale-key logging, and `Configure`-before-`Run` cadence).

---

## Low

### ~~11. `SendTemplate` returns `InvalidArgument` for missing templates~~ - FIXED 2026-08-18
`internal/server/send.go` (lines 47, 54)

A missing template is more accurately `codes.NotFound`; `InvalidArgument` is for malformed input. Minor semantic, but it also means the client can't distinguish "you sent a bad name" from "the template doesn't exist" — and the raw path in the error makes it worse (see #6).

Fixed alongside #6, in the same lines: missing HTML/text templates now return `codes.NotFound` with a static `"template not found"` message.

### ~~12. `local.env` contains real-looking SMTP credentials~~ - VERIFIED 2026-08-19 (no change needed)
`mailservice/local.env`

`local.env` holds an actual-looking `SMTP_PASSWORD` value. It is **gitignored and not tracked** (verified: only `env.example` is tracked), so this is not currently a leak — but the file sits in the repo working tree with a live password. Recommend moving it to a secrets manager / `.env` outside the repo and rotating the value if it has ever been shared.

Re-verified: `local.env` is listed in `mailservice/.gitignore` (line 2), `git check-ignore -v local.env` confirms it resolves to that rule, and `git ls-files` confirms it has never been tracked. Already correctly gitignored — no action was needed. The remaining recommendation (move to a secrets manager, rotate if ever shared) is an operational follow-up outside the scope of this repo.

### ~~13. Documentation drift~~ - FIXED 2026-08-18
`mailservice/README.md`

- The README says templates live at `assets/mail/templates`; the actual default (`DefMailTemplatesDir` in `main.go`) is `assets/templates`.
- The README's build instructions reference `backend/go build ./services/mailservice/...` paths that don't exist in this layout; the correct path is `go build ./cmd/mailservice`.
- The README lists `Send`/`SendTemplate` as "Require admin or service client" — accurate, but the "Admin or Service Client" wording should make clear a **non-admin user token is rejected** (the profile sets `RequiresAdmin`, so plain users get `ErrUserNotAdmin`).

Fixed by correcting all stale paths in `README.md`: the `MAIL_TEMPLATES_DIR` default and the "bundled in the container under" prose now both say `assets/templates`; the proto path dropped its stray `backend/` prefix (`protos/mail/v1/`, `protos/health/v1/`); the build instructions dropped the nonexistent `cd backend` / `services/mailservice/` prefix in favor of `go build ./cmd/mailservice` and `go run ./cmd/mailservice` (mailservice is its own Go module, run from its own directory); the adjacent `make proto` comment (also stale — no such root Makefile target exists) was corrected to `cd protos && make`; and the Admin access-level row now notes "(a valid JWT for a non-admin user is rejected)".

### ~~14. Shared gateway CORS is broad~~ - FIXED 2026-08-18
`swlib/app/grpc.go`

The shared HTTP gateway enables `AllowCredentials: true` with origins `http://*.hevanto-it.com` and `https://*.swayrider.com` (wildcard subdomains). This is shared by all services and not specific to mailservice, and mailservice sets no cookies, so the practical risk here is low — but a compromised subdomain under those wildcards would be trusted. Worth tightening globally if any service sets credentials.

Fixed by making `AllowCredentials` an explicit per-service opt-in on `GrpcConfig` (new `GrpcConfig.AllowCredentials` field, default `false`, set via `GrpcConfig.SetAllowCredentials(bool)`) instead of hardcoding `true` in `startGrpc()`'s CORS options. Of the five `swlib/app`-based services, only **authservice** sets cookies (the refresh-token cookie via `CookieForwarder`/`CookieHeaderMatcher`), so it's the only one that now calls `SetAllowCredentials(true)` (`authservice/cmd/authservice/main.go`); mailservice, regionservice, routerservice, and searchservice are unaffected by the change and simply stop sending `Access-Control-Allow-Credentials: true`. Also added a `validateCORSOrigins` startup guard (mirroring the equivalent check already shipped in `swayrider-api/internal/server/server.go`) that fails fast if `AllowCredentials: true` is ever paired with a bare `"*"` origin — a defensive backstop, since the current origin list only has scoped wildcard-subdomain patterns, not a bare `*`. Covered by new `swlib/app/grpc_test.go` (`AllowCredentials` defaults to `false`, the setter flips it, and `validateCORSOrigins` rejects a bare `"*"` while allowing scoped subdomain wildcards) — `swlib/app` previously had zero tests.

---

## Positive observations

- **Centralized endpoint security** — all access levels are declared in one `init()` block in `internal/server/server.go`; the intent (public vs admin vs service-client with `email:send` scope) is immediately readable.
- **`html/template` auto-escaping** — HTML bodies rendered from templates are escaped, which protects recipients from HTML/script injection via attacker-controlled `data` (relevant because the internal endpoint accepts unauthenticated data).
- **Address validation inherited from the library** — `email.Send` parses `To`/`Cc`/`Bcc`/`From` with `mail.ParseAddress` before building the envelope, and `headerToBytes` drops unparseable addresses from headers; `Subject` is RFC 2047-encoded. Header injection via newlines is effectively mitigated, and **Bcc is never written to the headers** (recipient privacy preserved).
- **Clean, testable design** — `MailServer` depends on a `MailSender` interface, so the SMTP backend is fully mockable; handlers are small and focused.
- **Templates bundled in the container** (`Dockerfile` copies `assets/templates`), so deployments don't depend on a writable template store.
- **Sensible `From` fallback** — empty `From` falls back to the configured SMTP user (`mail.go`), which is the correct default for internal sends.
- **Nullability handled** — `Send`/`SendTemplate` fail cleanly when required fields are absent rather than panicking.
- **Tests pass** — `go test ./...` is green for `internal/server` and `internal/mail`.

---

## Test-coverage gaps

- ~~**No test for path traversal**~~ FIXED 2026-08-18 — was already covered as of the finding #1 fix (`send_test.go`'s `TestSendTemplate_PathTraversalHTMLTemplate`/`TextTemplate`, `TestSendTemplate_AbsolutePathTemplate`, `TestSendTemplate_HiddenOrEmptyTemplateName`). Note: `SendInternal`/`SendTemplateInternal` no longer exist — they were deleted outright by the finding #2 fix rather than re-scoped, so this bullet's original wording is stale; the surviving `Send`/`SendTemplate` are what's covered.
- ~~**No auth-enforcement tests**~~ FIXED 2026-08-18 — new `auth_test.go` boots the real `AuthInterceptor` over an in-memory `bufconn` server (modeled on `regionservice/internal/server/auth_test.go`, the only existing precedent for this pattern in the monorepo) and asserts: anonymous calls to `Send`/`SendTemplate` are rejected (`Unauthenticated`), a verified admin user JWT succeeds, a verified *non-admin* user JWT is rejected, an *unverified* admin user JWT is rejected (`EmailVerified` is required since the profile doesn't set `AllowUnverified`), a service token with the `email:send` scope succeeds, a service token without it is rejected, and health `Ping` stays public. `Send`/`SendTemplate` never read claims from `ctx` themselves — enforcement is entirely in the interceptor — so this could only be exercised through a real gRPC server, not by calling the handler methods directly.
- ~~**No validation tests**~~ — `From`/recipient handling, empty recipients, and invalid addresses were already covered in `send_test.go` prior to this pass. The one missing piece, **oversized bodies**, is now covered: FIXED 2026-08-18 — new `limits_test.go` confirms there's no application-level size check (only the transport-level `grpc.MaxRecvMsgSize` configured in `cmd/mailservice/main.go`, outside `internal/server` entirely) by driving a real gRPC server with a small configured limit and asserting an oversized `Send` request is rejected with `codes.ResourceExhausted`, and that a request within the limit still succeeds.
- ~~**No SMTP integration test**~~ FIXED 2026-08-18 — `mail.go`'s email construction was split out into a pure `buildEmail` helper (no behavior change) so `mail_test.go` can assert the constructed message is well-formed (parses as valid RFC 5322, correct From/To/Cc, Bcc absent from headers and from the raw bytes, non-ASCII subject RFC 2047-encoded, multipart HTML+text) without needing a network round-trip. Separately, a new `TestSend_SuccessfulSTARTTLSTransaction` drives a **complete, successful** STARTTLS→EHLO→AUTH→MAIL→RCPT→DATA→QUIT transaction against a fake SMTP server that performs a real TLS handshake with an ephemeral self-signed certificate — the first test in the suite to exercise the success path end-to-end (previously only the STARTTLS-refusal, canceled-context, and timeout negative cases existed). This required one small testability seam on `Mailer`: an unexported `rootCAs *x509.CertPool` field (nil by default, i.e. unchanged production behavior) that tests in the same package can set directly to trust the fake server's certificate, since `sendWithMandatorySTARTTLS` had no way to inject a trusted CA otherwise.
- ~~**No test** that templates with missing `data` keys render~~ FIXED 2026-08-18 — new `TestSendTemplate_MissingDataKeyRendersEmpty` in `send_test.go` confirms a template referencing a `Data` key absent from the request map renders that lookup as empty rather than erroring, matching Go's `text/template`/`html/template` `index` behavior on maps.
- ~~No test that template caching behaves (there is none).~~ FIXED 2026-08-18 — `templatecache_test.go` and two new `send_test.go` cases cover cache-reuse, reparse-on-change, and error propagation.
- ~~**Health check tests** only verify the component-name switch, not whether SMTP is actually reachable (it never is checked).~~ FIXED 2026-08-18 — `health_test.go` now covers the reachable/unreachable/TTL-cache cases.

**Unrelated pre-existing issue found while verifying this pass**: `go test -race ./...` fails on `TestTemplateCache_ConcurrentAccess` (a genuine data race in `templatecache.go`, confirmed present with this session's changes stashed out — not introduced here). Not part of the test-coverage gaps list; flagging for separate follow-up since `-race` isn't run in the normal `go test ./...` path.

---

## Recommended fix order

1. ~~**#1 (critical)** — sanitize/contain template names in `SendTemplate` so `../` can never escape the templates directory.~~ FIXED 2026-08-17
2. ~~**#2 (critical)** — require authentication (shared secret / service token / mTLS) on the `*Internal` endpoints, or bind the service to the internal network and stop exposing those routes; then enforce it so the "internal only" guarantee is real.~~ FIXED 2026-08-18
3. ~~**#3** — constrain `From` to allowed domains and validate recipients; reject spoofed senders.~~ FIXED 2026-08-18
4. ~~**#4** — add rate limiting / per-caller quotas and explicit size limits.~~ FIXED 2026-08-18
5. ~~**#5** — force TLS for SMTP and refuse to send credentials over an unencrypted channel.~~ FIXED 2026-08-18
6. ~~**#6 / #7 / #8** — sanitize client-facing errors, make the health check probe SMTP, and add connection timeouts / context propagation.~~ FIXED 2026-08-18 (also fixed #11 alongside #6)
7. ~~**#9** — cache parsed templates keyed by name+mtime.~~ FIXED 2026-08-18
8. ~~**#10** — shorten the public-key refresh interval so key rotation propagates promptly.~~ FIXED 2026-08-18
9. ~~**#13** — fix stale paths and wording in `README.md`.~~ FIXED 2026-08-18
10. ~~**#14** — make the shared gateway's `AllowCredentials` an opt-in, scoped to services that actually set cookies.~~ FIXED 2026-08-18

The first two are security-critical and should be addressed before this service is exposed beyond a strictly controlled network.
