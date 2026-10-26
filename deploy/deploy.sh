#!/usr/bin/env bash
# Brings the server up to date with main, run by hand on the server (no CI here, the owner's
# call): this checkout, then every image rebuilt from it, then the stack restarted where it
# changed.
#
#   /srv/shafaq/api-geo-shafaq/deploy/deploy.sh
#
# The migrations run before the new API starts (compose: migrate). Players notice a few
# seconds' pause at most; the game state lives in the database, not in the API.
set -euo pipefail
cd "$(dirname "$0")/.."

git fetch -q origin main
git reset -q --hard origin/main # the server never edits its checkout

export VERSION=$(git rev-parse --short HEAD)
docker compose build -q
docker compose pull -q --ignore-buildable
docker compose up -d --remove-orphans --wait --wait-timeout 300
docker image prune -f >/dev/null

curl -fsS -m 10 "https://$(sed -n 's/^API_DOMAIN=//p' .env)/v1/ready"
echo
