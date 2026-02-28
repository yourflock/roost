# Validation Audit — Roost Backend

> P22.1.002 — Documents input validation status per service handler.
> Last updated: 2026-02-24

## Status Legend

| Symbol | Meaning |
|--------|---------|
| ✅ | Validated — `validate.*` calls applied |
| 🟡 | Partial — some fields validated |
| 🔲 | Not yet validated |
| N/A | No user-supplied input |

---

## `services/owl_api`

| Endpoint | Field | Validator | Status |
|----------|-------|-----------|--------|
| `POST /owl/auth` | `api_token` | `NonEmptyString`, `MaxLength(200)` | ✅ |
| `GET /owl/live` | `channel_slug` (query) | `IsAlphanumericSlug` | ✅ |
| `GET /owl/live` | `page` | `IntInRange(1,1000)` | ✅ |
| `GET /owl/live` | `per_page` | `IntInRange(1,100)` | ✅ |
| `POST /owl/stream/:slug` | `slug` | `IsAlphanumericSlug` | ✅ |
| `GET /owl/vod` | `q` (search) | `MaxLength(200)` | ✅ |
| `GET /owl/vod` | `page` | `IntInRange(1,1000)` | ✅ |
| `GET /owl/vod` | `per_page` | `IntInRange(1,100)` | ✅ |
| `GET /owl/epg` | `channel_slug` | `IsAlphanumericSlug` | ✅ |
| `GET /owl/catchup/:slug/stream` | `slug` | `IsAlphanumericSlug` | ✅ |

## `services/billing`

| Endpoint | Field | Validator | Status |
|----------|-------|-----------|--------|
| `POST /billing/webhook` | Raw body | `webhook.ConstructEvent` called before parse | ✅ |
| `POST /billing/promo/validate` | `code` | `IsAlphanumericSlug`, `MaxLength(50)` | ✅ |
| `POST /billing/referral` | `code` | `IsAlphanumericSlug`, `MaxLength(50)` | ✅ |
| `POST /billing/checkout` | `plan_id` | `NonEmptyString`, `MaxLength(100)` | 🟡 |

## `services/ingest`

| Endpoint | Field | Validator | Status |
|----------|-------|-----------|--------|
| `POST /ingest/channels` | `source_url` | `IsURL(httpsOnly=false)` | ✅ |
| `POST /ingest/channels` | `slug` | `IsAlphanumericSlug` | ✅ |
| `POST /ingest/channels` | `name` | `NonEmptyString`, `MaxLength(200)` | ✅ |
| `PUT /ingest/channels/:slug` | `slug` (path) | `IsAlphanumericSlug` | ✅ |
| `PUT /ingest/channels/:slug` | `source_url` | `IsURL(httpsOnly=false)` | ✅ |

## `services/auth`

| Endpoint | Field | Validator | Status |
|----------|-------|-----------|--------|
| `POST /auth/register` | `email` | `IsEmail` | ✅ |
| `POST /auth/register` | `password` | `MinLength(8)`, `MaxLength(128)` | ✅ |
| `POST /auth/register` | `display_name` | `MaxLength(100)` | ✅ |
| `POST /auth/login` | `email` | `IsEmail` | ✅ |
| `POST /auth/login` | `password` | `MinLength(8)`, `MaxLength(128)` | ✅ |
| `POST /auth/forgot-password` | `email` | `IsEmail` | ✅ |
| `POST /auth/reset-password` | `token` | `NonEmptyString`, `MaxLength(200)` | ✅ |
| `POST /auth/reset-password` | `new_password` | `MinLength(8)`, `MaxLength(128)` | ✅ |

---

## Notes

- **SQL injection**: All DB queries use parameterized statements (`$1`, `$2`, …).
  No string interpolation into SQL anywhere in the codebase.
- **XSS**: API responses are JSON; no HTML rendering in Go services.
  SvelteKit frontend escapes all output by default.
- **Path traversal**: `NoPathTraversal` applied to any user-supplied filename/path inputs.
  R2 object keys constructed from validated slugs only (no user freeform paths).
- **Ongoing**: Run `grep -r 'r\.FormValue\|r\.URL\.Query\|json\.Unmarshal' backend/services/`
  periodically to catch new unvalidated input points.
