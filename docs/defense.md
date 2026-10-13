# Defense evaluation kit

Everything an evaluator needs to check the mandatory part of the subject against this app:
demo accounts, legal sample films (one mkv to show on-the-fly conversion), an automated API
walkthrough, and a checklist mapping each requirement to what to click and what to expect.

## 0. Setup

```sh
cp .env.example .env        # set POSTGRES_PASSWORD (+ DATABASE_URL); never commit .env
make dev                    # postgres + backend :8080 + mailpit :8025 + ng serve :4200
make migrate-up             # once, on a fresh database
make seed                   # demo users + an API client for alice (prints CLIENT_ID/SECRET)
make api-demo               # asserts every /api/v1 route; exits 1 on any mismatch
```

Against the deployed instance: `make seed api-demo BASE_URL=https://42.bombatomica64.dev BASE_PATH=/hypertube`.
Both scripts need `curl` and `jq`. `VERBOSE=1 make api-demo` also prints response bodies.

| Make target | What it does |
| --- | --- |
| `make seed` | `scripts/seed.sh`: registers alice, bob, carol through `POST /api/auth/register` (409 = already there, skipped), logs in as alice with a cookie jar + CSRF double submit, creates an API client, prints `client_id` / `client_secret` |
| `make api-demo` | `scripts/api-demo.sh`: about 50 numbered PASS/FAIL steps over the public API (section 5 below); creates and deletes its own clients and comments |
| `make test` / `make lint` | backend `go vet` + `go test`, frontend `ng test` / `gofmt` + `go vet` |

### Demo accounts

| Username | Password | Email |
| --- | --- | --- |
| alice | `Alice-demo-42` | alice@example.com |
| bob | `Bob-demo-42` | bob@example.com |
| carol | `Carol-demo-42` | carol@example.com |

Created through the real register endpoint, so they pass the same validators (password 8-128
chars, at least one letter and one digit) and argon2id hashing as a user signing up in the UI.
Registration is rate limited to 10 per hour per IP: `seed.sh` treats a 429 as "already exists".
If you change alice's or bob's password during the defense, `api-demo` cannot log in any more;
change it back or reset the database.

## 1. Sample films (legal sources)

The library searches archive.org (items in `moviesandfilms` that publish an `Archive BitTorrent`)
and publicdomaintorrents.info. Search words are ANDed; archive.org results are sorted by title.

| Purpose | Source / identifier | Search for | Files in the torrent |
| --- | --- | --- | --- |
| **mkv, on-the-fly conversion** | archive.org `the-immigrant-1917-directed-by-charlie-chaplin` — Chaplin, *The Immigrant* (1917), US public domain (pre-1929) | `immigrant directed chaplin` (3 results; pick "The Immigrant (1917) Directed By Charlie Chaplin") | `The Immigrant (1917) Directed by Charlie Chaplin.mkv` 1.60 GB, `... (colorized).mkv` 113 MB, jpg/xml; **no mp4/ogv/webm** |
| mp4, native playback | archive.org `CarnivalofSouls` — *Carnival of Souls* (1962), license `creativecommons.org/licenses/publicdomain` | `carnival souls` | `CarnivalOfSouls.mp4` 519 MB (picked), `.ogv`, `_512kb.mp4`, `.mpeg`, `.asr.srt` |
| second provider | publicdomaintorrents.info movieid `192` — *Night of the Living Dead* (1968) | `night of the living dead` (the publicdomaintorrents result) | the app prefers `Night_of_the_Living_Dead_PSP.MP4.torrent` (339 MB) over the DivX AVIs |

Why that mkv: the engine picks the largest **browser-native** file (mp4/m4v/webm/ogv) and only
falls back to mkv/avi/... when there is none (`pickVideoFile`, `backend/internal/media/torrent.go`).
Almost every archive.org film with an mkv also has derived `.mp4`/`.ogv` files *inside the same
torrent* (e.g. `Harakiri1919fritzLang`, `AliceInWonderland1915_503`, `TheHauntedHouse1921`), so it
would never be transcoded. The Immigrant item has no derivatives. Verified 2026-10-13:

```sh
curl -s https://archive.org/metadata/the-immigrant-1917-directed-by-charlie-chaplin \
  | jq -r '.files[] | "\(.format)\t\(.size)\t\(.name)"'
# Matroska  112716611   The Immigrant (1917) Directed by Charlie Chaplin (colorized).mkv
# Matroska  1597894941  The Immigrant (1917) Directed by Charlie Chaplin.mkv
# JPEG ... / Archive BitTorrent ... / Metadata ...
```

The `_archive.torrent` itself was parsed and lists only the two mkv, the jpgs and the metadata
files. archive.org torrents carry web seeds, so they download even with no peers.

**Demo of the conversion**: log in, search `immigrant directed chaplin`, open the film and press
play. The backend log shows the download starting and an ffmpeg process; the player receives a
fragmented MP4 (DevTools > Network: the stream request answers `video/mp4` while the file on disk
is `.mkv`). In parallel a full conversion to `<name>.mp4` is written next to the mkv
(`DATA_DIR/downloads/<infohash>/...`), used for later views. Contrast with Carnival of Souls,
which streams the mp4 directly (Range requests, `206 Partial Content`).

## 2. Eliminatory and general rules

| Check | How to demonstrate | Expected |
| --- | --- | --- |
| No credentials in git | `git ls-files \| grep -i env` ; `git log --all --oneline -- .env` ; `cat .gitignore` | only `.env.example` (placeholders); `.env` never committed; `.env` is in `.gitignore` |
| Production secrets | README "Production": they come from the `hypertube-secrets` ExternalSecret | nothing secret in the repo |
| No forbidden streaming lib | `grep -E 'webtorrent\|peerflix\|pulsar' -r backend frontend/package.json` | nothing. Torrent: `anacrolix/torrent` (a BitTorrent client, not a torrent-to-stream library); the streaming (Range on partial files) is our code in `internal/media` |
| No console errors/warnings | DevTools console on every page (Firefox and Chrome latest), including login, search, scrolling, profile, player | empty console; server log has no errors |
| Layout | any page | header (logo, search, language, logout), main, footer |
| Mobile | DevTools device toolbar, ~375 px wide | usable layout, no horizontal scroll |

### Security

| Check | How | Expected |
| --- | --- | --- |
| Passwords hashed | `docker compose exec postgres psql -U hypertube -d hypertube -c "SELECT username, left(password_hash, 40) AS hash FROM users ORDER BY username;"` | `$argon2id$v=19$m=19456,t=2,p=1$...` for every local account (salted: alice and bob differ even with similar passwords); `NULL` only for OAuth-only accounts |
| SQL injection (login) | log in with username `' OR '1'='1` / `admin'--` and any password | "Invalid username or password", no 500 |
| SQL injection (search) | search `') OR 1=1 --`, `"; DROP TABLE users; --` | normal (empty or text) results; `users` still there. All SQL is sqlc-generated with `$n` placeholders (`backend/db/queries`), search terms are Lucene-escaped before going to archive.org |
| SQL injection (API) | `curl -H "$AUTH" "$API/users/1%20OR%201=1"` | 404 (ids are parsed as UUIDs first) |
| XSS in comments | post the comment `<img src=x onerror=alert(1)><script>alert(2)</script>` on a film | shown as literal text, no alert. Angular interpolation escapes it; no `innerHTML` / `bypassSecurityTrust*` in `frontend/src`; the CSP header (`backend/internal/httpsec`) blocks inline scripts anyway |
| XSS in profile | set first name to `<b>x</b><script>alert(1)</script>`, view `/users/<you>` as bob | literal text |
| CSRF | replay a logged-in cookie jar without the header: `curl -i -b jar -X PATCH -H 'Content-Type: application/json' -d '{"first_name":"x"}' $BASE/api/users/me` (or with a wrong `X-XSRF-TOKEN`) | `403 {"error":"Invalid CSRF token"}`. Every unsafe `/api` call needs `X-XSRF-TOKEN` = the `XSRF-TOKEN` cookie; session cookie `ht_session` is `HttpOnly`, `SameSite=Lax`, `Secure` in production |
| Upload of unwanted content | profile > avatar: upload a `.txt` renamed `.png`, an SVG, a >5 MB file | rejected with a validation message; only real JPEG/PNG/GIF/WebP (content sniffed, re-encoded, downscaled to 512 px) |
| Form validation | register with empty fields, bad email, 3-char password, password without digit, duplicate username/email | inline errors client side; the same rules server side (`curl` with bad JSON gives 400 with `fields`), duplicates give 409 |
| Brute force | 6 wrong logins for one username (5/user, 20/IP per window) | 429 with `Retry-After` |
| Headers | `curl -sI $BASE/` | `Content-Security-Policy`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY` |

## 3. User interface (III.1)

| Requirement | How to demonstrate | Expected |
| --- | --- | --- |
| Register with email, username, last name, first name, password | `/register` | account created and logged in |
| OAuth: 42 + another | `/login` > "42" and "GitHub" (need the client ids in `.env`) | provider consent, back logged in; first time asks for a username if needed |
| Login with username + password | `/login` as alice / `Alice-demo-42` | library |
| Password reset by email | `/forgot-password` with alice@example.com; open Mailpit http://localhost:8025 (or the logged link without `SMTP_HOST`) | link to `/reset-password?...`; new password works, old one does not, other sessions are logged out |
| One-click logout from any page | header button on library, film page, profile | back to `/login`; browser back does not show protected data |
| Preferred language, default English | new account shows English; pick French/Italian in the header or profile | UI switches and the choice persists after re-login |
| Edit email, picture, info | `/profile` | saved; duplicate email gives an error |
| View other users without email | `/users/bob` as alice | avatar, username, names; **no email** |

## 4. Library and video (III.2, III.3)

| Requirement | How to demonstrate | Expected |
| --- | --- | --- |
| Only for authenticated users | open `/` or `/watch/<id>` in a private window | redirected to `/login`; `/stream/...` answers 401 |
| Search field, two sources | search `night of the living dead` | thumbnails from archive.org **and** publicdomaintorrents |
| Results sorted by name | any search | alphabetical |
| No search: most popular | open `/` | archive.org feature films by downloads, plus publicdomaintorrents |
| Thumbnail content | look at a card | name, year (if known), IMDb rating (if known, needs `OMDB_API_KEY`), cover |
| Watched vs unwatched | play a film, go back | "Watched" badge on that card only |
| Infinite scroll | scroll down | next page loads by itself, no "next" link |
| Sort and filter | sort by name/rating/year, filter by genre, rating range, year range | list updates |
| Video page details | open a film | player, summary, cast (director, producer, main actors when OMDb knows them), year, length, IMDb rating, cover |
| Comments | post a comment, reload | appears with author and date; prior comments listed |
| Download starts on play, streams as soon as possible, non-blocking | play Carnival of Souls (not downloaded yet) | progress shown, playback starts before 100%; the rest of the site stays responsive meanwhile |
| Saved once downloaded | play it again | starts immediately, no new download |
| Erased after a month unwatched | `MEDIA_RETENTION` (default `720h`), hourly sweep in `internal/media/retention.go`; for the demo run with `MEDIA_RETENTION=2m` | after the sweep the film is back to not downloaded and its files are gone |
| Subtitles (English, and user's language) | see "Known gaps" below | |
| mkv converted on the fly | section 1, The Immigrant | plays in Firefox and Chrome |

## 5. API (III.4) — `make api-demo`

Full contract and curl examples: [`api.md`](api.md), [`openapi.yaml`](openapi.yaml). The API lives
at `/api/v1` (reason in `api.md`). `scripts/api-demo.sh` asserts, in order:

- `POST /oauth/token`: wrong secret 401, bad grant 400, Basic auth 200 (Bearer, `expires_in`), JSON body 200
- `GET /users` 200 (only `id`, `username`); `GET /users/:id` own 200 **with** email, other 200 **without** email, unknown 404
- `PATCH /users/:bob` as alice **403**; own with invalid email 400
- `GET /movies` 200 **without a token** (`id`, `name`); `GET /movies/:id` 200 with name, id, imdb_rating, production_year, length, subtitles, comments_count; unknown 404
- `POST /comments` 201 and `POST /movies/:id/comments` 201; empty comment 400
- `GET /comments`, `GET /movies/:id/comments`, `GET /comments/:id` 200
- `PATCH` own comment 200; `PATCH` and `DELETE` bob's comment as alice **403**; `DELETE` own 204, again 404
- missing token 401 + `WWW-Authenticate: Bearer`, junk token 401, session cookie only 401, unknown route 404, wrong method 405 + `Allow`

RESTful evidence: see "Why it is RESTful" in `api.md` (nouns + HTTP verbs, 201 + `Location`,
204, 401/403/404/405 semantics, stateless bearer auth, uniform JSON errors).

## Known gaps (check before the defense)

- **Subtitles are not implemented**: no subtitle source is wired (`backend/internal/publicapi/movies.go`
  returns `subtitles: []`, nothing in `internal/media`). III.3 requires English subtitles when
  available and the user's language when it differs. This is a mandatory item. archive.org torrents
  often contain an `.srt` (Harakiri1919, Carnival of Souls `.asr.srt`), which is the cheapest source.
- The film page (player, details, comments UI) was being reworked when this kit was written; recheck
  section 4 rows against the merged version.
- publicdomaintorrents trackers date from 2005; a film from that source may find no peers and stall
  (`TORRENT_STALL_TIMEOUT`). Use archive.org films (web seeds) for the live streaming demo.
