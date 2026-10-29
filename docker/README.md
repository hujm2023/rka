# RKA 部署与运维手册

适用范围：Compose 部署（Go 应用、Worker、PostgreSQL、DeepTutor、Embedding，以及可选网关）。
接口：[后端 API 文档](../backend/API.md)。本文命令均在仓库根目录执行。

## 1. 准备条件

- 一台安装 Docker 与 Docker Compose v2 的机器。宿主机**不需要** Node、Go、Python、PostgreSQL 或模型网关。
- 项目目录（包含 `docker-compose.yml`、`docker/Dockerfile`、`backend/`、`frontend/`、`content/`）。
- 首次构建镜像需要能访问 npm registry 与 Go module proxy；日常运行不需要外网（AI 调用除外）。

```bash
cp .env.example .env
chmod 600 .env
# 编辑 .env：至少设置 POSTGRES_PASSWORD（长随机值）
```

## 2. 首次启动

```bash
docker compose up -d --build
docker compose ps -a              # init 应为 Exited(0)，app/worker/postgres 为 running
curl -fsS http://127.0.0.1:8080/readyz | jq .
```

`init` 是一次性、可重复执行的任务，顺序为：

1. 创建项目内数据目录（`./data/*`）与密钥文件（`./data/secrets/secret-key`，0600）
2. 创建一次性初始化令牌（`./data/secrets/bootstrap-token`，0600；已存在时不覆盖）
3. 执行数据库迁移（advisory lock，重复执行无副作用）
4. 导入内容快照（同版本幂等；失败明细写入 `jobs.errors`）
5. 若设置了 `RKA_ADMIN_EMAIL` / `RKA_ADMIN_PASSWORD`，幂等创建首个管理员

建立首个管理员（二选一）：

**方式 A（推荐，受控初始化）**：在 `.env` 设置 `RKA_ADMIN_EMAIL`、`RKA_ADMIN_PASSWORD`、`RKA_ADMIN_DISPLAY_NAME`，然后 `docker compose run --rm init`。

**方式 B（一次性令牌）**：不设置管理员变量，启动后读取 `./data/secrets/bootstrap-token`（0600），调用：

```bash
TOKEN=$(cat data/secrets/bootstrap-token)
curl -fsS -X POST http://127.0.0.1:8080/api/auth/bootstrap \
  -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\",\"email\":\"admin@example.com\",\"password\":\"<至少10位>\",\"displayName\":\"管理员\"}"
```

成功后入口自动关闭；令牌不随镜像分发，也不会写入公开日志。应用启动时若未设置 `RKA_BOOTSTRAP_TOKEN`，会自动读取 `$RKA_DATA_DIR/secrets/bootstrap-token`，因此 Compose 下 app 与 init 共享 `./data` 即可直接使用；显式环境变量优先。

## 3. 日常命令

| 目的 | 命令 |
| --- | --- |
| 查看状态 | `docker compose ps` / `curl -fsS localhost:8080/readyz` |
| 应用日志 | `docker compose logs -f app worker` |
| 重建应用（保留数据） | `docker compose up -d --build app worker` |
| 停止（保留数据） | `docker compose down` |
| 停止并删除数据 | `docker compose down -v` **且** 手动删除 `./data`（不可逆） |
| 手动导入内容 | `docker compose run --rm app import-content` |
| 手动迁移 | `docker compose run --rm app migrate` |
| 备份 | `scripts/backup.sh` |
| 校验备份 | `docker compose run --rm app verify-backup <backup-id>` |
| 恢复演练 | `docker compose run --rm -v ./data:/data app restore <backup-id> /data/restore-drill` |
| 保留策略 | `docker compose run --rm app retention` |
| 依赖与状态诊断 | `docker compose run --rm app doctor` |

> 运行一次性命令时不要同时删除 `./data`；这些命令只读写项目内数据目录。

## 4. 数据根目录与备份范围

`./data`（容器内 `/data`）是唯一的应用数据根：

| 路径 | 内容 | 是否纳入一致性备份 |
| --- | --- | --- |
| `postgres/` | PostgreSQL 数据目录（bind mount） | 通过 `db.dump` 逻辑备份 |
| `originals/` | 内容不可变原件（内容寻址） | 是（manifest 校验 sha256） |
| `derived/` | 解析产物、检索索引 | 可重建，备份清单记录 |
| `exports/` | 用户导出产物 | 可选 |
| `teaching/` | DeepTutor 原生持久目录 | 维护脚本停写后复制原生账号、会话、索引及配置，逐文件校验 |
| `cache/` | 模型缓存 | 可重建，不纳入 |
| `gateway/` | Magpie 配置 | 维护脚本停写后复制网关配置与凭据，逐文件校验 |
| `logs/` | 结构化日志 | 按保留策略 |
| `secrets/` | 应用密钥、初始化令牌 | 备份批次内含 `secret-key`（敏感，需加密保管，manifest 记录 sha256）；`bootstrap-token` 不备份 |
| `backups/` | 一致性备份批次 | 不纳入自身 |

**不要**直接复制 `data/postgres` 目录作为备份：数据库可能正在写入。使用 `rka backup`（`pg_dump` 一致性快照）。

## 5. 备份、校验与恢复演练

```bash
# 1) 创建备份（输出备份 id 与路径，例如 data/backups/20261028T121500Z）
scripts/backup.sh

# 2) 可用性校验（隔离目录真实恢复，核对行数、原件哈希与密钥）
docker compose run --rm app verify-backup <backup-id>

# 3) 恢复到新目录（不覆盖现有数据）
docker compose run --rm -v ./data:/data app restore <backup-id> /data/restore-drill
```

`restore` 完成后会提示三步：新建空数据库 → `pg_restore` 导入 `db.dump` → 用新数据根启动。执行示例（端口/库名自选）：

```bash
# 4a) 新建空库
docker run -d --name rka-restore-pg -e POSTGRES_USER=rka -e POSTGRES_PASSWORD='<password>' \
  -e POSTGRES_DB=rka_restore -p 127.0.0.1:55433:5432 postgres:17.6-alpine

# 4b) 导入逻辑备份
docker run --rm --network host -e PGPASSWORD='<password>' -v "$PWD/data/restore-drill:/restore" postgres:17.6-alpine \
  pg_restore -h 127.0.0.1 -p 55433 -U rka -d rka_restore --no-owner --no-privileges /restore/db.dump

# 4c) 用恢复目录和恢复出的密钥启动（密钥已复制到 <target>/secret-key）
docker run -d --name rka-restore-app --network host \
  -e RKA_DATABASE_URL='postgres://rka:<password>@127.0.0.1:55433/rka_restore?sslmode=disable' \
  -e RKA_DATA_DIR=/data -e RKA_STATIC_DIR=/app/dist \
  -e RKA_SECRET_KEY_FILE=/data/secrets/secret-key \
  -e RKA_ADDR=127.0.0.1:8081 -e RKA_COOKIE_SECURE=never \
  -v "$PWD/data/restore-drill:/data" rka:local serve
```

`verify-backup` 会校验批次内的 `secret-key`（32 字节 hex，manifest 记录 sha256）；旧格式批次没有该文件时记为 warned，不判失败，但恢复后必须单独提供 `RKA_SECRET_KEY`，否则模型凭据与教学身份密文无法解密。脚本化版本见 `scripts/verify-deployment.sh` 第 14 步。

**备份批次属于敏感数据**：其中包含应用密钥 `secret-key`，可解密模型凭据与教学身份密文。备份目录、恢复演练目录和传输过程都要按密钥级别保护，不要放进公开仓库或普通共享；`data/secrets` 本身仍不进入镜像与版本库，`bootstrap-token` 不备份。

恢复演练的通过标准（见 `scripts/verify-deployment.sh`）：

1. `verify-backup` 的 checks 全部为 ok；旧批次缺少 `secret-key` 时至少确认 warned 项已被记录；
2. 恢复目录中的 `db.dump` 能导入空库，`originals/` 哈希一致，批次内含密钥时 `secret-key` 与 manifest 一致；
3. 用恢复目录启动的实例能登录、读到同一份作答/草稿/任务记录，并能解密已有模型凭据与教学身份；
4. 原实例数据不受影响。

## 6. 升级与回退

1. **升级前**：`scripts/backup.sh` 并 `verify-backup`；记录当前镜像 tag（Compose 变量 `RKA_IMAGE_TAG`，示例文件未列出，可写入 `.env`）。
2. **升级**：修改 `RKA_IMAGE_TAG` 或重新 `--build`，`docker compose up -d`。`init` 会执行迁移；迁移持有 advisory lock，重复执行安全。
3. **兼容性门**：`rka serve` 启动时比较数据库 `schema_migrations` 最大值与二进制内置迁移版本。数据库更新而镜像回退时会**拒绝启动**并提示，而不是带病运行。
4. **回退**：把镜像 tag 改回上一个已验证版本；若数据库已被新版本迁移，必须同时恢复同批次备份（代码回退 ≠ 数据恢复）。
5. **固定版本**：镜像 tag、PostgreSQL/教学/Embedding 镜像、模型名与维度都应固定在 `.env`，不要依赖 `latest`。

## 7. 健康、日志与排障

| 信号 | 含义 |
| --- | --- |
| `GET /healthz` | 进程存活（不检查依赖） |
| `GET /readyz` | 数据库连接、schema 版本、核心内容（资料/题目）三项都就绪才 200 |
| `GET /api/admin/status` | process / database / teaching / model / index 分开呈现 |
| `docker compose ps -a` | init 必须 Exited(0) 才能启动 app |

常见问题：

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `readyz` 返回 `schema mismatch` | 镜像与数据库迁移版本不一致 | 运行 `rka migrate`，或换回兼容镜像 |
| `readyz` 返回 `content: empty` | 内容快照未导入 | `docker compose run --rm app import-content` |
| AI 功能显示不可用 | 未配置 `RKA_AI_*` 或教学服务 | 按 `/api/ai/status` 的 `unavailableReason` 补齐；基础学习不受影响 |
| 备份失败 | 数据库连接或磁盘空间 | 查看 `docker compose logs app`；确认 `data/backups` 可写 |
| 登录失败次数过多 | 限流（15 分钟 10 次） | 等待窗口结束或换账号 |

日志为 JSON 行，包含 `request_id`、`user_id`、`status`、`duration_ms`；AI 任务与教学回合带 `task_id`。日志不记录密码、令牌、密钥与完整私人草稿。

## 8. 安全要点

1. `.env` 与 `data/secrets/` 下的密钥文件权限 0600，不提交版本库、不进入镜像；一致性备份包含 `secret-key`，备份批次按密钥数据保管。
2. 会话 Cookie 为 HttpOnly + SameSite=Lax；`RKA_COOKIE_SECURE=auto` 在 TLS 下自动加 Secure。若部署在反向代理后，设置 `RKA_TRUST_PROXY=true` 并确保代理覆盖 `X-Forwarded-Proto`。
3. 首个管理员只能通过一次性令牌或受控环境变量建立；入口成功后自动关闭。
4. 模型凭据只在服务端加密保存；任何接口都不返回明文。
5. 写请求做同源校验：带 `Origin` 时其主机和端口必须与 `Host` 一致，否则返回 403；接口不通过放宽 CORS 允许跨站写入。
6. 内容快照（含参考答案）不作为静态目录暴露，只有 `/content/licenses/{file}` 可下载来源许可；题目答案通过受权限约束的 `/solution` 接口获取。
7. 教学工具只暴露受控资源，不挂载 Docker socket、整个项目目录或其他用户数据。

## 9. 教学与模型服务配置

模型供应商凭据、路由组和 Embedding 模型应与当前部署配置一致。配置完成后通过管理后台检查连接与教学能力：

- 默认 Compose 启动应用、Worker、PostgreSQL、DeepTutor、Embedding 与初始化任务；
- 只有 `gateway` 为可选 profile，需配置 provider、路由与 gateway key；
- 只配置 `RKA_AI_*`（OpenAI 兼容网关）而不启用 DeepTutor 时，教学任务由 Go 侧的网关后端直接执行；两者都未配置时，`/api/ai/status` 返回明确的 `unavailableReason`，基础学习不受影响。


## 10. 完整备份与教学服务恢复

使用 `scripts/backup.sh`（自定义 Compose 配置时把 `--env-file`、`-p`、`-f` 参数传给脚本）。脚本对本项目加维护锁，停止写入服务，创建备份，随后恢复原运行服务；执行期间页面会暂时不可访问。服务端不挂载 Docker socket。

`verify-backup` 会核对数据库、原件、密钥以及 `components/teaching`、`components/gateway` 的全部哈希。只有清单、没有教学/网关文件的旧备份不能通过完整恢复校验。

`restore` 将上述目录还原为新数据根下的 `teaching/`、`gateway/`、`originals/` 与 `secrets/`，并复制数据库 dump。对新 PostgreSQL 执行 `pg_restore --no-owner --no-privileges --single-transaction` 后，以新数据根启动 Compose；教学初始化会恢复 UID/GID 权限，不重新注册已有账号。保持相应 `.env` 配置，尤其是显式设置的外部教学管理员密码。默认的教学管理员密码由应用密钥派生，恢复密钥即可保持一致。

维护锁容器的名字由 PostgreSQL 容器 ID 派生。若宿主机断电导致遗留锁，应先确认没有备份任务运行、手动恢复原服务，再移除对应锁容器。不要并行运行两个维护备份。
