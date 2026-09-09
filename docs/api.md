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

普通操作员凭据具有 reader/editor/reviewer/publisher 角色。Agent 身份额外固定 `audience: "agent"`，只允许下表注明的入口以及 `GET /api/me`；不能访问其余内核端点以绕过草稿门禁。

| 方法与路径 | 内容 / 权限 |
| --- | --- |
| `GET /api/projects` | 可见项目列表；Agent 仅收到 id、name、active_release |
| `POST /api/projects` | editor；ProjectSpec（id、name、dependencies、checks、excludes），不接受服务器 root |
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
| `GET /api/sessions/{session}/search?q=...` | Agent 可用；固定版本片段的字面查询，最多 100 条结果，含来源哈希与位置 |

提交返回 id、project_id、release_id、parent_id、message、author、created_at。`expected_parent` 必须显式传入，且等于当前最新提交的 id；历史变化或内容与父提交一致返回 409，空说明返回 422。提交不扫描目录、不批准、不激活、不写回原件。`compare` 重算 changes 和 impact；verification 仍是目标快照的构建检查，原始审核报告不变。提交和比较均不向 Agent 凭据开放。

`restore` 返回新的快照，`report.restore_from` 是历史来源，`report.base_release` 是本次保存的实时原件基线，`report.changes` 是将要写入的完整文件集差异。候选保留现有审批/发布流程，可另行记录提交；201 不代表已恢复原件或业务检查通过。目标与原件相同时、目标撤销时或存在中断写回时返回 409；来源无权访问返回 403，上传项目返回 400。写回时再次核对来源与当前原件，后续外部修改不被覆盖。观察开关不限制人工恢复。

项目版本的撤销与指针回退沿用 `POST /api/releases/{release}/revoke` 和 `/rollback`。回退不会覆盖真实目录，后续读取仍需通过哈希匹配。搜索是有界字面匹配，未接入向量检索、zvec-grep 或 TeamAI。

全套请求仍受入口认证和正文大小限制；重复上传路径、越界路径拒绝。所有原文件字节留在本地，Base64 只是响应编码。语义提示、报告校验和与读取回执均不是数字签名或自动业务正确性证明。

## 专用演示

只有 `filewise showcase` 创建的临时合成实例启用 `GET /demo`（演示身份和项目 ID）与 editor 的 `POST /api/demo/change`、`/repair`。普通 `filewise serve` 不启用这些能力。不得将专用演示实例作为真实项目的认证服务。

本机首次使用、原生 Agent 连接、关注、受控写回与恢复接口见 [中间件 API](middleware.md#程序接入)。目录注册和配置安装仅在 start/follow 的本机设置模式开启，仍要求操作员凭据；普通 serve 默认不开放此能力。
