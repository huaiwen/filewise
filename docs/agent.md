# 在 Agent 与真实文件之间使用 Filewise

Agent 使用 HTTP 文件入口，操作员管理真实目录、审批和发布。下面以 Codex 为例，其他能调用命令行的 Agent 使用相同接口。仓库环境中使用 `uv run --no-sync filewise`；独立 Agent 环境需把安装后的 filewise 所在 bin 目录加入 PATH（例如 `/absolute/path/to/Filewise/.venv/bin`）。安装目录应在被保护的真实项目目录之外。

## 自动观察与受控修改

日常从 `filewise follow /absolute/path/to/files` 开始，默认同时启用保存后观察和受控修改。使用 `filewise run /absolute/path/to/files -- 模型命令` 启动修改任务，模型编辑副本；操作员在工作台审核并写回。完整命令见 [中间件说明](middleware.md)。

`run` 用于修改候选；下文的 `agent` 与 `project launch` 用于只读消费已发布版本。它们沿用同一文件、依据和发布内核。

## 建立项目

在 Filewise 仓库安装后，可信本地操作员注册一个目录。数据库和令牌应放在受保护、且不属于被管理文件集的目录中；不要注册包含私人申报材料的整个父目录。

```bash
uv sync --locked --all-extras --no-editable
uv run --no-sync filewise auth-init
uv run --no-sync filewise project add /absolute/path/to/project --id inspection --name 检验资料 --spec examples/project-contract.json
uv run --no-sync filewise project sync inspection
```

注册只保存目录边界，`sync` 生成候选版本。`--spec` 可省略，此时只检查文件完整性。示例契约需要 `requirement.json` 的 `/pressure_kpa` 与 `inspection.xlsx` 的 `sheet:1/cell:B2` 相等，并声明检验计划、交付清单的依赖。契约在项目创建时固定；后续修改依赖/校验需新项目 ID。

运行服务并在工作台审核/发布，也可使用本地操作员命令：

```bash
uv run --no-sync filewise serve --tokens .filewise/tokens.json
# 在另一个终端执行：RELEASE_ID 来自 project sync 的 release_id
uv run --no-sync filewise --actor reviewer --roles reviewer project review inspection RELEASE_ID
uv run --no-sync filewise --actor publisher --roles publisher project publish inspection RELEASE_ID
# 后续发布需追加 --expected-active CURRENT_RELEASE_ID
```

本地管理命令的身份由可信操作者给出，不能把数据库提供给受限 Agent。身份和角色认证发生在 HTTP 服务边界。

## Agent 专用 CLI

管理员将令牌文件中 `audience: "agent"`、`roles: ["reader"]` 对应的键通过安全的进程环境交给 Agent。不要把包含所有角色的令牌文件交给 Agent。

```bash
export FILEWISE_URL=http://127.0.0.1:8000
# FILEWISE_TOKEN 由启动环境注入，不放入仓库或提示词
filewise agent projects
filewise agent open inspection
# 把返回的 session_id 用作 SESSION_ID
filewise agent ls SESSION_ID
filewise agent read SESSION_ID requirement.json
filewise agent search SESSION_ID 压力
filewise agent read SESSION_ID inspection.xlsx --output /tmp/inspection-copy.xlsx
```

stdout 为 JSON，包含 release_id、source_id、SHA-256、原文/坐标和 receipt。`--output` 校验哈希后写入原字节，并拒绝覆盖已有文件。错误输出到 stderr，退出码非零。HTTP 客户端拒绝重定向；远程地址要求 HTTPS，回环可用 HTTP。

Agent 凭据只能访问项目摘要与文件会话入口，不能绕道读取 `/api/sources`、`/api/resolve` 或草稿发布包。服务端令牌由 `auth-init` 生成，修改后重启服务生效。旧四角色令牌配置仍可使用，但需要显式添加独立的 Agent 身份。

## 新启动的 Codex 使用受限目录

在 macOS 上，可信操作员可启动一个新 Agent 进程，将真实项目、SQLite 主文件/WAL/SHM/journal、操作员令牌文件排除在该进程的文件读写权限之外。子进程工作在临时目录，只收到 Agent 凭据、FILEWISE_SESSION 和 CLI 使用说明。

```bash
# 安装后的 filewise 和 codex 应在 PATH 中，且位于受保护项目目录之外。
# launch 选项放在项目标识之前；-- 后原样传递给 Agent，不经过 shell 拼接。
filewise project launch --tokens .filewise/tokens.json --url http://127.0.0.1:8000 inspection -- codex
```

`launch` 先建立已发布文件会话；没有可用发布或文件已变动时，不启动 Agent。非 macOS 或缺少 `sandbox-exec` 时明确拒绝，不退化为未隔离执行。底层边界继承到这个子进程的后代。

这不拦截已运行的 Codex，也不管控另一个终端、已有 MCP 服务或其他进程代为读取。macOS 限制只覆盖指定文件路径，不能被解释为隔离任意恶意程序的整机安全边界。更强隔离应使用独立 OS 身份或容器，将原文件和操作员数据库置于 Agent 的挂载空间之外，并单独约束外部工具。不要授予受限 Agent 额外的主机文件读取服务。

## 修改、回退和撤销

- 上传项目：新候选不会改变已有发布字节；批准、发布后新会话绑定新 release_id。
- 本地目录项目：任何纳管文件变动均使旧会话返回 STALE。重新同步、检查、独立审核并发布后建立新会话。
- 回退只切换活动指针。上传项目仍可读取有效的历史固定版本；目录项目还要求实际文件与所选历史版本一致。回退指针本身不覆盖真实文件；只有显式批准的受控写回才修改原件。
- 发布或证据撤销会拒绝后续读取；已经交付给 Agent 的内容不能远程收回。

## 可运行的边界检查

```bash
PYTHONPATH=src .venv/bin/python examples/check_agent_boundary.py
```

检查使用真实回环 HTTP 服务和独立 CLI 子进程；验证下载原字节、禁止覆盖、文件变化失效。在 macOS 还验证原文件/数据库/操作员令牌读写边界，以及认证网关仍可读取。它不调用 Codex 云模型，不处理真实业务资料。
