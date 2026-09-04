#!/usr/bin/env bash
# Destroy the preview stack and every volume it owns, then bring it back empty.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project="${COMPOSE_PROJECT_NAME:-gitea-preview}"
cd "$here"
RUNNER_TOKEN=unset docker compose -p "$project" --profile runner down -v
rm -f app.ini
exec ./up.sh
