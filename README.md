# mixpost-go

Go rewrite bootstrap for Mixpost-like social publishing.

## Current state

This repository currently includes:

- Three runtime binaries: `api`, `worker`, `scheduler`
- Shared config, logging, Postgres, and Redis wiring
- Health endpoints (`/health/liveness`, `/health/readiness`)
- Initial SQL schema for core entities
- Session authentication + API token auth
- CRUD endpoints for `services`, `accounts`, and `posts`
- Redis-backed publish queue (`queue:publish-post`)
- Scheduler that scans due posts and enqueues account-level publish jobs
- Provider abstraction layer with connector manager
- Initial provider modules:
  - `facebook_page` (real Graph API publish call)
  - `twitter`, `mastodon`, `bluesky` (stub connectors)
- Worker that consumes publish jobs, resolves post-version content, and publishes via provider manager
- Local infrastructure via Docker Compose (Postgres + Redis)

## Quick start

1. Start infrastructure:

```bash
make compose-up
```

If local ports are already in use, override ports:

```bash
POSTGRES_PORT=55432 REDIS_PORT=6380 make compose-up
```

2. Install dependencies:

```bash
make deps
```

3. Apply schema:

```bash
make migrate-up
```

4. Run services in separate terminals:

```bash
make run-api
make run-worker
make run-scheduler
```

5. Check API health:

```bash
curl -s http://localhost:8080/health/liveness
curl -s http://localhost:8080/health/readiness
```

## Environment variables

Copy `.env.example` values into your shell/session (or your preferred env loader):

- `APP_ENV`
- `LOG_LEVEL`
- `API_LISTEN_ADDR`
- `POSTGRES_URL`
- `REDIS_ADDR`
- `REDIS_PASSWORD`
- `REDIS_DB`

## Auth flow

1. Register or login (session cookie based):

```bash
curl -X POST http://localhost:8080/auth/register \
  -H 'Content-Type: application/json' \
  -c cookie.txt \
  -d '{"name":"Darko","email":"darko@example.com","password":"password123"}'
```

2. Create API token from the authenticated session:

```bash
curl -X POST http://localhost:8080/auth/api-tokens \
  -H 'Content-Type: application/json' \
  -b cookie.txt \
  -d '{"name":"cli-token","expires_in_days":7}'
```

3. Use bearer token for `/api/v1/*` endpoints:

```bash
curl -X GET http://localhost:8080/api/v1/services \
  -H "Authorization: Bearer <token>"
```

## Implemented API endpoints

### Auth/session

- `POST /auth/register`
- `POST /auth/login`
- `POST /auth/logout`
- `GET /auth/me`
- `GET /auth/api-tokens`
- `POST /auth/api-tokens`
- `DELETE /auth/api-tokens/{id}`

### Services

- `GET /api/v1/services`
- `POST /api/v1/services`
- `PUT /api/v1/services/{id}`
- `DELETE /api/v1/services/{id}`

### Accounts

- `GET /api/v1/accounts`
- `POST /api/v1/accounts`
- `PUT /api/v1/accounts/{id}`
- `DELETE /api/v1/accounts/{id}`

### Facebook

- `GET /api/v1/facebook/oauth/start`
- `GET /api/v1/facebook/oauth/callback`
- `POST /api/v1/facebook/pages/import`

### Posts

- `GET /api/v1/posts`
- `GET /api/v1/posts/{id}`
- `POST /api/v1/posts`
- `PUT /api/v1/posts/{id}`
- `DELETE /api/v1/posts/{id}` (soft delete)

## Current scheduler/worker behavior

- Scheduler scans due posts where:
  - `status = 1` (scheduled)
  - `schedule_status = 0` (pending)
  - `scheduled_at <= NOW()`
- Scheduler sets `schedule_status = 1` and pushes one Redis job per `post_accounts` row.
- Worker loads account + post version content and routes publish through provider connectors.
- Worker enforces provider service binding (for example, `facebook_page` requires an active `facebook` service).
- Worker marks account unauthorized when connector returns unauthorized.
- Worker retries transient failures up to 3 attempts.
- Post finalization:
  - success for all accounts -> `status = 2`, `schedule_status = 2`, `published_at = NOW()`
  - any account error -> `status = 3`, `schedule_status = 2`

## Facebook-first account import

1. Create and activate the Facebook service (requires `client_id` and `client_secret`):

```bash
curl -X POST http://localhost:8080/api/v1/services \
  -H "Authorization: Bearer <token>" \
  -H 'Content-Type: application/json' \
  -d '{
    "name":"facebook",
    "active":true,
    "configuration":{
      "client_id":"<facebook-app-id>",
      "client_secret":"<facebook-app-secret>",
      "api_version":"v21.0",
      "redirect_uri":"http://localhost:8080/api/v1/facebook/oauth/callback"
    }
  }'
```

2. Start OAuth (returns `authorization_url`):

```bash
curl -X GET http://localhost:8080/api/v1/facebook/oauth/start \
  -H "Authorization: Bearer <token>"
```

3. Open `authorization_url` in a browser, approve permissions, and Facebook will redirect to:

- `GET /api/v1/facebook/oauth/callback?state=...&code=...`

The callback exchanges the code, fetches managed pages, and upserts `facebook_page` accounts automatically.

Optional fallback: if you already have a Facebook user token, you can still import directly:

```bash
curl -X POST http://localhost:8080/api/v1/facebook/pages/import \
  -H "Authorization: Bearer <token>" \
  -H 'Content-Type: application/json' \
  -d '{
    "user_access_token":"<facebook-user-access-token>",
    "exchange_for_long_lived":true
  }'
```

## Next implementation targets

- Media upload endpoints and conversion pipeline
- Reports/analytics import jobs
- Replace remaining stub provider modules with real API integrations
- Add per-provider rate-limit and backoff policies
- Add service configuration APIs/validation for provider-specific fields (ex: Facebook API version, app credentials)
