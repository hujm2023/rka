# 软考高级-系统架构设计师

[![CI](https://github.com/hujm2023/rka/actions/workflows/ci.yml/badge.svg)](https://github.com/hujm2023/rka/actions/workflows/ci.yml)
[![CodeQL](https://github.com/hujm2023/rka/actions/workflows/codeql.yml/badge.svg)](https://github.com/hujm2023/rka/actions/workflows/codeql.yml)

RKA 是面向软考高级「系统架构设计师」的学习平台，将资料阅读、章节练习、错题复习、案例与论文写作、AI 辅导整合到同一个学习空间。

**阅读原文 → 练习关联题目 → 对照解析 → 回到知识点 → 复习巩固。**

## 开始使用

安装并启动 Docker（包含 Compose v2）后，在项目根目录执行：

```bash
make run
```

自动准备配置、构建镜像、初始化数据并启动整套服务，就绪后打开网页。首次生成的管理员账号和密码保存在 `.env`，已有配置与数据会保留。

| 你想做什么 | 阅读文档 |
| --- | --- |
| 部署并启动服务 | [部署启动](docker/README.md) |
| 阅读、刷题、写作和使用 AI | [使用指南](USER_GUIDE.md) |
| 配置模型、端口和运行参数 | [配置参考](docker/CONFIGURATION.md) |
| 备份、恢复或升级 | [运维指南](docker/OPERATIONS.md) |

## 主要功能

- **知识手册**：阅读完整原文、定位章节，直接练习关联题目。
- **题库练习**：自由练习、章节突破、真题套卷、错题、收藏与到期复习。
- **案例与论文**：自动保存草稿、查看历史版本、处理多设备编辑冲突。
- **AI 辅导**：知识讲解、解题提示与写作点评，结合资料引用辅助学习。
- **学习记录**：账号同步、刷新续学、进度查看及个人数据导入导出。

AI 功能需要配置模型服务；阅读、练习和写作可独立使用。AI 点评和题库参考答案用于辅助学习，不代表官方阅卷结果。

## 开发

前端使用 React + TypeScript，后端使用 Go，数据存储使用 PostgreSQL。Docker Compose 管理整套服务，持久数据默认保存在项目目录 `data/`。

```text
backend/    Go 服务、Worker、数据库迁移与测试
frontend/   页面、组件、npm 依赖与浏览器测试
docker/     镜像构建、部署配置与操作指南
content/    题库、学习资料和来源许可
scripts/    内容处理、开发和运维工具
```

开发说明：[后端](backend/README.md) · [前端](frontend/README.md) · [API](backend/API.md)。运行数据、构建产物、测试报告和真实密钥由 `.gitignore` 排除。

## 资料来源

学习资料与题库来自 `ruankao-architect-practice`、`ruankao-architect-notes` 和 `awesome-ruankao`，保留原文及来源声明。各来源的许可证分别适用，使用和分发前请阅读[资料来源与许可](content/README.md)。

应用代码采用 [MIT](LICENSE)。参与改进见[贡献指南](CONTRIBUTING.md)，发布流程与镜像使用见[发布指南](RELEASING.md)。
