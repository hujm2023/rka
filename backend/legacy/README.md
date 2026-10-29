# 旧 Node 入口

这里保留迁移前的静态服务器和 AI 转发器，供历史对照；当前平台运行 Go 服务，不使用此目录。它不提供当前平台的账号、练习与持久化 API。

- `server.mjs`：旧服务入口，静态路径已调整为 `../../frontend/dist/`。
- `server/ai-handler.mjs`：旧 Chat Completions 转发与状态接口。
- `ai-handler.test.mjs`：从前端测试中移入的两项网关元数据检查，使用 Node 内置测试器，无 npm 依赖。

在仓库根目录运行 `make test-legacy`，或在此目录执行 `node --test ai-handler.test.mjs`。旧 `playwright.production.config.ts` 指向的用例此前已删除，本次一并移除失效入口；当前浏览器验收统一在 `frontend/`。

如仍维护外部 systemd 的旧 Node 启动命令，入口已从根目录 `server.mjs` 移到本目录，需要另行更新该服务的路径。目录整理不会切换或重启外部服务。
