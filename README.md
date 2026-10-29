# RKA · 架构师学习室

RKA 是面向软考高级「系统架构设计师」的备考学习平台，把完整资料阅读、章节练习、错题复习、案例作答、论文写作、AI 辅导和学习报告放在同一个账号下。

学习过程围绕同一条路径展开：**阅读教材原文 → 练习关联题目 → 对照解析 → 回到具体知识小节 → 独立复测**。学习数据以服务器为权威：换设备登录同一账号可以继续上次的练习现场；浏览器只保留缓存和待同步队列，断网时普通练习和草稿仍可暂存，恢复网络后按幂等事件补交。

应用在本仓库独立实现。相邻三个项目仅作为题库与学习资料来源，没有复用它们的页面、业务逻辑、数据库实现或 AI 调用代码。

> **部署状态：** 目标部署是 Docker Compose（Go 应用 + Go Worker + PostgreSQL + 项目内数据目录）。sgdev1 历史上由 Nginx、systemd `rka.service` 和 Node 服务（`127.0.0.1:4173`）提供 8104 入口，那套链路是迁移前遗留，不再是本平台的运行方式；切换步骤见[从旧 Node 入口迁移](#从旧-node-入口迁移)。

> 本文的部署方式、命令和目录结构核对于 2026-10-29。运行状态以实际服务为准；题量以 `content/audit.json` 为准。

## 阅读导航

- [能力范围](#能力范围)
- [快速开始](#快速开始)
- [系统架构](#系统架构)
- [环境变量](#环境变量)
- [数据目录、备份与恢复](#数据目录备份与恢复)
- [开发与测试](#开发与测试)
- [验证命令](#验证命令)
- [从旧 Node 入口迁移](#从旧-node-入口迁移)
- [安全说明](#安全说明)
- [内容来源与许可](#内容来源与许可)
- [代码结构](#代码结构)
- [文档导航](#文档导航)
- [常见问题](#常见问题)
- [当前限制](#当前限制)

## 能力范围

| 能力 | 现在可以做什么 | 边界与状态 |
| --- | --- | --- |
| 账号与设备 | 邮箱 + 密码登录、HttpOnly 会话 Cookie、查看并退出设备会话、修改密码；管理员可创建账号、发邀请、停用账号、调整角色 | 不开放公开注册；首个管理员通过受控初始化建立；密码至少 10 个字符 |
| 跨设备同步 | 服务器保存练习现场、作答、草稿、阅读进度和标记；另一台设备登录后恢复服务器已确认的现场 | 浏览器 IndexedDB 仅作缓存与待同步队列；界面分别显示本地保存、待同步、已同步、冲突四态 |
| 内容原文、版本与检索 | 38 份完整资料按版本提供；章内目录、小节定位、全文检索、原文下载、我的批注；题目与原文小节关联可回查 | 自动关联是启发式结果，可能漏配或误配；修改需重建索引 |
| 练习与正式考试 | 六种练习模式；服务端保存题目队列与每题版本；同一事件 ID 重复提交只计一次；正式考试按服务器时间截止，交卷前不揭示答案 | 正式考试默认 150 分钟，可由创建参数指定；评分规则为 v1 单选 1 分，后续题目修订不影响历史成绩 |
| 错题与复习 | 最近一次作答仍错误的题目进入错题本；到期复习按固定间隔生成；可从错题回到具体原文小节再返回 | 复习间隔是固定规则（答错 1 天、辅助答对 2 天、独立答对 7 天），不是校准后的记忆模型 |
| AI 辅导 | 任务式辅导：创建任务、SSE 接收事件、追问、取消、断线按事件序号恢复；提示模式不透露答案 | 需要配置 DeepTutor 或 OpenAI 兼容网关之一；都未配置时明确返回不可用原因，阅读、练习、写作不受影响 |
| 案例与论文写作 | 案例按子问题、论文按素材/提纲/摘要/正文分别保存；版本化草稿；并发保存返回 409 并保留双方版本；AI 点评绑定生成时的稿件版本 | AI 点评不是官方阅卷；模型不得把虚构指标写成用户事实 |
| 学习证据与报告 | 区分阅读、提示辅助、独立通过、自我声明四类证据；知识点掌握状态；复习安排；报告结论可回查具体证据 | 无数据时结论保守；不输出未经校准的通过率 |
| 管理后台 | 服务健康、用户与邀请、资源授权、模型配置、后台任务、审计日志、备份与保留策略、调用指标 | 配置状态、连接状态、能力验证分开显示；凭据只写不读 |
| 备份与恢复 | 一致性备份（数据库逻辑 dump + 原件 + manifest sha256）、隔离校验、恢复到新目录；可重复执行保留策略 | 备份批次包含应用密钥，按敏感数据保管；不要直接复制运行中的 PostgreSQL 数据目录 |
| 数据自主 | 导出我的全部学习数据（JSON）、删除学习数据（保留账号）、导入旧浏览器 v1 备份 | 服务端导出的 v2 数据可回导到同一账号；跨账号数据会被拒绝 |

### 页面入口

| 路由 | 页面 | 常用参数 |
| --- | --- | --- |
| `/` | 学习概览：继续练习、领域进度、错题与复习提示 | 无 |
| `/practice` | 题库练习：模式、筛选、答题卡、交卷 | `mode`、`module`、`material`、`section`、`start=1` |
| `/knowledge` | 资料目录与原文阅读器 | `id`、`section`、`from=practice` |
| `/cases` | 案例工坊 | `id` |
| `/essays` | 论文写作 | `id` |
| `/settings` | 账号、设备、AI 配置、导出与删除 | 无 |
| `/admin` | 管理后台（仅管理员） | 无 |
| `/login`、`/setup`、`/invite` | 登录、建立首个管理员、接受邀请 | `/invite?token=...` |

### 从阅读进入练习

1. 在「知识手册」选择一份完整资料，按原文阅读，或通过章内目录定位小节。
2. 点击「练本章」，或在文末选择「开始本章练习」「练当前小节」「巩固本章错题」。
3. 服务端按关联索引确定题目队列和每题版本；同范围已有未完成现场时直接继续，切换范围需要确认替换。
4. 提交答案后，在解析下方打开关联知识原文，页面定位到对应小节；点击「返回本题」继续原队列。

没有足够关联依据的题目保持未关联，不会强行分配到章节；缺图题默认从练习队列排除，可在筛选里手动包含。

### 练习模式

| 模式 | 题目范围和排序 |
| --- | --- |
| 自由练习 | 按筛选条件选题，优先未作答题 |
| 章节突破 | 按知识领域练习；从资料进入时限制为该资料或小节关联的题目 |
| 真题套卷 | 选择考试期，按原题号组成整套题，以正式考试模式运行：按服务器时间截止，交卷前不揭示答案 |
| 错题巩固 | 最近一次作答仍然错误的题目 |
| 我的收藏 | 手动收藏的题目 |
| 到期复习 | 根据最近一次作答时间筛选已到复习时间的题目 |

### 快捷操作

| 操作 | 快捷键 |
| --- | --- |
| 选择 A–D | `A`–`D` 或 `1`–`4` |
| 提交当前选择 | `Enter` |
| 上一题 / 下一题 | `←` / `→` |
| 搜索知识资料 | `Ctrl+K` / `⌘K` |
| 发送 AI 提问 / 输入框换行 | `Enter` / `Shift+Enter` |

在输入框内输入或弹窗打开时，练习快捷键不会抢占输入操作。

## 快速开始

### 准备条件

- 一台安装 Docker 与 Docker Compose v2 的机器。宿主机**不需要** Node、Go、Python、PostgreSQL 或模型网关。
- 项目目录包含 `backend/`、`frontend/`、`docker/`、`docker-compose.yml` 和 `content/`。
- 首次构建镜像需要能访问 npm registry 与 Go module proxy；日常运行不需要外网（AI 调用除外）。
- 默认对外端口 `8080`，数据库与内部服务只在 Compose 网络内。

### 启动

```bash
cd /path/to/rka
cp .env.example .env
chmod 600 .env
# 编辑 .env：至少把 POSTGRES_PASSWORD 改成足够长的随机值
docker compose up -d --build
```

`docker compose up` 会先运行一次性服务 `init`（创建 `./data` 目录与密钥、迁移数据库、导入内容快照、可选创建首个管理员），成功后启动 `app`、`worker` 和 `postgres`。

```bash
docker compose ps -a               # init 应为 Exited(0)，app/worker/postgres 为 running
curl -fsS http://127.0.0.1:8080/readyz
```

就绪响应示例（具体数量以实际导入为准）：

```json
{
  "status": "ready",
  "checks": {
    "database": "ok",
    "schema": "v10",
    "content": {"materials": 38, "questions": 2389},
    "teaching": "unconfigured"
  }
}
```

浏览器打开 `http://<服务器地址>:8080/`（若修改了 `RKA_PORT`，换成实际端口）。默认只有登录、接受邀请和初始化管理员入口；没有公开注册。

### 建立首个管理员（二选一）

**方式 A（推荐，受控初始化）。** 在 `.env` 中设置以下变量，然后重新执行初始化：

```dotenv
RKA_ADMIN_EMAIL=admin@example.com
RKA_ADMIN_PASSWORD=<至少 10 个字符>
RKA_ADMIN_DISPLAY_NAME=管理员
```

```bash
docker compose run --rm init
```

`init` 可重复执行：已有管理员时不会重建，也不会重置密码。

**方式 B（一次性令牌）。** 不设置管理员变量，`init` 会生成 `./data/secrets/bootstrap-token`（0600）。应用启动时若未设置 `RKA_BOOTSTRAP_TOKEN`，会自动读取该文件；也可显式设置环境变量覆盖。

```bash
cat data/secrets/bootstrap-token
```

然后在 `/setup` 页面粘贴令牌，或调用接口：

```bash
TOKEN=$(cat data/secrets/bootstrap-token)
curl -fsS -X POST http://127.0.0.1:8080/api/auth/bootstrap \
  -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\",\"email\":\"admin@example.com\",\"password\":\"<至少10位>\",\"displayName\":\"管理员\"}"
```

成功后入口自动关闭，令牌不随镜像分发，也不写入公开日志。

### 常用命令

| 目的 | 命令 |
| --- | --- |
| 查看服务状态 | `docker compose ps` |
| 查看健康 | `curl -fsS http://127.0.0.1:8080/readyz` |
| 应用与 Worker 日志 | `docker compose logs -f app worker` |
| 重建应用（保留数据） | `docker compose up -d --build app worker` |
| 停止服务（保留数据） | `docker compose down` |
| 停止并删除数据 | `docker compose down -v`，再手动删除 `./data`（不可逆） |
| 手动迁移 / 导入内容 | `docker compose run --rm app migrate` / `docker compose run --rm app import-content` |
| 备份 / 校验 / 恢复 | `scripts/backup.sh`；`docker compose run --rm app verify-backup <id>`；`docker compose run --rm -v ./data:/data app restore <id> /data/restore-drill` |
| 保留策略 / 依赖诊断 | `docker compose run --rm app retention` / `... doctor` |

默认 Compose 同时启动 Go 应用、Worker、PostgreSQL、DeepTutor 和本地中文 Embedding（含一次性初始化任务）。无需宿主机 Node、Python、Magpie 或 Nginx。首次启动会下载 Embedding 模型并构建资料索引；原文、练习和写作不等待 AI 就绪。

Magpie 是可选的容器网关：`COMPOSE_PROFILES=gateway docker compose up -d`。需要配置其 provider、路由组和正式 gateway key，随后设置 `RKA_AI_ENDPOINT=http://magpie:3425/v1/chat/completions`、`RKA_AI_MODEL`、`RKA_AI_API_KEY`。也可直接连接已有的 OpenAI 兼容服务。模型供应商凭据必须由部署者提供。

## 系统架构

```mermaid
%%{init: {"themeVariables": {"background": "transparent", "primaryColor": "transparent", "secondaryColor": "transparent", "tertiaryColor": "transparent", "actorBkg": "transparent", "noteBkgColor": "transparent", "edgeLabelBackground": "transparent", "fontFamily": "sans-serif", "fontSize": "16px"}}}%%
flowchart LR
  B["浏览器<br/>React 构建产物<br/>IndexedDB 缓存与待同步队列"]
  A["Go 应用 rka serve<br/>页面 · 静态资源 · API · SSE"]
  W["Go Worker rka worker<br/>任务租约 · 心跳 · 事件写入"]
  PG[("PostgreSQL 17")]
  D[("./data<br/>原件 · 导出 · 备份 · 日志 · 密钥")]
  I["rka init（一次性）<br/>目录 · 密钥 · 迁移 · 内容 · 管理员"]
  T["DeepTutor<br/>可选 profile"]
  M["OpenAI 兼容网关<br/>或 Magpie（可选 profile）"]
  E["Embedding / TEI<br/>可选 profile"]

  B -->|"同源 HTTP / SSE"| A
  A --> PG
  A --> D
  W --> PG
  W -->|"教学回合"| T
  A -->|"模型调用"| M
  A -->|"检索"| E
  I --> PG
  I --> D
```

| 组件 | 职责 |
| --- | --- |
| React 前端 | 页面与交互；构建产物由 Go 直接提供，Node 只参与镜像构建，不进入运行时 |
| Go 应用（`rka serve`） | 账号、内容、练习、写作、证据、AI 任务、管理接口；静态资源与应用路由回退；SSE |
| Go Worker（`rka worker`） | 领取教学任务、租约与心跳、超时回收、重试上限、取消处理 |
| PostgreSQL 17 | 用户、内容元数据、题目版本、作答、练习现场、草稿、任务与审计 |
| 项目内数据目录 | `./data` 绑定挂载；原件、衍生文件、导出、教学状态、缓存、网关配置、日志、密钥、备份 |
| 一次性 `init` | 目录与密钥、数据库迁移、内容快照导入、首个管理员（可重复执行） |

数据根 `./data`（容器内 `/data`）是应用持久数据的唯一位置，容器重建不会丢数据：

| 路径 | 内容 | 是否纳入一致性备份 |
| --- | --- | --- |
| `postgres/` | PostgreSQL 数据目录（bind mount） | 通过 `db.dump` 逻辑备份 |
| `originals/` | 内容不可变原件（内容寻址） | 是，manifest 校验 sha256 |
| `derived/` | 解析产物、检索索引 | 可重建，只记录清单 |
| `exports/` | 用户导出产物 | 可选 |
| `teaching/` | DeepTutor 原生持久目录 | 启用教学服务时只记录清单；DeepTutor 快照需按其自身维护窗口导出 |
| `cache/` | 模型缓存 | 可重建，不纳入 |
| `gateway/` | Magpie 配置 | 启用网关时只记录清单，不复制运行数据 |
| `logs/` | 结构化日志 | 按保留策略 |
| `secrets/` | 应用密钥 `secret-key`、一次性令牌 `bootstrap-token` | 备份批次内含 `secret-key`（敏感，需加密保管）；`bootstrap-token` 不备份 |
| `backups/` | 一致性备份批次 | 不纳入自身 |

本机开发和测试还会用到 `./data/dev`、`./data/e2e` 和 `./data-verify`，它们是可丢弃的测试数据根，不属于部署数据。

## 环境变量

配置写在项目根 `.env`（`chmod 600`，不要提交）。`.env.example` 是完整样例；下表列出全部变量及默认值。

### 必填

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `POSTGRES_PASSWORD` | 无，必填 | Compose 内 PostgreSQL 的密码，同时用于拼 `RKA_DATABASE_URL`；请使用长随机值 |

### 应用

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RKA_PORT` | `8080` | 宿主机对外端口（容器内固定监听 8080） |
| `RKA_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `RKA_LOG_FORMAT` | `json` | `json` 或 `text` |
| `RKA_COOKIE_SECURE` | `auto` | `auto` 按请求协议决定 Secure；`always` / `never` 强制 |
| `RKA_TRUST_PROXY` | `false` | 仅在反向代理会覆盖 `X-Forwarded-*` 时设为 `true` |
| `RKA_SESSION_TTL` | `720h` | 登录会话有效期 |

### 首个管理员

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RKA_ADMIN_EMAIL` | 空 | 设置后 `rka init` 幂等创建该管理员；为空则使用一次性令牌 |
| `RKA_ADMIN_PASSWORD` | 空 | 设置 `RKA_ADMIN_EMAIL` 时必填，至少 10 个字符 |
| `RKA_ADMIN_DISPLAY_NAME` | 空 | 显示名；为空时取邮箱前缀 |

### 模型与教学

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RKA_AI_ENDPOINT` | 空 | OpenAI 兼容 Chat Completions 完整地址（包含 `/chat/completions`）；浏览器不会收到该值 |
| `RKA_AI_MODEL` | 空 | 上游模型 ID 或网关路由组 |
| `RKA_AI_API_KEY` | 空 | 上游 Bearer 凭据，只保存在服务端 |
| `RKA_AI_GATEWAY_NAME` | `服务端 AI 网关` | 设置页显示名称 |
| `RKA_AI_MAX_CONCURRENCY` | `3` | 全平台活动 AI 任务上限（通过数据库事务控制） |
| `RKA_AI_DAILY_BUDGET` | `0` | 按 UTC 自然日预占教学回合，新建和追问都计入；删除学习数据不返还预算；`0` 不限制 |
| `RKA_AI_TIMEOUT` | `120s` | 单次上游请求超时 |
| `RKA_DEEPTUTOR_URL` | 空 | DeepTutor 后端地址（直连 FastAPI，如 `http://deeptutor:8001`） |
| `RKA_DEEPTUTOR_ADMIN_USERNAME` | `rka-admin` | DeepTutor 内部管理员账号 |
| `RKA_DEEPTUTOR_ADMIN_PASSWORD` | 空 | 空时由应用密钥派生；连接已有外部服务时填其管理员密码 |
| `RKA_EMBEDDING_URL` | 空 | Embedding 服务地址；为空时不启用本地检索 |
| `RKA_EMBEDDING_MODEL` | `Xenova/bge-small-zh-v1.5` | Embedding 模型标识 |
| `RKA_EMBEDDING_DIMS` | `512` | 向量维度 |

已配置 DeepTutor 时教学任务走 DeepTutor；只配置 `RKA_AI_*` 时由 Go 侧的 OpenAI 兼容网关后端直接执行；两者都为空时 AI 功能明确不可用。

设置页仍保留「自定义接口」模式：接口地址和模型保存在浏览器 `localStorage`，API Key 只保留在当前页面内存，不写入服务器、浏览器数据库或备份。自定义模式由浏览器直连供应商，需要供应商支持跨域；HTTPS 页面访问 HTTP 上游还会受混合内容限制。生产部署建议使用服务器网关，避免把模型密钥交给浏览器。

### Worker 与保留策略

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

### 可选 profile

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RKA_DEEPTUTOR_IMAGE` | `ghcr.io/hkuds/deeptutor:1.6.15` | 教学服务镜像（固定 tag，勿用 latest） |
| `RKA_EMBEDDING_IMAGE` | `ghcr.io/huggingface/text-embeddings-inference:cpu-1.9` | Embedding 镜像 |
| `RKA_EMBEDDING_MODEL_ID` | `Xenova/bge-small-zh-v1.5` | TEI 加载的模型 |
| `RKA_MAGPIE_IMAGE` | `ghcr.io/yetone/magpie:0.1.1893` | 容器内网关镜像 |
| `MAGPIE_WEB_KEY` | 空 | Magpie Web UI 的 key；启用 gateway profile 时必填 |

### Compose 变量与本地运行变量

下列变量不在 `.env.example` 中，按需在 `.env` 或 shell 中设置：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `RKA_IMAGE_TAG` | `local` | 应用镜像 tag；升级/回退时固定到已验证版本 |
| `RKA_DATA_HOST_DIR` | `./data` | 宿主机数据根路径（bind mount 源） |
| `COMPOSE_PROFILES` | 空 | 逗号分隔，如 `teaching,embedding` |
| `RKA_ADDR` | `127.0.0.1:8080` | 非 Compose 运行时应用监听地址 |
| `RKA_DATABASE_URL` | 本机 `postgres://rka:...@127.0.0.1:5432/rka` | 非 Compose 运行时的数据库连接 |
| `RKA_DATA_DIR` | `./data`（CLI 原始默认） | Makefile 自动设置为仓库根 `data/`；Compose 使用 `/data` |
| `RKA_STATIC_DIR` | `./dist`（CLI 原始默认） | Makefile 自动设置为仓库的 `frontend/dist/`；Compose 使用 `/app/dist` |
| `RKA_CONTENT_SNAPSHOT` | `./content`（CLI 原始默认） | Makefile 自动设置为仓库根 `content/`；Compose 使用 `/app/content` |
| `RKA_SECRET_KEY_FILE` | `$RKA_DATA_DIR/secrets/secret-key` | 应用密钥文件；`rka init` 自动生成 |
| `RKA_SECRET_KEY` | 空 | 64 个十六进制字符（32 字节）的应用密钥，优先于文件；便于容器编排注入 |
| `RKA_BOOTSTRAP_TOKEN` | 空 | 一次性初始化令牌；为空时自动读取 `$RKA_DATA_DIR/secrets/bootstrap-token` |
| `RKA_WORKER_ENABLED` | `true` | 设为 `false` 时不处理后台任务，便于单独调试应用进程 |
| `RKA_WORKER_INLINE` | `false` | 应用进程内联运行 Worker，便于单进程本地调试 |

## 数据目录、备份与恢复

### 一致性备份

备份使用 `pg_dump` 逻辑快照，不直接复制正在写入的 `data/postgres`。下面示例中的 `<backup-id>`、`<target>`、`<新库>` 等占位符需要替换为实际值：

```bash
# 1) 创建备份，输出备份 id 与路径（例如 data/backups/20261028T121500Z）
scripts/backup.sh

# 2) 在隔离临时目录真实恢复并核对行数、原件哈希与密钥
docker compose run --rm app verify-backup <backup-id>

# 3) 恢复到新目录（不覆盖现有数据）
docker compose run --rm -v ./data:/data app restore <backup-id> /data/restore-drill
```

备份脚本先对本 Compose 项目加维护锁，停止原先运行的 app、worker、DeepTutor 和 Magpie，再使用 `pg_dump` 与文件快照创建同一批备份，最后恢复原先运行的服务。批次包含数据库、原件、DeepTutor 原生账号/会话/知识索引、网关配置和 `secret-key`，每个文件记录哈希。模型缓存与可重建的 Go 解析产物不复制。存在教学或网关数据时，在线备份接口会明确要求使用维护脚本，避免把热拷贝当作一致性备份。**备份批次属于敏感数据：其中的应用密钥可解密模型凭据与教学身份密文**，要按密钥级别保管，不要提交仓库或普通共享；`bootstrap-token` 不在备份内。

`verify-backup` 会校验批次内的 `secret-key`；旧格式备份没有该文件时记为 warned，不判失败，但恢复后必须单独提供 `RKA_SECRET_KEY`。`restore` 只负责把数据与密钥复制到新目录，不会覆盖现有数据，也不会自动建库；按它输出的三步执行：

1. 新建空数据库；
2. `pg_restore --dbname <新库> <target>/db.dump`；
3. 用新数据根与恢复出的密钥启动：`RKA_DATA_DIR=<target>`、`RKA_SECRET_KEY_FILE=<target>/secrets/secret-key`、`RKA_DATABASE_URL=<新库>`。

完整命令见 [部署与运维手册](docker/README.md) 的“备份、校验与恢复演练”，脚本化演练见 [scripts/verify-deployment.sh](scripts/verify-deployment.sh) 第 14 步。恢复演练的通过标准：

1. `verify-backup` 的 checks 全部为 ok，包括教学和网关文件哈希；
2. `db.dump` 能导入空库，`originals/`、`teaching/`、`gateway/` 恢复一致；
3. 用恢复目录启动的实例能登录并读到同一份作答、草稿和任务记录；
4. 原实例数据不受影响。

### 保留策略

```bash
docker compose run --rm app retention
```

按 `.env` 中的 `RKA_RETENTION_*` 清理任务、事件、审计、备份和日志。

### 导出与删除（数据自主）

在「设置与数据」页面：

- **导出完整备份**：调用 `GET /api/export/me`，下载 JSON（schema v2），用于离线留存或换机。v2 数据可回导同一账号（幂等）；跨账号导入返回 `foreign_backup`，服务器已更新的草稿不会被覆盖，冲突版本保留。
- **导入学习进度**：接受旧浏览器 RKA v1 备份；旧练题台 v1/v2 进度文件会在浏览器内转换为 v1 结构后提交，只导入能匹配到当前题库的作答与收藏。
- **删除我的学习数据**：调用 `DELETE /api/me/data`，删除作答、标记、练习现场、阅读进度、批注、写作文档、证据和 AI 任务，保留账号；需要输入 `DELETE` 确认。

导入接口是 `POST /api/sync/import`，接受 v1 浏览器备份与 v2 服务端导出，逐条校验、幂等写入，返回 `imported` / `skipped` / `conflicts`；无法匹配的题目会跳过并给出原因，不静默丢弃。

### 升级与回退

1. 升级前创建并校验备份，记录当前 `RKA_IMAGE_TAG`。
2. 修改 tag 或重新 `--build` 后执行 `docker compose up -d`；`init` 会运行迁移（持有 advisory lock，可重复执行）。
3. `rka serve` 启动时比较数据库 `schema_migrations` 最大值与二进制内置迁移版本；数据库 schema 更新而镜像回退时**拒绝启动**并提示，不带病运行。
4. 回退镜像前，如果数据库已被新版本迁移，必须同时恢复同批次备份。代码回退不等于数据恢复。

## 开发与测试

### 本机工具链

| 工具 | 版本（仓库核对值） |
| --- | --- |
| Go | `backend/go.mod` 要求 1.25；当前环境 1.27.1 |
| Node / npm | Node 22.23.3 / npm 12.2.0（镜像构建使用同版本） |
| PostgreSQL | 17.6（开发库与 Compose 一致） |
| Playwright | 首次使用先 `(cd frontend && npx playwright install chromium)` |

```bash
npm --prefix frontend ci
make dev-db          # 启动 127.0.0.1:55432 的开发 PostgreSQL（脚本会打印可直接使用的连接串）
make build
make vet
make test            # 串行运行 Go 规则与公开 API 合同测试
```

Go 模块位于 `backend/`，可在该目录直接使用 `go test ./...`。根目录 `make build/vet/test` 会转交后端 Makefile；运行数据仍在根目录 `data/`，不会进入 Go 包遍历。

### 本机运行后端与前端

```bash
make dev-db
# 把 dev-db.sh 打印的 RKA_TEST_DATABASE_URL 值作为 RKA_DATABASE_URL 导出
export RKA_DATABASE_URL='<开发数据库连接串>'
export RKA_DATA_DIR=./data/dev
make init            # 建目录、密钥、迁移、导入内容
make serve           # Go 应用监听 127.0.0.1:8080
```

另开终端启动 Vite 开发服务器，`/api`、`/healthz`、`/readyz`、`/content` 会代理到 `RKA_BACKEND_URL`（默认 `http://127.0.0.1:8080`）：

```bash
npm --prefix frontend run dev
```

Vite 监听 `0.0.0.0:5173`，端口被占用会直接失败，不会自动换端口。AI 任务由 Go 应用和 Worker 执行；本机希望单进程调试时，可用 `RKA_WORKER_INLINE=true make serve` 让 Worker 跟随应用运行。

### 前端构建与测试

```bash
npm --prefix frontend run typecheck    # tsc -b
npm --prefix frontend run build        # 类型检查 + 构建到 frontend/dist/
npm --prefix frontend test             # Vitest 单元测试
make test             # 后端规则与 API 测试（需要开发数据库）
make test-platform    # 后端跨模块验收测试（需要开发数据库）
```

### 浏览器端到端

Playwright 跑的是生产形态：Go 应用同时提供构建产物、API 和数据库，不使用 Vite。

```bash
(cd frontend && npx playwright install chromium)   # 首次
make dev-db                       # e2e-server.sh 复用该开发数据库
npm --prefix frontend run build
(cd frontend && npx playwright test)
```

`frontend/playwright.config.ts` 会通过 `scripts/e2e-server.sh` 重建 `rka_e2e` 数据库、在 `data/e2e` 初始化数据根、构建二进制并在 `127.0.0.1:4199` 启动服务；测试结束后关闭它自己启动的服务。失败时 trace 保存在 `frontend/test-results/`，可用 `(cd frontend && npx playwright show-trace <trace.zip>)` 查看。

浏览器验收只运行当前 Go 平台的测试。旧 Node 转发器的独立检查位于 `backend/legacy/ai-handler.test.mjs`，用 `make test-legacy` 执行。

### 部署验收

```bash
bash scripts/verify-deployment.sh
```

脚本使用独立的 Compose 项目 `rka-verify`、数据目录 `./data-verify` 和端口 `8099`/`8098`/`55434`，不触碰 `./data` 或正在运行的服务。流程覆盖：干净启动 → 初始化幂等 → `/readyz` → 登录 → 内容不下发答案 → 静态快照 404 → 幂等作答 → 第二设备恢复现场 → 容器重建保留数据 → 备份 → 隔离校验 → 新目录恢复并启动第二个实例。设置 `RKA_VERIFY_KEEP=1` 可保留演练环境。

### 命令索引

| 命令 | 用途 |
| --- | --- |
| `make build` / `make vet` | Go 构建 / 静态检查 |
| `make test` | Go 规则与公开 API 合同测试（需要 `make dev-db`） |
| `make dev-db` | 启动开发 PostgreSQL（`scripts/dev-db.sh`） |
| `make migrate` / `make init` / `make import-content` | 数据库迁移 / 幂等初始化 / 内容导入 |
| `make serve` / `make worker` | 本机运行应用 / Worker |
| `make backup` / `make verify-backup ARGS=<id>` / `make retention` / `make doctor` | 本机运维命令 |
| `npm --prefix frontend run dev` / `npm --prefix frontend run build` / `npm --prefix frontend run preview` | Vite 开发 / 构建 / 预览（预览的 API 代理同样指向 Go 应用） |
| `make content` | 从相邻资料项目重建内容快照 |
| `(cd frontend && npx playwright test)` | 浏览器端到端 |
| `bash scripts/verify-deployment.sh` | Compose 部署验收 |

## 验证命令

| 检查 | 根目录命令 |
| --- | --- |
| Go 构建、静态检查与测试 | `make build vet`、`make dev-db && make test` |
| 后端跨模块验收 | `make test-platform` |
| 前端类型检查与单元测试 | `make typecheck frontend-test` |
| 浏览器端到端 | `make e2e` |
| Compose 启动、备份和恢复 | `bash scripts/verify-deployment.sh` |

集成测试使用 PostgreSQL；备份测试需要 `pg_dump` / `pg_restore`。缺少依赖时部分测试可能跳过，检查结果时应同时核对失败数与跳过数。实际模型调用需要配置有效的供应商凭据，并在管理后台验证连接和教学能力。

测试输出、浏览器 trace、运行日志和本地验收记录不提交 Git。

## 从旧 Node 入口迁移

### 遗留内容

仓库仍保留以下迁移前文件，方便对照和回退；它们不属于目标运行时：

| 遗留项 | 位置 | 状态 |
| --- | --- | --- |
| Node 生产服务器与静态资源路由 | `backend/legacy/server.mjs` | 不再作为生产入口；仅保留历史对照；详情见 `backend/legacy/README.md` |
| AI 转发处理器 | `backend/legacy/server/ai-handler.mjs` | 由 Go 的 `/api/ai/*` 任务接口替代 |
| 旧网关元数据测试 | `backend/legacy/ai-handler.test.mjs` | `make test-legacy`；已移除指向不存在用例的旧 Playwright 配置 |
| systemd 服务 | sgdev1 上的 `rka.service` | 迁移完成后停用 |
| Nginx 8104 入口 | sgdev1 的 `rka` 站点 | 迁移后改为反代 Compose 应用端口 |

### 切换步骤

1. **保留旧服务可回退。** 先不要删除旧文件和数据库；记录当前发布版本与 `rka.service` 状态。
2. **准备新部署。** 按[快速开始](#快速开始)在目标端口启动 Compose，确认 `/readyz` 为 `ready`，并建立首个管理员。
3. **迁移浏览器学习数据。** 在旧站点「设置与数据」导出完整备份（`app:"rka"`, `schemaVersion:1` 的 JSON），保存到本地。
4. **导入到新账号。** 登录新站点，在「设置与数据」选择「导入学习进度」，或带会话 Cookie 调用 `POST /api/sync/import`：

   ```bash
   curl -fsS -b cookies.txt -X POST http://127.0.0.1:8080/api/sync/import \
     -H 'Content-Type: application/json' \
     --data-binary @rka-backup.json
   ```

   `cookies.txt` 是登录后导出的会话 Cookie；也可以直接用页面上的导入入口，避免手工处理 Cookie。

   导入结果会分别列出 `imported`、`skipped` 和 `conflicts`。旧练习现场和旧 AI 对话作为只读存档导入，不会恢复成可继续的服务端现场；历史作答、标记、草稿和阅读进度进入业务表。
5. **切换入口。** 把 Nginx upstream 从 `127.0.0.1:4173` 改为 Compose 的应用端口（默认 `8080`），`nginx -t` 通过后 reload；确认 HTTPS、Cookie 和 `X-Forwarded-Proto` 正常后，再停止并禁用 `rka.service`。
6. **核对。** 抽查账号、练习题号、错题原文定位、案例/论文草稿、AI 任务和备份；旧 5173 开发入口的进度按既定约定不迁移。

### 旧练题台文件

旧练题台导出的 v1/v2 进度文件在「设置与数据」导入时，由浏览器转换为 RKA v1 结构再提交，只导入能匹配当前题库的作答与收藏；不迁移其他科目、旧 AI 范文或旧站练习现场。转换失败或没有匹配项时不会覆盖现有数据。

### 回退

Compose 部署回退用 `RKA_IMAGE_TAG` 切回已验证镜像；如果数据库已被新版本迁移，要同时恢复同批次备份（代码回退不等于数据恢复）。旧 Node 服务在确认新入口稳定前可以保留但保持停止，避免两个入口同时写入。

## 安全说明

1. **会话**：登录成功后服务端下发 HttpOnly + SameSite=Lax 的 `rka_session` Cookie；`RKA_COOKIE_SECURE=auto` 在 TLS 下自动加 `Secure`。浏览器不读取、不保存会话令牌。
2. **同源与 CSRF**：所有写请求带 `Origin` 时，其主机和端口必须与 `Host` 一致，否则返回 403；接口不通过放宽 CORS 允许跨站写入。
3. **密码与限流**：密码使用 Argon2id 哈希；同一 IP + 邮箱 15 分钟内失败 10 次后返回 429；改密、停用账号或重置密码会撤销该用户的其他设备会话。
4. **首个管理员**：只能通过受控环境变量或一次性令牌建立，成功后入口自动关闭；管理员账号不会被 `init` 重置。
5. **密钥文件**：`data/secrets/secret-key` 和 `bootstrap-token` 由 `init` 以 0600 生成；`.env`、`data/`、`docker/rka.env` 不提交版本库、不进入镜像构建上下文。
6. **凭据加密**：模型与教学凭据只在服务端保存，使用应用密钥做 AES-256-GCM 加密；管理接口只写不读，任何响应都不返回明文。
7. **答案不下发**：目录接口和题目详情不包含答案与解析；练习提交后通过 `/solution` 获取，正式考试交卷前 `revealed=false`。内容快照不作为静态资源提供，`/content/catalog.json` 返回 404，只有 `/content/licenses/{file}` 可下载来源许可。
8. **日志**：JSON 行日志包含 `request_id`、`user_id`、状态与耗时；AI 任务和教学回合带 `task_id`。日志不记录密码、令牌、密钥和完整私人草稿。
9. **备份**：一致性备份包含 `secret-key`，能解密模型凭据与教学身份；备份目录和恢复演练目录按密钥保管。

## 内容来源与许可

### 当前内容快照

| 内容 | 数量 | 说明 |
| --- | ---: | --- |
| 选择题 | 2,389 | 包含真题 / 回忆版 1,489 条、模拟题 900 条 |
| 案例分析 | 132 | 保留背景、子问题及第三方参考答案 |
| 论文题 | 104 | 保留题目要求与参考写作材料 |
| 完整资料 | 38 | 教材 20 章及前言、大纲 6 份、专题速记 8 份、备考方法 3 份 |
| 检索小节快照 | 329 | 用于检索、历史链接兼容，不是额外的完整教材 |
| 建立原文关联的选择题 | 2,088 | 自动匹配结果，包含存在缺图标记的记录 |
| 未关联选择题 | 301 | 保留在题库，不强行指向无关章节 |
| 疑似缺图选择题 | 79 | 默认从练习队列排除，可手动包含 |

题量指记录数，不等于独立知识点数。共享题干的不同填空分别保留原题号。一个题目可以关联多份资料，因此各章节题数不能直接相加为题库总量。

上述数量取自 [content/audit.json](content/audit.json) 与 [content/knowledge-links-audit.json](content/knowledge-links-audit.json)。缺图检测和知识关联均为启发式结果，不能代替逐题校对。

### 来源分工

| 来源项目 | 导入内容 |
| --- | --- |
| `ruankao-architect-practice` | 基础题库、案例、论文，以及其保存的大纲与教材清洗版资料 |
| `ruankao-architect-notes` | 四本主要手册和四份专题资料 |
| `awesome-ruankao` | 2026 上半年 67 道选择题回忆版、2026 年 10 月五套模拟卷的 375 道选择题，以及备考方法资料 |

38 份完整资料直接复制本地源文件，保留原文段落、例子、表格和图示说明，未做 AI 摘要或改写。`sourceHash` 记录源文件全文的 SHA-256。教材清洗版中的结构化重绘图、缺图说明和其他来源声明也原样保留；它们不等同于教材扫描页。

### 重建内容

只有重建内容时才需要以下相邻目录：

```text
ruankao-src/
├── awesome-ruankao/
├── ruankao-architect-notes/
├── ruankao-architect-practice/
└── rka/
```

在 `rka` 目录执行：

```bash
make content
```

脚本依次读取基础题库、解析补充题目、复制完整资料、生成检索快照和题目关联，并刷新导入报告。它不调用 AI，也不启动网站。

| 产物 | 用途 |
| --- | --- |
| `content/catalog.json` | 题库、完整资料元数据、小节元数据和双向关联索引 |
| `content/materials/*.md` | 按源文件完整保存的资料正文 |
| `content/lessons/*.md` | 用于检索和历史链接的小节正文 |
| `content/material-search.json` | 完整资料的全文搜索索引 |
| `content/search.json` | 小节级搜索索引 |
| `content/audit.json` | 数量、导入失败项、疑似缺图清单 |
| `content/knowledge-links-audit.json` | 关联覆盖、未关联题目和各资料题数 |
| `content/licenses/` | 来源许可与版权声明 |

内容版本 `version` 标识一份快照；重复导入同一版本是幂等的，不产生重复行，也不覆盖用户数据。题目 `revision` 标识题干、选项和答案内容版本：新修订发布后旧作答保留，历史成绩按当时的评分规则解释。

### 章节关联如何生成

[scripts/link-knowledge.mjs](scripts/link-knowledge.mjs) 在构建内容时离线匹配题目与原文：

1. 将完整教材与专题资料切分成有真实标题位置的小节。
2. 结合题干、有效知识标签和参考选项提取词项，并归一化 ATAM、范式、访问控制矩阵等术语。
3. 排除“PDF 自动抽取”等非知识标签，按标题、正文和术语匹配评分。
4. 为题目保存最多两份资料中的具体小节，同时记录父级标题路径和关联词。
5. 没有足够匹配依据时保留未关联状态。

章节练习筛选与错题原文入口共用这份索引。它不是官方考点归类，也没有经过全部题目人工核定；纠正关联应修改匹配规则或源数据后重建，而不是只改界面显示。

内容更新的建议检查顺序：

```bash
make content
npm --prefix frontend test
npm --prefix frontend run build
```

再抽查变动题目的题干、答案、图示和原文位置，检查两个审计文件，最后按部署流程发布。

### 许可

题库、参考答案和学习资料存在各自的来源与许可：

- `ruankao-architect-notes` 标注 **CC BY-NC-SA 4.0**，见[原许可](content/licenses/ruankao-architect-notes.txt)。
- `awesome-ruankao` 标注 **CC BY-SA 4.0**，真题保留原出题方权利声明，见[原许可](content/licenses/awesome-ruankao.txt)。
- 练题台明确区分应用代码和第三方题库授权，见[来源声明](content/licenses/practice-NOTICE.md)与[原项目许可](content/licenses/ruankao-architect-practice.txt)。

运行中的应用通过 `/content/licenses/<file>` 提供这些声明下载。来源许可不应混为 RKA 全部内容的统一许可。本文没有为 RKA 应用代码新增分发许可；npm 依赖继续遵循各包自身的许可证。对外使用资料时，应同时保留对应来源、署名和原始声明。

## 代码结构

参考 [Coze Studio 的目录分工](https://github.com/coze-dev/coze-studio)，采用 `backend/`、`frontend/`、`docker/` 和 `scripts/` 的目录分工。前后端独立管理依赖与测试；共享资料和运行数据位于根目录，生产仍由一个 Go 应用提供页面和 API。

独立开发说明：[后端](backend/README.md)、[前端](frontend/README.md)。

```text
rka/
├── backend/                  独立 Go 模块
│   ├── cmd/rka/              HTTP 服务、Worker、初始化与运维 CLI
│   ├── internal/             业务领域、基础设施、数据库迁移及 Go 测试
│   ├── legacy/               迁移前 Node 代码与独立测试
│   ├── go.mod / go.sum       后端依赖
│   ├── Makefile              后端构建、测试、运行
│   └── bin/                  本机构建产物（忽略）
├── frontend/                 独立 React + TypeScript 应用
│   ├── src/                  页面、组件、API、缓存与同步、单元测试
│   ├── public/               浏览器公开资源
│   ├── tests/                Playwright 端到端测试
│   ├── package.json          前端命令与依赖，旁边为锁文件
│   ├── *config.ts/json       Vite、Vitest、Playwright、TypeScript 配置
│   └── dist/                 前端构建产物（忽略）
├── docker/                   Dockerfile 与部署配置样例
├── content/                  共享内容快照与来源许可
├── scripts/                  内容工具、开发数据库、e2e、备份与部署验收
├── data/                     所有应用持久数据（忽略）
├── docker-compose.yml        一键启动整套服务
├── .env.example              Compose 环境配置样例
└── Makefile                  全项目统一入口，委派前后端命令
```

接口路径、载荷和错误信封见 [后端 API 文档](backend/API.md)。

## 文档导航

| 文档 | 内容 |
| --- | --- |
| [后端 README](backend/README.md) | Go 构建、测试、运行与目录职责 |
| [前端 README](frontend/README.md) | npm 安装、构建、开发与浏览器测试 |
| [后端 API 文档](backend/API.md) | HTTP 接口、载荷、状态码与并发约定 |
| [部署与运维手册](docker/README.md) | 初始化、配置、备份恢复、升级与排障 |

### 版本管理范围

提交前后端源码、依赖锁文件、数据库迁移、测试、部署配置样例、内容快照及上述使用文档。`content/` 是随应用交付的题库与原文资料，不属于运行数据。

开发规格、实施计划、评审记录、验收日志和过程截图不提交。运行数据、备份、模型缓存和本地记录统一放在 `data/`；依赖目录、构建输出、测试报告、覆盖率、日志、真实环境变量和临时文件由 `.gitignore` 排除。新增工具产生的输出也应放在这些目录内。

现有业务数据保持原位；忽略规则只影响版本管理。过去已被跟踪的产物须从 Git 索引移除，不能只添加忽略规则。

## 常见问题

| 现象 | 先检查什么 | 处理方式 |
| --- | --- | --- |
| `/readyz` 返回 503 或 `not_ready` | `checks` 里哪一项不是 ok | 逐项处理：数据库不可用查 `docker compose ps`/日志；schema 不一致执行 `docker compose run --rm app migrate`；content empty 执行 `docker compose run --rm app import-content` |
| `init` 退出码非 0 | `docker compose logs init` | 常见原因是 `POSTGRES_PASSWORD` 未设置、数据库未就绪或内容快照缺失；修复后重跑 `docker compose run --rm init` |
| 页面能打开但接口 401 | 是否登录、Cookie 是否被浏览器拦截 | 重新登录；检查访问协议与 `RKA_COOKIE_SECURE` 是否匹配；反向代理下设置 `RKA_TRUST_PROXY=true` 并透传 `X-Forwarded-Proto` |
| 登录提示失败次数过多 | 是否触发限流（同一 IP+邮箱 15 分钟 10 次） | 等待窗口结束或换账号；不要用脚本反复重试 |
| AI 显示不可用 | `/api/ai/status` 的 `unavailableReason` | 配置 `RKA_AI_ENDPOINT`/`MODEL`/`API_KEY`，或启用 teaching profile；基础学习不受影响 |
| AI 显示已配置但没有回复 | 连接状态、能力验证、上游额度与网络 | 查看 `connectionStatus`/`capabilityStatus`；容器内 Magpie 需要正式 gateway key；状态接口只验证配置字段，不等于模型可用 |
| 备份失败 | 数据库连接、磁盘空间、`data/backups` 可写 | 看 `docker compose logs app`；不要在数据库写入时直接复制 `data/postgres` |
| 恢复后登录不了或模型凭据失效 | 是否同时恢复了 `secret-key` | 用同批次备份恢复；密钥丢失时模型凭据和教学身份密文无法解密 |
| 换设备后进度没同步 | 是否同一个账号、网络是否恢复 | 查看页面顶部的同步状态；待同步项可手动重试；服务器数据不受浏览器清理影响 |
| 两台设备同时改草稿出现冲突 | 409 与冲突版本 | 在界面选择保留本机或服务器版本；双方版本都会记录，不会静默覆盖 |
| 正式考试刷新后答案还在 | 是否交卷、是否在考试模式 | 考试现场、截止时间和评分以服务器为准；交卷前不展示参考答案 |
| 旧练题台备份导入 0 条 | 题目 ID 是否在当前题库、文件是否为 v1/v2 | 查看 `skipped` 原因；只有能匹配的作答与收藏会导入，旧 AI 范文和旧站现场不迁移 |
| 旧入口 8104 打不开 | Nginx 与 `rka.service` 状态 | 迁移期间先恢复旧服务；切换后确认 Nginx 指向 Compose 端口 |
| 端口 8080 被占用 | `ss -ltnp` 或 `docker compose ps` | 修改 `.env` 的 `RKA_PORT` 后 `docker compose up -d` |

## 当前限制

- **单机 Compose 部署**：不承诺多节点高可用、任意规模并发或固定响应时延；容量、索引耗时、模型延迟和恢复耗时尚未测试。
- **外部模型需要凭据**：真实 DeepTutor、Embedding 与当前 Magpie 路由已验证；OCR、其他模型和供应商组合仍需单独验收。缺少凭据时不影响基础学习。
- **内容以随仓库快照为准**：当前没有网页上传自有资料、OCR 或复杂版面解析；题目与原文关联是启发式结果，需要人工抽查和修正。
- **不提供官方评分或通过率预测**：AI 点评、学习报告和复习安排是学习辅助，不是官方阅卷，也不输出未经校准的通过率。
- **单账号数据边界**：共享资料由管理员授权，个人作答、草稿、笔记和 AI 任务按用户隔离；不要把管理员凭据交给普通学习者。
- **旧 Node 入口保留但不再演进**：它只用于迁移对照与回退，修复和新功能只进入 Go + Compose 部署。

题库、参考答案和学习资料的版权与许可见[内容来源与许可](#内容来源与许可)；它们不随应用代码的许可一并分发。
