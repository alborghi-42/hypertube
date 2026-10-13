# Hypertube

A 42 project: search legal video sources (archive.org, legittorrents.info) and watch them in the
browser while the server downloads the torrent in the background. Go backend + Angular 21 SSR
frontend, shipped as a single container image.

This replaces the deprecated prototype embedded in Clashboard
(`clashboard.bombatomica64.dev/hypertube`); production home is **hypertube.bombatomica64.dev**.

## Architecture

- **`backend/`** — Go (Gin + pgx/sqlc). The Go binary is the container entrypoint on port `8080`:
  it serves `/api/*`, `/stream/*` and `/healthz`, and reverse-proxies everything else to the
  Angular SSR node process it spawns (`SSR_COMMAND`).
- **`frontend/`** — Angular 21 SSR (standalone components, signals). In dev it runs `ng serve`
  on `:4200` with a proxy config that forwards `/api`, `/stream` and `/healthz` to the backend.
- **Torrenting** uses the `anacrolix/torrent` client library; streaming, piece prioritization and
  the ffmpeg transcode pipeline (HLS for non-mp4/webm, mkv required) are implemented here.

## API

Ported from the Clashboard `media-service`. Media routes are only mounted when `DATABASE_URL` is set.

| Route | Purpose |
| --- | --- |
| `GET /api/media?page=&rows=&provider=` | Popular feature films |
| `GET /api/media/search?q=&page=&rows=&provider=` | Search (archive.org items with a torrent) |
| `GET /api/media/details?external_id=&provider=` | Metadata, cached in `media` on first access |
| `GET /api/media/play?external_id=&provider=` | Start the background download; returns `{status, url}` once 5% is on disk (200) or after 15s (202) |
| `GET/POST /api/media/:mediaId/comments`, `GET/PATCH/DELETE /api/media/:mediaId/comments/:commentId` | Comments (no auth yet: placeholder author, no ownership check) |
| `GET /stream/files/<provider>/<file>` | Downloaded files (Range requests supported), stored under `$DATA_DIR/downloads` |

`provider` defaults to `archive.org`. `OMDB_API_KEY` (optional) adds IMDb rating, genres and actors
(looked up by IMDb id, else title + year; cached in memory for 24h).

Library sort (name, IMDb rating, year) and filters (genre, rating range, year range) run in the
browser over the results loaded so far: providers page independently and ratings/genres come from
OMDb, so the merged catalogue cannot be sorted server-side. Infinite scroll keeps loading pages
while the filtered list is short, and new matches slot into the chosen order.

Frontend: `/` is the browse feed, `/watch/:id` opens the player for an item.

## Development

```sh
cp .env.example .env   # fill in secrets — .env is gitignored, never commit it
make dev               # postgres + backend (air hot reload) + ng serve on :4200
```

Other targets: `make migrate-up`, `make sqlc`, `make test`, `make lint`, `make build-image`.

## Deployment

Push to `main` → GitHub Actions builds `ghcr.io/bombatomica64/hypertube:<short-sha>` → bumps
`apps/hypertube/values.yaml` in [`CodeDrevia/simple-server`](https://github.com/CodeDrevia/simple-server)
→ ArgoCD syncs it to the k3s cluster behind Traefik + Cloudflare Tunnel.

Requires the `GITOPS_PAT` repository secret (PAT with `repo` scope on `simple-server`).
