# 部署启动

使用 Docker Compose 启动 RKA。本文命令均在项目根目录执行；网站启动后的操作见[使用指南](../USER_GUIDE.md)。

## 准备条件

- Docker 与 Docker Compose v2。
- 首次构建时可访问镜像仓库、npm、Go 模块源及模型下载源。
- 对外提供一个 HTTP 端口，默认 `8080`。

Go 应用、Worker、PostgreSQL、DeepTutor 和 Embedding 均通过容器运行，无需分别安装这些运行环境。首次下载镜像和 Embedding 模型可能需要较长时间。

## 一条命令启动

安装并启动 Docker（包含 Compose v2）后，在项目根目录执行：

```bash
make run
```

该命令会依次完成：

1. 检查 Docker 与 Compose 是否可用。
2. 首次从 `.env.example` 生成权限为 `600` 的 `.env`，创建独立随机数据库密码和管理员密码；已有文件保持原样。
3. 构建前后端镜像，拉取缺失的依赖镜像，初始化数据目录、密钥、数据库和内容。
4. 准备分词资源与 Embedding 模型，等待各容器的健康检查通过。
5. 打开浏览器。首次登录账号为 `admin@example.com`，密码见 `.env` 中的 `RKA_ADMIN_PASSWORD`。

只需 Make、Bash 和 Docker，无需在宿主机安装 Node.js、Go 或 PostgreSQL。AI 模型凭据需要自行配置；知识索引会在后台继续处理。命令不会安装系统软件或重置已有账号。

无图形界面的服务器会打印访问链接。默认地址根据实际映射端口生成；如果使用域名或反向代理，在 `.env` 设置 `RKA_OPEN_URL=https://your-domain.example/`。也可通过环境变量控制启动：

```bash
RKA_OPEN_BROWSER=0 make run       # 启动后不打开浏览器
RKA_RUN_BUILD=0 make run          # 使用已有镜像，不强制重新构建
RKA_RUN_TIMEOUT=1800 make run     # 服务就绪等待上限，单位秒，默认 900
BROWSER=firefox make run          # 指定浏览器可执行文件或包装脚本
```

以下步骤适用于希望手动配置、启动的部署方式。

## 获取项目与配置

```bash
git clone https://github.com/hujm2023/rka.git
cd rka
cp .env.example .env
chmod 600 .env
```

已有 `.env` 时直接编辑它，保留原有配置。至少填写数据库密码和首个管理员信息，以下值均为占位示例：

```dotenv
POSTGRES_PASSWORD=<长随机字母数字串>
RKA_ADMIN_EMAIL=admin@example.com
RKA_ADMIN_PASSWORD=<至少10个字符的密码>
RKA_ADMIN_DISPLAY_NAME=管理员
RKA_PORT=8080
```

更多参数见[配置参考](CONFIGURATION.md)。`.env` 包含私密配置，不提交 Git。

## 启动并登录

```bash
docker compose up -d --build
docker compose ps -a
curl -fsS http://localhost:8080/readyz
```

一次性任务 `init` 创建数据目录、密钥、数据库结构和内容，并初始化管理员。`init` 显示 `Exited (0)` 为正常；应用、Worker 和数据库应进入运行状态，`/readyz` 返回 `status: "ready"` 表示基础学习服务就绪。

浏览器打开 `http://localhost:8080/`，使用配置的管理员账号登录。在其他设备访问时，将 `localhost` 换成部署主机的地址；修改端口后使用相应端口。

管理员可以在「管理后台」创建账号或发出邀请。已有管理员时，重复运行 `init` 不会重置其密码。

<details>
<summary>不在环境变量中设置管理员：使用一次性令牌</summary>

留空 `RKA_ADMIN_EMAIL` 和 `RKA_ADMIN_PASSWORD` 后启动服务，通过以下命令读取初始化令牌：

```bash
docker compose exec app cat /data/secrets/bootstrap-token
```

打开 `/setup`，填写令牌和管理员信息。首个管理员创建成功后，该入口关闭。不要分享或提交令牌。

</details>

## 配置 AI

在 `.env` 中填写一个兼容 Chat Completions 的模型接口；端点必须是容器可访问的完整地址：

```dotenv
RKA_AI_ENDPOINT=https://api.example.com/v1/chat/completions
RKA_AI_MODEL=<模型名称或路由组>
RKA_AI_API_KEY=<供应商或网关密钥>
RKA_AI_GATEWAY_NAME=模型服务
```

```bash
docker compose up -d app worker
```

默认由 DeepTutor 提供教学能力，Embedding 用于知识检索。到「管理后台」检查模型连接和知识索引状态，再在页面中发起一次提问。基础服务就绪不代表模型凭据和全部索引已经可用。

如果仅使用兼容网关，可将 `RKA_DEEPTUTOR_URL` 显式设为空，让 Go 直接调用网关；此设置改变调用方式，不会自动移除 Compose 中的教学容器。

也可以用 `COMPOSE_PROFILES=gateway` 启用 Magpie 容器。需要配置其供应商、路由组和独立的 gateway key；`MAGPIE_WEB_KEY` 是管理界面密钥，不能作为推理密钥使用。相关参数见[配置参考](CONFIGURATION.md)。

网站「设置」还支持浏览器直连自定义接口，需要供应商允许跨域访问。自定义 API Key 仅保留在当前页面内存；共享部署建议统一配置服务端模型接口。

## 日常启动与停止

| 操作 | 命令 |
| --- | --- |
| 启动 | `docker compose up -d` |
| 查看服务状态 | `docker compose ps -a` |
| 查看应用日志 | `docker compose logs -f app worker` |
| 停止服务 | `docker compose down` |
| 应用配置修改 | `docker compose up -d` |

学习记录、原文、教学状态和密钥默认保存在项目的 `data/` 中。停止或重建容器会保留该目录；不要删除它来解决启动问题。

升级、备份、恢复和故障处理见[运维指南](OPERATIONS.md)。前后端源码开发分别见[后端](../backend/README.md)和[前端](../frontend/README.md)。

[返回项目首页](../README.md)
