# Roost Security Hardening — OWASP Top 10 Status

**Last reviewed**: 2026-02-24
**Status**: Active hardening — all API-level mitigations implemented.

## OWASP Top 10 (2021) Checklist

### A01 — Broken Access Control ✅
- All subscriber endpoints require `Authorization: Bearer {jwt}` via `internal/auth` middleware
- Superowner-only admin endpoints check `is_superowner` via DB lookup (`requireSuperowner`)
- UUID path parameters validated via `security.ValidateUUID` before DB lookup
- Row-level security enforced via Hasura JWT claims (`x-hasura-subscriber-id`)
- No IDOR: all resource fetches filter by authenticated subscriber ID

### A02 — Cryptographic Failures ✅
- All traffic via Cloudflare (TLS 1.2+ enforced at edge)
- JWT secrets: `AUTH_JWT_SECRET` environment variable, min 32 bytes, never in code
- API tokens: 32-byte random hex (`crypto/rand`), stored as bcrypt hash in DB
- HLS stream keys: AES-128, per-channel, 16-byte via `crypto/rand` (P16-T03)
- Passwords: bcrypt with cost factor 12 (Phase 2)
- R2 stream URLs: HMAC-SHA256 signed, 15-minute expiry

### A03 — Injection ✅
- SQL: all queries use `database/sql` with parameterized arguments (`$1`, `$2`, ...)
  No string interpolation in SQL anywhere in the codebase.
- Path params: `security.ValidateUUID` gates all UUID extraction from URL paths
- XSS: JSON-only API (no HTML rendering server-side), `security.SanitizeString` on string inputs
- Admin SvelteKit app: Svelte auto-escapes all template expressions

### A04 — Insecure Design 🟡
- Threat model documented (below)
- Rate limiting on auth endpoints (10 req/min) and stream endpoints (60 req/min)
- Abuse detection on stream endpoints (P16-T05)
- TODO: formal security design review before public launch

### A05 — Security Misconfiguration ✅
- `SecurityHeaders` middleware: X-Frame-Options, X-Content-Type-Options, CSP, Referrer-Policy
- CORS: restricted to known origins (`roost.unity.dev`, `owl.unity.dev`)
- Debug endpoints disabled in production (`LOG_LEVEL` ≠ debug)
- Nginx config: `server_tokens off`, `ssl_protocols TLSv1.2 TLSv1.3`
- Docker: non-root user in all service containers

### A06 — Vulnerable Components 🟡
- Go modules: `go.sum` pins all dependency hashes
- CI pipeline runs `govulncheck ./...` on every PR
- TODO: add `govulncheck` step to all service CI workflows
- Node/pnpm: `pnpm audit` in admin+portal CI
- No known CVEs in current dependency tree (last checked 2026-02-24)

### A07 — Identification and Authentication Failures ✅
- JWT access tokens: 15-minute TTL, HS256 signed, validated on every request
- Refresh tokens: 30-day TTL, single-use (rotation), stored as SHA-256 hash
- API tokens: 32-byte random hex, bcrypt stored, never returned after generation
- Brute-force protection: 10 req/min rate limit on `/auth/*` endpoints
- `RequireVerifiedEmail` middleware enforces email verification before token generation

### A08 — Software and Data Integrity Failures ✅
- Stripe webhook signatures validated via `stripe.ConstructEventWithOptions` (HMAC-SHA256)
- SSO webhook shared secret validated (P13)
- Go module checksums in `go.sum` (tamper-evident)
- Docker images pinned by digest in production compose

### A09 — Security Logging and Monitoring Failures ✅ (this phase)
- `audit_log` table: append-only, all admin/subscriber/system events
- Structured JSON logging via `pkg/logging` (logrus)
- Sensitive data redacted in logs: `RedactToken`, `RedactEmail`
- Abuse detection: shared-token detection, rate exceeded events (P16-T05)
- Incident response runbook: `infra/runbooks/INCIDENT_RESPONSE.md`

### A10 — Server-Side Request Forgery (SSRF) ✅
- No user-controlled HTTP requests from backend
- Cloudflare origin URLs are operator-configured at deploy time (not subscriber input)
- EPG source URLs: admin-only endpoint, validates URL format before accepting
- Webhook URLs: fixed Stripe/SSO endpoints, not user-configurable

## Threat Model

### Primary Threats

| Threat | Likelihood | Impact | Mitigation |
|--------|-----------|--------|------------|
| Token sharing (subscriber shares API token publicly) | Medium | High | Shared-token detection (P16-T05), token revocation |
| Brute-force auth | Medium | High | 10 req/min rate limit, bcrypt passwords |
| SQL injection via path params | Low | Critical | UUID validation, parameterized queries |
| Account takeover | Low | High | JWT short TTL, email verification, 2FA planned |
| DDoS on stream endpoints | Medium | High | Cloudflare WAF + rate limiting |
| Insider threat (compromised admin) | Low | Critical | Audit log, superowner-only for destructive actions |
| CSRF on admin panel | Low | Medium | Double Submit Cookie (P16-T02) |

### Out of Scope

- Physical server access (Hetzner datacenter security)
- Cloudflare account compromise (operator responsibility)
- Browser-side attacks on end users (Owl client security)

## Dependency Scanning

Run locally or in CI:

```bash
# Go vulnerabilities
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...

# Node/pnpm (admin panel + portal)
cd web/admin && pnpm audit
cd web/subscribe && pnpm audit
```

## Security Contacts

Report vulnerabilities to: `security@roost.unity.dev` (TBD — update before public launch)
