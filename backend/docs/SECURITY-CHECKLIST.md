# OWASP Top 10 Security Checklist — Roost Backend

> P22.6.002 — Maps OWASP Top 10 (2021) to Roost mitigations.
> Status: MITIGATED | PARTIAL | TODO
> Last updated: 2026-02-24

---

## A01:2021 — Broken Access Control

**Status: MITIGATED**

- JWT-based auth on all subscriber endpoints (`internal/auth/middleware.go`)
- Scope enforcement: `stream:read`, `billing:write`, `admin` scopes (`internal/auth/scope_checker.go`)
- Superowner RBAC for admin operations (`services/billing/handlers_*.go`)
- COPPA guard blocks kid profiles from billing endpoints (`internal/middleware/coppa.go`)
- Reseller isolation: resellers can only access their own subscribers
- Row-level security via Hasura JWT claims (`x-hasura-subscriber-id`)

---

## A02:2021 — Cryptographic Failures

**Status: MITIGATED**

- TLS 1.2+ only, ECDHE+AESGCM ciphers (no RC4, no 3DES) — `infra/nginx/roost.conf`
- HSTS with 2-year max-age, preload-ready — `infra/nginx/roost.conf`
- OCSP stapling enabled — `infra/nginx/roost.conf`
- JWT: HS256 with key rotation support, alg:none rejected (`internal/auth/jwt_hardened.go`)
- Passwords hashed with bcrypt cost 12 (`services/auth/handlers_register.go`)
- Stream URLs signed with HMAC-SHA256, 15-min expiry (`internal/cdn/relay.go`)
- API tokens: 32 random bytes, stored as SHA-256 hash (`internal/auth/token.go`)
- Refresh tokens: 32 random bytes, stored as SHA-256 hash
- IP addresses hashed before storage (SHA-256) (`internal/middleware/ip_anonymize.go`)

---

## A03:2021 — Injection

**Status: MITIGATED**

- All DB queries use parameterized statements (`$1`, `$2`, …) — no string interpolation in SQL
- Input validation package with attack-test coverage (`internal/validate/validate.go`)
- Attack tests: SQL injection, XSS, path traversal, null bytes, 10k chars (`internal/validate/attack_test.go`)
- SSRF guard: blocks RFC 1918 ranges, loopback, metadata endpoints (`internal/middleware/ssrf.go`)
- No `html/template` rendering in Go services (API is JSON-only)

---

## A04:2021 — Insecure Design

**Status: MITIGATED**

- Threat model documented in the project planning docs for each phase
- CDN origin obfuscation: Cloudflare Tunnel + Workers, no origin IP in responses
- Zero-logging for stream endpoints (`nginx access_log off` on stream routes)
- Key rotation procedure documented (`internal/auth/key_rotation.go`)
- GDPR erasure: hard-delete with compliance audit trail (`services/billing/handlers_gdpr.go`)

---

## A05:2021 — Security Misconfiguration

**Status: MITIGATED**

- Security headers enforced: HSTS, CSP, X-Frame-Options, X-Content-Type-Options (`internal/middleware/security.go`)
- CORS allowlist with explicit 403 for unknown origins (`internal/middleware/security.go`)
- No debug endpoints in production (`ROOST_ENV` gate)
- Docker images built from minimal base (Alpine)
- `govulncheck` + `gosec` + `golangci-lint` in CI (`security-scan.yml`)

---

## A06:2021 — Vulnerable and Outdated Components

**Status: MITIGATED**

- `govulncheck ./...` in CI (`security-scan.yml`) — runs on every push
- `staticcheck ./...` in CI (`security-scan.yml`) — static analysis
- Trivy container scan in CI (OS + library CVEs)
- `pnpm audit --audit-level=high` for web packages in CI
- Go module `go.sum` pinned — no unpinned dependencies

---

## A07:2021 — Identification and Authentication Failures

**Status: MITIGATED**

- Hardened JWT validation: alg:none rejected, exp required, iat validated, iss verified (`internal/auth/jwt_hardened.go`)
- JWT revocation list with Redis cache (60s TTL) (`internal/middleware/revocation.go`)
- Key rotation: active key + 2 previous keys (zero-downtime rotation) (`internal/auth/key_rotation.go`)
- Rate limiting: auth 10/min, API 60/min, stream 300/min, admin 30/min (`internal/ratelimit/ratelimit.go`)
- Email lockout: progressive delays (5 fails → 5 min, 10 → 30 min, 15 → 24hr)
- TOTP 2FA available for subscribers
- bcrypt cost 12 for password storage

---

## A08:2021 — Software and Data Integrity Failures

**Status: PARTIAL**

- Stripe webhook: `webhook.ConstructEvent` called before payload parsing (`services/billing/handlers_webhook.go`)
- Stream URL signatures enforced on all HLS segment endpoints (`internal/cdn/relay.go`)
- Go module checksums verified via `go.sum`
- TODO: Implement subresource integrity (SRI) on web portal assets

---

## A09:2021 — Security Logging and Monitoring Failures

**Status: MITIGATED**

- Audit log: all subscriber actions logged with actor, action, resource, timestamp (`db/migrations/026_audit_log.sql`)
- GDPR events: export, erasure logged permanently for compliance
- COPPA events: child deletion logged permanently
- Log redaction: no PII (email, IP) in application logs (`internal/logger/redact.go`)
- IP hashing before storage in audit_log (`db/migrations/042_ip_anonymization.sql`)
- Sentry error tracking in production (`internal/metrics/metrics.go`)
- Prometheus metrics for rate limit violations and auth failures

---

## A10:2021 — Server-Side Request Forgery (SSRF)

**Status: MITIGATED**

- SSRF guard middleware blocks RFC 1918, loopback, link-local, ULA ranges (`internal/middleware/ssrf.go`)
- `validate.IsURL` blocks private addresses in user-supplied URL fields (`internal/validate/validate.go`)
- No user-controlled DNS or URL-fetch in production code paths
- Cloudflare Tunnel: origin server never accepts direct inbound connections

---

## Summary

| Risk | Status | Phase |
|------|--------|-------|
| A01 Broken Access Control | MITIGATED | P2, P22.2 |
| A02 Cryptographic Failures | MITIGATED | P0, P22.5 |
| A03 Injection | MITIGATED | P22.1 |
| A04 Insecure Design | MITIGATED | P20, P22 |
| A05 Security Misconfiguration | MITIGATED | P21, P22.5 |
| A06 Vulnerable Components | MITIGATED | P22.6 |
| A07 Auth Failures | MITIGATED | P2, P22.2 |
| A08 Integrity Failures | PARTIAL | P3, P22.5 |
| A09 Logging Failures | MITIGATED | P16, P22.3 |
| A10 SSRF | MITIGATED | P22.1 |
