# Go 运行时与迁移记录

当前源码版本 **`0.3.0-dev (Go)`** 已实现原生项目工作空间、HTTP/Agent CLI、后台监控与双语工作台、PDF/Office 提取、检索、质量/任务检查和独立发布。运行、构建、CI、Docker 与本地发行入口均使用 Go，不调用 Rust 或 Python。

**Go 源码开发版，二进制与 npm 包尚未发行。** 历史 `v0.2.0` 的源码、标签、附件和验证记录保持不变；Go 安装器拒绝旧版回退。网站保持用户批准的字节和静态展示，其中的 v0.2.0 安装入口仍是历史发行物。当前运行说明以本指南、[Go CLI 指南](go.md)和[安装指南](install.md)为准。

## 构建与状态

```bash
make build
make check
./build/filewise --version
./build/filewise --licenses
```

Go 1.27+；普通构建关闭 CGO。race 检查需要 C 编译工具链，网站/npm 检查使用 Node.js 22+。SQLite/FTS5 由 `modernc.org/sqlite` 提供，PDF 使用固定版本的 Go 解析库；其他核心机制使用 Go 标准库、glob 与 Unix 文件接口。依赖及校验固定在 `go.mod` / `go.sum`，许可文本嵌入二进制。

- 项目库标识：`filewise-go-v1`。快照与任务分别使用 `filewise-go/snapshot-v1`、`filewise-go/task-v1`。
- 后台默认状态：macOS `~/Library/Application Support/Filewise-Go/filewise.db`；Linux `~/.local/share/filewise-go/filewise.db`。
- 底层 CLI 默认状态：`.filewise-go/filewise.db`。混用接口时指定同一个绝对 `--db`。
- 拒绝直接打开旧数据库；不重写旧哈希，不隐式导入，不回填观察时间。Go 的确定性 JSON 哈希不是旧实现的字节兼容声明。
- `document` journal 是独立的操作员工具；项目 Agent 只使用有范围授权的 HTTP 网关。

## 功能与行为检查

| 模块 | Go 实现 | 验证入口 |
| --- | --- | --- |
| 存储、权限和完整性 | 私有 SQLite、原件、来源 ACL/撤销、快照/完成时间/审计链 | `internal/workspace/workspace_test.go` |
| 工作保存与恢复 | 精确基线、原件 CAS、幂等、候选隔离、补偿、属主恢复、保留外部编辑 | 同上 |
| 质量与用途 | JSON/CSV/XLSX 全表、精确数值、固定政策、原始群体基线、有效性与 lineage | 上述测试及 `documents_test.go` |
| 检索与任务 | 中英文 FTS5/BM25/精确匹配、diff/impact、固定证据、输出断言和撤销重验 | `workspace_test.go`、`documents_test.go` |
| 独立发布 | 作者与审核者分离、当前检查、活动发布 CAS、撤销 | `workspace_test.go` |
| HTTP 与 Agent | 范围凭据、未知字段拒绝、只走 HTTP、远端 HTTPS、禁止代理/跳转 | `workspace_test.go`、`tests/skill.test.mjs` |
| 监控与工作台 | 稳定扫描、规则 CAS、保留/建议/自动命名、原 inode 移动/撤销、崩溃回执重放、人工声明保护 | `watch_test.go`、`documents_test.go` |
| 原生后台生命周期 | 同一 Go 程序启停/重启、身份核验、loopback Host/Origin、持久规则和历史 | `cmd/filewise/native_test.go` |
| 文档与 worker | DOCX/PPTX/XLSX/PDF、位置/精度/类型、恶意输入、部分覆盖、隔离解析和限额 | `internal/documents/*_test.go`、原生进程测试 |
| 语义 meta 与 journal | 双版本证据、精确变化、复核、私有原件和记录、并发基线检查 | `internal/changes`、`internal/journal` 测试 |
| 构建与发行配置 | CGO0 四目标、Go CI/Docker、只接受 Go 的安装器、统一 Skill | 工作流配置、npm/安装器测试 |

工作台资源迁移到 `internal/workspace/ui/`，保留视觉、交互和 165 项双语文案，仅把后台运行时名称改为 Go。静态官网 `site/` 字节保持不变。历史源码保留在 Git 标签，而不是活动构建中的备用运行时。

## 变化的含义与原文一起保存

项目文件的 **`meta.semantic_change`** 与人工维护的 `metadata` 分开。自动派生不会刷新或替换人工声明。单文档 journal 则保存为 `record.meta.semantic_change`。

四份 `site/examples/acceptance-{zh,en}-{before,after}.docx` 是合成原件。第三段测试压力从 100 kPa 改为 120 kPa，真实定位为 `word/document.xml/paragraph:3`：

```bash
./build/filewise document inspect site/examples/acceptance-zh-after.docx
./build/filewise document compare \
  --before site/examples/acceptance-zh-before.docx \
  --after site/examples/acceptance-zh-after.docx
```

| 字段 | 内容 |
| --- | --- |
| `schema` / `engine` | 元信息与规则版本 |
| `before` / `after` | 前后原件 SHA-256 与提取状态 |
| `summary` / `changes[].meaning` | 变化摘要和逐项解释 |
| `changes[].before` / `after` | 文件哈希、实际 locator、原文及截断标记 |
| `changes[].quantity` | 原数值、单位、精确增量和相对变化 |
| `requires_review` / `coverage` | 复核要求；`extracted_text_only` |
| `impact_status` | `not_evaluated`，与依赖影响分析分开 |

示例为 `delta: "+20"`、`relative_change: "1/5"`，即 20%。规则只解释同上下文、同单位、单个数值变化。一般措辞、单位/义务变化、多数值、重复段落或部分提取保留证据并要求复核；零基线不计算比例。对齐最多 100 万 LCS 单元，明细最多 200 项，单条原文最多 4,000 字符，超限明确标记。

### 独立单文档 journal

```bash
DEMO=$(mktemp -d)
DOC="$DEMO/acceptance.docx"
STATE="$DEMO/history"
cp site/examples/acceptance-zh-before.docx "$DOC"
./build/filewise document capture --state "$STATE" --base none "$DOC"
```

把返回的完整记录 `id` 输入后续提示。记录 ID 绑定整条记录，文件 SHA-256 绑定原始字节：

```bash
printf 'Previous record ID: '
read -r BASE
cp site/examples/acceptance-zh-after.docx "$DOC"
./build/filewise document capture --state "$STATE" --base "$BASE" "$DOC"
./build/filewise document record --state "$STATE" --file "$DOC" --version latest
```

每份文档使用单独私有目录，放在受管理目录和 Agent 可访问文件系统之外。目录 0700、记录/原件 0600，拒绝链接和不安全文件；原件和不可变记录先写入并同步，再原子切换 latest。精确基线防止覆盖更新；相同内容只有在基线有效时才返回已有记录。采集不改写原件；提取失败仍可保存并标记不完整。没有自动垃圾回收；哈希不是外部签名。

## 文档与资源边界

- 文字提取，不做 OCR、版式重建或旧版 DOC/XLS/PPT 解析；不执行宏/公式，也不读取外部关系。PDF 表单/图片/嵌入内容、幻灯片模板、无法解码的文本等遗漏明确标记。
- 单文件 10 MiB；ZIP 4,096 项、展开 32 MiB、单 XML 8 MiB、整包 200,000 个元素及属性、128 层；最多 20,000 个片段 / 200 万字符。
- PDF/PPTX 最多 500 页/幻灯片；PDF 声明对象数最多 100,000，检查页树、计数、重复引用和继承深度；加密（包括空密码）拒绝。
- XLSX 最多 100 表、20,000 个声明单元格；位置不超过 20,001 行 / 512 列，不按 `dimension` 分配。数值保留词法精度，日期保留序列/日期系统，公式缓存不作已验证数据。
- 原生入口使用同一可执行文件的独立解析进程：输入 10 MiB、输出 24 MiB、20 秒墙钟、15 秒 CPU、禁 core dump、清空继承环境，超限终止并回收。
- Go 软内存目标 256 MiB；父进程每 25 ms 检查 RSS，超过 384 MiB 时终止；Linux 另设 384 MiB RLIMIT_DATA。**这不是硬 RSS 上限，也不是文件/网络权限沙箱**；与旧版 Rust 分配器的 384 MiB 堆策略不同。资源压力下宁可提取失败，原件仍保存。
- 按格式和原件哈希保留最多 16 项 / 8 MiB 序列化缓存；失败不缓存，取出时复制。当前来源权限仍由调用路径重新验证。

`partial` / `no_text` / `failed` 保持可见，不能通过完整任务证据检查。读取和导出的是同一原件，检索片段不能代替全表质量检查。

## 保留的使用边界

- Agent 凭据限定项目；历史证据仍受当前 ACL、撤销与完整性约束。浏览器操作员令牌、SQLite、原件和完整令牌映射不交给受限 Agent。Skill 不构成操作系统沙箱。
- 预览与正式保存使用不同请求 ID；同一请求重试保持 ID 与全部载荷。保存和自动整理不批准或发布。
- 每项目 1,000 文件 / 50 MiB，串行文件操作；外部编辑器不遵守 Filewise 锁。相同字节的外部写入在最终检查与替换间隙无法区分，需要严格隔离时由 OS 限制写权限。
- 原型中未进入原生基线的命名提交/审核历史还原、对象级双时间、生产会话和 hooks 仍不提供；向量检索未配置返回 503，写入任务 postflight 返回 501。`resolve` 是快照文件选择，不是行级 point-in-time join。

## 验证记录与发行边界

当前 Go 的本地测试包括真实 CGO0 可执行文件、清理后的子进程环境、四格式 worker、后台启停/重启、监控改名/撤销、坏文件隔离、临时 HOME 下登录项文件创建/删除和项目 Skill 的 HTTP 链路。临时登录项文件不等于真实登录启动验证。

本轮本机验收：Go 格式/vet/uncached race、Node 22 的 11 项 npm 检查（含实际 Go 网关，无跳过）、165 项 i18n、真实二进制离线安装/启停/worker、两项限时 fuzz，以及 CGO0 的 macOS/Linux × ARM64/AMD64 交叉构建通过。交叉构建不是非本机执行证明。

Go 工作台已在 Chromium 中实际完成注册、监控、建议改名、原 inode 撤销、暂停/恢复、坏 PDF 错误展示及语言切换保留表单；中英文各 1440/390/320px 无横向溢出。四次 axe 检查均发现既有工作台配色的一类对比度问题（每页 14–15 个节点），另有图标对比度待人工判断；不称为无障碍验收通过。此次保留原布局/配色，不改已批准官网。

上述记录来自本地验收。推送后的远程结果以对应提交的 [Go checks](https://github.com/huaiwen/filewise/actions/workflows/ci.yml) 为准；历史 Rust CI 不替代 Go 验证。源码推送不发布 Go/npm 包，也不部署网站。

尚需独立平台验收：非本机系统执行、最低 OS、真实 Ollama 模型、Codex/Claude/Cursor 的实际模型调用、原生目录选择和真实登录启动。Docker 配置迁移不代表已运行容器。OfficeCLI 的额外 OpenXML schema 检查曾因缺少 `System.Private.Xml 10.0.0.0` 被阻断；真实 Word 原件的文本与 Go 解析验证有效，但不称为 OfficeCLI schema 认证。

## English

The active native runtime is **Go, `0.3.0-dev`**, covering workspace storage, permissions/history, CAS/idempotency/recovery, HTTP/Agent commands, monitoring/undo, the existing bilingual workbench with Go runtime labels, PDF/modern Office extraction, retrieval, quality/tasks and independent publication. Builds, CI, Docker and local release entrypoints no longer depend on Rust. The Go release is not yet public; immutable v0.2.0 assets remain historical, with no installer fallback or automatic database conversion.

Go uses separate `filewise-go-v1` state. Derived `meta.semantic_change` is separate from human metadata. It binds before/after hashes, quotations and locators; unambiguous same-context/unit quantity changes use exact arithmetic. Other prose, incomplete extraction and ambiguous alignment require review. Downstream impact stays `not_evaluated`. The document journal is a separate operator tool, not an Agent gateway.

Extraction workers have bounded input/output, wall/CPU deadlines, a cleared environment, RSS monitoring with kill/wait, and Linux RLIMIT_DATA. Go's soft target and sampled RSS threshold are not a hard RSS or OS-permission sandbox guarantee. Failed/partial extraction remains visible and cannot satisfy complete-evidence checks. Original bytes remain available independently.

Local process tests exercise real Go workers, start/status/stop/restart, HTTP boundaries, watches, inode-preserving rename/undo and failure isolation. Chromium functional checks passed in both languages at desktop/mobile widths. Four axe scans retain one inherited contrast-violation category (14–15 nodes per view); accessibility is not marked passed. Cross-builds, remote CI, real model invocations, minimum-OS/device execution and actual login startup are distinct evidence categories. Remote results belong to the matching commit's Go checks run; source pushes do not publish Go/npm packages or deploy the site. See the Chinese matrix for code/test paths and the [Go usage guide](go.md) for commands.
