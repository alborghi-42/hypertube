#!/usr/bin/env bash
# Seed the demo accounts for a Hypertube defense and print an API client.
#
#   BASE_URL=http://localhost:8080 scripts/seed.sh      (make seed)
#   BASE_URL=https://42.bombatomica64.dev BASE_PATH=/hypertube scripts/seed.sh
#
# Users are created through POST /api/auth/register (never raw SQL) so the
# passwords go through the app's validator and argon2id hashing. Re-running is
# safe: existing users answer 409 and are skipped. Then it logs in as alice
# (cookie jar + XSRF double submit) and creates an OAuth2 client for /api/v1.
#
# Demo credentials (see docs/defense.md):
#   alice / Alice-demo-42    bob / Bob-demo-42    carol / Carol-demo-42
set -euo pipefail
# shellcheck source=scripts/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

log "Hypertube at $BASE_URL"
log "Registering demo users"
for entry in "${DEMO_USERS[@]}"; do
  register_user "${entry%%:*}"
done

log "Logging in as alice and creating an API client"
login_user alice
create_client "$JAR" "evaluation $(date +%Y-%m-%dT%H:%M:%S)"

cat <<EOF

Demo users (username / password):
EOF
for entry in "${DEMO_USERS[@]}"; do
  IFS=: read -r u p _ <<<"$entry"
  printf '  %-6s / %s\n' "$u" "$p"
done
cat <<EOF

API client for alice (the secret is shown only once):
  CLIENT_ID=$CLIENT_ID
  CLIENT_SECRET=$CLIENT_SECRET

Get a token:
  curl -s -u "\$CLIENT_ID:\$CLIENT_SECRET" -d grant_type=client_credentials $API_V1/oauth/token
EOF
