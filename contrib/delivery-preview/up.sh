#!/usr/bin/env bash
# Bring up an isolated Gitea delivery preview and print an admin token.
# Reuses the repo's already-built ./gitea binary; nothing is compiled in Docker.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
project="${COMPOSE_PROJECT_NAME:-gitea-preview}"
admin_user="${PREVIEW_ADMIN:-admin}"
admin_pass="${PREVIEW_PASSWORD:-preview1234}"
url="${PREVIEW_URL:-http://127.0.0.1:3001}"

[ -x "$repo/gitea" ] || { echo "build the binary first: make build" >&2; exit 1; }

cd "$here"
cp "$repo/gitea" ./gitea

if [ ! -f app.ini ]; then
  SECRET_KEY="$("$repo/gitea" generate secret SECRET_KEY)" \
  INTERNAL_TOKEN="$("$repo/gitea" generate secret INTERNAL_TOKEN)" \
    envsubst '$SECRET_KEY $INTERNAL_TOKEN' < app.ini.tmpl > app.ini
fi

compose() { RUNNER_TOKEN="${RUNNER_TOKEN:-unset}" docker compose -p "$project" "$@"; }

compose build gitea
compose up -d db
docker run --rm -v "${project}_data:/data" -v "$here/app.ini:/seed/app.ini:ro" alpine:3.22 \
  sh -c 'mkdir -p /data/gitea/custom/conf /data/git/repositories &&
         cp /seed/app.ini /data/gitea/custom/conf/app.ini && chown -R 1000:1000 /data'
compose up -d gitea

for _ in $(seq 1 60); do
  [ "$(curl -s -o /dev/null -w '%{http_code}' "$url/")" = "200" ] && break
  sleep 1
done

compose exec -T gitea gitea admin user create \
  --admin --username "$admin_user" --password "$admin_pass" \
  --email "$admin_user@example.test" --must-change-password=false >/dev/null 2>&1 || true

token="$(compose exec -T gitea gitea admin user generate-access-token \
  --username "$admin_user" --scopes all --raw --token-name "preview-$(date +%s)")"

# The approval gate holds at task assignment, so a runner has to exist for a held deploy
# to appear at all.
RUNNER_TOKEN="$(compose exec -T gitea gitea actions generate-runner-token | tr -d '\r\n')"
export RUNNER_TOKEN
docker compose -p "$project" --profile runner up -d runner

echo
echo "preview: $url/  ($admin_user / $admin_pass)"
echo "token:   $token"
echo
echo "seed it: ./seed.py --token $token"
