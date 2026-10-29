# RKA 平台 HTTP 接口合同（v1）

本文件是前后端、Worker 与测试共同遵守的**唯一接口指针**。实现方按此实现，前端按此调用，测试按此断言。

## 通用约定

- 所有接口前缀 `/api`，同源调用；浏览器使用 `credentials: 'same-origin'`。
- 请求与响应均为 JSON（`Content-Type: application/json; charset=utf-8`），SSE 除外。
- 身份通过 HttpOnly Cookie `rka_session` 传递，前端不读取、不存储 token。
- 未登录返回 `401 {"error":{"code":"unauthorized","message":"请先登录。"}}`。
- 权限不足返回 `403`，不存在返回 `404`，版本冲突返回 `409`，参数错误返回 `400`，限流返回 `429`。
- 错误信封固定为：

```json
{"error":{"code":"conflict","message":"数据已被其他修改更新，请刷新后重试。","details":{"serverVersion":7,"serverValues":{}}}}
```

- 所有时间均为 RFC 3339 UTC 字符串。
- 所有写入接口都必须幂等或显式冲突：客户端生成的 ID（作答事件、AI 任务）用于幂等；版本号用于乐观并发。
- 服务端时间以 PostgreSQL `now()` 为准（`GET /api/sync/state` 返回 `serverTime`）。
- 分页统一使用 `?limit=&cursor=`，响应返回 `{"items":[], "nextCursor":""}`。

## 1. 身份与会话（`backend/internal/auth`）

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| POST | `/api/auth/bootstrap` | 匿名 + 一次性令牌 | 建立首个管理员，成功后关闭入口 |
| POST | `/api/auth/login` | 匿名 | 登录并下发 Cookie |
| POST | `/api/auth/logout` | 登录 | 撤销当前会话 |
| GET | `/api/auth/me` | 匿名可用 | 当前用户；未登录返回 `{"user":null}` |
| POST | `/api/auth/password` | 登录 | 修改密码；其他设备会话被撤销 |
| GET | `/api/auth/sessions` | 登录 | 设备会话列表 |
| DELETE | `/api/auth/sessions/{id}` | 登录 | 退出指定设备 |
| POST | `/api/auth/invites` | 管理员 | 创建邀请，返回一次性 token |
| POST | `/api/auth/invites/accept` | 匿名 | 使用邀请设置密码并登录 |
| GET | `/api/admin/users` | 管理员 | 账号列表 |
| POST | `/api/admin/users` | 管理员 | 直接创建账号（首次登录需改密） |
| PATCH | `/api/admin/users/{id}` | 管理员 | 角色 / 状态 / 重置密码；立即生效 |
| GET | `/api/admin/grants` | 管理员 | 共享资源授权列表 |
| POST | `/api/admin/grants` | 管理员 | 授予 material / collection / capability |
| DELETE | `/api/admin/grants/{id}` | 管理员 | 撤销授权 |

关键载荷：

```jsonc
// POST /api/auth/login  {email, password} -> 200 {user, expiresAt}
// POST /api/auth/bootstrap {token, email, password, displayName} -> 201 {user, expiresAt}
// GET  /api/auth/me -> {user, capabilities:{admin:bool}, session:{id, expiresAt}}
// GET  /api/auth/sessions -> {sessions:[{id,userAgent,ip,createdAt,lastSeenAt,expiresAt,current}]}
// POST /api/auth/invites {email, role} -> 201 {id, email, role, token, expiresAt}
// PATCH /api/admin/users/{id} {displayName?, role?, status?, newPassword?} -> {user}
// POST /api/admin/grants {userId, resourceType, resourceId} -> 201 {id}
```

规则：

1. 角色只有 `admin`、`learner`；状态只有 `active`、`disabled`。
2. 角色变更、停用、撤销授权在**下一个请求**生效（中间件每次从数据库读取身份）。
3. 停用账号或重置密码时撤销该用户的其他会话。
4. 平台必须始终保留至少一个可用管理员（`last_admin` 冲突）。
5. 登录失败限流：同一 IP+邮箱 15 分钟内 10 次失败后返回 429。

## 2. 内容（`backend/internal/content`）

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/content/catalog` | 登录 | 资料元数据、题目元数据、案例/论文条目、知识点关联索引 |
| GET | `/api/content/questions` | 登录 | 题目分页查询（**不含答案与解析**） |
| GET | `/api/content/questions/{id}` | 登录 | 题目详情（**不含答案与解析**） |
| GET | `/api/content/questions/{id}/solution` | 登录 | 答案与解析；需已提交作答或管理员 |
| GET | `/api/content/materials` | 登录 | 资料列表 |
| GET | `/api/content/materials/{id}` | 登录 + 资料权限 | 资料元数据 + 小节 + 当前版本 |
| GET | `/api/content/materials/{id}/versions` | 登录 + 资料权限 | 历史版本列表 |
| GET | `/api/content/materials/{id}/raw` | 登录 + 资料权限 | 下载原件（`text/markdown`），可带 `?version=` |
| GET | `/api/content/materials/{id}/content` | 登录 + 资料权限 | 返回正文（用于阅读器） |
| GET | `/api/content/links?questionId=` | 登录 | 题目到原文小节的关联（含 `materialId`、`anchor`、`sectionKey`、`title`、`path`、`status`） |
| GET | `/api/content/search?q=&limit=` | 登录 | 资料标题、正文、题目联合检索 |
| POST | `/api/content/annotations` | 登录 | 新增批注/摘录 |
| GET | `/api/content/annotations?materialId=` | 登录 | 我的批注 |
| DELETE | `/api/content/annotations/{id}` | 登录 | 删除批注（软删除） |
| POST | `/api/admin/content/import` | 管理员 | 触发导入任务，返回 job |
| GET | `/api/admin/content/jobs` | 管理员 | 导入任务与失败明细 |
| GET | `/api/admin/content/jobs/{id}` | 管理员 | 单条任务 |
| PATCH | `/api/admin/content/links/{id}` | 管理员 | 审核关联：`{"status":"approved"\|"rejected"}` |
| PATCH | `/api/admin/content/questions/{id}` | 管理员 | 发布新题目修订（生成新 `revision`，旧作答保留） |
| PATCH | `/api/admin/content/questions/{id}/review` | 管理员 | 缺图/重复/争议状态：`{"reviewStatus":"published"\|"needs_review"\|"withdrawn"}` |
| PATCH | `/api/admin/content/materials/{id}/review` | 管理员 | 资料发布状态 |

`GET /api/content/catalog` 响应（结构保持与现有前端兼容，去掉答案字段）：

```jsonc
{
  "version": "d58c30981ac20a",
  "serverTime": "2026-10-28T12:00:00Z",
  "questions": [{
    "id":"mock-...-001","revision":"ab12","term":"2026年5月 模拟卷1","paper":"模拟卷1",
    "questionNo":1,"module":"computer_foundation","knowledge":"操作系统—进程同步与互斥",
    "sourceType":"mock","difficulty":"★","stem":"...","options":{"A":"0","B":"1","C":"5","D":"6"},
    "figureMissing":false,"multiBlank":false,"figure":null,
    "sourceFile":"...","sourceLabel":"...","sourceUrl":"...","reviewStatus":"published"
  }],
  "materials": [{
    "id":"material-...","title":"第6章 数据库设计基础知识","book":"...","kind":"textbook",
    "module":"database","sourceFile":"...","sourceUrl":"...","license":"...",
    "chars":12345,"minutes":25,"excerpt":"...","version":3,"indexStatus":"ready",
    "sections":[{"id":"section-28226","key":"sec-ch6-relations","title":"6.2 关系数据库","level":2,"start":28226,"path":["section-0"]}]
  }],
  "cases": [{"id":"case-...","revision":"...","title":"试题一","module":"architecture","term":"...","sourceType":"mock","sourceUrl":"...","subQuestions":[{"prompt":"...","referenceAnswer":"..."}]}],
  "essays": [{"id":"essay-...","revision":"...","title":"论...","prompt":"...","writingPoints":"...","module":"architecture","term":"...","sourceType":"mock","sourceUrl":"..."}],
  "knowledgeLinks": {"<questionId>":[{"materialId":"...","sectionId":"section-28226","sectionKey":"sec-...","sectionTitle":"6.2 关系数据库","sectionPath":["section-0"],"terms":["范式"],"score":73.7,"status":"auto"}]}
}
```

规则：

1. **答案与解析绝不随目录下发**。练习模式提交后通过 `/solution` 获取；正式模拟考试在交卷前返回 `answerHidden: true`。
2. 资料原件按内容寻址保存（`data/originals/materials/<sha256>.md`），解析产物不覆盖原件；历史引用绑定 `materialVersionId`。
3. 小节 `id`（anchor，版本内有效）用于阅读器定位；`key`（stable_key）用于跨版本身份。关联返回两者。
4. 导入必须幂等：同版本重复导入不产生重复行、不覆盖用户数据；新版本发布前旧版本继续可读。
5. 缺图/未审核题目默认不进入练习队列（由练习包按 `figureMissing`、`reviewStatus` 过滤）。
6. 内容快照不作为静态资源目录暴露：`/content/catalog.json` 等返回 `404`；只有 `/content/licenses/{file}` 提供来源许可与声明下载，路径不允许穿越。

## 3. 练习与考试（`backend/internal/practice`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/practice/sessions` | 创建练习现场；`{mode, scope, count, replace?}` |
| GET | `/api/practice/sessions/active` | 当前未完成现场（无则 `{"session":null}`） |
| GET | `/api/practice/sessions/{id}` | 现场详情（含题目版本映射、作答状态、计时） |
| PATCH | `/api/practice/sessions/{id}` | 更新 `index`/`elapsedSeconds`，需带 `version` |
| POST | `/api/practice/sessions/{id}/answers` | 提交一题作答（幂等） |
| POST | `/api/practice/sessions/{id}/submit` | 正式考试交卷（按服务器时间） |
| POST | `/api/practice/sessions/{id}/finish` | 结束练习 |
| GET | `/api/practice/attempts?questionId=` | 我的作答历史（追加事实） |
| GET | `/api/practice/wrong-book` | 最近一次作答仍错误的题目 |
| GET | `/api/practice/review-due` | 到期复习题目 |
| GET | `/api/practice/marks` | 我的收藏/存疑/笔记 |
| PUT | `/api/practice/marks/{questionId}` | 更新标记，需带 `version`（乐观并发） |
| GET | `/api/practice/stats` | 领域进度、正确率、最近活动 |

现场结构：

```jsonc
{
  "session": {
    "id":"uuid","mode":"chapter|all|exam|wrong|favorites|review",
    "scope":{"materialId":"material-...","sectionId":"section-...","module":"architecture","term":"all"},
    "questionIds":["..."],"revisions":{"<questionId>":"ab12"},
    "index":3,"elapsedSeconds":420,"status":"active","bankVersion":"d58c...",
    "version":7,"createdAt":"...","updatedAt":"...",
    "deadlineAt":null,"submittedAt":null,"score":null,"maxScore":null,
    "answers":{"<questionId>":{"selected":"B","submitted":true,"assisted":false,"attemptId":"..."}}
  }
}
```

提交作答：

```jsonc
// POST /api/practice/sessions/{id}/answers
// {"id":"<客户端事件ID>","questionId":"...","selected":"B","assisted":false,"durationMs":15000,"answeredAt":"..."}
// -> 200 {"attempt":{...},"correct":true,"revealed":true,"solution":{"answer":"B","analysis":"...","scoringRule":{...}},"session":{...}}
// 正式考试未交卷：revealed=false，solution=null
```

规则：

1. 创建现场时服务端确定题目队列与每题的**题目版本**；重开同范围继续原现场，不同范围必须显式 `replace:true`，否则 `409 {"code":"active_session","details":{"session":{...}}}`。
2. 作答以客户端事件 ID 幂等：重复提交同一 `id` 返回同一结果，不重复计分。
3. 同一题重复作答（不同 `id`）追加新事实，不覆盖历史。
4. 正式考试（`mode=exam`）创建时写入 `deadlineAt = serverTime + 时长`（默认 150 分钟，可由 `durationMinutes` 指定），交卷前不返回答案；超过截止时间提交返回 `409 {"code":"deadline_passed"}`，以已收到的有效提交为准。
5. `PATCH` 与 `PUT marks` 使用 `version` 乐观并发，冲突返回 `409` 并带服务器当前值。
6. 评分使用现场记录时的 `scoringRule`（`v1`：单选 1 分），题目后续修订不影响历史成绩。
7. 练习模式允许逐题揭示解析；错题视图、统计与复习建议由作答事实派生，不覆盖事实。

## 4. 写作（`backend/internal/writing`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/writing/{kind}/{targetId}` | `kind` = `case` \| `essay`；返回文档或空文档 |
| PUT | `/api/writing/{kind}/{targetId}` | 保存，需带 `version`；冲突返回 409 + 双方版本 |
| GET | `/api/writing/{kind}/{targetId}/versions` | 版本列表 |
| GET | `/api/writing/{kind}/{targetId}/versions/{version}` | 指定版本内容 |
| POST | `/api/writing/{kind}/{targetId}/reviews` | 创建绑定当前版本的 AI 点评任务 |
| GET | `/api/writing/{kind}/{targetId}/reviews` | 点评历史（含绑定的版本号） |

```jsonc
// GET -> {"document":{"id":"uuid","kind":"essay","targetId":"essay-...","values":{"material":"...","outline":"...","abstract":"...","body":"..."},"version":4,"updatedAt":"..."}}
// PUT {"values":{...},"version":4} -> 200 {"document":{...version:5}}
//     冲突 -> 409 {"error":{"code":"conflict","message":"...","details":{"serverVersion":5,"serverValues":{...},"clientValues":{...}}}}
// POST reviews -> 201 {"review":{"id":"...","documentVersion":4,"taskId":"uuid","status":"queued"}}
```

规则：点评绑定生成时的稿件版本；采纳前版本变化必须提示；冲突时保留双方版本（写入 `writing_versions` 且 `source='conflict'`）。

## 5. AI 教学任务（`backend/internal/ai` + `backend/internal/teaching`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/ai/status` | 配置状态、连接状态、能力验证结果（分开呈现） |
| POST | `/api/ai/tasks` | 创建教学任务（幂等：`idempotencyKey`） |
| GET | `/api/ai/tasks/{id}` | 任务状态与最近事件 |
| GET | `/api/ai/tasks/{id}/events?after=<seq>` | SSE 事件流，可恢复（`Last-Event-ID`） |
| POST | `/api/ai/tasks/{id}/messages` | 用户追问 / 回复 |
| POST | `/api/ai/tasks/{id}/cancel` | 取消任务 |
| DELETE | `/api/ai/tasks/{id}` | 删除自己的任务与总结（软删除事件） |
| GET | `/api/ai/tasks` | 我的任务列表 |

```jsonc
// GET /api/ai/status
{"configured":true,"gatewayName":"Magpie","endpoint":"https://...","model":"group/...",
 "authConfigured":true,"connectionStatus":"ok|failed|unknown","capabilityStatus":"verified|unverified|failed",
 "unavailableReason":"","teaching":{"configured":false,"reachable":false,"reason":"..."},
 "budget":{"dailyLimit":0,"usedToday":0},"concurrency":{"limit":3,"active":1}}

// POST /api/ai/tasks
// {"kind":"hint|explain|followup|review_case|review_essay|path|report",
//  "idempotencyKey":"<客户端生成，唯一>",
//  "context":{"questionId":"...","questionRevision":"...","sessionId":"...","materialId":"...",
//             "sectionId":"...","documentId":"...","documentVersion":4,"prompt":"...","hint":true}}
// -> 201 {"task":{"id":"uuid","status":"queued","kind":"hint","createdAt":"..."}}

// SSE: event: task  id: 12  data: {"seq":12,"type":"delta","payload":{"text":"..."}}
//      event: task  id: 13  data: {"seq":13,"type":"citation","payload":{"sourceId":"S0","materialId":"...","sectionId":"...","title":"..."}}
//      event: task  id: 14  data: {"seq":14,"type":"done","payload":{"status":"completed"}}
```

事件类型：`accepted`、`delta`、`citation`、`question`（等待用户回答）、`done`、`error`、`cancelled`。事件 `seq` 单调递增且持久化，断线重连用 `after`/`Last-Event-ID` 补发。

规则：

1. 任务状态：`queued → running → (waiting_user) → completed | failed | cancelling → cancelled`；上游状态未知时置 `recovering`，不盲目重试。
2. Worker 使用租约、心跳、超时回收、重试上限与并发上限；至少一次投递，业务幂等。
3. 未配置模型或教学服务时：任务创建返回 `503 {"code":"ai_unavailable","message":"<原因>"}`，前端明确显示原因，基础学习功能不受影响。
4. 每个 RKA 用户映射独立教学身份；不得共用管理员身份。凭据只存服务端（加密），不进入前端、备份与日志。
5. 用户可纠正或删除自己的 AI 总结（`DELETE /api/ai/tasks/{id}`，软删除事件与总结）。

## 6. 学习证据与报告（`backend/internal/evidence`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/learning/summary` | 阅读/提示辅助/独立通过/自我声明的计数与最近证据 |
| GET | `/api/learning/report` | 学习报告：结论、依据、建议（可回查证据） |
| GET | `/api/learning/mastery` | 知识点掌握状态列表 |
| POST | `/api/learning/reading` | 上报阅读进度（材料、版本、小节、比例、done） |
| POST | `/api/learning/self-declared` | 自我声明掌握/撤销 |
| GET | `/api/learning/review-plan` | 复习安排（含理由） |

```jsonc
// POST /api/learning/reading {"materialId":"...","materialVersionId":"uuid","progress":0.42,
//   "sectionId":"section-28226","sectionKey":"sec-...","offset":0,"done":false}
// POST /api/learning/self-declared {"knowledgePointId":"kp-...","mastered":true}
// GET  /api/learning/report -> {"generatedAt":"...","evidence":[...],"conclusion":{"focus":"...","reason":"...","evidence":[...],"suggestedSteps":[...]}}
```

规则：掌握状态区分 `read`、`assisted`、`independent`、`self_declared`，不输出未经校准的通过率；报告结论必须能回查具体学习事实。

## 7. 同步与数据自主（`backend/internal/ops`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/sync/state` | 服务器时间、题库版本、用户数据版本摘要 |
| POST | `/api/sync/import` | 导入 RKA v1（浏览器）或 v2（服务端导出）备份（幂等，重复导入不重复计数） |
| GET | `/api/export/me` | 导出我的全部学习数据（JSON） |
| DELETE | `/api/me/data` | 删除我的学习数据（保留账号，需 `{"confirm":"DELETE"}`） |

`POST /api/sync/import` 接受两种备份：

- `schemaVersion: 1`：旧浏览器 RKA 备份 `{app:"rka",schemaVersion:1,data:{attempts,marks,sessions,drafts,reading,chats}}`。作答、标记、草稿与阅读进度写入业务表；旧练习现场和 AI 对话作为只读存档导入，不恢复为可继续的服务端现场。
- `schemaVersion: 2`：`GET /api/export/me` 导出的服务端数据，可回导到同一账号（幂等）；跨账号的导出数据返回 `400 foreign_backup`。服务器上已更新的草稿不会被旧导出覆盖，冲突版本保留。

逐条校验、幂等写入，响应形如 `{"imported":{"attempts":n,...},"skipped":[...],"conflicts":[...]}`。无法匹配的题目 ID 跳过并说明原因，不静默丢弃；请求体上限 50 MiB，单类记录上限 100000 条。

## 8. 运维与健康（`backend/internal/ops` + `backend/internal/admin`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 进程存活 |
| GET | `/readyz` | 就绪：数据库、schema 兼容、核心内容 |
| GET | `/api/admin/status` | 进程/数据库/教学/模型/索引分别的健康状态 + 版本 |
| GET | `/api/admin/tasks` | 后台任务列表（含失败原因） |
| GET | `/api/admin/audit` | 审计日志 |
| GET | `/api/admin/model-configs` | 模型配置：配置状态、连接状态、能力状态 |
| POST | `/api/admin/model-configs` | 新增模型配置（凭据只写不读） |
| PUT | `/api/admin/model-configs/{id}` | 更新配置（凭据只写不读） |
| POST | `/api/admin/model-configs/{id}/check` | 主动连接检查（记录结果，不伪装成功） |
| GET | `/api/admin/backups` | 备份列表 |
| POST | `/api/admin/backups` | 创建一致性备份 |
| POST | `/api/admin/backups/{id}/verify` | 隔离目录校验 |
| POST | `/api/admin/backups/{id}/restore` | 恢复到新目录（返回目标路径，需重启切换） |
| GET | `/api/admin/retention` / PUT | 保留策略 |
| GET | `/api/admin/metrics` | 任务耗时、失败数、AI 调用计数（无金额编造） |

## 9. 前端状态约定

前端保留 IndexedDB 作为**缓存与待同步队列**，界面必须分别显示四种状态：

| 状态 | 含义 | 触发 |
| --- | --- | --- |
| `local` | 已写入本地缓存 | 离线或尚未提交 |
| `pending` | 已进入待同步队列 | 网络不可用 |
| `synced` | 服务器已确认 | 接口 2xx |
| `conflict` | 服务器版本更新，需要用户选择 | 接口 409 |

规则：

1. 普通练习与草稿允许离线暂存；正式模拟考试以服务器截止时间为准，断线不自动延长。
2. 重试必须复用同一客户端事件 ID，避免重复计分。
3. 冲突版本保留双方内容，不静默覆盖。


## 同步、索引与备份补充约定

- `X-RKA-User-ID`：浏览器携带当前页面所属账号，服务端仍用 HttpOnly Cookie 鉴权。两者不一致返回 `409 account_changed`，阻止切换 Cookie 后重放旧账号数据。登录等身份切换请求不携带此头。
- `POST /api/learning/reading`：`offset` 为非负整数像素；`done` 可省略，省略时保留服务器完成状态；显式布尔值才修改完成状态。
- 未交卷考试的判题事实不进入 attempts/wrong-book/stats/learning summary/report/export 等可读投影；交卷后公开。
- 写作 versions 列表新增 `id`。`GET .../versions/{version}?source=conflict&snapshotId=<id>` 精确读取某一冲突；省略 `snapshotId` 时兼容返回同版本/来源的最新记录。恢复内容通过正常版本化 PUT 保存为新版本。
- `GET /api/content/indexes` 返回 `{configured,items:[{id,materialId,versionId,title,status,error}]}`，按当前资料权限过滤；状态为 queued/indexing/ready/failed。原有资料 `indexStatus` 仅表示结构解析状态。
- `POST /api/admin/content/indexes/{id}/retry` 为失败索引建立新的远端命名空间并排队，旧版本保持可引用；非失败状态返回 409。
- AI 预算按 UTC 自然日统计教学回合，追问也计数；检查与预占在同一数据库事务完成。不是供应商 Token 或金额上限。
- 有教学/网关持久数据时，在线创建备份返回 `409 backup_requires_quiescence`，通过 `scripts/backup.sh` 协调停写创建完整批次。
