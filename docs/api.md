# REST API

启动：`filewise --db .filewise/demo.db serve --tokens .filewise/tokens.json`。默认地址 `http://127.0.0.1:8000`。

所有业务请求使用 `Authorization: Bearer TOKEN`，JSON 请求另设 `Content-Type: application/json`。`GET /api/openapi.json` 返回实际输入模式；HTML Swagger 页未启用，以避免依赖外部 CDN。

| 方法 / 路径 | 输入 | 要求 |
| --- | --- | --- |
| GET `/api/me` | 无 | 任一有效身份 |
| GET `/api/overview` | 无 | 按 ACL 返回范围、来源、发布与活动指针 |
| POST `/api/scopes` | Scope | editor；不可变 |
| POST `/api/scopes/{id}/sources` | multipart `file`，可选查询参数 `acl=reader,editor,reviewer,publisher` | editor；返回来源 ID、SHA-256 和原文坐标 |
| GET `/api/sources/{id}` | 无 | 范围和来源可见且未撤销 |
| PUT `/api/sources/{id}/policy` | `{"acl":["editor","reviewer"],"revoked":false}` | reviewer |
| POST `/api/revisions` | Revision | editor；证据必须在同一范围且锚点有效 |
| POST `/api/revisions/{id}/approved` | 无 | reviewer |
| POST `/api/revisions/{id}/revoked` | 无 | reviewer |
| POST `/api/resolve` | ResolveRequest | 按权限解析 |
| POST `/api/impact` | ResolveRequest + `seeds`、`direction` | forward/reverse |
| POST `/api/diff` | `before_release`、`after_release` | 两包同范围且证据可见 |
| POST `/api/build` | BuildRequest | editor；包含 diff/impact/compile/verify |
| GET `/api/releases/{id}` | 无 | 完整发布包及可变审批/撤销元数据 |
| GET `/api/releases/{id}/verify` | 无 | 构建报告与当前运行时门禁分开返回 |
| POST `/api/releases/{id}/approve` | 无 | reviewer，与 builder ID 不同 |
| POST `/api/releases/{id}/activate` | `{"expected_active":null}` 或当前 ID | publisher；原子比较后切换 |
| POST `/api/releases/{id}/rollback` | `{"expected_active":"CURRENT_ID"}` | publisher；目标曾激活 |
| POST `/api/releases/{id}/revoke` | 无 | publisher；永久撤销并移除其活动指针 |
| POST `/api/releases/{id}/context` | `{"object_ids":["procedure"]}` | 已发布、当前可用；只读依赖闭包 |
| GET `/api/scopes/{id}/audit` | 无 | reviewer；全库链检查，只返回当前范围事件 |

ResolveRequest：

```json
{"scope_id":"demo","valid_time":"2026-09-08T00:00:00Z"}
```

可增加 `transaction_time` 查询历史记录视图，但不会恢复历史访问权限。BuildRequest 使用同样字段，可增加 `base_release` 和 `goal`；省略基线时取该范围当前活动包。

HTTP 201 表示成功创建/幂等提交来源、范围、修订或构建。**BLOCKED 构建仍返回 201**，因为候选包已记录；调用者必须检查 `bundle.verification.decision`，不能把 HTTP 成功视为允许发布。

401 为未认证，403 为角色/证据权限不足，404 为不存在，409 为门禁或并发冲突，413 为体积超限，415 为格式或依赖不支持，422 为输入/解析失败，400 为其他领域拒绝。错误不授权客户端降级为无检查路径。

## Agent 原生知识工作区

`/api/workspaces/{project_id}` 为原生读写与计算接口。Agent 凭据必须有 `workspace_projects` 项目授权；写/删/同步/恢复另须 `editor`，读取与计算可用项目专用 reader。无此授权的旧 Agent 令牌仍只能用发布会话。任何 Agent 身份都不能含 reviewer/publisher 角色。

除 `versions` 和 `evidence` 用 GET 外，下表均 POST JSON；成功返回200。省略 `version` 时为 `latest`，还支持 `published`、`HEAD`、snapshot ID、commit ID。latest 排除未完成写回及 dry-run 候选；明确版本可读候选，撤销与当前来源权限始终生效。

| 子路径 | 请求 | 返回 |
| --- | --- | --- |
| `versions` | GET | 快照、写入说明/状态、命名提交与发布指针 |
| `ls` | `{ "version":"latest" }` | 具体版本、文件 manifest、metadata、构建检查 |
| `read` | `path`、可选 version/include_bytes | 文本、坐标、metadata、哈希与 receipt；仅 include_bytes=true 返回原件 Base64 |
| `search` | query、version/paths/tags、mode、limit、min_similarity、max_per_file、rebuild | 精确/BM25/本地语义/RRF，最多100条，片段/来源/坐标/哈希/排名依据 |
| `quality` | version/as_of/paths、requirements（路径→DataUse） | 固定版本/文件哈希、数据口径、质量规则、输入绑定、decision 与回执 |
| `write` | WriteQuery，见下 | 新版本、saved/status、逐文件变化、verification、写入 metadata/receipt；不发布 |
| `sync` | 无 | 捕获已注册目录中的外部保存；不修改原文件 |
| `resolve` | version/paths；或 valid_time + 可选 transaction_time | 固定版本状态；时间模式为已审核修订双时间解析 |
| `diff` | before、可选 after/paths | 服务端位置化与类型化差分、metadata 变化、统计和影响 |
| `impact` | version、paths、base_version、direction | 前后依赖图上的传播路径与缺失边界 |
| `compile` | goal、version/paths/base_version/direction、max_chars、output_checks、query/retrieval_mode/retrieval_limit | task_id、检索 discovery、固定版本上下文、证据、回归与输出断言、工具及审批契约 |
| `verify` | phase、version/task_id、operation、paths；postflight 可加 result/outputs/citations/output_version | decision、实际断言、阻断原因、receipt；production_authorized=false |
| `evidence/{source_id}` | GET，可选 locator | 同项目当前可见的原始证据片段 |
| `trace` | version | 版本基线、写入 provenance、相关审计（最多200条） |
| `recover` | `{ "version":"CONCRETE_WRITE_VERSION" }` | 恢复此身份自己的中断工作写入，之后可用原幂等请求重试 |

ls/read/search/quality/resolve/impact/compile/verify/trace 支持带时区 `as_of`：latest 选择截止时最后完成快照，显式版本/别名必须满足截止，未来或无完成回执的版本拒绝。历史任务只读。它不是供应商真实送达认证或行级 point-in-time join；与 resolve 的 valid_time/transaction_time 模式不可混用。recover 不接受 as_of。

search/compile/quality 可带 `requirements: {"data.csv":{"expect":{"unit":"CNY"},"purpose":"panel_spending","require_quality":true}}`。compile 另接受 `model_use: none|extractive|generative`；任务固化 requirements/as_of，verify 重验并返回 data_guard。模型未来知识风险为 NEEDS_REVIEW，不被普通数据检查 PASS 覆盖。详见[数据契约](data.md)。

WriteQuery 示例（changes 是局部操作集，不是完整上传集）：

```json
{
  "base_version":"CONCRETE_VERSION_FROM_READ",
  "request_id":"change-001",
  "message":"更新要求",
  "task":"pressure-review",
  "model":"model-name",
  "changes":{
    "requirement.json":{"text":"{\"pressure_kpa\":120}","meta":{"facts":{"pressure_kpa":120}}},
    "old.md":{"delete":true}
  },
  "dry_run":false,
  "require_pass":false
}
```

每个 change 选择 text/base64/delete 之一，或仅 meta。meta 为 FileMetadata：summary、tags、entities、depends_on、facts、evidence、owner、authority、valid_from/valid_until、data（DataContract）、processing（source/deterministic/model）、lineage（具体快照/path/role）。服务端绑定声明身份与内容 SHA，不能从输入注入；提供 meta 整份替换，省略时继承。事实、依赖和元数据进入不可变版本与差分。单 metadata 最大64,000字节，facts JSON 最多16,000字符。

写入正文上限70 MiB（允许50 MiB文件集的 Base64 编码）；仍限制1,000文件、10 MiB/文件、50 MiB总原件。路径严格限制于注册项目关注范围。基线或幂等冲突409；既有文件外部漂移不覆盖；完成工作保存不触发生产指针变化。

**HTTP 成功不是业务 PASS。** 默认即使构建 BLOCKED 也保存工作版本供继续修复。require_pass=true 则返回 saved=false/status=blocked；dry_run 保留候选。重复同一 request_id+请求返回原版本，不同请求409。服务端 actor 有身份保证；model/task/tool 仅为调用者声明。

`verify` 的 phase 为 build/preflight/postflight。output_checks 只检查 object_id=result，不允许外部 reference；outputs 为路径→SHA256，citations 为 Evidence 数组。写操作 postflight 必须证明有本身份从输入基线完成的 Filewise 写入。编译上下文截断、过期或旧内容的声明事实、证据失效、任务/路径越权、输出不符会阻断。max_chars 限制序列化 context，最大50,000；任务和回归契约另列。查询时间模式仅解析审核事实，不改变访问权限。

完整 CLI、机器断言示例与恢复语义见 [Agent 使用说明](agent.md)。

## 固定版本消费

```bash
# TOKEN 与 RELEASE_ID 由本地管理员和发布记录提供
curl -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"object_ids":["procedure"]}' \
  "http://127.0.0.1:8000/api/releases/$RELEASE_ID/context"
```

结果中的 `release_id` 必须随下游结果保存。`instruction` 是数据处理约束，没有授权调用工具；被引文中的命令也没有权限。若返回 403/409，应停止依赖该上下文的操作并请求新的批准版本，不自动切换到最新索引或工作目录。

## 项目文件网关

普通操作员凭据具有 reader/editor/reviewer/publisher 角色。Agent 身份额外固定 `audience: "agent"`，只允许下表注明的会话入口、`GET /api/me`，以及上面明确授权的工作区入口；不能访问其余内核端点。

| 方法与路径 | 内容 / 权限 |
| --- | --- |
| `GET /api/projects` | 可见项目列表；Agent 仅收到 id、name、active_release |
| `POST /api/projects` | editor；ProjectSpec（id、name、dependencies、checks、excludes、data_contracts），不接受服务器 root |
| `GET /api/projects/{id}` | 操作员项目详情、契约、snapshots 和 commits（新到旧） |
| `POST /api/projects/{id}/commits` | editor；正文为 release_id、message（1–500 字符）、expected_parent（首次为 null），记录所选文件集 |
| `GET /api/projects/{id}/snapshots/{release}/compare?base_release=...` | 操作员；对比同项目两个不可变快照，省略 base_release 时对比空文件集 |
| `POST /api/projects/{id}/sync` | editor；扫描已明确注册的本地目录 |
| `POST /api/projects/{id}/upload` | editor；multipart 多个 `files`，文件名为项目相对路径；完整文件集创建候选版本 |
| `GET /api/projects/{id}/snapshots/{release}` | 操作员查看 manifest、定位变更、影响与检查结果 |
| `GET /api/projects/{id}/snapshots/{release}/preview?path=...` | editor/reviewer/publisher 审核预览，receipt.decision 为 DRAFT_PREVIEW |
| `POST /api/projects/{id}/snapshots/{release}/restore` | editor；无正文，以目标历史字节生成本地恢复候选，返回 201；不写原件 |
| `POST /api/projects/{id}/snapshots/{release}/approve` | reviewer，须独立于候选作者且构建 PASS |
| `POST /api/projects/{id}/snapshots/{release}/apply` | publisher；正文 `{"expected_active": null}` 或当前 ID，写回已审核候选并发布 |
| `POST /api/projects/{id}/snapshots/{release}/activate` | publisher，正文 `{"expected_active": null}` 或当前 ID；目录必须匹配快照 |
| `POST /api/projects/{id}/sessions?release_id=...` | Agent 可用；省略 release_id 时固定当前发布，返回 session_id |
| `GET /api/sessions/{session}` | Agent 可用；仅会话拥有者，返回固定版本文件集 |
| `GET /api/sessions/{session}/read?path=...` | Agent 可用；每次重验资格，返回 text、fragments、原件 base64、receipt |
| `GET /api/sessions/{session}/search?q=...` | Agent 可用；固定发布版本的共享排序检索，支持 mode/limit/paths/tags/min_similarity/max_per_file/rebuild 参数，仍执行会话和原件漂移检查 |

提交返回 id、project_id、release_id、parent_id、message、author、created_at。`expected_parent` 必须显式传入，且等于当前最新提交的 id；历史变化或内容与父提交一致返回 409，空说明返回 422。提交不扫描目录、不批准、不激活、不写回原件。`compare` 重算 changes 和 impact；verification 仍是目标快照的构建检查，原始审核报告不变。操作员提交和 compare 入口不向 Agent 凭据开放；工作区 Agent 用 `/diff` 比较并用 `/versions` 读取提交历史。

`restore` 返回新的快照，`report.restore_from` 是历史来源，`report.base_release` 是本次保存的实时原件基线，`report.changes` 是将要写入的完整文件集差异。候选保留现有审批/发布流程，可另行记录提交；201 不代表已恢复原件或业务检查通过。目标与原件相同时、目标撤销时或存在中断写回时返回 409；来源无权访问返回 403，上传项目返回 400。写回时再次核对来源与当前原件，后续外部修改不被覆盖。观察开关不限制人工恢复。

项目版本的撤销与指针回退沿用 `POST /api/releases/{release}/revoke` 和 `/rollback`。回退不会覆盖真实目录，后续读取仍需通过哈希匹配。搜索使用有界精确/BM25和可选本地向量检索，未使用 zvec-grep 或 TeamAI。模式为 hybrid（默认）、exact、lexical、semantic；未配置模型的显式 semantic 返回503，hybrid 明确列出已启用通道；已配置模型故障时不隐式降级。范围最多20,000片段，超限413；问题为空白422。详见[检索契约](retrieval.md)。

全套请求仍受入口认证和正文大小限制；重复上传路径、越界路径拒绝。所有原文件字节留在本地，Base64 只是响应编码。语义提示、报告校验和与读取回执均不是数字签名或自动业务正确性证明。

## 专用演示

只有 `filewise showcase` 创建的临时合成实例启用 `GET /demo`（演示身份和项目 ID）与 editor 的 `POST /api/demo/change`、`/repair`。普通 `filewise serve` 不启用这些能力。不得将专用演示实例作为真实项目的认证服务。

本机首次使用、原生 Agent 连接、关注、受控写回与恢复接口见 [中间件 API](middleware.md#程序接入)。目录注册和配置安装仅在 start/follow 的本机设置模式开启，仍要求操作员凭据；普通 serve 默认不开放此能力。
