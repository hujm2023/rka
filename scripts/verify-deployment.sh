#!/usr/bin/env bash
# Full Compose acceptance drill for the RKA platform.
#
# Verifies the spec's deployment matrix:
#   clean environment → init (idempotent) → ready → login → content → practice
#   → container rebuild keeps data → consistency backup → isolated verify
#   → restore into a new data root → second instance serves the restored data.
#
# It uses its own data directory (./data-verify) and Compose project name, so it
# never touches ./data or a running deployment. Add RKA_VERIFY_KEEP=1 to keep
# the drill environment for inspection.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

port="${RKA_VERIFY_PORT:-8099}"
restore_port="${RKA_VERIFY_RESTORE_PORT:-8098}"
pg_restore_port="${RKA_VERIFY_PG_PORT:-55434}"
data_dir="${RKA_DATA_HOST_DIR:-$root/data-verify}"
admin_email="verify-admin@example.com"
admin_password="${RKA_VERIFY_ADMIN_PASSWORD:-verify-admin-password}"
db_password="${RKA_VERIFY_DB_PASSWORD:-verify-db-password}"
project="${COMPOSE_PROJECT_NAME:-rka-verify}"
image_tag="${RKA_VERIFY_IMAGE_TAG:-verify}"
image="rka:${image_tag}"
compose() { docker compose -p "$project" -f docker-compose.yml "$@"; }
step() { printf '\n==> %s\n' "$*"; }
fail() { printf '\nFAILED: %s\n' "$*" >&2; exit 1; }
json() { python3 -c "import json,sys;d=json.load(sys.stdin);$1"; }

cleanup() {
  if [ "${RKA_VERIFY_KEEP:-0}" = "1" ]; then
    echo "kept verification environment (project $project, data $data_dir)"
    return
  fi
  docker rm -f rka-verify-app2 >/dev/null 2>&1 || true
  docker rm -f "rka-verify-pg2" >/dev/null 2>&1 || true
  compose down --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

export POSTGRES_PASSWORD="$db_password"
export RKA_DATA_HOST_DIR="$data_dir"
export RKA_PORT="$port"
export RKA_IMAGE_TAG="$image_tag"
export RKA_ADMIN_EMAIL="$admin_email"
export RKA_ADMIN_PASSWORD="$admin_password"
export RKA_ADMIN_DISPLAY_NAME="部署验收管理员"
export RKA_LOG_LEVEL=warn
export RKA_LOG_FORMAT=text

step "0. clean verification directories"
rm -rf "$data_dir"
mkdir -p "$data_dir"

step "1. build and start PostgreSQL"
compose build >/dev/null
compose up -d postgres
for _ in $(seq 1 60); do
  if docker exec "${project}-postgres-1" pg_isready -U rka -d rka >/dev/null 2>&1; then break; fi
  sleep 1
done

step "2. first initialisation (dirs, secrets, schema, content, admin)"
compose run --rm init
step "3. second initialisation must be idempotent"
compose run --rm init

step "4. start application and worker"
compose up -d app worker
ready=0
for _ in $(seq 1 120); do
  if curl -fsS "http://127.0.0.1:${port}/readyz" >/tmp/rka-ready.json 2>/dev/null; then ready=1; break; fi
  sleep 1
done
[ "$ready" = "1" ] || { cat /tmp/rka-ready.json 2>/dev/null; fail "application did not become ready"; }
cat /tmp/rka-ready.json | json 'print("readyz:", d["status"], d["checks"])'

step "5. login as the seeded administrator"
curl -fsS -c /tmp/rka-verify-cookies.txt -X POST "http://127.0.0.1:${port}/api/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$admin_email\",\"password\":\"$admin_password\"}" >/tmp/rka-login.json
json 'assert d["user"]["role"]=="admin"; print("login ok:", d["user"]["email"])' </tmp/rka-login.json

step "6. content snapshot is served without answers"
curl -fsS -b /tmp/rka-verify-cookies.txt "http://127.0.0.1:${port}/api/content/catalog" >/tmp/rka-catalog.json
json 'q=d.get("questions") or []; print("questions:", len(q), "materials:", len(d.get("materials") or [])); assert len(q)>1000; assert "answer" not in q[0] and "analysis" not in q[0]' </tmp/rka-catalog.json

step "6b. the content snapshot (reference answers) must not be served statically"
static_status=$(curl -sS -o /dev/null -w "%{http_code}" "http://127.0.0.1:${port}/content/catalog.json")
[ "$static_status" = "404" ] || fail "content snapshot is exposed as static content (HTTP $static_status)"
license_status=$(curl -sS -o /dev/null -w "%{http_code}" "http://127.0.0.1:${port}/content/licenses/practice-NOTICE.md")
[ "$license_status" = "200" ] || fail "license notices are no longer downloadable (HTTP $license_status)"
echo "static content snapshot: 404; license notice: 200"

step "7. practice: create a server-side session and submit once"
curl -fsS -b /tmp/rka-verify-cookies.txt -X POST "http://127.0.0.1:${port}/api/practice/sessions" \
  -H 'Content-Type: application/json' -d '{"mode":"all","count":5}' >/tmp/rka-session.json
json 's=d["session"]; print("session:", s["id"], "queue:", len(s["questionIds"]))' </tmp/rka-session.json
SESSION_ID=$(json 'print(d["session"]["id"])' </tmp/rka-session.json)
QUESTION_ID=$(json 'print(d["session"]["questionIds"][0])' </tmp/rka-session.json)
EVENT_ID="verify-$(date +%s)-0001"
answer_request() {
  curl -fsS -b /tmp/rka-verify-cookies.txt -X POST "http://127.0.0.1:${port}/api/practice/sessions/${SESSION_ID}/answers" \
    -H 'Content-Type: application/json' \
    -d "{\"id\":\"$EVENT_ID\",\"questionId\":\"$QUESTION_ID\",\"selected\":\"A\",\"assisted\":false,\"durationMs\":1200}"
}
answer_request >/tmp/rka-answer-1.json
answer_request >/tmp/rka-answer-2.json
step "8. replaying the same answer event must not double count"
curl -fsS -b /tmp/rka-verify-cookies.txt "http://127.0.0.1:${port}/api/practice/attempts?questionId=${QUESTION_ID}" >/tmp/rka-attempts.json
json 'rows=d.get("attempts") or d.get("items") or []; print("attempt rows:", len(rows)); assert len(rows)==1, rows' </tmp/rka-attempts.json

step "9. second device sees the same server-confirmed session"
curl -fsS -c /tmp/rka-verify-device2.txt -X POST "http://127.0.0.1:${port}/api/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$admin_email\",\"password\":\"$admin_password\"}" >/dev/null
curl -fsS -b /tmp/rka-verify-device2.txt "http://127.0.0.1:${port}/api/practice/sessions/active" >/tmp/rka-device2.json
python3 - "$SESSION_ID" <<'PY'
import json,sys
d=json.load(open('/tmp/rka-device2.json'))
s=d.get('session')
assert s and s['id']==sys.argv[1], d
print('second device restored session', s['id'])
PY

step "10. container rebuild keeps accounts, sessions and attempts"
compose down
compose up -d postgres
for _ in $(seq 1 60); do docker exec "${project}-postgres-1" pg_isready -U rka -d rka >/dev/null 2>&1 && break; sleep 1; done
compose up -d app worker
for _ in $(seq 1 120); do curl -fsS "http://127.0.0.1:${port}/readyz" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS -b /tmp/rka-verify-cookies.txt "http://127.0.0.1:${port}/api/auth/me" >/tmp/rka-me2.json
json 'assert d.get("user"), d; print("session survived rebuild:", d["user"]["email"])' </tmp/rka-me2.json

step "11. consistency backup"
scripts/backup.sh -p "$project" -f docker-compose.yml >/tmp/rka-backup.txt
BACKUP_PATH=$(tail -n 1 /tmp/rka-backup.txt | tr -d '[:space:]')
echo "backup path (container): $BACKUP_PATH"
docker run --rm -v "$data_dir:/data" alpine:3.24.3 test -f "${BACKUP_PATH}/db.dump" \
  || fail "backup did not produce db.dump at $BACKUP_PATH"
BACKUP_ID=$(basename "$BACKUP_PATH")
step "12. isolated verification of the backup"
compose run --rm -T app verify-backup "$BACKUP_ID" >/tmp/rka-verify-backup.txt
tail -n 2 /tmp/rka-verify-backup.txt

step "13. restore into a new data root (does not touch the live one)"
compose run --rm -T app restore "$BACKUP_ID" /data/restore-drill >/tmp/rka-restore.txt
tail -n 2 /tmp/rka-restore.txt
# The containers run as root inside the bind mount, so host-side file checks
# must go through a container as well.
docker run --rm -v "$data_dir:/data" alpine:3.24.3 test -f /data/restore-drill/db.dump \
  || fail "restore did not produce db.dump"

step "14. start a second stack on the restored data root"
docker rm -f rka-verify-pg2 >/dev/null 2>&1 || true
docker run -d --name rka-verify-pg2 -e POSTGRES_USER=rka -e POSTGRES_PASSWORD="$db_password" -e POSTGRES_DB=rka_restore \
  -p "127.0.0.1:${pg_restore_port}:5432" postgres:18.6-alpine >/dev/null
for _ in $(seq 1 60); do docker exec rka-verify-pg2 pg_isready -U rka -d rka_restore >/dev/null 2>&1 && break; sleep 1; done
docker run --rm --network host -e PGPASSWORD="$db_password" -v "$data_dir/restore-drill:/restore" postgres:18.6-alpine \
  pg_restore -h 127.0.0.1 -p "$pg_restore_port" -U rka -d rka_restore --no-owner --no-privileges /restore/db.dump
docker rm -f rka-verify-app2 >/dev/null 2>&1 || true
docker run -d --name rka-verify-app2 --network host \
  -e RKA_DATABASE_URL="postgres://rka:${db_password}@127.0.0.1:${pg_restore_port}/rka_restore?sslmode=disable" \
  -e RKA_DATA_DIR=/data -e RKA_STATIC_DIR=/app/dist -e RKA_SECRET_KEY_FILE=/data/secrets/secret-key \
  -e RKA_ADDR="127.0.0.1:${restore_port}" -e RKA_COOKIE_SECURE=never -e RKA_LOG_FORMAT=text -e RKA_LOG_LEVEL=warn \
  -v "$data_dir/restore-drill:/data" "$image" serve >/dev/null
for _ in $(seq 1 90); do curl -fsS "http://127.0.0.1:${restore_port}/readyz" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS "http://127.0.0.1:${restore_port}/readyz" | json 'print("restored readyz:", d["status"])'
curl -fsS -c /tmp/rka-restore-cookies.txt -X POST "http://127.0.0.1:${restore_port}/api/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$admin_email\",\"password\":\"$admin_password\"}" >/tmp/rka-restore-login.json
json 'print("restored login:", d["user"]["email"])' </tmp/rka-restore-login.json
curl -fsS -b /tmp/rka-restore-cookies.txt "http://127.0.0.1:${restore_port}/api/practice/attempts?questionId=${QUESTION_ID}" >/tmp/rka-restore-attempts.json
json 'rows=d.get("attempts") or d.get("items") or []; print("restored attempt rows:", len(rows)); assert len(rows)>=1' </tmp/rka-restore-attempts.json

printf '\nALL DEPLOYMENT CHECKS PASSED\n'
printf 'evidence: data=%s backup=%s restore=%s\n' "$data_dir" "$BACKUP_PATH" "$data_dir/restore-drill"
