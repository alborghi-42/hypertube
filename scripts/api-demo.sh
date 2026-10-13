#!/usr/bin/env bash
# End-to-end walkthrough of the public REST API (/api/v1) for the defense.
#
#   make seed && make api-demo
#   BASE_URL=https://42.bombatomica64.dev BASE_PATH=/hypertube scripts/api-demo.sh
#
# Every step prints METHOD PATH -> status and checks it against the expected
# status; the script exits 1 if any step differs. It needs the demo users from
# scripts/seed.sh (alice and bob) and at least one movie on the front page
# (the archive.org / publicdomaintorrents providers must be reachable).
#
# It creates a throwaway API client for alice and for bob (deleted at the end),
# posts comments and cleans up after itself. Set VERBOSE=1 to print bodies.
#
# The library steps (42-27) add a movie found with GET /movies?q=$DEMO_SEARCH
# that is not in the library yet, and delete only that movie (never played,
# so it has no download). If every result is already in the library, the
# add/delete steps are skipped. Watch-history steps restore alice's history.
set -euo pipefail
# shellcheck source=scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

FAILS=0
STEP=0
VERBOSE=${VERBOSE:-0}

# call <expected-status> <description> <curl args...>: runs curl against
# $API_V1 (the last argument is the path), prints the result and asserts the
# status. The response body is left in $KIT_TMP/body for jq.
LAST_STATUS=""

call() {
  local want=$1 desc=$2 got method=GET path
  shift 2
  path=${*: -1}
  local args=("${@:1:$#-1}")
  local i
  for ((i = 0; i < ${#args[@]}; i++)); do
    if [[ ${args[$i]} == -X ]]; then method=${args[$((i + 1))]}; fi
    if [[ ${args[$i]} == -d && $method == GET ]]; then method=POST; fi
  done
  STEP=$((STEP + 1))
  got=$(curl -s -o "$KIT_TMP/body" -D "$KIT_TMP/headers" -w '%{http_code}' "${args[@]}" "$API_V1$path") \
    || die "cannot reach $API_V1$path"
  LAST_STATUS=$got
  if [[ $got == "$want" ]]; then
    printf '%s%2d. PASS%s %-6s %-48s -> %s  %s\n' "$c_green" "$STEP" "$c_off" "$method" "$path" "$got" "$desc"
  else
    printf '%s%2d. FAIL%s %-6s %-48s -> %s (expected %s)  %s\n' "$c_red" "$STEP" "$c_off" "$method" "$path" "$got" "$want" "$desc"
    printf '      %s\n' "$(head -c 300 "$KIT_TMP/body")"
    FAILS=$((FAILS + 1))
  fi
  if [[ $VERBOSE == 1 ]]; then printf '      %s%s%s\n' "$c_dim" "$(head -c 400 "$KIT_TMP/body")" "$c_off"; fi
}

# check <description> <jq filter>: asserts a jq predicate on the last body.
check() {
  STEP=$((STEP + 1))
  if jq -e "$2" "$KIT_TMP/body" >/dev/null 2>&1; then
    printf '%s%2d. PASS%s        %s\n' "$c_green" "$STEP" "$c_off" "$1"
  else
    printf '%s%2d. FAIL%s        %s\n' "$c_red" "$STEP" "$c_off" "$1"
    printf '      %s\n' "$(head -c 300 "$KIT_TMP/body")"
    FAILS=$((FAILS + 1))
  fi
}

CT="-HContent-Type: application/json" # attached short option, one argv word

log "Hypertube API at $API_V1"
log "Setup: logging in as alice and bob, creating one API client each"
login_user alice
ALICE_JAR=$JAR
create_client "$ALICE_JAR" "api-demo"
ALICE_CLIENT=$CLIENT_ID
ALICE_SECRET=$CLIENT_SECRET
login_user bob
BOB_JAR=$JAR
create_client "$BOB_JAR" "api-demo"
BOB_CLIENT=$CLIENT_ID
get_token "$BOB_CLIENT" "$CLIENT_SECRET"
BOB_AUTH="Authorization: Bearer $TOKEN"

DEMO_SEARCH=${DEMO_SEARCH:-night}
FRESH_ID="" # a movie this run added (201), deleted at the end

# exists <movie id>: 0 if GET /movies/:id answers 200 (not a counted step).
exists() {
  [[ $(curl -s -o /dev/null -w '%{http_code}' -H "$AUTH" "$API_V1/movies/$1") == 200 ]]
}

cleanup_clients() {
  if [[ -n $FRESH_ID ]]; then
    curl -s -o /dev/null -X DELETE -H "$AUTH" "$API_V1/movies/$FRESH_ID" || true
  fi
  session_req "$ALICE_JAR" DELETE "/api/auth/clients/$ALICE_CLIENT" || true
  session_req "$BOB_JAR" DELETE "/api/auth/clients/$BOB_CLIENT" || true
}

echo
echo "== OAuth2 client credentials"
call 401 "wrong secret -> invalid_client" -u "$ALICE_CLIENT:wrong" -d grant_type=client_credentials /oauth/token
call 400 "unsupported grant type" -u "$ALICE_CLIENT:$ALICE_SECRET" -d grant_type=password /oauth/token
call 200 "token via HTTP Basic" -u "$ALICE_CLIENT:$ALICE_SECRET" -d grant_type=client_credentials /oauth/token
check "token_type is Bearer, expires_in > 0" '.token_type == "Bearer" and .expires_in > 0 and (.access_token | length) > 0'
TOKEN=$(jq -r .access_token "$KIT_TMP/body")
AUTH="Authorization: Bearer $TOKEN"
call 200 "token via JSON body" "$CT" \
  -d "$(jq -nc --arg i "$ALICE_CLIENT" --arg s "$ALICE_SECRET" '{grant_type:"client_credentials",client_id:$i,client_secret:$s}')" \
  /oauth/token

echo
echo "== Users"
call 200 "list users (id + username)" -H "$AUTH" /users
check "every user has only id and username" 'type == "array" and length >= 2 and all(.[]; (keys | sort) == ["id","username"])'
ALICE_ID=$(jq -r '.[] | select(.username == "alice") | .id' "$KIT_TMP/body")
BOB_ID=$(jq -r '.[] | select(.username == "bob") | .id' "$KIT_TMP/body")
[[ -n $ALICE_ID && -n $BOB_ID ]] || { cleanup_clients; die "alice/bob not in GET /users: run scripts/seed.sh"; }
call 200 "own profile" -H "$AUTH" "/users/$ALICE_ID"
check "own profile includes the email" '.username == "alice" and (.email | type) == "string" and has("avatar_url")'
call 200 "someone else's profile" -H "$AUTH" "/users/$BOB_ID"
check "other profile hides the email" '.username == "bob" and (has("email") | not) and has("avatar_url")'
call 404 "unknown user" -H "$AUTH" /users/00000000-0000-0000-0000-000000000000
call 403 "PATCH another user's profile" -X PATCH -H "$AUTH" "$CT" -d '{"username":"pwned"}' "/users/$BOB_ID"
call 400 "PATCH own profile with an invalid email" -X PATCH -H "$AUTH" "$CT" -d '{"email":"nope"}' "/users/$ALICE_ID"

echo
echo "== Movies"
call 200 "front page without a token" "/movies?page=1&rows=5"
check "list of {id, name}" 'type == "array" and length > 0 and all(.[]; has("id") and has("name"))'
MOVIE_ID=$(jq -r '.[0].id // empty' "$KIT_TMP/body")
MOVIE_NAME=$(jq -r '.[0].name // empty' "$KIT_TMP/body")
MOVIE_PROVIDER=$(jq -r '.[0].provider // empty' "$KIT_TMP/body")
MOVIE_EXTERNAL=$(jq -r '.[0].external_id // empty' "$KIT_TMP/body")
[[ -n $MOVIE_ID ]] || { cleanup_clients; die "front page is empty: are archive.org / publicdomaintorrents reachable?"; }
call 200 "movie details: $MOVIE_NAME" -H "$AUTH" "/movies/$MOVIE_ID"
check "name, id, imdb_rating, production_year, length, subtitles, comments_count" \
  'has("id") and has("name") and has("imdb_rating") and has("production_year") and has("length") and (.subtitles | type) == "array" and (.comments_count | type) == "number"'
call 404 "unknown movie" -H "$AUTH" /movies/00000000-0000-0000-0000-000000000000

echo
echo "== Comments"
call 201 "POST /comments {comment, movie_id}" -H "$AUTH" "$CT" \
  -d "$(jq -nc --arg m "$MOVIE_ID" '{comment:"api-demo: posted via /comments", movie_id:$m}')" /comments
C1=$(jq -r .id "$KIT_TMP/body")
check "author is the token owner" '.username == "alice" and .movie_id != null'
call 201 "POST /movies/:id/comments {comment}" -H "$AUTH" "$CT" \
  -d '{"comment":"api-demo: posted via /movies/:id/comments"}' "/movies/$MOVIE_ID/comments"
C2=$(jq -r .id "$KIT_TMP/body")
call 201 "bob comments the same movie" -H "$BOB_AUTH" "$CT" \
  -d '{"comment":"api-demo: bob was here"}' "/movies/$MOVIE_ID/comments"
CB=$(jq -r .id "$KIT_TMP/body")
call 400 "empty comment is rejected" -H "$AUTH" "$CT" -d '{"comment":""}' "/movies/$MOVIE_ID/comments"
call 200 "latest comments" -H "$AUTH" /comments
check "items have id, comment, username, date" 'type == "array" and all(.[]; has("id") and has("comment") and has("username") and has("date"))'
call 200 "comments of the movie" -H "$AUTH" "/movies/$MOVIE_ID/comments"
call 200 "one comment" -H "$AUTH" "/comments/$C1"
check "id, comment, username, date" '.id != null and .comment != null and .username == "alice" and .date != null'
call 200 "PATCH own comment" -X PATCH -H "$AUTH" "$CT" \
  -d '{"comment":"api-demo: edited","username":"alice"}' "/comments/$C1"
check "comment updated" '.comment == "api-demo: edited"'
call 403 "PATCH bob's comment as alice" -X PATCH -H "$AUTH" "$CT" -d '{"comment":"hijacked"}' "/comments/$CB"
call 403 "DELETE bob's comment as alice" -X DELETE -H "$AUTH" "/comments/$CB"
call 204 "DELETE own comment" -X DELETE -H "$AUTH" "/comments/$C1"
call 404 "DELETE it again" -X DELETE -H "$AUTH" "/comments/$C1"
call 204 "cleanup: DELETE own second comment" -X DELETE -H "$AUTH" "/comments/$C2"
call 204 "cleanup: bob deletes his comment" -X DELETE -H "$BOB_AUTH" "/comments/$CB"

echo
echo "== Library: search, add, delete (bonus)"
call 200 "search: q=$DEMO_SEARCH, no token" "/movies?q=$DEMO_SEARCH&rows=20"
check "results carry provider + external_id" 'type == "array" and all(.[]; has("id") and has("provider") and has("external_id"))'
cp "$KIT_TMP/body" "$KIT_TMP/search"
FRESH_PROVIDER="" FRESH_EXTERNAL=""
while IFS=$'\t' read -r id prov ext; do
  if [[ -n $id ]] && ! exists "$id"; then FRESH_PROVIDER=$prov FRESH_EXTERNAL=$ext; break; fi
done < <(jq -r '.[] | [.id, .provider, .external_id] | @tsv' "$KIT_TMP/search")
call 400 "add: unknown provider" -H "$AUTH" "$CT" -d '{"provider":"nope","external_id":"x"}' /movies
check "field error on provider" '.fields.provider != null'
call 400 "add: missing external_id" -H "$AUTH" "$CT" -d "$(jq -nc --arg p "$MOVIE_PROVIDER" '{provider:$p}')" /movies
call 400 "add: external_id with a path" -H "$AUTH" "$CT" \
  -d "$(jq -nc --arg p "$MOVIE_PROVIDER" '{provider:$p, external_id:"../etc"}')" /movies
call 404 "add: no such item at archive.org" -H "$AUTH" "$CT" \
  -d '{"provider":"archive.org","external_id":"api-demo-no-such-item-0000"}' /movies
call 200 "add: front-page movie is already in the library" -H "$AUTH" "$CT" \
  -d "$(jq -nc --arg p "$MOVIE_PROVIDER" --arg e "$MOVIE_EXTERNAL" '{provider:$p, external_id:$e}')" /movies
check "same id as the front page" ".id == \"$MOVIE_ID\""
call 401 "add without a token" "$CT" -d '{"provider":"archive.org","external_id":"x"}' /movies
call 404 "DELETE an unknown movie" -X DELETE -H "$AUTH" /movies/00000000-0000-0000-0000-000000000000
if [[ -n $FRESH_EXTERNAL ]]; then
  call 201 "add: $FRESH_PROVIDER/$FRESH_EXTERNAL" -H "$AUTH" "$CT" \
    -d "$(jq -nc --arg p "$FRESH_PROVIDER" --arg e "$FRESH_EXTERNAL" '{provider:$p, external_id:$e}')" /movies
  if [[ $LAST_STATUS == 201 ]]; then FRESH_ID=$(jq -r .id "$KIT_TMP/body"); fi
fi
if [[ -n $FRESH_ID ]]; then
  STEP=$((STEP + 1))
  if grep -qi "^location: .*/api/v1/movies/$FRESH_ID" "$KIT_TMP/headers"; then
    printf '%s%2d. PASS%s        Location: /api/v1/movies/%s\n' "$c_green" "$STEP" "$c_off" "$FRESH_ID"
  else
    printf '%s%2d. FAIL%s        Location header missing\n' "$c_red" "$STEP" "$c_off"
    FAILS=$((FAILS + 1))
  fi
  call 200 "add it again: idempotent" -H "$AUTH" "$CT" \
    -d "$(jq -nc --arg p "$FRESH_PROVIDER" --arg e "$FRESH_EXTERNAL" '{provider:$p, external_id:$e}')" /movies
  call 200 "it resolves now" -H "$AUTH" "/movies/$FRESH_ID"
elif [[ -z $FRESH_EXTERNAL ]]; then
  echo "      (every q=$DEMO_SEARCH result is already in the library: add/delete steps skipped)"
fi

echo
echo "== Watch history (bonus)"
W_ID=${FRESH_ID:-$MOVIE_ID}
WAS_WATCHED=0
call 200 "own watch history" -H "$AUTH" "/users/$ALICE_ID/watched?rows=100"
check "items have movie_id, name, watched_at" 'type == "array" and all(.[]; has("movie_id") and has("name") and has("watched_at"))'
if jq -e --arg m "$W_ID" 'any(.[]; .movie_id == $m)' "$KIT_TMP/body" >/dev/null; then WAS_WATCHED=1; fi
call 403 "bob's watch history as alice" -H "$AUTH" "/users/$BOB_ID/watched"
call 400 "invalid rows parameter" -H "$AUTH" "/users/$ALICE_ID/watched?rows=0"
call 204 "mark as watched" -X POST -H "$AUTH" "/movies/$W_ID/watched"
call 204 "mark again: idempotent" -X POST -H "$AUTH" "/movies/$W_ID/watched"
call 404 "mark an unknown movie" -X POST -H "$AUTH" /movies/00000000-0000-0000-0000-000000000000/watched
call 200 "history page 1, 1 row" -H "$AUTH" "/users/$ALICE_ID/watched?page=1&rows=1"
check "most recent first: the movie just marked" ".[0].movie_id == \"$W_ID\""
call 204 "unmark" -X DELETE -H "$AUTH" "/movies/$W_ID/watched"
call 404 "unmark again: not in history" -X DELETE -H "$AUTH" "/movies/$W_ID/watched"
if ((WAS_WATCHED)); then
  call 204 "restore: alice had watched it before" -X POST -H "$AUTH" "/movies/$W_ID/watched"
fi

if [[ -n $FRESH_ID ]]; then
  echo
  echo "== Library: delete policy (bonus)"
  call 201 "bob comments the added movie" -H "$BOB_AUTH" "$CT" -d '{"comment":"api-demo: keep it"}' "/movies/$FRESH_ID/comments"
  CF=$(jq -r .id "$KIT_TMP/body")
  call 409 "DELETE a movie someone else commented" -X DELETE -H "$AUTH" "/movies/$FRESH_ID"
  call 204 "bob deletes his comment" -X DELETE -H "$BOB_AUTH" "/comments/$CF"
  call 201 "alice comments it (her own comments cascade)" -H "$AUTH" "$CT" -d '{"comment":"api-demo: mine"}' "/movies/$FRESH_ID/comments"
  CA=$(jq -r .id "$KIT_TMP/body")
  call 204 "alice marks it watched (cascades too)" -X POST -H "$AUTH" "/movies/$FRESH_ID/watched"
  call 204 "DELETE the movie this run added" -X DELETE -H "$AUTH" "/movies/$FRESH_ID"
  call 404 "it is gone" -H "$AUTH" "/movies/$FRESH_ID"
  call 404 "its comment went with it" -H "$AUTH" "/comments/$CA"
  call 404 "DELETE it again" -X DELETE -H "$AUTH" "/movies/$FRESH_ID"
  FRESH_ID=""
fi

echo
echo "== Everything else is refused"
call 401 "missing token" /users
grep -qi '^www-authenticate: *bearer' "$KIT_TMP/headers" && echo "      WWW-Authenticate: Bearer present" \
  || { echo "      ${c_red}WWW-Authenticate header missing${c_off}"; FAILS=$((FAILS + 1)); }
call 401 "invalid token" -H "Authorization: Bearer junk" /users
call 401 "session cookie alone does not authenticate the API" -b "$ALICE_JAR" /users
call 404 "unknown route" -H "$AUTH" /nothing
call 405 "wrong method on /users" -X DELETE -H "$AUTH" /users
echo "      Allow: $(awk -F': *' 'tolower($1)=="allow"{print $2}' "$KIT_TMP/headers" | tr -d '\r')"
call 405 "wrong method on /comments/:id" -X PUT -H "$AUTH" "$CT" -d '{}' "/comments/$C2"

cleanup_clients

echo
if ((FAILS > 0)); then
  echo "${c_red}$FAILS of $STEP checks failed${c_off}"
  exit 1
fi
echo "${c_green}All $STEP checks passed${c_off}"
