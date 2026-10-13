# Hypertube public REST API

Subject section III.4. Full contract: [`openapi.yaml`](openapi.yaml).

## Where it lives and why

`/api/v1/...` (below `BASE_PATH`; on the portal: `https://42.bombatomica64.dev/hypertube/api/v1`).

The subject lists the routes at the root (`/oauth/token`, `/users`, `/movies`...), but in this
app every path that is not `/api`, `/stream` or `/healthz` belongs to the Angular SSR app, which
already has `/users/:username` and `/oauth/complete`. Mounting under `/api/v1` keeps both working,
gives the API a version, and keeps the backend's JSON 404/405 handling (`isBackendPath` in
`main.go`). The relative paths below the prefix are exactly the subject's.

## Authentication: OAuth2 client credentials

1. A logged-in user creates a client in the web app session API:
   `POST /api/auth/clients {"name": "..."}` (session cookie + `X-XSRF-TOKEN` header).
   The answer holds `client_id` and `client_secret`. The secret is shown only once; the server
   keeps only its SHA-256. `GET /api/auth/clients` lists clients, `DELETE /api/auth/clients/:id`
   revokes one along with every token issued to it.
2. `POST /api/v1/oauth/token` with `grant_type=client_credentials` + client id/secret (HTTP Basic
   or body, form or JSON) returns an opaque bearer token, valid 1 hour.
3. Every other call sends `Authorization: Bearer <token>` and acts **as the client's owner**.
   The API ignores cookies and has no CSRF check, so a browser session cannot drive it and the
   token cannot be sent cross-site.

`GET /api/v1/movies` (the front page) needs no token: the subject says *any* user can see it.

## Walkthrough

```sh
BASE=http://localhost:8080          # or https://42.bombatomica64.dev/hypertube
API=$BASE/api/v1
```

### 0. Create a client (once, as a logged-in user)

```sh
# Log in and keep the cookies (the first GET hands out the XSRF-TOKEN cookie).
curl -s -c jar -b jar $BASE/api/ping >/dev/null
XSRF=$(awk '$6=="XSRF-TOKEN"{print $7}' jar)
curl -s -c jar -b jar -H "X-XSRF-TOKEN: $XSRF" -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"secret123"}' $BASE/api/auth/login
XSRF=$(awk '$6=="XSRF-TOKEN"{print $7}' jar)   # rotated by the login

curl -s -i -b jar -H "X-XSRF-TOKEN: $XSRF" -H 'Content-Type: application/json' \
  -d '{"name":"evaluation"}' $BASE/api/auth/clients
# HTTP/1.1 201 Created
# {"client_id":"6f1c...","name":"evaluation","created_at":"...","client_secret":"Qm9..."}
CLIENT_ID=6f1c...; CLIENT_SECRET=Qm9...
```

### 1. Get a token — `POST /oauth/token`

```sh
curl -s -u "$CLIENT_ID:$CLIENT_SECRET" -d grant_type=client_credentials $API/oauth/token
# 200 {"access_token":"x7P...","token_type":"Bearer","expires_in":3600}
TOKEN=x7P...
AUTH="Authorization: Bearer $TOKEN"

# Body credentials work too (form or JSON):
curl -s -H 'Content-Type: application/json' \
  -d "{\"grant_type\":\"client_credentials\",\"client_id\":\"$CLIENT_ID\",\"client_secret\":\"$CLIENT_SECRET\"}" \
  $API/oauth/token

curl -s -i -u "$CLIENT_ID:wrong" -d grant_type=client_credentials $API/oauth/token
# 401 {"error":"invalid_client","error_description":"Unknown client or wrong secret"}
curl -s -i -u "$CLIENT_ID:$CLIENT_SECRET" -d grant_type=password $API/oauth/token
# 400 {"error":"unsupported_grant_type",...}
```

### 2. Users

```sh
curl -s -H "$AUTH" $API/users
# 200 [{"id":"0b9e...","username":"alice"},{"id":"5d21...","username":"bob"}]

curl -s -H "$AUTH" $API/users/0b9e...     # yourself: email included
# 200 {"id":"0b9e...","username":"alice","avatar_url":"api/avatars/ab.webp","email":"alice@example.com"}
curl -s -H "$AUTH" $API/users/5d21...     # someone else: no email
# 200 {"id":"5d21...","username":"bob","avatar_url":null}

curl -s -i -X PATCH -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"username":"alice2","email":"alice2@example.com","password":"newpass123","avatar_url":"https://example.com/me.png"}' \
  $API/users/0b9e...
# 200 {"id":"0b9e...","username":"alice2","avatar_url":"https://example.com/me.png","email":"alice2@example.com"}

curl -s -i -X PATCH -H "$AUTH" -H 'Content-Type: application/json' -d '{"username":"pwned"}' $API/users/5d21...
# 403 {"error":"You can only modify your own profile"}
curl -s -i -X PATCH -H "$AUTH" -H 'Content-Type: application/json' -d '{"email":"nope"}' $API/users/0b9e...
# 400 {"error":"Validation failed","fields":{"email":"Email address is invalid"}}
```

A password change also logs the user out of the web app (like a reset); the API token stays valid.

### 3. Movies

```sh
curl -s "$API/movies?page=1&rows=5"       # no token needed
# 200 [{"id":"a3c4...","name":"Night of the Living Dead"}, ...]

curl -s -H "$AUTH" $API/movies/a3c4...
# 200 {"id":"a3c4...","name":"Night of the Living Dead","imdb_rating":7.8,"production_year":1968,
#      "length":5760,"subtitles":["en","fr"],"comments_count":2,
#      "directors":["George A. Romero"],"writers":["John A. Russo","George A. Romero"],
#      "production":"Image Ten","poster_url":"https://m.media-amazon.com/images/..."}
curl -s -i -H "$AUTH" $API/movies/00000000-0000-0000-0000-000000000000
# 404 {"error":"Movie not found"}
```

`length` is in seconds; `imdb_rating`, `production_year` and `length` are `null` when unknown.
Movie ids are the ids returned by `GET /movies` (listing caches the movie).
`subtitles` lists the languages (ISO 639-1: `en`, `fr`, `it`) of the WebVTT tracks cached for
the movie, English first. They are fetched from OpenSubtitles the first time the movie is played
in the web app (English, plus the viewer's language when the film is not originally in it), so a
never-played movie has `[]`. `directors`, `writers`, `production` and `poster_url` come from OMDb
and are empty (`[]` / `""`) when OMDb has no match.

### 4. Comments

```sh
curl -s -i -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"comment":"Great classic","movie_id":"a3c4..."}' $API/comments
# 201  Location: /api/v1/comments/91fe...
# {"id":"91fe...","comment":"Great classic","username":"alice2","movie_id":"a3c4...","date":"...","updated_at":"..."}

curl -s -i -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"comment":"Second thought"}' $API/movies/a3c4.../comments
# 201

curl -s -H "$AUTH" $API/comments                 # latest 50, newest first
curl -s -H "$AUTH" $API/movies/a3c4.../comments  # one movie
curl -s -H "$AUTH" $API/comments/91fe...
# 200 {"id":"91fe...","comment":"Great classic","username":"alice2","movie_id":"a3c4...","date":"..."}

curl -s -X PATCH -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"comment":"Great classic, edited","username":"alice2"}' $API/comments/91fe...
# 200 (username is accepted but ignored: the author is the token's user)

curl -s -i -X DELETE -H "$AUTH" $API/comments/91fe...
# 204 No Content
curl -s -i -X DELETE -H "$AUTH" $API/comments/91fe...
# 404 {"error":"Comment not found"}

# Someone else's comment:
curl -s -i -X DELETE -H "$AUTH" $API/comments/<bob-comment-id>
# 403 {"error":"You can only modify your own comments"}
curl -s -i -H "$AUTH" -H 'Content-Type: application/json' -d '{"comment":""}' $API/movies/a3c4.../comments
# 400 {"error":"Validation failed","fields":{"comment":"Comment cannot be empty"}}
```

### 5. Everything else is refused

```sh
curl -s -i $API/users                              # 401 + WWW-Authenticate: Bearer realm="hypertube"
curl -s -i -H 'Authorization: Bearer junk' $API/users   # 401 {"error":"Invalid or expired token"}
curl -s -i -X DELETE -H "$AUTH" $API/users         # 405 + Allow: GET
curl -s -i -X PUT -H "$AUTH" $API/comments/91fe... # 405 + Allow: GET, PATCH, DELETE
curl -s -i -H "$AUTH" $API/nothing                 # 404 {"error":"Not found"}
# A session cookie alone does not authenticate the API:
curl -s -i -b jar $API/users                       # 401
```

## Why it is RESTful

- **Resources, not actions**: nouns in the URL (`/users`, `/movies`, `/comments`,
  `/movies/:id/comments`); the HTTP method is the verb (GET read, POST create, PATCH partial
  update, DELETE remove). No `/getUser` or `/deleteComment` style endpoints.
- **Correct status codes**: 200 read/update, 201 + `Location` on create, 204 on delete, 400
  invalid input, 401 missing/invalid token (with `WWW-Authenticate`), 403 someone else's resource,
  404 unknown resource or path, 405 + `Allow` for unsupported methods, 409 conflict, 502 upstream
  provider failure.
- **Stateless**: every request carries its credentials (bearer token); no server-side session or
  cookie is involved.
- **Uniform representations**: JSON in and out; errors are always `{"error": "..."}` (plus
  `fields` for validation), OAuth errors follow RFC 6749 section 5.2.
- **Safe and idempotent methods behave as such**: GET never changes data (except caching
  listed movie metadata), PATCH with the same body gives the same result, DELETE twice gives
  204 then 404 with no further change.
- **Cache-aware**: responses carry `Cache-Control: no-store` since they are per-user.
