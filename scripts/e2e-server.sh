#!/usr/bin/env bash
# Prepare an isolated end-to-end environment and run the Go application on it.
#
#   scripts/e2e-server.sh            # build if needed, init, serve on 127.0.0.1:4199
#   RKA_E2E_BUILD=1 scripts/e2e-server.sh
#
# The database is recreated on every run; the data root lives under ./data/e2e so
# nothing leaks outside the project. Playwright starts this script as its
# webServer.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

port="${RKA_E2E_PORT:-4199}"
db="${RKA_E2E_DB:-rka_e2e}"
data_dir="${RKA_E2E_DATA_DIR:-$root/data/e2e}"
db_container="${RKA_TEST_DB_CONTAINER:-rka-dev-postgres}"
db_port="${RKA_TEST_DB_PORT:-55432}"
db_password="${RKA_TEST_DB_PASSWORD:-devpassword}"
admin_email="${RKA_E2E_ADMIN_EMAIL:-admin@example.com}"
admin_password="${RKA_E2E_ADMIN_PASSWORD:-rka-e2e-admin-password}"
admin_name="${RKA_E2E_ADMIN_NAME:-端到端管理员}"

if [ "$(docker inspect -f '{{.State.Running}}' "$db_container" 2>/dev/null || true)" != "true" ]; then
  echo "development database is not running; start it with scripts/dev-db.sh" >&2
  exit 1
fi

psql_exec() {
  docker exec -i "$db_container" psql -U rka -d postgres -v ON_ERROR_STOP=1 -c "$1" >/dev/null
}

echo "==> recreating database $db"
psql_exec "drop database if exists $db with (force)"
psql_exec "create database $db"

echo "==> preparing data root $data_dir"
mkdir -p "$data_dir"
rm -rf "$data_dir"/e2e
mkdir -p "$data_dir"/e2e

export RKA_DATABASE_URL="postgres://rka:${db_password}@127.0.0.1:${db_port}/${db}?sslmode=disable"
export RKA_DATA_DIR="$data_dir"
export RKA_STATIC_DIR="$root/frontend/dist"
export RKA_CONTENT_SNAPSHOT="$root/content"
export RKA_SECRET_KEY_FILE="$data_dir/secrets/secret-key"
export RKA_ADDR="127.0.0.1:${port}"
export RKA_ADMIN_EMAIL="$admin_email"
export RKA_ADMIN_PASSWORD="$admin_password"
export RKA_ADMIN_DISPLAY_NAME="$admin_name"
export RKA_LOG_LEVEL="${RKA_E2E_LOG_LEVEL:-warn}"
export RKA_LOG_FORMAT=text
export RKA_WORKER_INLINE=true
export RKA_COOKIE_SECURE=never

if [ "${RKA_E2E_BUILD:-0}" = "1" ] || [ ! -f "$root/frontend/dist/index.html" ]; then
  echo "==> building browser bundle"
  npm --prefix "$root/frontend" run build
fi

echo "==> building application binary"
go -C "$root/backend" build -o "$data_dir/rka" ./cmd/rka

echo "==> initialising schema and content"
"$data_dir/rka" init

echo "==> serving on http://127.0.0.1:${port}"
exec "$data_dir/rka" serve
