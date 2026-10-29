# 参与贡献

欢迎提交可复现的问题、使用建议和范围明确的 Pull Request。较大的功能改动建议先开 Issue 讨论使用场景。

## 开发与验证

前端、后端独立管理，分别阅读 [frontend/README.md](frontend/README.md) 和 [backend/README.md](backend/README.md)。在项目根目录常用：

```bash
make frontend-install
make dev-db
make build vet test
make typecheck frontend-test
make e2e
```

Go 集成测试需要 PostgreSQL，备份测试还需要 PostgreSQL 17 客户端。CI 会提供这些依赖，并将任何测试跳过视为失败。浏览器测试与容器检查均运行实际应用；模型调用在测试中使用受控替身，不需要真实模型密钥。

## 提交 Pull Request

从 `main` 建立分支，说明具体问题、修改后的行为与实际验证结果。提交标题可采用 `feat:`、`fix:`、`docs:`、`test:`、`chore:` 等前缀。涉及 API 或数据库时，说明迁移与兼容性；界面变更可附截图。

仅提交源码、测试、依赖锁文件、必要使用文档和配置样例。运行数据、密钥、测试报告及过程记录应放入忽略目录。不要修改已有数据库迁移来代替新迁移。

应用代码贡献采用 [MIT](LICENSE)。学习资料和题库必须保留来源及各自许可，不能因代码采用 MIT 而重新授权。请避免无关的大范围格式化。

## 沟通

围绕问题和证据交流，尊重不同经验与背景，避免人身攻击、骚扰和公开私人信息。安全问题按 [SECURITY.md](SECURITY.md) 联系维护者。
