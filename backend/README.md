# RKA 后端

独立 Go 模块（`go.mod` / `go.sum`，Go 1.25+），模块名保持 `rka`。HTTP 服务、Worker、运维 CLI 和数据库迁移全部在这里维护。

```text
backend/
├── cmd/rka/              serve、worker、init、migrate、backup 等命令
├── internal/             按业务领域组织的实现及 Go 测试
│   ├── store/migrations/ 内嵌 SQL 迁移
│   └── platform/         跨模块 API 验收
├── legacy/               旧 Node 转发器，仅供历史对照
├── bin/                  本机构建输出，不提交 Git
├── go.mod / go.sum       Go 依赖
└── Makefile              后端开发、测试、运行入口
```

在仓库根目录先执行 `make dev-db`，然后可以在本目录独立操作：

```bash
go mod download
make build               # bin/rka
make vet
make test                # 需要 PostgreSQL；每个测试创建并清理独立数据库
make test-platform       # 跨模块验收
```

测试库连接可通过 `RKA_TEST_DATABASE_URL` 指定；备份往返测试需要匹配 PostgreSQL 主版本的 `pg_dump` / `pg_restore`。数据库或客户端工具缺失时部分测试会跳过，验收时应检查测试输出。

本机启动：

```bash
export RKA_DATABASE_URL='<scripts/dev-db.sh 输出的开发库连接串>'
make init
make serve               # 127.0.0.1:8080
# 在另一个终端运行 make worker，或使用 RKA_WORKER_INLINE=true make serve
```

本目录 Makefile 默认将数据、内容快照和静态资源分别指向仓库根目录的 `data/`、`content/`、`frontend/dist/`，可通过 `RKA_DATA_DIR`、`RKA_CONTENT_SNAPSHOT`、`RKA_STATIC_DIR` 覆盖。直接运行 `go run` 或 `bin/rka` 时应显式传入这些路径；CLI 按进程工作目录解析相对路径，不自动加载 `.env`。根目录 Makefile 会先将路径转换为绝对路径再转交本目录。

只做 Go 构建或 API 开发不需要 Node。需要由 Go 提供完整页面时，先在仓库根目录运行 `make web`。生产镜像统一由 [docker/Dockerfile](../docker/Dockerfile) 构建；Compose 和运行数据继续位于仓库根目录。

接口见 [API 合同](API.md)，生产运维见 [运维手册](../docker/README.md)。
