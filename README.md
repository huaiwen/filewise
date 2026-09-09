# Filewise

**供 Agent 和人员直接使用的文件知识计算与版本层。** Agent 调用 Filewise 完成读写与 metadata 提交，直接查询历史、差分、影响，并编译和验证任务上下文。普通工作保存与独立审核后的生产发布分开。

本地单租户实现：Python 3.11+、SQLite、FastAPI、浏览器文件工作台与认证 CLI。无需模型、向量库、Node 服务或外部账号；演示和测试全部使用合成资料。

## Agent 原生用法

由操作员注册项目并执行 `filewise auth-agent PROJECT_ID`，将返回的项目专用令牌注入 Agent 环境，重启服务后即可直接调用：

```bash
filewise agent ls PROJECT_ID
filewise agent read PROJECT_ID requirement.json
filewise agent write PROJECT_ID requirement.json --base VERSION_FROM_LS \
  --request-id pressure-001 --content '{"pressure_kpa":120}' \
  --meta '{"summary":"调整压力","facts":{"pressure_kpa":120}}' -m "更新要求"
filewise agent read PROJECT_ID requirement.json --version HISTORICAL_VERSION
filewise agent diff PROJECT_ID OLD_VERSION NEW_VERSION
filewise agent impact PROJECT_ID --path requirement.json
filewise agent compile PROJECT_ID --path requirement.json --goal "核对相关要求"
filewise agent verify PROJECT_ID --phase preflight --task-id TASK_ID
```

**Filewise 自己保存原件与版本，不依赖先改文件再通知监听器。** 支持批量增删改、二进制原字节、metadata-only、CAS、幂等、dry-run 和失败恢复。`--require-pass` 可要求检查通过才保存；普通保存不自动批准或发布。生产只读会话、保存后观察和原生审核副本仍作为独立模式保留。

完整授权、命令、版本选择及三阶段验证见 [Agent 使用说明](docs/agent.md)。可直接运行合成端到端检查：

```bash
uv sync --locked --all-extras --no-editable
uv run --no-sync python examples/check_workspace.py
```

## 人员工作台与可选原生连接

在本仓库执行：

```bash
uv sync --locked --all-extras --no-editable
uv run --no-sync filewise start
```

工作台会自动在浏览器打开并登录。首次使用按页面完成三步：

1. **关注文件夹**：粘贴资料目录路径；默认包含其中的常用文档，可展开“只关注部分文件”限定范围。
2. **连接 Agent**：在 Codex、Claude Code 或 Pi 旁点击“连接”。Filewise 自动安装这个文件夹的接入配置，保留已有设置；点击“连接自检”检查是否可用。
3. **验证接入**：在该文件夹中新开 Agent 任务，照常让它读写文件。Agent 如提示信任项目钩子/扩展，需要在那里启用一次。收到工具请求后，Filewise 自动更新状态。

之后，受支持的 Agent 工具在审核副本里修改，你回到工作台查看 **哪里变了 → 检查与影响 → 审核通过 → 写回原文件并发布**。同一 Agent 任务会持续看到自己的修改。普通 Excel 或编辑器直接保存原文件时，Filewise 也会在保存后记录变化。

完成一轮修改后，在“变更”中查看累计差异，点击 **记录一次提交**，填写说明，再到 **提交历史** 回看。这一步保存版本与说明，不要求先审核；审核和写回仍可随后单独进行。首次使用可先把初始文件集记为第一条提交。

**原生工具的审核副本模式需要连接 Agent；上面的原生 CLI 不需要。** 配置由 Filewise 的“连接”按钮完成，不需要手写 JSON；仅选择关注文件夹只能保证保存后的观察。已经打开的任务需重新加载项目配置。侧栏“Agent 连接”可查看状态、自检和断开，不会把“配置已安装”显示成“已接入”。

当前原生受控连接支持 **macOS**，覆盖原生文件工具与前台 Shell。外部 MCP、后台进程、独立终端不经过这层钩子。Codex/Claude Code 已验证协议与实际钩子命令，Pi 另验证了安装版本的扩展运行时；未调用云模型。详细范围见 [接入与中间件说明](docs/middleware.md)。

保持启动的终端运行。以后在同一 Filewise 目录运行 `filewise start` 会恢复关注列表、配置和历史；关闭浏览器不停止服务。默认本机端口 8000，可用 `--port` 修改；无图形桌面时用 `--no-open` 获取本机登录链接。安装后的可执行文件是 `.venv/bin/filewise`。

## 用合成资料体验

在本仓库执行：

```bash
uv sync --locked --all-extras --no-editable
uv run --no-sync filewise showcase
```

打开 [Filewise 工作台](http://127.0.0.1:8765)。这是独立临时数据库中的合成项目，已经发布 100 kPa 的需求与 Excel 检验计划：

1. 先点 **记录一次提交**，填写“初始检验资料”；再点击 **修改需求**：JSON 压力变为 120，Excel 仍为 100，检查 BLOCKED。
2. 查看变更位置与“影响路径”；点击 **验证 Agent 读取**，原目录变化使旧发布读取返回 STALE。
3. 点击 **修复检验计划**：Excel B2 改为 120，检查 PASS。查看两次保存累计的改动，记录提交“将检验压力统一为 120 kPa”；提交历史中点开说明可回看。
4. 右上角切换 **审核者**，审核通过；切换 **发布者**，发布此版本。
5. 再验证 Agent 读取，得到新版本及文件来源、SHA-256 和读取凭据。

演示身份只用于此临时实例。停止服务后演示数据删除；重启得到新的项目与凭据。日常服务不启用演示登录。

## 处理模式与高级配置

工作台的“关注设置”默认**两种都启用**，可以暂停或改为保存后分析、仅受控修改。仅保存后分析不会阻止已经发生的写入，也不能凭文件变化判断是哪一个模型或人员所改。

需要从命令行创建项目和契约时，仍可使用：

```bash
uv run --no-sync filewise follow "/absolute/path/to/资料" --include '*.xlsx' --include '*.md' --include '*.json'
```

`follow` 的状态保存在被关注目录的 `.filewise/`；重启使用 `filewise follow 同一目录`。终端给出网页和凭据，网页仍提供“Agent 连接”。普通使用优先采用上面的 `start` 向导。一次性受控 CLI 任务 `filewise run` 的操作见 [中间件说明](docs/middleware.md)。

首次配置时可再传 `--spec examples/project-contract.json` 声明文件依赖与业务约束，例如 Excel B2 必须等于 JSON 中的压力值。省略时 PASS 只表示文件集完整性，业务适用性仍需人工审核。项目范围与契约约束在创建时固定，文件可通过版本化 metadata 补充依赖；`local-owner` 是人工操作身份，自动观察/受控候选由不同系统身份提交。

高级的多项目、上传和分角色流程仍可用：

```bash
uv run --no-sync filewise auth-init
uv run --no-sync filewise project add /absolute/path/to/project --id inspection --name 检验资料 --spec examples/project-contract.json
uv run --no-sync filewise project sync inspection
uv run --no-sync filewise serve --tokens .filewise/tokens.json
```

普通 `serve` 的网页创建上传项目，上传完整文件集；本地目录需在 CLI 显式注册。只有绑定本机的 `start` / `follow` 开启目录选择和接入安装接口。分别使用编辑、审核和发布凭据操作。上传时未包含的旧文件作为删除变更接受审核，历史原件保留。

Agent 接口不读取本地数据库。下面假设安装后的 `filewise` 已在 PATH 中；在仓库内可用 `uv run --no-sync filewise` 代替：

```bash
# 兼容的生产只读模式：使用未授予 workspace_projects 的 audience=agent 凭据
export FILEWISE_URL=http://127.0.0.1:8000
filewise agent open inspection
filewise agent ls SESSION_ID
filewise agent read SESSION_ID requirement.json
filewise agent search SESSION_ID 压力
```

支持 macOS 新进程文件隔离启动。完整操作、Codex 接入和边界检查见 [Agent 使用说明](docs/agent.md)。

## 文件格式与变更定位

| 文件 | 定位与比较 |
| --- | --- |
| UTF-8 文本、Markdown、代码 | 文本行对齐，减少插入一行导致整篇误报 |
| JSON | 类型敏感的 JSON Pointer 变化与字段检查 |
| CSV | 行/列坐标 |
| Excel XLSX | 工作表/单元格、值与公式原文；公式不执行 |
| Word DOCX | 段落与表格坐标 |
| PowerPoint PPTX | 幻灯片/文本框与表格坐标 |
| PDF | 文本页码，未做 OCR |
| 其他二进制或无法抽取的文档 | 保存原字节、哈希与版本，明确提示暂无文本定位 |

基础安装提供文本/CSV；`--all-extras` 安装 PDF/Office 解析器。XLS 等旧二进制 Office、图像、图纸、音视频目前仅管理原件；没有自动业务语义理解。数值、公式、约束措辞变化是确定性审核提示，业务影响依据显式依赖与断言。

## 用类似 Git 的方式管理知识

当前可运行的是 **查看改动 → 记录提交 → 查看历史 → 审核恢复**：

- **快照**自动保存每次捕获的完整文件集；完成一轮修改后，选择其中一个版本并填写说明，记为一次**提交**。多次保存的累计改动按上次提交计算。
- **变更**默认对比上次提交；历史中的提交对比它的父提交。也可切到“相对构建基线”，查看原来的审核报告。Excel 单元格、JSON 字段及声明依赖复用同一比较逻辑。
- **提交历史**保留提交说明、作者、时间、父提交与原文件版本；自动快照在“全部快照与发布记录”中。内容未变化时拒绝重复提交；并发改变历史时要求刷新后重新核对。
- **记录提交**不等于批准使用。草稿或检查失败的版本也可留档；独立审核、发布、受控写回和 Agent 读取门禁仍单独执行。

每次提交包含所选快照中的全部关注文件，尚无逐文件/逐块暂存。已有数据库升级后保留全部快照，提交历史从用户首次记录开始。

**恢复历史文件**：在提交历史中点开目标版本，点击 **从此版本恢复文件 → 生成恢复候选**。Filewise 先保存此刻的原件，再显示恢复带来的新增、修改和删除；确认后由人工 **审核通过 → 写回原文件并发布**。原件与历史完全一致时提示无需恢复。目标版本尚未审核或检查失败时，也只生成重新检查的候选，不能跳过发布门禁。

恢复针对本地目录的完整关注文件集：补回历史文件，删除目标版本中没有的关注文件，保留排除文件。生成候选不改原件，也不重写提交历史；可将恢复结果另记为一次提交。原件再次变化时拒绝写回，需重新生成候选。操作不要求 Agent 连接或启用自动观察；当前需 macOS/Linux。上传项目可下载历史原件，但不提供本机目录恢复。“仅切换发布指针”仍不修改原文件。

本地 CLI 对应命令如下，`PROJECT_ID` 和 `RELEASE_ID` 来自 `project status` / `project sync` 输出；用 `--db` 指向启动服务时的同一数据库：

```bash
filewise project status PROJECT_ID
filewise project diff PROJECT_ID RELEASE_ID
filewise project commit PROJECT_ID RELEASE_ID -m "初始资料" --expected-parent NONE
filewise project log PROJECT_ID
# 后续提交把 NONE 换成 log 中最新提交的 id
```

命令行恢复与审核写回（先设置同一数据库路径）：

```bash
filewise --db DB_PATH project restore PROJECT_ID HISTORICAL_RELEASE_ID
# 输出新候选 CANDIDATE_ID，report.base_release 是生成时保存的原件版本
filewise --db DB_PATH project diff PROJECT_ID CANDIDATE_ID --base-release BASELINE_ID
filewise --db DB_PATH --actor local-reviewer --roles reviewer project review PROJECT_ID CANDIDATE_ID
filewise --db DB_PATH --actor local-publisher --roles publisher project apply PROJECT_ID CANDIDATE_ID --expected-active CURRENT_RELEASE_ID
# 尚无当前发布时省略 --expected-active
```

这些命令操作 Filewise 的本地知识历史，不会执行 Git commit 或向 GitHub 上传。

这套流程已可运行；分支合并、远程 push/pull、自动消解知识冲突没有实现。Filewise 不替代 PLM/MES/QMS 的原始审批和事务。[TeamAI 评估](docs/teamai-assessment.md)将其归为 Agent 接入与团队协作层，借鉴配置分发、图谱来源与反馈；没有安装或自动接入上游代码。

## 实际资料如何进入内核

文件导入生成证据片段；业务字段和依赖由编辑者提交 JSON IR，由审核者批准。检索命中或文件导入均不能直接批准事实。

```bash
uv run --no-sync filewise scope examples/scope.json
uv run --no-sync filewise ingest inspection examples/synthetic-pressure.txt --acl reader,editor,reviewer,publisher
```

保存返回的 `id`、`fragments[].locator` 和原文，用于修订请求。例如将下列内容保存为本地 `revision.json`，替换 `SOURCE_ID_FROM_INGEST`：

```json
{
  "id": "rule-v1", "scope_id": "inspection", "object_id": "rule",
  "kind": "rule", "title": "合成压力规则", "fields": {"pressure_kpa": 100},
  "valid_from": "2024-01-01T00:00:00Z",
  "evidence": [{"source_id": "SOURCE_ID_FROM_INGEST", "locator": "line:2", "quote": "压力值为 100 kPa"}]
}
```

```bash
uv run --no-sync filewise propose revision.json
uv run --no-sync filewise --actor local-reviewer --roles reviewer decide rule-v1 approved
uv run --no-sync filewise build examples/build.json
```

这时 `procedure` 尚未提交，构建应返回 **BLOCKED**。按相同方式提交程序对象，设置 `fields.pressure_kpa=100`、`depends_on=["rule"]`，证据指向第 3 行，再批准并构建。`examples/build.json` 的业务时间可按需修改，必须包含时区。完整发布操作：

```bash
# RELEASE_ID 为构建返回的 id；首次激活不传 --expected-active
uv run --no-sync filewise --actor local-reviewer --roles reviewer approve RELEASE_ID
uv run --no-sync filewise --actor local-publisher --roles publisher activate RELEASE_ID
uv run --no-sync filewise --actor local-reader --roles reader context RELEASE_ID procedure
```

后续激活/回滚必须提供 `--expected-active CURRENT_ID`，防止并发覆盖。`verify RELEASE_ID` 同时返回构建结果和当前运行时门禁。更多命令见 `filewise --help`。

## 计算和发布契约

| 操作 | 当前实现 |
| --- | --- |
| `resolve` | 按业务有效时间和记录时间解析已审核修订；相同权威级别取较新的生效时间，同级同时间冲突明确拒绝 |
| `diff` | 比较类型化字段、阈值、依赖、ACL、权威和证据；不声称理解任意自然语言差异 |
| `impact` | 声明依赖上的双向、环安全遍历；构建时合并旧/新依赖，返回路径及缺失边界 |
| `compile` | 工作区按目标路径生成有界、版本固定的依赖上下文、回归任务、输出断言和工具/审批契约；内核 build 保留审核任务与全范围回归 |
| `verify` | 构建、执行前、执行后分别核对声明断言、来源、任务权限、基线与输出；没有可检查输出时 NEEDS_REVIEW，结果不自动授权生产 |
| 发布 | 内容寻址、不可变发布包；不同身份审批、原子激活、回滚及永久撤销 |
| 消费 | 显式 `release_id`，读取对象及其依赖闭包；每次重新核对来源策略和撤销状态 |

“影响完整”只相对于显式声明的范围、对象和依赖成立。PASS 表示这些断言通过，不证明规程符合真实业务，也不授权设备操作。

[zvec-grep 评估](docs/zvec-grep-assessment.md)仍是可选检索层的设计建议，**本版本未安装、接入或基准测试 zvec-grep**，也不创建其索引。

## REST 与 Python API

REST 使用 `Authorization: Bearer TOKEN`；服务端令牌文件由 `auth-init` 生成，也可由管理员配置。无默认密码或匿名业务接口。运行时不联网、不调用模型。

```python
from filewise import Engine, Actor

engine = Engine(".filewise/demo.db")
reader = Actor(id="trusted-local-reader", roles={"reader"})
print(engine.overview(reader))
# release_id 必须来自已批准且曾激活的发布包
# print(engine.context(release_id, ["procedure"], reader))
```

Python API 和本地管理 CLI 是可信操作接口；`filewise agent` 则只使用 HTTP 认证网关。HTTP 身份由服务端固定，工作区 Agent 只访问明确授权项目的知识接口，不能进入操作员审批/发布接口；旧只读凭据仍不能读草稿。数据库管理员仍在可信边界内。

完整请求模型和端点见带认证的 `/api/openapi.json`，或 [API 文档](docs/api.md)。主界面提供项目与文件工作流；`/admin` 保留底层范围、修订和发布控制台。

## 测试与打包

```bash
uv sync --locked --all-extras --no-editable
uv run --no-sync ruff check src tests
uv run --no-sync ruff format --check src tests
uv run --no-sync python -m unittest discover -s tests -v
uv run --no-sync python examples/check_workspace.py
node --check src/filewise/static/workspace.js
uv build
```

源码开发时可用 `uv sync --locked --all-extras`；若环境跳过可编辑安装的 `.pth`，使用上面的普通安装，或 `PYTHONPATH=src uv run --no-sync python -m unittest discover -s tests -v`。修改源码后，普通安装需重新执行 `uv sync --reinstall-package filewise-engine --no-editable --all-extras`，然后重启已有 Filewise 服务并刷新页面。

测试用标准库 `unittest`，包括真实 SQLite 并发、权限/撤销、时间边界、原文锚点、HTTP 全流程、CLI 子进程和合成 Office/PDF 文件。[验证记录](docs/validation.md)区分本地已执行项目与尚未执行的 CI/Docker 检查。

## 部署与资料边界

默认只监听 `127.0.0.1`。跨机器使用时，由部署方提供 HTTPS、网络访问限制和身份生命周期管理，见 [安全边界](docs/security.md)。

提供 `Dockerfile`，镜像默认仅含基础解析器。构建上下文必须是本仓库：

```bash
docker build -t filewise .
```

容器以非 root 用户运行；运行时为 `/data` 配置可写持久卷，将令牌文件只读挂载到 `/run/secrets/filewise_tokens`，确保该用户可读。不要将令牌烘焙进镜像。Docker 配置尚未在本机实际构建。

Git、sdist 和 Docker 均限定仓库内容。`.filewise`、数据库、令牌、环境配置和本地工作记录被排除。合成示例位于 `examples/`；没有复制任何父目录业务材料。源码包使用显式文件白名单。

[架构与状态语义](docs/architecture.md) · [安全边界](docs/security.md) · [REST API](docs/api.md) · [Agent 接入](docs/agent.md) · [TeamAI 评估](docs/teamai-assessment.md)
