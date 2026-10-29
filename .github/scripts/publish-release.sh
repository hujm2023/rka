#!/usr/bin/env bash
set -euo pipefail
version="${RELEASE_TAG#v}"
bundle="rka-${RELEASE_TAG}-deploy"
mkdir -p "data/release/$bundle/docker" "data/release/$bundle/scripts" "data/release/$bundle/content"
cp docker-compose.yml USER_GUIDE.md LICENSE "data/release/$bundle/"
cp docker/*.md "data/release/$bundle/docker/"
cp scripts/backup.sh "data/release/$bundle/scripts/"
cp -R content/licenses "data/release/$bundle/content/"
cp .env.example "data/release/$bundle/.env.example"
# The archive runs a published image; it never needs application source builds.
python3 - "$bundle" "$IMAGE" "$version" <<'PY'
from pathlib import Path
import sys
root = Path('data/release') / sys.argv[1]
p = root / '.env.example'
s = p.read_text().replace('RKA_IMAGE=rka\n', f'RKA_IMAGE={sys.argv[2]}\n').replace('RKA_IMAGE_TAG=local\n', f'RKA_IMAGE_TAG={sys.argv[3]}\n')
p.write_text(s)
(root / 'START.md').write_text('''# 启动发布镜像

复制 `.env.example` 为 `.env`，填写数据库密码、管理员及模型配置，然后执行：

```bash
docker compose pull
docker compose up -d --no-build
```

访问 `http://localhost:8080/`。应用数据保存在当前目录的 `data/`。
镜像包含应用、前端和内容快照，归档无需安装 Go 或 Node。
源码开发及源码构建说明请阅读 GitHub 仓库；本归档不包含构建源码。
''')
(root / 'README.md').write_text((root / 'START.md').read_text() + '\n使用说明：[使用指南](USER_GUIDE.md)。部署参数见 [配置参考](docker/CONFIGURATION.md)，资料许可见 `content/licenses/`。\n')
# Keep reference links usable without copying source into the deployment archive.
for p in (root / 'docker').glob('*.md'):
    s = p.read_text().replace('../backend/README.md', 'https://github.com/' + __import__('os').environ['GITHUB_REPOSITORY'] + '/blob/' + __import__('os').environ['RELEASE_TAG'] + '/backend/README.md').replace('../frontend/README.md', 'https://github.com/' + __import__('os').environ['GITHUB_REPOSITORY'] + '/blob/' + __import__('os').environ['RELEASE_TAG'] + '/frontend/README.md')
    p.write_text(s)
PY
printf '%s@%s\n' "$IMAGE" "$IMAGE_DIGEST" > "data/release/$bundle/image-digest.txt"
tar -czf "data/release/${bundle}.tar.gz" -C data/release "$bundle"
(cd data/release && sha256sum "${bundle}.tar.gz" > SHA256SUMS)
cat > data/release/notes.md <<EOF_NOTES
## 安装

下载部署归档，解压后按 START.md 配置并启动。
应用镜像：\`$IMAGE:$version\`（linux/amd64、linux/arm64）。
校验摘要：\`$IMAGE@$IMAGE_DIGEST\`。

默认教学与 Embedding 依赖的架构支持以各自镜像为准。
首次 GHCR 包可能需要维护者在 Package settings 中改为 Public 才能匿名拉取。

## 许可

应用代码采用 MIT；镜像内学习资料按 content/licenses 中各自的条款使用，
其中部分资料包含非商业使用限制。资料许可不因应用代码的 MIT 许可而改变。
EOF_NOTES
flags=(--verify-tag --title "RKA $RELEASE_TAG" --notes-file data/release/notes.md --generate-notes)
if [ "$PRERELEASE" = "true" ]; then flags+=(--prerelease --latest=false); fi
gh release create "$RELEASE_TAG" "data/release/${bundle}.tar.gz" data/release/SHA256SUMS "${flags[@]}"
