# Hypertube

A 42 project: a web app to search legal, public-domain films and watch them in the browser while
the server downloads them over BitTorrent. Go (Gin) backend + Angular 21 SSR frontend, shipped
as a single container image. Production: **42.bombatomica64.dev/hypertube** (intended to run below
`BASE_PATH=/hypertube`).

## Features

- **Accounts** — register/login with email + password, server-side sessions (cookie, 7-day TTL)
  with a double-submit CSRF token (`XSRF-TOKEN` cookie / `X-XSRF-TOKEN` header, Angular's
  defaults), login rate limiting. OAuth login with **42 intra** and **GitHub** (each provider is
  enabled only when its client id/secret are set). Password reset by email over SMTP (Mailpit in
  dev; without `SMTP_HOST` the reset link is logged).
- **Profiles** — edit your own profile, view other users at `/users/:username`, avatar upload
  (max 5 MB, downscaled to 512 px, stored under `DATA_DIR`).
- **i18n** — English, French and Italian via Transloco (`frontend/src/app/i18n/translations`).
- **Library** — browse/search two sources: **archive.org** (items that publish a torrent) and
  **publicdomaintorrents.info**. Optional OMDb enrichment (IMDb rating, genres, actors; looked up
  by IMDb id, else title + year, cached 24h). Sort (name, rating, year) and filters (genre, rating
  and year ranges) run in the browser over the results loaded so far, since the providers page
  independently. Played films get a "Watched" badge (`watch_history`).
- **Streaming while downloading** — an embedded `anacrolix/torrent` engine downloads into
  `DATA_DIR/downloads`; the player starts once enough is on disk and `/stream/media/:id` serves
  Range requests from the partial file.
- **On-the-fly transcoding** — formats a browser cannot play (mkv, avi...) are converted with
  ffmpeg to fragmented MP4 while streaming, with a bounded number of concurrent conversions.
- **Comments** on each film.
- **Retention** — downloads nobody watched for `MEDIA_RETENTION` (default 30 days) are deleted
  hourly and go back to the not-downloaded state.
- **Public REST API** at `/api/v1` with OAuth2 client credentials (users, movies, comments) —
  see [`docs/api.md`](docs/api.md) and [`docs/openapi.yaml`](docs/openapi.yaml).

## Architecture

```
browser ──► Go backend :8080 ──┬─ /api/*, /api/v1/*, /stream/*, /healthz  (Gin handlers)
                               └─ everything else ──► Angular SSR node server :4000 (SSR_UPSTREAM)
                Postgres ◄─────┤
                BitTorrent peers ◄─► :6881 tcp/udp      ffmpeg (child processes)
```

- **`backend/`** — Go module, entrypoint `cmd/hypertube`. Packages in `internal/`: `config`
  (env vars), `auth` (sessions, CSRF, OAuth, reset, profiles, avatars), `mail` (SMTP),
  `media` (providers, torrent engine, streaming, transcoding, retention, comments),
  `publicapi` (`/api/v1`), `repository` (sqlc-generated from `db/queries` + `migrations`, pgx/v5).
  Media and auth routes are only mounted when `DATABASE_URL` is set.
- **`frontend/`** — Angular 21 SSR, standalone components and signals. In production the Go
  binary spawns it (`SSR_COMMAND`) and reverse-proxies to it. In dev `ng serve` runs on `:4200`
  and proxies `/api`, `/stream`, `/healthz` to the backend (`proxy.conf*.json`).
- **`BASE_PATH`** — the whole app (routes, cookies, OAuth callbacks, reset links) can live below a
  path prefix. Build the image with the matching `--build-arg BASE_HREF=/prefix/`.

## Local development

Requirements: Docker + Compose; Go 1.25 for `make sqlc`/`make lint`/`make test`; the
[golang-migrate](https://github.com/golang-migrate/migrate) CLI for migrations.

```sh
cp .env.example .env   # set POSTGRES_PASSWORD (and DATABASE_URL to match); .env is gitignored
make dev               # postgres + backend (air hot reload) + mailpit + ng serve
make migrate-up        # apply migrations (from the host, uses DATABASE_URL in .env)
```

Open http://localhost:4200. Password-reset emails land in Mailpit at http://localhost:8025.

| Make target | What it does |
| --- | --- |
| `make dev` / `make down` | `docker compose up --build` / `docker compose down` |
| `make migrate-up` / `make migrate-down` | apply all / roll back one migration (golang-migrate CLI) |
| `make sqlc` | regenerate `internal/repository` with the pinned sqlc v1.29.0 via `go run` (no global install) |
| `make test` | `go vet` + `go test` in backend, `ng test` in frontend |
| `make lint` | `gofmt -l` + `go vet` |
| `make build-image` | build the production image `hypertube:local` |

### Ports

| Port | Service |
| --- | --- |
| 4200 | Angular dev server (`ng serve`) |
| 8080 | Go backend (in production the only HTTP entrypoint) |
| 4000 | Angular SSR server inside the production container (`SSR_PORT`), not exposed |
| 6881 tcp+udp | BitTorrent peers (`TORRENT_LISTEN_PORT`) |
| 5432 | Postgres |
| 8025 / 1025 | Mailpit web UI / SMTP |

### Migrations

Plain SQL files in `backend/migrations` (`NNNN_name.up.sql` / `.down.sql`), golang-migrate
format. They are **not** applied by the app at startup: run `make migrate-up` (or the migrate CLI
against the production DSN). The image ships them in `/app/migrations`. After changing schema or
`backend/db/queries`, run `make sqlc` and commit the generated code.

## Configuration

All configuration is environment variables (`backend/internal/config`, `.env.example` documents
each one). In compose, `.env` feeds both variable substitution and the backend's `env_file`.

| Variable | Default | Purpose |
| --- | --- | --- |
| `POSTGRES_DB` / `POSTGRES_USER` / `POSTGRES_PASSWORD` | `hypertube` / `hypertube` / — (required) | compose only: Postgres container + backend DSN |
| `DATABASE_URL` | empty | Postgres DSN; empty disables DB-backed routes |
| `PORT` | `8080` | backend HTTP port |
| `DATA_DIR` | `/data` | downloads, conversions, avatars |
| `BASE_PATH` | empty | public path prefix (e.g. `/hypertube`) |
| `SSR_UPSTREAM` | `http://127.0.0.1:4000` | SSR server to reverse-proxy to |
| `SSR_COMMAND` | empty | command to spawn the SSR server (set in the image) |
| `SSR_PORT` | `4000` | SSR node server port (read by `frontend/src/server.ts`) |
| `COOKIE_SECURE` | `true` | Secure flag on session/CSRF cookies; `false` only for local http |
| `PUBLIC_URL` | — | external origin (no path) for OAuth callbacks and reset links |
| `FORTYTWO_CLIENT_ID` / `FORTYTWO_CLIENT_SECRET` | empty | 42 OAuth (disabled when empty) |
| `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` | empty | GitHub OAuth (disabled when empty) |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | empty | Google OpenID Connect login (disabled when empty) |
| `OIDC_PROVIDER_NAME` / `OIDC_ISSUER` / `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` | empty | optional extra OIDC provider (endpoints from issuer discovery; routes `/api/auth/<name>/...`) |
| `SMTP_HOST` | empty | SMTP server; empty logs reset links instead |
| `SMTP_PORT` | `587` | 465 = implicit TLS, otherwise STARTTLS if offered |
| `SMTP_USER` / `SMTP_PASSWORD` | empty | SMTP auth |
| `SMTP_FROM` | `hypertube@localhost` | sender address |
| `OMDB_API_KEY` | empty | optional OMDb enrichment |
| `OPENSUBTITLES_API_KEY` | empty | OpenSubtitles consumer key; empty disables subtitles |
| `OPENSUBTITLES_USER_AGENT` | `Hypertube v1.0` | `User-Agent` sent to OpenSubtitles (the consumer's app name + version) |
| `OPENSUBTITLES_USERNAME` / `OPENSUBTITLES_PASSWORD` | empty | opensubtitles.com account, logged in for the download endpoint |
| `TORRENT_LISTEN_PORT` | `6881` | peer port, TCP+UDP; `0` = random |
| `TORRENT_NO_UPLOAD` | `false` | never upload to peers |
| `TORRENT_SEED` | `false` | keep seeding completed files |
| `TORRENT_STALL_TIMEOUT` | `10m` | fail a download with no progress for this long; `0` = never |
| `MEDIA_RETENTION` | `720h` | delete unwatched downloads after this long; `0` = never |
| `TRUSTED_PROXIES` | _(empty)_ | comma-separated IPs/CIDRs whose `X-Forwarded-For` is trusted (production: the k3s pod CIDR, e.g. `10.42.0.0/16`); empty = trust nobody, so per-IP rate limits see the proxy IP |
| `FFMPEG_PATH` / `FFPROBE_PATH` | from `PATH` | ffmpeg binaries |
| `MAX_TRANSCODES` | `2` | concurrent on-the-fly conversions |

OAuth callback URLs to register at the providers: `PUBLIC_URL + BASE_PATH + /api/auth/42/callback`
`.../api/auth/github/callback` and `.../api/auth/google/callback` (plus
`.../api/auth/<OIDC_PROVIDER_NAME>/callback` for the optional OIDC provider).

## Production

- Image: `Dockerfile` (multi-stage: Angular SSR build, Go build, `node:22-alpine` runtime with
  ffmpeg), runs as `node`, exposes `8080` and `6881/tcp+udp`. Build arg `BASE_HREF` sets both the
  Angular base href and `BASE_PATH`.
- Deploy: push to `main` → GitHub Actions (`.github/workflows/deploy.yml`) builds
  `ghcr.io/alborghi-42/hypertube:<short-sha>` and bumps `apps/hypertube/values.yaml` in
  [`CodeDrevia/simple-server`](https://github.com/CodeDrevia/simple-server); Argo CD syncs it to
  k3s behind Traefik + Cloudflare Tunnel. Needs the `GITOPS_PAT` repository secret.
- Secrets come from the `hypertube-secrets` ExternalSecret, never from git. Mount a persistent
  volume at `DATA_DIR`, keep `COOKIE_SECURE=true`, and open the torrent port if you want
  incoming peers.
