# Agent 直接使用 Filewise

主路径是 **Agent 调用 Filewise 读、写、计算知识**。`filewise agent write` 由服务端保存原文件与版本、记录 metadata 并计算检查结果，不需要先改原文件，也不需要 watcher 或原生钩子。

普通工作保存与生产发布分开：可以保存尚未通过业务检查的工作版本，继续修复；生产使用仍须独立审核和发布。需要“检查通过才保存”时传 `--require-pass`。

## 1. 操作员建立项目并授权

在 Filewise 仓库安装，明确选择自己的资料目录：

```bash
uv sync --locked --all-extras --no-editable
uv run --no-sync filewise auth-init
uv run --no-sync filewise project add /absolute/path/to/project --id inspection --name 检验资料
uv run --no-sync filewise project sync inspection
uv run --no-sync filewise auth-agent inspection --tokens .filewise/tokens.json
uv run --no-sync filewise serve --tokens .filewise/tokens.json
```

创建时可加 `--spec examples/project-contract.json` 声明跨文件业务检查；省略时只检查文件集与来源完整性。已有 `start` 项目可直接执行 `auth-agent PROJECT_ID`，使用同一 `--db` 与 `--tokens` 路径，无需重新注册。凭据变更后重启服务。

`auth-agent` 只向指定项目授权，返回独立身份的 `FILEWISE_TOKEN`；`--read-only` 只授予该项目工作与历史知识的读取/计算权限。管理员将这一个令牌安全注入 Agent 进程，不交出数据库或全角色令牌文件。令牌形如：

```json
{"id":"agent-unique-id","audience":"agent","roles":["reader","editor"],"workspace_projects":["inspection"]}
```

项目范围由服务端凭据固定，Agent 不能通过参数指定身份、批准或发布。服务端写入使用 macOS/Linux 的项目锁及文件系统保护；客户端可独立安装。远程服务必须用 HTTPS。

## 2. 直接读写

以下命令假设安装后的 `filewise` 已在 PATH，`FILEWISE_URL`、`FILEWISE_TOKEN` 已由启动环境提供。仓库内可用 `uv run --no-sync filewise`。

```bash
filewise agent projects
filewise agent ls inspection
filewise agent read inspection requirement.json
filewise agent write inspection requirement.json \
  --base VERSION_FROM_LS --request-id pressure-change-001 \
  --content '{"pressure_kpa":120}' \
  --meta '{"summary":"更新压力要求","tags":["压力"],"facts":{"pressure_kpa":120},"owner":"质量组"}' \
  --task pressure-review --model model-name -m "将要求调整为120 kPa"
filewise agent read inspection requirement.json
```

- `--base` 必须是读取返回的具体 `version`，不是 `latest`。其他写入、同步或原件变化会返回 409；先读或 diff，再使用新基线，不能盲目覆盖。
- 重试用同一 `--request-id` 和相同请求；返回同一版本，不重复保存。同一键不同内容返回 409。省略时自动生成一次性 ID，可靠重试应显式提供。
- `--file /path/to/copy.xlsx` 保存本地副本的精确字节；`--file -` 从 stdin 读。`--content` 是 UTF-8 文本。
- `--dry-run` 保存可检查的候选，不改原件或 `latest`。正式保存使用新的 request ID；`--require-pass` 检查失败也只留候选。
- `filewise agent delete inspection old.md --base VERSION -m "删除过期文件"` 删除一个关注文件，历史保留。没有提交的其他路径保持原样。
- `agent sync inspection` 显式捕获原生编辑器的外部修改；Agent 原生写入不需要此步。`latest` 是 Filewise 最新已保存/同步的工作版本，不是每次读时隐式扫描磁盘。

### 批量文件与 metadata

`filewise agent write inspection --request request.json` 一次提交完整操作请求；用 `changes` 同时修改关联 JSON/Excel、新增和删除文件，避免中间不一致版本。二进制用 `base64`。

```json
{
  "base_version":"VERSION_FROM_LS",
  "request_id":"batch-001",
  "message":"同步调整需求与说明",
  "task":"pressure-review",
  "model":"model-name",
  "changes":{
    "requirement.json":{"text":"{\"pressure_kpa\":120}","meta":{"summary":"压力要求","facts":{"pressure_kpa":120}}},
    "delivery.md":{"text":"交付按新版要求执行。","meta":{"depends_on":["requirement.json"],"entities":["delivery"]}},
    "old.md":{"delete":true}
  }
}
```

`meta` 支持 `summary / tags / entities / depends_on / facts / evidence / owner / authority / valid_from / valid_until`。`evidence` 项为 `source_id / locator / quote`，必须能在同项目可见来源中核对。时间须带时区，结束时间晚于开始时间。

提供 `meta` 是**整份替换该文件的声明**，不是浅合并；仅提供 `meta` 可创建 metadata-only 版本。未提供时沿用已有声明；文件内容变动后标记 `metadata_current=false`，旧 `facts` 不再提升为类型化事实字段，需重新声明。服务端记录 `declared_by` 与 `content_sha256`，调用者不能伪造这两个字段。声明是工作知识，不自动成为审核事实。

JSON Pointer、Excel/CSV 坐标自动形成 `value:*` 字段（最多前120项，额外保留契约检查字段）；metadata 的 facts 形成 `fact:*` 字段。原始片段仍可完整读取，不把被截断的类型化投影当作完整解析。

## 3. 最新、历史与服务端差分

```bash
filewise agent versions inspection
filewise agent read inspection requirement.json --version HISTORICAL_VERSION
filewise agent read inspection requirement.json --version published
filewise agent read inspection requirement.json --version HEAD
filewise agent read inspection requirement.json --version COMMIT_ID
filewise agent diff inspection OLD_VERSION NEW_VERSION
filewise agent search inspection "压力" --version HISTORICAL_VERSION
```

`latest` 为默认工作版本；`published` 为当前发布指针；`HEAD` 为最新命名提交；还可指定 snapshot ID 或 commit ID。历史读取只读不可变来源，不要求当前原件等于历史字节，但每次应用当前权限和撤销状态。命名提交由操作员记录；每次 Agent 写入自身已经有说明、作者和版本。

`diff` 由服务端直接返回增删改、JSON Pointer/单元格/行变化、metadata 前后值、类型化变化和影响路径；Agent 无须下载两份文件再比较。搜索是有界字面匹配。

`read` 返回 text、fragments、metadata、SHA-256、版本及 receipt，默认不携带大段 Base64。`--output /tmp/copy.xlsx` 下载原字节并校验 SHA-256，拒绝覆盖已有文件。

## 4. resolve / impact / compile / verify / evidence / trace

```bash
filewise agent resolve inspection --path requirement.json
filewise agent resolve inspection --valid-time 2026-09-09T00:00:00Z
filewise agent impact inspection --path requirement.json
filewise agent impact inspection --path delivery.md --direction reverse
filewise agent impact inspection --base-version OLD_VERSION
filewise agent compile inspection --path delivery.md --direction reverse \
  --goal "核对交付压力并给出有依据的结果" --max-chars 12000 --checks checks.json
filewise agent verify inspection --phase build
filewise agent verify inspection --phase preflight --task-id TASK_ID --operation read
filewise agent verify inspection --phase postflight --task-id TASK_ID --result result.json
filewise agent evidence inspection SOURCE_ID --locator sheet:1/cell:B2
filewise agent trace inspection --version VERSION
```

| 功能 | 实际行为 |
| --- | --- |
| `resolve` | 版本模式返回固定文件状态和类型化字段；指定 `--valid-time` 则走已审核修订的双时间解析，可加 `--transaction-time`，返回未知/冲突/撤销状态，不回溯历史权限 |
| `impact` | 项目契约与版本化 `depends_on` 上的双向遍历，返回路径、缺失依赖和传播边界；有基线时联合前后图 |
| `compile` | 按显式目标路径及影响集合取反向依赖闭包，固定版本、来源、坐标、哈希、回归任务、输出断言、工具契约、审批点和拒绝条件；不靠模型猜任务范围 |
| build verify | 真正运行文件/业务断言，另列生产运行时门禁；不把保存成功等同检查 PASS |
| preflight verify | 重验任务身份、固定版本、证据、有效期、工具/路径权限、上下文完整性；写操作再查最新基线与原件漂移 |
| postflight verify | 核对结果字段断言、引用坐标/原文、输出文件哈希；写操作须有该身份从输入基线完成的 Filewise 写入及匹配原件。没有可检查的输出返回 NEEDS_REVIEW |
| `evidence / trace` | 原始来源片段，及版本父链、写入说明、模型/任务声明、构建来源、读写和计算审计；模型/tool 是调用者声明，actor 是服务端身份 |

`checks.json` 是机器断言数组，当前输出检查对象固定为 `result`，支持 `eq/gte/lte/contains/exists`：

```json
[{"id":"pressure-result","object_id":"result","field":"pressure_kpa","op":"eq","expected":120}]
```

`result.json` 为 `{"pressure_kpa":120}`。错误值确实导致 BLOCKED。文件输出可用 `--outputs outputs.json`（`{"requirement.json":"SHA256"}`）；引用用 `--citations citations.json`（Evidence 数组）。验证一次写入时额外传 `--operation write --output-version SAVED_VERSION`，输入仍固定于 task_id，输出不得越过任务文件范围。

`max_chars` 限制序列化 context，不包含任务契约与回归计划。截断会显式标记，preflight 不放行，需缩小路径或扩大预算（最大50,000字符）。任务对模型只是数据和可核验契约，不执行任意代码；`verify` 不把自由文本答案等同语义证明，也不执行外部系统动作。

状态码：成功 JSON 到 stdout；HTTP/输入错误到 stderr、退出1；verify 的 BLOCKED/NEEDS_REVIEW 或 `require_pass` 拒绝保存时仍输出结构化结果、退出2。普通保存即使业务检查 BLOCKED 仍退出0，因为保存成功且未发布。`production_authorized` 始终为 false，生产门禁由独立发布流程承担。

## 5. 持久化、冲突与恢复

单项目序列化写入；完整文件集 CAS 后，逐文件原子替换。原件备份、意图、版本和幂等记录持久化；失败尽量补偿，无法安全补偿时阻断后续写入，不覆盖外部冲突。多文件不是跨文件系统事务，强制中断可暂留部分写入。

```bash
filewise agent recover inspection --version INTERRUPTED_WRITE_VERSION
# 恢复后用原 request-id 与完全相同的请求重试
```

Agent 只能恢复自己在这个项目的原生写入。恢复不会发布，也不会恢复任意历史版本；操作员的历史恢复另走审核候选流程。候选和中断前未完成的记录不成为 latest，未保存 metadata 不会被后续同步继承。

## 6. 兼容的生产只读与原生连接

原有 `auth-init` 的 Agent 凭据没有 `workspace_projects`，仍只能读已发布版本：

```bash
filewise agent open inspection
filewise agent ls SESSION_ID
filewise agent read SESSION_ID requirement.json
filewise agent search SESSION_ID 压力
```

这种会话固定发布版本，目录漂移或来源/发布撤销使后续读取拒绝。`project launch` 仍只为这一模式选择令牌，可在 macOS 新进程中限制真实文件、数据库及全角色令牌访问。工作区授权不会自动改变旧凭据权限。

原生 Codex/Claude/Pi 连接与 `filewise run` 是可选的审核副本路径；保存后观察用于普通编辑器。它们不是 Agent 原生 CLI 的前置条件。具体范围见 [中间件说明](middleware.md)。要强制 Agent 只能经 HTTP 使用知识，将数据库、原目录和操作员凭据置于独立 OS 身份/容器之外，仅提供 Agent 令牌；单靠提示词不形成文件系统隔离。

## 可重复验证

```bash
PYTHONPATH=src .venv/bin/python examples/check_workspace.py
PYTHONPATH=src .venv/bin/python examples/check_agent_boundary.py
```

第一项实测授权、真实 HTTP 和 CLI 子进程：读→写+meta→历史/diff→impact→compile→三阶段 verify→二进制修复→删除；不用 watcher，不调用云模型，发布指针保持不变。第二项验证兼容的生产只读网关与 macOS 进程边界。全部只使用临时合成文件。

行业系统连接器、任意图像/图纸理解、自动依赖推断，以及研究材料中的召回率、影响准确率和专家验收目标，需各自接入或实测；当前不冒充这些结果。已实现的是上表可调用、可检查的文件与知识计算契约。
