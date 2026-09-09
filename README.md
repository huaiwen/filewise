# Filewise

**位于 Agent、人员与真实文件之间的知识版本与发布层。** 文件变化后，定位改动、检查依赖影响、验证业务约束，经独立审核发布，再按固定版本提供给 Agent。

本地单租户实现：Python 3.11+、SQLite、FastAPI、浏览器文件工作台与认证 CLI。无需模型、向量库、Node 服务或外部账号；演示和测试全部使用合成资料。

## 直接体验

在本仓库执行：

```bash
uv sync --locked --all-extras --no-editable
uv run --no-sync filewise showcase
```

打开 [Filewise 工作台](http://127.0.0.1:8765)。这是独立临时数据库中的合成项目，已经发布 100 kPa 的需求与 Excel 检验计划：

1. 点击 **修改需求**：JSON 压力变为 120，Excel 仍为 100，检查 BLOCKED。
2. 查看变更位置与“影响路径”；点击 **验证 Agent 读取**，原目录变化使旧发布读取返回 STALE。
3. 点击 **修复检验计划**：Excel B2 改为 120，检查 PASS。
4. 右上角切换 **审核者**，审核通过；切换 **发布者**，发布此版本。
5. 再验证 Agent 读取，得到新版本及文件来源、SHA-256 和读取凭据。

演示身份只用于此临时实例。停止服务后演示数据删除；重启得到新的项目与凭据。日常服务不启用演示登录。

## 关注自己的文件：配置一次，两种处理

安装后，在 Filewise 仓库执行下面一条命令，选择实际要关注的文件夹：

```bash
uv run --no-sync filewise follow "/absolute/path/to/资料" --include '*.xlsx' --include '*.md' --include '*.json'
```

打开终端显示的 [本地工作台](http://127.0.0.1:8000)，粘贴同一终端给出的访问凭据。默认**两种都启用**；左侧“关注设置”可以暂停或选择处理方式。省略 `--include` 表示管理该目录下所有未排除文件；配置、历史与凭据保存在所选目录的 `.filewise/` 中。重启只需 `filewise follow 同一目录`，会沿用配置。保持此服务运行才会自动关注，关闭浏览器不影响服务。

**保存后自动分析**：照常让模型、Excel 或其他编辑器修改原文件。保存稳定约 1–3 秒后，工作台出现“发现文件变更”。点击即可看改了哪里、影响哪些已声明的依赖、哪些检查未通过。没有自动批准或发布，写入者未知时明确记为 unknown。

**先审核、再写原文件**：另开终端启动模型任务，例如已安装 Codex CLI 时：

```bash
uv run --no-sync filewise run "/absolute/path/to/资料" -- codex exec --skip-git-repo-check -s workspace-write "修改当前目录里的文件，完成后说明改动"
```

模型在临时工作副本中修改文件，命令结束后工作台出现“有待审核修改”。查看差异与检查 → **审核通过** → **写回原文件并发布**。未批准、检查失败或原件再次变化时，拒绝写回。原件在批准前保持不变；Excel 等 Office 文件按审核时保存的原字节写回。

受控启动当前支持 **macOS**，只限制新启动进程及其后代对所选原目录和 Filewise 私有状态的文件访问；不能接管已经运行的模型或外部 MCP 服务。普通保存后的观察可用于任意编辑工具。真实 Codex 云调用未纳入测试；进程隔离与写回已用本地命令验证。详细边界、恢复及 API 见 [中间件说明](docs/middleware.md)。

首次配置时可再传 `--spec examples/project-contract.json` 声明文件依赖与业务约束，例如 Excel B2 必须等于 JSON 中的压力值。省略时 PASS 只表示文件集完整性，业务适用性仍需人工审核。范围、依赖和约束在创建时固定；`local-owner` 是人工操作身份，自动观察/受控候选由不同系统身份提交。

高级的多项目、上传和分角色流程仍可用：

```bash
uv run --no-sync filewise auth-init
uv run --no-sync filewise project add /absolute/path/to/project --id inspection --name 检验资料 --spec examples/project-contract.json
uv run --no-sync filewise project sync inspection
uv run --no-sync filewise serve --tokens .filewise/tokens.json
```

网页创建项目后可上传完整文件集；本地目录需在 CLI 显式注册，网页不能选择任意服务器路径。分别使用编辑、审核和发布凭据操作。上传时未包含的旧文件作为删除变更接受审核，历史原件保留。

Agent 接口不读取本地数据库。下面假设安装后的 `filewise` 已在 PATH 中；在仓库内可用 `uv run --no-sync filewise` 代替：

```bash
# FILEWISE_TOKEN 由管理员以环境变量提供，使用 audience=agent 的凭据
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

文件快照对应提交，位置差分对应 diff，独立审核后发布，当前 release_id 对应可切换指针，历史版本不可变。回退指针保留历史；撤销则拒绝该版本的未来读取。本地目录每次读取还验证真实文件哈希，发生变化必须同步、审核并发布新版本。

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
| `compile` | `build` 中生成受影响对象的审核任务和完整范围回归集；只读，工具权限为空 |
| `verify` | 确定性断言、来源坐标、哈希、完整性及实时权限检查；没有领域检查为 NEEDS_REVIEW |
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

Python API 和本地管理 CLI 是可信操作接口；`filewise agent` 则只使用 HTTP 认证网关。HTTP 身份由服务端固定，Agent 凭据不能访问操作员的草稿接口。数据库管理员仍在可信边界内。

完整请求模型和端点见带认证的 `/api/openapi.json`，或 [API 文档](docs/api.md)。主界面提供项目与文件工作流；`/admin` 保留底层范围、修订和发布控制台。

## 测试与打包

```bash
uv sync --locked --all-extras --no-editable
uv run --no-sync ruff check src tests
uv run --no-sync ruff format --check src tests
uv run --no-sync python -m unittest discover -s tests -v
node --check src/filewise/static/workspace.js
uv build
```

源码开发时可用 `uv sync --locked --all-extras`；若环境跳过可编辑安装的 `.pth`，使用上面的普通安装，或 `PYTHONPATH=src uv run --no-sync python -m unittest discover -s tests -v`。修改源码后，普通安装需重新执行 `uv sync --reinstall-package filewise-engine --no-editable --all-extras`。

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
