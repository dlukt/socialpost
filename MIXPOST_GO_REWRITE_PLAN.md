# Mixpost Go Rewrite Plan

Draft date: February 18, 2026

## 1) Goal

Build a Go-based social media management platform with Mixpost-like core functionality:

- Connect social accounts
- Compose and schedule posts across multiple accounts
- Manage media library
- Publish reliably via queue workers
- Import basic analytics/audience metrics

The rewrite does not need strict 1:1 parity with every Mixpost Pro/Enterprise feature on day one.

## 2) Scope Strategy

### 2.1 MVP (must-have parity)

- Authentication + basic RBAC
- Social account connection (OAuth where required)
- Post composer with per-account/post-version content
- Media upload/library + external media fetch hooks
- Scheduling + queue-based publishing
- Calendar/list views
- Error handling, retries, rate-limit aware execution
- Basic reports from imported metrics

### 2.2 Later Phases (after MVP)

- Workspaces/teams with granular roles
- Approval workflows
- Public API + webhooks
- Billing/subscriptions/multi-tenant controls
- Additional provider-specific growth features

## 3) Product Surface to Recreate

### 3.1 Core entities

- `services` (provider credentials/config)
- `accounts` (connected social identities + tokens + provider metadata)
- `posts`
- `post_accounts` (pivot with provider_post_id, per-account errors/data)
- `post_versions` (original and account-specific content variants)
- `tags` + post-tag relation
- `media` + conversion metadata
- `imported_posts`, `metrics`, `audience`, provider-specific insights

### 3.2 Runtime workflows

- Scheduled scan finds due posts
- Dispatch one publish job per account
- Track partial failure per account
- Mark final post status: published or failed
- Handle provider unauthorized status and rate limits
- Periodic imports for audience/posts/metrics

## 4) Proposed Go Architecture

Use a modular monolith first (clean boundaries, single deployable system) with separate processes:

- `api` (HTTP + auth + domain commands/queries)
- `worker` (job consumers)
- `scheduler` (cron-like triggers)

### 4.1 Suggested stack

- Language: Go 1.24+
- Router: Chi or Gin (prefer Chi for lean composition)
- DB: PostgreSQL
- Queue + cache + rate-limit state: Redis
- Object storage: S3-compatible (S3/MinIO/R2)
- Migrations: Atlas or golang-migrate
- SQL access: sqlc + pgx
- Background jobs: River (Postgres queue) or Asynq (Redis queue)
- Auth: session + API tokens (JWT optional; opaque token preferred)
- Frontend: React + TypeScript (or server-rendered HTMX if speed prioritized)
- Observability: OpenTelemetry + Prometheus + structured logging

### 4.2 Package boundaries

- `internal/auth`
- `internal/accounts`
- `internal/posts`
- `internal/media`
- `internal/providers`
- `internal/scheduler`
- `internal/jobs`
- `internal/reports`
- `internal/platform` (db, queue, storage, config, telemetry)

## 5) Provider Connector Design

Define a stable provider interface:

- `GetAuthURL()`
- `ExchangeToken()`
- `RefreshToken()`
- `GetAccount() / ListEntities()`
- `PublishPost(text, media, options)`
- `DeletePost(providerPostID)`
- `FetchMetrics(account, window)`
- `FetchAudience(account, window)`
- `Capabilities()` (text limits, media limits, mixing rules, simultaneous posting constraints)

Each provider module contains:

- OAuth/client config
- API client + DTO mapping
- Publisher
- Importers (metrics/audience/posts)
- Provider-specific rate-limit mapping

## 6) Data Model Notes

Keep model close to Mixpost initially for migration simplicity, then evolve:

- Preserve `post_versions` and `post_accounts` design
- Store provider raw payloads as JSONB for debugging/replay
- Add `idempotency_key` to publish jobs
- Add explicit `next_retry_at` and `attempt_count`
- Add outbox table for events/webhooks

## 7) Reliability and Safety Requirements

- Idempotent publishing (never double-post from retries)
- Per-provider backoff policy with jitter
- Distinguish transient vs permanent errors
- Circuit-breaker style cooldown per provider/account when limits hit
- Dead-letter queue for poisoned jobs
- Audit log for critical actions (account connect/disconnect, publish, delete, credential updates)

## 8) Security Requirements

- Encrypt provider tokens at rest (KMS or envelope encryption)
- Validate and sanitize remote media URLs (deny internal/private IP ranges)
- Strict MIME/type and size validation
- Signed URLs for media access where needed
- CSRF protection for session auth
- Role-based authorization checks on all mutating routes

## 9) Phased Delivery Plan

## Phase 0 - Foundation (2-3 weeks)

Deliverables:

- Monorepo/service layout
- Config system and env templates
- PostgreSQL + Redis wiring
- Migration pipeline
- Auth skeleton + user model
- Basic admin UI shell
- CI (lint, test, build, vulnerability scan)
- Telemetry baseline (logs, metrics, traces)

Exit criteria:

- Local + staging boot reliably
- Health checks and observability dashboards visible

## Phase 1 - Core Content and Scheduling (3-5 weeks)

Deliverables:

- Services config UI/API
- Accounts CRUD (without full OAuth yet)
- Posts, versions, tags, media models
- Media upload and library browsing
- Calendar/list filtering
- Schedule post endpoint + validation

Exit criteria:

- Users can create/edit/schedule posts with multiple account variants

## Phase 2 - Job System and Publisher Engine (3-4 weeks)

Deliverables:

- Due-post scanner (scheduler)
- Publish orchestrator
- One-job-per-account execution
- Final status resolver (published/failed)
- Retry/backoff/rate-limit/unauthed handling
- Job observability pages

Exit criteria:

- End-to-end publish flow stable under retry and partial failure scenarios

## Phase 3 - Provider Wave 1 (4-6 weeks)

Recommended first providers:

- X (Twitter)
- Mastodon
- Bluesky

Deliverables:

- OAuth/account connect for each provider
- Publish with text + supported media
- Basic audience/metrics import
- Capability-driven validation in composer

Exit criteria:

- Real posts published from staging to test accounts for all wave-1 providers

## Phase 4 - Provider Wave 2 (5-8 weeks)

Providers:

- Facebook Pages / Instagram
- Threads
- LinkedIn Share

Deliverables:

- Production-grade token refresh + permission checks
- Entity selection flows (pages/orgs where required)
- Provider-specific posting constraints

Exit criteria:

- Stable operation with provider review-approved apps

## Phase 5 - API, Teams, and Approvals (4-6 weeks)

Deliverables:

- Access tokens for API clients
- Public REST API for accounts/media/posts
- Webhooks (post published, failed, account unauthorized)
- Workspaces/teams/roles
- Optional approval flow for scheduled posts

Exit criteria:

- External automation clients can create/schedule posts safely

## Phase 6 - Hardening and Launch (3-5 weeks)

Deliverables:

- Load/perf tests
- Backup/restore runbooks
- Disaster recovery checks
- Security review and dependency audit
- Production rollout playbook

Exit criteria:

- SLOs defined and met in staging-like load

## 10) Delivery Risks and Mitigations

### 10.1 Platform approval delays

Risk:

- LinkedIn Community Management API, TikTok direct post, Pinterest standard tier, Google GBP access can delay launch.

Mitigation:

- Ship providers in waves.
- Keep fallback providers (X/Mastodon/Bluesky) for early customer value.
- Track approval lead times as release dependencies.

### 10.2 Provider API drift

Risk:

- Social APIs change limits/scopes often.

Mitigation:

- Centralize capability metadata.
- Add contract tests per provider.
- Build feature flags to disable unstable integrations quickly.

### 10.3 Duplicate posts from retries

Risk:

- Queue retries can create duplicate external posts.

Mitigation:

- Idempotency keys per post/account.
- Persist external request correlation IDs.

## 11) Team and Timeline

### Recommended team

- 2 backend engineers (Go/platform/integrations)
- 1 frontend engineer
- 1 QA/automation (shared or part-time)
- 1 PM/tech lead (can be combined role)

### Estimated timeline

- MVP + wave-1 providers: ~12-18 weeks
- wave-2 + API/teams hardening: +10-16 weeks
- Total to robust v1: ~22-34 weeks

Single full-stack engineer estimate: ~9-12 months.

## 12) First 10 Execution Tasks (next day checklist)

1. Create repo structure with `api`, `worker`, `scheduler` binaries.
2. Set up PostgreSQL + Redis via Docker Compose.
3. Implement migration for core tables (`services`, `accounts`, `posts`, `post_accounts`, `post_versions`, `media`, `tags`).
4. Add auth + user sessions + API token table.
5. Implement posts CRUD and scheduling endpoints.
6. Add media upload endpoint with size/MIME guardrails.
7. Add scheduler job that queries due posts.
8. Add publish orchestrator and per-account job execution.
9. Implement provider interface + stub provider.
10. Add integration test for end-to-end scheduled post lifecycle.

## 13) Reference Inputs Used

- Mixpost Lite source code (local clone):
  - `mixpost/database/migrations/create_mixpost_tables.php`
  - `mixpost/src/Commands/RunScheduledPosts.php`
  - `mixpost/src/Actions/PublishPost.php`
  - `mixpost/src/Jobs/AccountPublishPostJob.php`
  - `mixpost/src/SocialProviderManager.php`
  - `mixpost/routes/web.php`
- Mixpost docs and service integration guides:
  - `https://docs.mixpost.app/services/`
  - `https://docs.mixpost.app/api/`
  - provider-specific pages (X, Facebook, Threads, Bluesky, Pinterest, LinkedIn, TikTok, Google)
