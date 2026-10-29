#!/usr/bin/env bash
# Stop writers, snapshot PostgreSQL + originals + native teaching/gateway state,
# then restart exactly the services that were running. No Docker socket is
# mounted in any application container. Pass Compose flags before this script's
# implicit command, e.g. scripts/backup.sh --env-file docker/rka.env -p rka.
set -euo pipefail
cd "$(dirname "$0")/.."
compose=(docker compose "$@")
database="$("${compose[@]}" ps -q postgres)"
if [ -z "$database" ]; then echo "请先启动本项目的 PostgreSQL。" >&2; exit 1; fi
lock="rka-backup-${database}"
job="${lock}-job"
image="$(docker inspect -f '{{.Config.Image}}' "$database")"
# Reserving a container name is atomic across shells and needs no host flock,
# Python or Docker socket inside the application. Keep the lock until restart.
if ! docker create --name "$lock" --entrypoint /bin/true "$image" >/dev/null 2>&1; then
  echo "备份维护锁已存在：$lock。确认上次备份结束并恢复服务后，才可手动移除此锁。" >&2
  exit 1
fi
writers=()
cleanup() {
  local result=$?
  docker rm -fv "$job" >/dev/null 2>&1 || true
  if [ "${#writers[@]}" -gt 0 ]; then "${compose[@]}" start "${writers[@]}" || result=1; fi
  docker rm -v "$lock" >/dev/null || result=1
  exit "$result"
}
trap cleanup EXIT
while IFS= read -r service; do
  case "$service" in app|worker|deeptutor|magpie) writers+=("$service");; esac
done < <("${compose[@]}" ps --services --status running)
if [ "${#writers[@]}" -gt 0 ]; then "${compose[@]}" stop --timeout 60 "${writers[@]}"; fi
"${compose[@]}" run --name "$job" --rm --no-deps -e RKA_BACKUP_QUIESCED=true app backup
