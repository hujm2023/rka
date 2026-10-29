# RKA 前端

独立 React + TypeScript + Vite 应用。npm 依赖、锁文件、类型检查、单元测试和浏览器测试在本目录维护。

```text
frontend/
├── src/                  页面、组件、API、账号缓存与同步；含单元测试
├── public/               favicon 等公开静态文件
├── tests/                Playwright 端到端测试及辅助函数
├── index.html            浏览器入口
├── package.json / package-lock.json
├── vite.config.ts / vitest.config.ts / playwright.config.ts
├── tsconfig.json
└── dist/                 构建输出，不提交 Git
```

开发环境使用 Node 26.13.0，与镜像和 CI 保持一致。

在本目录执行：

```bash
npm ci
npm run dev              # 5173，API 默认代理到 Go 的 8080
npm run typecheck
npm test
npm run build            # 生成 dist/
npm run preview          # 4173，API 仍由 Go 提供
```

可设置 `RKA_BACKEND_URL` 指向其他开发后端。前端依赖安装与构建无需 Go 工具链；运行页面业务需要启动后端。

内容原文一致性单元测试使用共享的 `../content/` 快照。端到端测试使用真正的 Go 服务与 PostgreSQL：

```bash
# 先在仓库根目录 make dev-db
npx playwright install chromium
npm run build
npm run test:e2e
```

Playwright 通过 `../scripts/e2e-server.sh` 编译 `../backend/`、读取 `../content/`，使用根目录 `data/e2e/` 和独立的 `rka_e2e` 数据库。失败 trace 位于本目录 `test-results/`。可用 `RKA_E2E_DB` / `RKA_E2E_DATA_DIR` 为每次运行指定独立位置。

根目录 `make web`、`make frontend-test`、`make e2e` 为对应入口；Go 测试在 `backend/` 管理，不再通过 npm 脚本调用。接口见 [API 合同](../backend/API.md)。
