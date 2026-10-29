# 运维指南

本文用于已经运行的 RKA 实例，命令均在项目根目录执行。首次安装见[部署启动](README.md)，参数说明见[配置参考](CONFIGURATION.md)。

## 数据目录

默认将项目的 `data/` 挂载为容器内 `/data`。可以通过 `RKA_DATA_HOST_DIR` 指定其他持久化位置；下面的路径示例采用默认值。

| 目录 | 内容 |
| --- | --- |
| `postgres/` | PostgreSQL 数据 |
| `originals/` | 资料原件 |
| `teaching/` | DeepTutor 账号、会话、索引及配置 |
| `gateway/` | 可选 Magpie 网关的配置与凭据 |
| `secrets/` | 应用密钥与初始化令牌 |
| `backups/` | 一致性备份批次 |
| `derived/`、`cache/` | 衍生文件与模型缓存 |
| `exports/`、`logs/` | 导出文件与日志 |

数据、备份和真实环境配置均不提交 Git。不要直接复制正在写入的 PostgreSQL 数据目录作为备份。

## 备份

```bash
scripts/backup.sh
```

脚本会暂时停止本项目的写入服务，生成数据库逻辑备份，并复制原件、应用密钥、教学状态及网关文件，随后恢复原先运行的服务。执行期间网站会短暂不可用。

记下输出的备份 ID。将下面命令中的 `BACKUP_ID` 替换为实际值，执行恢复校验：

```bash
docker compose run --rm app verify-backup BACKUP_ID
```

校验会真实恢复到隔离数据库，并核对行数和文件哈希。只有文件清单、缺少教学或网关文件的批次不构成完整备份。模型缓存可以重新下载，不包含在备份中。

备份含有应用密钥和网关凭据，应限制访问并保存独立副本。个人学习数据 JSON 导出不能替代整站备份。

使用自定义 Compose 参数时，给维护脚本传入相同参数，例如：

```bash
scripts/backup.sh --env-file .env.production -p rka-production
```

如果备份被意外中断，先检查服务状态和任务是否仍在执行，再处理遗留的维护锁；锁容器名称以 `rka-backup-` 开头。

## 恢复到新实例

先完成备份校验。恢复目录必须为空，下面使用 `data/restored/` 和独立的 Compose 项目，原实例继续使用原数据目录。

```bash
docker compose run --rm app restore BACKUP_ID /data/restored
cp .env .env.restore
chmod 600 .env.restore
```

编辑 `.env.restore`，保留原有模型、教学服务和凭据配置，并修改：

```dotenv
RKA_DATA_HOST_DIR=./data/restored
RKA_PORT=8081
```

先仅启动新实例的数据库，再导入恢复目录中的逻辑备份。以下管道通过容器读取文件，不要求宿主机安装 PostgreSQL 客户端：

```bash
docker compose --env-file .env.restore -p rka-restored up -d --wait postgres

set -o pipefail
docker compose run --rm -T --no-deps --entrypoint cat app /data/restored/db.dump \
  | docker compose --env-file .env.restore -p rka-restored exec -T postgres \
      pg_restore -U rka -d rka --no-owner --no-privileges --single-transaction

docker compose --env-file .env.restore -p rka-restored up -d
```

新实例的 `init` 会检查数据库版本和目录权限。应用密钥从恢复目录加载，用于解密原有模型配置和教学身份；初始化令牌不会随备份复制。Embedding 模型缓存可能需要重新下载。

在新端口检查 `/readyz`，登录原账号，核对作答、草稿和教学记录。确认恢复结果后，再决定是否切换访问入口。不要使用旧二进制直接运行已升级的数据库。

## 升级

先创建并验证备份，记录当前代码版本、镜像标签与配置。对于从源码构建的部署：

```bash
git pull --ff-only
docker compose build
docker compose stop app worker
docker compose run --rm init
docker compose up -d
```

`init` 应成功退出，再检查 `/readyz` 与管理后台状态。升级失败时保留日志与备份；如果数据库已迁移，回退需要同时恢复匹配版本的数据，不能只切换镜像。

## 健康与排障

```bash
docker compose ps -a
docker compose logs --tail=100 app worker init
curl -fsS http://localhost:8080/readyz
```

| 现象 | 检查与处理 |
| --- | --- |
| 端口被占用 | 在 `.env` 修改 `RKA_PORT`，再执行 `docker compose up -d` |
| `init` 失败 | 检查其日志中的数据库连接、迁移或内容导入错误，修正配置后重新执行 `init` |
| `/readyz` 返回 503 | 检查响应中的数据库、schema 和内容状态，以及应用日志 |
| 页面正常但 AI 不可用 | 检查管理后台的模型连接、教学服务及索引状态，确认密钥与模型名称有效 |
| 登录后仍返回未登录 | 检查访问协议与 Cookie 配置；使用 TLS 终止代理时，按配置参考设置可信代理 |
| 备份失败 | 检查数据库连接、磁盘空间和目录权限；使用维护脚本创建完整备份 |

`/readyz` 反映基础服务就绪情况；教学服务、模型调用和知识索引应在管理后台分别确认。日志中的请求 ID、任务 ID 可用于关联排查。

## 保留策略

通过 `RKA_RETENTION_*` 配置任务、事件、审计、备份及日志的保留天数，执行：

```bash
docker compose run --rm app retention
```

清理前确认需要长期保存的备份已有独立副本。不要手工删除正在使用的应用密钥或数据库目录。

[返回部署启动](README.md)
