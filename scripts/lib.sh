# shellcheck shell=bash
# Shared helpers for seed.sh and api-demo.sh (sourced, not executed).
# Requires: bash, curl, jq.

BASE_URL=${BASE_URL:-http://localhost:8080}
BASE_PATH=${BASE_PATH:-}
# BASE_PATH ("/hypertube") is appended unless BASE_URL already ends with it.
BASE_PATH=${BASE_PATH%/}
if [[ -n $BASE_PATH && $BASE_PATH != /* ]]; then BASE_PATH=/$BASE_PATH; fi
BASE_URL=${BASE_URL%/}
if [[ -n $BASE_PATH && $BASE_URL != *"$BASE_PATH" ]]; then BASE_URL=$BASE_URL$BASE_PATH; fi
API_V1=$BASE_URL/api/v1

# Demo accounts: username:password:email:first:last. Passwords satisfy the
# backend validator (8-128 chars, at least one letter and one digit).
DEMO_USERS=(
  "alice:Alice-demo-42:alice@example.com:Alice:Liddell"
  "bob:Bob-demo-42:bob@example.com:Bob:Marley"
  "carol:Carol-demo-42:carol@example.com:Carol:Danvers"
)

for bin in curl jq; do
  command -v "$bin" >/dev/null 2>&1 || { echo "error: $bin is required" >&2; exit 2; }
done

KIT_TMP=$(mktemp -d)
trap 'rm -rf "$KIT_TMP"' EXIT

if [[ -t 2 ]]; then
  c_red=$'\e[31m'; c_green=$'\e[32m'; c_dim=$'\e[2m'; c_off=$'\e[0m'
else
  c_red=; c_green=; c_dim=; c_off=
fi

log() { printf '%s\n' "$*" >&2; }
die() { log "${c_red}error:${c_off} $*"; exit 1; }

# demo_field <username> <index>: field of DEMO_USERS
# (0 username, 1 password, 2 email, 3 first name, 4 last name).
demo_field() {
  local entry f
  for entry in "${DEMO_USERS[@]}"; do
    IFS=: read -r -a f <<<"$entry"
    if [[ ${f[0]} == "$1" ]]; then printf '%s' "${f[$2]}"; return 0; fi
  done
  return 1
}

# xsrf <jar>: current XSRF-TOKEN value from a Netscape cookie jar.
xsrf() { awk '$6=="XSRF-TOKEN"{v=$7} END{print v}' "$1"; }

# session_req <jar> <method> <path> [json-body]: request to the session API
# (/api/...) with the CSRF double submit: the XSRF-TOKEN cookie is echoed in
# the X-XSRF-TOKEN header. The jar is updated (login rotates the token).
# Writes the body to $KIT_TMP/body and the status to $KIT_TMP/status.
session_req() {
  local jar=$1 method=$2 path=$3 body=${4:-}
  local args=(-s -o "$KIT_TMP/body" -w '%{http_code}' -b "$jar" -c "$jar" -X "$method"
    -H "X-XSRF-TOKEN: $(xsrf "$jar")" -H 'Accept: application/json')
  if [[ -n $body ]]; then args+=(-H 'Content-Type: application/json' --data-binary "$body"); fi
  curl "${args[@]}" "$BASE_URL$path" >"$KIT_TMP/status" || die "cannot reach $BASE_URL$path"
}
status() { cat "$KIT_TMP/status"; }
body() { cat "$KIT_TMP/body"; }

# new_jar <name>: fresh cookie jar primed with an XSRF-TOKEN (any GET below
# /api hands it out). Sets JAR.
new_jar() {
  JAR=$KIT_TMP/$1.jar
  : >"$JAR"
  curl -s -o /dev/null -b "$JAR" -c "$JAR" "$BASE_URL/api/ping" || die "cannot reach $BASE_URL/api/ping"
  [[ -n $(xsrf "$JAR") ]] || die "no XSRF-TOKEN cookie from $BASE_URL/api/ping (wrong BASE_URL / BASE_PATH?)"
}

# register_user <username>: POST /api/auth/register, the real endpoint, so the
# password is hashed (argon2id) by the app. 201 created, 409 already exists.
register_user() {
  local u=$1 payload
  new_jar "reg-$u"
  payload=$(jq -nc --arg u "$u" --arg p "$(demo_field "$u" 1)" --arg e "$(demo_field "$u" 2)" \
    --arg f "$(demo_field "$u" 3)" --arg l "$(demo_field "$u" 4)" \
    '{username:$u, password:$p, email:$e, first_name:$f, last_name:$l}')
  session_req "$JAR" POST /api/auth/register "$payload"
  case $(status) in
    201) log "  ${c_green}201${c_off} registered $u" ;;
    409) log "  ${c_dim}409${c_off} $u already exists: $(jq -r '.error // empty' "$KIT_TMP/body")" ;;
    429) log "  429 register rate limit (10/hour per IP): assuming $u exists" ;;
    *) die "register $u: HTTP $(status) $(body)" ;;
  esac
}

# login_user <username>: logs in with the demo password. Sets JAR to the
# session cookie jar (ht_session + rotated XSRF-TOKEN).
login_user() {
  local u=$1
  new_jar "session-$u"
  session_req "$JAR" POST /api/auth/login \
    "$(jq -nc --arg u "$u" --arg p "$(demo_field "$u" 1)" '{username:$u, password:$p}')"
  [[ $(status) == 200 ]] || die "login $u: HTTP $(status) $(body) (password changed? see docs/defense.md)"
}

# create_client <jar> <name>: POST /api/auth/clients. Sets CLIENT_ID and
# CLIENT_SECRET (the secret is only ever returned here).
create_client() {
  session_req "$1" POST /api/auth/clients "$(jq -nc --arg n "$2" '{name:$n}')"
  [[ $(status) == 201 ]] || die "create API client: HTTP $(status) $(body)"
  CLIENT_ID=$(jq -r .client_id "$KIT_TMP/body")
  CLIENT_SECRET=$(jq -r .client_secret "$KIT_TMP/body")
}

# get_token <client_id> <client_secret>: client-credentials grant. Sets TOKEN.
get_token() {
  local code
  code=$(curl -s -o "$KIT_TMP/body" -w '%{http_code}' -u "$1:$2" \
    -d grant_type=client_credentials "$API_V1/oauth/token") || die "cannot reach $API_V1/oauth/token"
  [[ $code == 200 ]] || die "token: HTTP $code $(body)"
  TOKEN=$(jq -r .access_token "$KIT_TMP/body")
}
