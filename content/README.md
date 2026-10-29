# 学习资料来源与维护

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

上述数量取自 [content/audit.json](audit.json) 与 [content/knowledge-links-audit.json](knowledge-links-audit.json)。缺图检测和知识关联均为启发式结果，不能代替逐题校对。

### 来源分工

| 来源项目 | 导入内容 |
| --- | --- |
| `ruankao-architect-practice` | 基础题库、案例、论文，以及其保存的大纲与教材清洗版资料 |
| `ruankao-architect-notes` | 四本主要手册和四份专题资料 |
| `awesome-ruankao` | 2026 上半年 67 道选择题回忆版、2026 年 10 月五套模拟卷的 375 道选择题，以及备考方法资料 |

完整资料从来源项目原文导入，保留原文段落、例子、表格和图示说明，未做 AI 摘要或改写。`sourceHash` 记录源文件全文的 SHA-256。教材清洗版中的结构化重绘图、缺图说明和其他来源声明也原样保留；它们不等同于教材扫描页。

### 重建内容

只有重建内容时才需要以下相邻目录：

```text
workspace/
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

[scripts/link-knowledge.mjs](../scripts/link-knowledge.mjs) 在构建内容时离线匹配题目与原文：

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

- `ruankao-architect-notes` 标注 **CC BY-NC-SA 4.0**，见[原许可](licenses/ruankao-architect-notes.txt)。
- `awesome-ruankao` 标注 **CC BY-SA 4.0**，真题保留原出题方权利声明，见[原许可](licenses/awesome-ruankao.txt)。
- 练题台明确区分应用代码和第三方题库授权，见[来源声明](licenses/practice-NOTICE.md)与[原项目许可](licenses/ruankao-architect-practice.txt)。

运行中的应用通过 `/content/licenses/<file>` 提供这些声明下载。来源许可不应混为 RKA 全部内容的统一许可。应用代码与资料的授权范围分别判断；npm 依赖继续遵循各包自身的许可证。对外使用资料时，应同时保留对应来源、署名和原始声明。

[返回项目首页](../README.md)
