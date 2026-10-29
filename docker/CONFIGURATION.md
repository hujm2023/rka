# 配置参考

配置写在项目根 `.env`（`chmod 600`，不要提交）。[.env.example](../.env.example) 提供部署样例。下表分别注明 Compose 配置与直接运行 Go 服务时的默认值。

## 必填

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `POSTGRES_PASSWORD` | 无，必填 | Compose 内 PostgreSQL 的密码，同时用于拼 `RKA_DATABASE_URL`；请使用长随机值 |

## 应用

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RKA_PORT` | `8080` | 宿主机对外端口（容器内固定监听 8080） |
| `RKA_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `RKA_LOG_FORMAT` | `json` | `json` 或 `text` |
| `RKA_COOKIE_SECURE` | `auto` | `auto` 按请求协议决定 Secure；`always` / `never` 强制 |
| `RKA_TRUST_PROXY` | `false` | 仅在反向代理会覆盖 `X-Forwarded-*` 时设为 `true` |
| `RKA_SESSION_TTL` | `720h` | 登录会话有效期 |

## 首个管理员

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RKA_ADMIN_EMAIL` | 空 | 设置后 `rka init` 幂等创建该管理员；为空则使用一次性令牌 |
| `RKA_ADMIN_PASSWORD` | 空 | 设置 `RKA_ADMIN_EMAIL` 时必填，至少 10 个字符 |
| `RKA_ADMIN_DISPLAY_NAME` | 空 | 显示名；为空时取邮箱前缀 |

## 模型与教学

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RKA_AI_ENDPOINT` | 空 | OpenAI 兼容 Chat Completions 完整地址（包含 `/chat/completions`）；浏览器不会收到该值 |
| `RKA_AI_MODEL` | 空 | 上游模型 ID 或网关路由组 |
| `RKA_AI_API_KEY` | 空 | 上游 Bearer 凭据，只保存在服务端 |
| `RKA_AI_GATEWAY_NAME` | `服务端 AI 网关` | 设置页显示名称 |
| `RKA_AI_MAX_CONCURRENCY` | `3` | 全平台活动 AI 任务上限（通过数据库事务控制） |
| `RKA_AI_DAILY_BUDGET` | `0` | 按 UTC 自然日预占教学回合，新建和追问都计入；删除学习数据不返还预算；`0` 不限制 |
| `RKA_AI_TIMEOUT` | `120s` | 单次上游请求超时 |
| `RKA_DEEPTUTOR_URL` | Compose 为 `http://deeptutor:8001`；直接运行 Go 时为空 | DeepTutor 后端地址（直连 FastAPI，如 `http://deeptutor:8001`） |
| `RKA_DEEPTUTOR_ADMIN_USERNAME` | `rka-admin` | DeepTutor 内部管理员账号 |
| `RKA_DEEPTUTOR_ADMIN_PASSWORD` | 空 | 空时由应用密钥派生；连接已有外部服务时填其管理员密码 |
| `RKA_EMBEDDING_URL` | Compose 为 `http://embedding:80/v1/embeddings`；直接运行 Go 时为空 | 知识索引使用的 Embedding 服务地址 |
| `RKA_EMBEDDING_MODEL` | `Xenova/bge-small-zh-v1.5` | Embedding 模型标识 |
| `RKA_EMBEDDING_DIMS` | `512` | 向量维度 |

已配置 DeepTutor 时教学任务走 DeepTutor；只配置 `RKA_AI_*` 时由 Go 侧的 OpenAI 兼容网关后端直接执行；两者都为空时 AI 功能明确不可用。

设置页仍保留「自定义接口」模式：接口地址和模型保存在浏览器 `localStorage`，API Key 只保留在当前页面内存，不写入服务器、浏览器数据库或备份。自定义模式由浏览器直连供应商，需要供应商支持跨域；HTTPS 页面访问 HTTP 上游还会受混合内容限制。生产部署建议使用服务器网关，避免把模型密钥交给浏览器。

## Worker 与保留策略

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RKA_WORKER_CONCURRENCY` | `2` | Worker 并发任务数 |
| `RKA_WORKER_POLL` | `2s` | 轮询间隔 |
| `RKA_WORKER_LEASE_TTL` | `60s` | 任务租约时长 |
| `RKA_WORKER_HEARTBEAT` | `15s` | 心跳间隔 |
| `RKA_WORKER_MAX_ATTEMPTS` | `3` | 最大尝试次数 |
| `RKA_RETENTION_TASK_DAYS` | `90` | 任务保留天数 |
| `RKA_RETENTION_EVENT_DAYS` | `30` | AI 事件保留天数 |
| `RKA_RETENTION_AUDIT_DAYS` | `365` | 审计保留天数 |
| `RKA_RETENTION_BACKUP_DAYS` | `30` | 备份保留天数 |
| `RKA_RETENTION_LOG_DAYS` | `30` | 日志保留天数 |

## 依赖服务镜像

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RKA_DEEPTUTOR_IMAGE` | `ghcr.io/hkuds/deeptutor:1.7.0` | 教学服务镜像（固定 tag，勿用 latest） |
| `RKA_EMBEDDING_IMAGE` | `ghcr.io/huggingface/text-embeddings-inference:cpu-1.9.4` | Embedding 镜像 |
| `RKA_EMBEDDING_MODEL_ID` | `Xenova/bge-small-zh-v1.5` | TEI 加载的模型 |
| `RKA_MAGPIE_IMAGE` | `ghcr.io/yetone/magpie:0.1.1997` | 容器内网关镜像 |
| `MAGPIE_WEB_KEY` | 空 | Magpie Web UI 的 key；启用 gateway profile 时必填 |

## Compose 变量与本地运行变量

以下变量控制镜像、数据挂载和开发运行方式，按需在 `.env` 或 shell 中设置：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RKA_IMAGE` | `rka` | 镜像仓库；使用发布包时设为 `ghcr.io/hujm2023/rka` |
| `RKA_IMAGE_TAG` | `local` | 应用镜像 tag；升级/回退时固定到已验证版本 |
| `RKA_DATA_HOST_DIR` | `./data` | 宿主机数据根路径（bind mount 源） |
| `COMPOSE_PROFILES` | 空 | 设置为 `gateway` 启用 Magpie；DeepTutor 与 Embedding 默认启动 |
| `RKA_ADDR` | `127.0.0.1:8080` | 非 Compose 运行时应用监听地址 |
| `RKA_DATABASE_URL` | `postgres://rka:...@127.0.0.1:5432/rka` | 非 Compose 运行时的数据库连接 |
| `RKA_DATA_DIR` | `./data`（CLI 原始默认） | Makefile 自动设置为仓库根 `data/`；Compose 使用 `/data` |
| `RKA_STATIC_DIR` | `./dist`（CLI 原始默认） | Makefile 自动设置为仓库的 `frontend/dist/`；Compose 使用 `/app/dist` |
| `RKA_CONTENT_SNAPSHOT` | `./content`（CLI 原始默认） | Makefile 自动设置为仓库根 `content/`；Compose 使用 `/app/content` |
| `RKA_SECRET_KEY_FILE` | `$RKA_DATA_DIR/secrets/secret-key` | 应用密钥文件；`rka init` 自动生成 |
| `RKA_SECRET_KEY` | 空 | 64 个十六进制字符（32 字节）的应用密钥，优先于文件；便于容器编排注入 |
| `RKA_BOOTSTRAP_TOKEN` | 空 | 一次性初始化令牌；为空时自动读取 `$RKA_DATA_DIR/secrets/bootstrap-token` |
| `RKA_WORKER_ENABLED` | `true` | 设为 `false` 时不处理后台任务，便于单独调试应用进程 |
| `RKA_WORKER_INLINE` | `false` | 应用进程内联运行 Worker，便于单进程本地调试 |

[返回部署启动](README.md)
