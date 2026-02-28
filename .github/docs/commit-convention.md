# Commit Convention

Roost uses [Conventional Commits](https://www.conventionalcommits.org/) for all commit messages.
This enables automated changelog generation and clear history.

## Format

```
type(scope): short description

[optional body]

[optional footer(s)]
```

- **Header**: required, 72 characters max
- **Body**: optional, explain _why_ (not what)
- **Footer**: optional, reference issues (`Closes #123`) or note breaking changes

## Types

| Type | When to use |
| ---- | ----------- |
| `feat` | New feature or user-visible behavior |
| `fix` | Bug fix |
| `chore` | Maintenance — dependency updates, config, tooling |
| `docs` | Documentation only changes |
| `test` | Adding or updating tests |
| `ci` | CI/CD workflow changes |
| `refactor` | Code restructuring with no behavior change |
| `perf` | Performance improvement |

## Scopes

| Scope | Covers |
| ----- | ------ |
| `ingest` | Stream ingest service |
| `relay` | Stream relay service |
| `billing` | Billing and subscription service |
| `auth` | Authentication service |
| `catalog` | Content catalog service |
| `epg` | EPG (Electronic Programme Guide) service |
| `owl-api` | Owl Community Addon API service |
| `sports` | Sports intelligence service |
| `portal` | Subscriber web portal (web/subscribe) |
| `admin` | Admin web panel (web/admin) |
| `infra` | Infrastructure — Docker, CI, server config |
| `db` | Database migrations or schema changes |

## Examples

```
feat(billing): add promo code validation for trial extensions

fix(ingest): retry failed stream health checks up to 3 times

chore(deps): update stripe-go to v76.25.0

ci(infra): add Docker build matrix for all Go services

refactor(relay): extract HLS segment cache into separate package

docs(owl-api): document manifest endpoint response schema

test(billing): add integration test for webhook processing flow
```

## Breaking Changes

Breaking changes must be noted in the footer:

```
feat(owl-api): change manifest format to v2

BREAKING CHANGE: manifest.json schema updated. Owl clients < 2.0 are no longer supported.
```

## Branch Naming

| Pattern | Purpose |
| ------- | ------- |
| `feat/short-description` | New feature |
| `fix/short-description` | Bug fix |
| `chore/short-description` | Maintenance |
| `hotfix/short-description` | Urgent production fix (branch from main) |

## Pull Request Flow

1. Branch from `develop` (or `main` for hotfixes)
2. Write conventional commits throughout
3. Open PR to `develop`
4. CI must pass (tests + lint)
5. Merge to `develop` — auto-deploys to staging
6. PR `develop` → `main` requires manual approval — auto-deploys to production
