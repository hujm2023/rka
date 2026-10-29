#!/usr/bin/env bash
# Start the development PostgreSQL used by Go integration tests.
#
#   scripts/dev-db.sh          start (idempotent)
#   scripts/dev-db.sh stop     stop and remove the container (data is kept)
#
# The data directory lives inside the project so nothing leaks outside it.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
name="rka-dev-postgres"
port="${RKA_TEST_DB_PORT:-55432}"
# Backend Go commands now walk only backend/, so test persistence can remain
# under the project data root. An already-created container keeps its mount.
data_dir="${RKA_TEST_DB_DATA_DIR:-$root/data/postgres-dev}"
password="${RKA_TEST_DB_PASSWORD:-devpassword}"

case "${1:-start}" in
  stop)
    docker rm -f "$name" >/dev/null 2>&1 || true
    echo "stopped $name"
    exit 0
    ;;
  start) ;;
  *)
    echo "usage: $0 [start|stop]" >&2
    exit 2
    ;;
esac

mkdir -p "$data_dir"
if docker ps -a --format '{{.Names}}' | grep -qx "$name"; then
  docker start "$name" >/dev/null
else
  docker run -d --name "$name" \
    -e POSTGRES_USER=rka \
    -e POSTGRES_PASSWORD="$password" \
    -e POSTGRES_DB=rka \
    -e PGDATA=/var/lib/postgresql/data/pgdata \
    -p "127.0.0.1:${port}:5432" \
    -v "$data_dir:/var/lib/postgresql/data" \
    postgres:17.6-alpine >/dev/null
fi

for _ in $(seq 1 60); do
  if docker exec "$name" pg_isready -U rka -d rka >/dev/null 2>&1; then
    echo "postgres ready on 127.0.0.1:${port} (user rka, database rka)"
    echo "export RKA_TEST_DATABASE_URL='postgres://rka:${password}@127.0.0.1:${port}/postgres?sslmode=disable'"
    exit 0
  fi
  sleep 1
done
echo "postgres did not become ready" >&2
exit 1
