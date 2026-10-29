# 发布与仓库维护

RKA 通过 GitHub Actions 验证变更，通过 GHCR 发布应用镜像，通过 GitHub Releases 分发部署归档。前端是应用的一部分，`package.json` 保持 `private: true`，不发布到 npm。

## 自动检查

- PR 和 `main` 提交运行 CI：前端、Go race 与真实数据库测试、浏览器测试、容器启动及工作流校验。
- CodeQL 检查 Go 与 TypeScript，并每周运行。
- Dependabot 每周检查 Go、npm、Docker、Compose 和 Actions 更新，以 PR 提交，不自动合并。
- 第三方 Actions 固定到完整提交 SHA，默认令牌只读；发布任务才申请 Release 和 Packages 写权限。
- 测试报告与失败 trace 作为短期 Actions artifacts 保存，不提交 Git，也不上传运行数据目录。

## 创建版本

使用 `vX.Y.Z` 表示正式版本，`vX.Y.Z-rc.N`、`-beta.N` 或 `-alpha.N` 表示预发布。标签的基础版本须与 `frontend/package.json` 一致，并指向 `main` 历史中的提交。

以首次候选版为例，在 CI 通过后执行：

```bash
git switch main
git pull --ff-only
git tag -a v0.1.0-rc.1 -m 'RKA v0.1.0-rc.1'
git push origin v0.1.0-rc.1
```

升级基础版本时，先在 `frontend/` 执行 `npm version X.Y.Z --no-git-tag-version`，提交清单和锁文件，经 CI 检查后再打标签。已经发布的标签不移动；修复问题请发布新版本。

Release 工作流会重新执行完整 CI，随后发布 `linux/amd64` 和 `linux/arm64` 应用镜像、构建来源信息、SBOM、部署归档与 SHA-256 校验文件。预发布不更新 `latest`；正式版本同时更新版本标签、次版本标签和 `latest`。

发布失败时，修复配置后可重跑失败任务，或从 Actions 手动运行 Release 并填写已有标签。模型服务镜像的架构兼容性由各镜像维护者决定；应用镜像的双架构支持不代表所有依赖均已在两种架构验收。

## 使用发布镜像

优先从 [Releases](https://github.com/hujm2023/rka/releases) 下载部署归档，核对 `SHA256SUMS` 后按归档内 `START.md` 操作。

也可在源码目录 `.env` 设置：

```dotenv
RKA_IMAGE=ghcr.io/hujm2023/rka
RKA_IMAGE_TAG=0.1.0-rc.1
```

```bash
docker compose pull
docker compose up -d --no-build
```

版本号应替换为实际已发布版本。持久数据仍位于 `data/`，发布包不会包含用户数据或真实配置。镜像内的学习资料保留各自许可，包括部分资料的非商业使用限制。

GHCR 首次发布的包默认可能为私有。维护者需在 [Package settings](https://github.com/users/hujm2023/packages/container/rka/settings) 将其设为 Public，并确认关联当前仓库，匿名用户才可拉取。包可见性与仓库可见性分开管理；不要仅凭仓库公开就宣称镜像可匿名下载。

## 仓库管理设置

仓库中的工作流、模板、CODEOWNERS、Dependabot 和 Release 分类随提交生效。分支保护、合并方式、标签、Topics、安全报告入口需要额外的仓库管理权限。

维护者使用具有相应写权限的 GitHub CLI 凭据执行：

```bash
scripts/configure-github.sh hujm2023/rka
```

设置内容保存在 `.github/repository-settings.json` 与 `.github/branch-protection.json`：启用 Issues/Discussions、使用 squash merge、合并后删除分支，以及要求 PR 审查和全部 CI 检查。管理员保留直接维护能力。

该脚本执行成功才表示远程设置已应用；这些 JSON 文件本身不是 GitHub 自动读取的配置。若返回 403，请由仓库管理员补齐 Administration、Issues 和 Vulnerability alerts 等对应权限后执行。不要把个人访问令牌提交到代码中。

实现依据：[GitHub 镜像发布](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)、[Dependabot 配置](https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/secure-your-dependencies/configure-version-updates)、[Packages 权限](https://docs.github.com/en/packages/learn-github-packages/about-permissions-for-github-packages)。
