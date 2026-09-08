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
