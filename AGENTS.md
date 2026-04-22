# CLAUDE.md - mailservice

Service-specific constraints for the SwayRider mail service.
The root CLAUDE.md rules always apply.

## Scope

- Limit all work strictly to `backend/services/mailservice/`
- Do NOT inspect other services unless explicitly instructed
- Do NOT inspect `swlib/` or `protos/` unless explicitly named

## API & Auth Rules

- gRPC endpoints and security levels are registered in `server/server.go`
- Any new endpoint **must** be explicitly registered with:
  - `PublicEndpoint`
  - `UnverifiedEndpoint`
  - `AdminEndpoint`
  - or `ServiceClientEndpoint`
- Do NOT bypass or weaken security interceptors

## Execution Rules

- Follow plan → execute strictly
- No refactors outside the requested scope
- Assume all unspecified behavior is correct

## Documentation

Do NOT read documentation files by default.
Ask permission before reading:
- `README.md`
- architecture or API docs

