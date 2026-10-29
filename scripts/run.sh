#!/usr/bin/env bash
# One-command startup; application dependencies and setup run in containers.
set -euo pipefail
umask 077
project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$project_dir"
fail() { printf '错误：%s\n' "$*" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || fail '请先安装并启动 Docker（包含 Docker Compose v2）。'
docker compose version >/dev/null 2>&1 || fail '请安装 Docker Compose v2。'
docker info >/dev/null 2>&1 || fail 'Docker 尚未启动，或当前用户没有访问 Docker 的权限。'

created=0
temporary=''
trap 'if [[ -n "$temporary" ]]; then rm -f -- "$temporary"; fi' EXIT
if [[ ! -e .env ]]; then
  [[ -r .env.example ]] || fail '缺少 .env.example，请使用完整的项目源码。'
  random_hex() { od -An -N24 -tx1 /dev/urandom | tr -d ' \n'; }
  database_password="$(random_hex)"
  admin_password="Rka-$(random_hex)-9"
  temporary="$(mktemp "$project_dir/.env.run.XXXXXX")"
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in
      POSTGRES_PASSWORD=*) printf 'POSTGRES_PASSWORD=%s\n' "$database_password" ;;
      RKA_ADMIN_EMAIL=*) printf 'RKA_ADMIN_EMAIL=admin@example.com\n' ;;
      RKA_ADMIN_PASSWORD=*) printf 'RKA_ADMIN_PASSWORD=%s\n' "$admin_password" ;;
      RKA_ADMIN_DISPLAY_NAME=*) printf 'RKA_ADMIN_DISPLAY_NAME=管理员\n' ;;
      *) printf '%s\n' "$line" ;;
    esac
  done < .env.example > "$temporary"
  # Publish a complete file atomically, without replacing concurrent setup.
  if ln "$temporary" .env 2>/dev/null; then
    created=1
    printf '已生成 .env（权限 600），数据库与管理员使用独立随机密码。\n'
  elif [[ ! -f .env ]]; then
    fail '无法创建 .env，请检查项目目录权限。'
  fi
  rm -f -- "$temporary"
  temporary=''
  unset database_password admin_password
fi

docker compose config --quiet
# Resolve .env and shell overrides through Compose, never source .env as code.
launch_url=''
open_browser=1
build=1
wait_timeout=900
settings="$(docker compose config --environment | awk '/^RKA_(OPEN_URL|OPEN_BROWSER|RUN_BUILD|RUN_TIMEOUT)=/')"
while IFS='=' read -r key value; do
  case "$key" in
    RKA_OPEN_URL) launch_url="$value" ;;
    RKA_OPEN_BROWSER) open_browser="${value:-1}" ;;
    RKA_RUN_BUILD) build="${value:-1}" ;;
    RKA_RUN_TIMEOUT) wait_timeout="${value:-900}" ;;
  esac
done <<< "$settings"
[[ "$build" == 0 || "$build" == 1 ]] || fail 'RKA_RUN_BUILD 必须是 0 或 1。'
[[ "$open_browser" == 0 || "$open_browser" == 1 ]] || fail 'RKA_OPEN_BROWSER 必须是 0 或 1。'
[[ "$wait_timeout" =~ ^[1-9][0-9]*$ ]] || fail 'RKA_RUN_TIMEOUT 必须是正整数（秒）。'
if [[ -n "$launch_url" ]]; then
  case "$launch_url" in http://*|https://*) ;; *) fail 'RKA_OPEN_URL 必须是 http:// 或 https:// 地址。' ;; esac
fi

printf '正在准备镜像、初始化数据并启动服务；首次下载可能需要较长时间。\n'
args=(up -d --wait --wait-timeout "$wait_timeout")
if [[ "$build" == 1 ]]; then args+=(--build); fi
if ! docker compose "${args[@]}"; then
  docker compose ps -a
  printf '\n启动未完成，配置和数据已保留。运行 docker compose logs --tail=80 查看原因，再重试 make run。\n' >&2
  exit 1
fi

if [[ -z "$launch_url" ]]; then
  binding="$(docker compose port app 8080 | head -n 1)"
  [[ -n "$binding" ]] || fail '未找到网站的端口映射，请设置 RKA_OPEN_URL。'
  address="${binding%:*}"
  port="${binding##*:}"
  case "$address" in 0.0.0.0|127.0.0.1|'[::]'|'[::1]') address=localhost ;; esac
  launch_url="http://$address:$port/"
fi
printf '\nRKA 已启动：%s\n' "$launch_url"
if [[ "$created" == 1 ]]; then
  printf '首次登录账号：admin@example.com\n初始密码：查看 .env 中的 RKA_ADMIN_PASSWORD；登录后可修改。\n'
fi
printf '停止服务：make compose-down（数据保留）\n'
[[ "$open_browser" == 1 ]] || exit 0

# BROWSER may name an executable or wrapper script; do not eval shell text.
if [[ -n "${BROWSER:-}" ]]; then
  if "$BROWSER" "$launch_url" >/dev/null 2>&1; then exit 0; fi
elif [[ "$(uname -s)" == Darwin ]] && command -v open >/dev/null 2>&1; then
  if open "$launch_url" >/dev/null 2>&1; then exit 0; fi
elif command -v wslview >/dev/null 2>&1; then
  if wslview "$launch_url" >/dev/null 2>&1; then exit 0; fi
elif [[ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]]; then
  if command -v xdg-open >/dev/null 2>&1; then
    if xdg-open "$launch_url" >/dev/null 2>&1; then exit 0; fi
  elif command -v gio >/dev/null 2>&1; then
    if gio open "$launch_url" >/dev/null 2>&1; then exit 0; fi
  fi
fi
printf '当前环境无法自动打开浏览器，请打开上面的链接；也可通过 BROWSER 指定浏览器或打开链接的脚本。\n'
