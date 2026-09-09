# Filewise 文件中间件

## 最短使用路径

先在 Filewise 仓库安装：`uv sync --locked --all-extras --no-editable`，然后运行 `uv run --no-sync filewise start`。页面自动打开并登录：**关注文件夹 → 连接 Agent → 验证接入**。之后照常使用 Agent，通过页面检查、审核和写回修改。所需项目配置由“连接”自动安装；Agent 自身可能要求一次项目钩子/扩展信任，随后新开任务或重新加载。

`start` 的关注列表、版本和凭据保存在运行目录的 `.filewise/`；再次从同一目录启动即可恢复。`--db` 可指定独立状态路径。默认只监听 `127.0.0.1:8000`，`start --port 8001` 修改端口；`start --no-open` 只显示本机登录链接。只在自己的服务成功监听后自动打开浏览器。关闭浏览器不停止服务；Ctrl+C 停止，未设置开机自启。

下文的 `filewise` 在仓库内可替换成 `uv run --no-sync filewise`；安装后的可执行文件也位于 `.venv/bin/filewise`。Filewise 和模型运行环境应放在被关注目录之外。

## 原生 Agent 接入

连接只安装在选定目录，保留已有设置和其他钩子；不修改用户全局配置。断开时移除 Filewise 项并保留其他设置；已有历史、审核副本及候选不删除。

| Agent | 项目接入文件 | 操作方式 |
| --- | --- | --- |
| Codex | `.codex/hooks.json` | 在同一目录新开任务，按客户端要求信任项目钩子 |
| Claude Code | `.claude/settings.local.json` | 在同一目录新开任务，按客户端要求启用项目钩子 |
| Pi | `.pi/extensions/filewise.ts` | 新开任务或 `/reload`，按客户端要求信任扩展 |

界面依次显示“配置已安装 · 等待新任务”“Agent 已加载 · 等待文件操作”“已收到受控工具请求”。**连接自检通过不代表 Agent 已实际加载**：自检只验证配置、路径改写、真实 macOS 文件限制及副本可写。只有接收到工具请求后才记录最后工具与时间；此状态也不代表所有其他工具都受控。CLI 是否出现在 PATH 只作提示，不能证明桌面端能力。

文件读写路径进入同一任务的持久审核副本，任务中的后续读取能看到自己之前的修改；Shell 在该副本内执行。副本只包含关注文件，所需项目脚本也要选入。修改成为候选，原件不变；人工审核后使用“写回原文件并发布”。同一任务的候选写回后可继续编辑；如果原件被其他人或另一任务改过，旧任务拒绝继续，需基于最新原件新开任务。

覆盖 Codex `Bash`/`exec_command`/`apply_patch`，Claude 原生读写、编辑、查找及 `Bash`，Pi 的 `read`/`write`/`edit`/`bash`/查找工具。Codex 钩子协议要求用 `permissionDecision=allow` 才能接受 `updatedInput`，因此被改写的工具在该钩子层获准执行；实际 Shell 另外受 Filewise OS 规则约束，候选仍必须人工批准写回。Claude 的参数改写继续交由原有工具权限判断。Filewise 不写入 Agent 的宽松权限设置。具体协议见 [Codex 官方钩子文档](https://learn.chatgpt.com/docs/hooks)、[Claude Code 官方钩子文档](https://code.claude.com/docs/en/hooks)。

原生 Shell 的后代不能读写原目录和 Filewise 私有状态，只能写审核副本及 `/dev/null`；临时文件使用副本中的 `.filewise-tmp`。原生文件工具的路径改写依赖 Agent 正常执行钩子。钩子被关闭、宿主超时或其他钩子改写行为时，不能保证接管；Filewise 自身的启动/输入错误用阻断返回，仍不能替代宿主的钩子失败策略。外部 MCP、宿主绕过钩子的工具、后台进程、Pi `!` 用户终端命令、独立终端、文档打开前的自动上下文加载均不覆盖。适配器不是整个 Agent 的 OS 沙箱。

目前需 macOS。文件保存后的观察仍可独立使用；其他系统的受控接入明确不可用。暂停或切到仅观察时，已经安装的受控钩子会阻断新工具请求，避免静默直写；如要恢复原有 Agent 操作，请在 Filewise 中断开，再重载 Agent 配置。运行中的命令不会被暂停操作强制终止。

连接命令绑定本机 Python 安装和数据库的绝对路径，移动安装位置后需重新连接。接入配置不含访问令牌，不应作为跨机器共享配置提交。不同 Filewise 数据库不要同时接管同一个目录。候选副本会保留供同一任务继续使用，当前没有自动清理历史/副本功能。

## 命令行关注

```bash
# 第一次：选一个目录和格式；省略 --include 则关注所有未排除文件。
filewise follow /absolute/path/to/files --include '*.xlsx' --include '*.md' --include '*.json'
# 后续重启：沿用保存在目录 .filewise/ 中的设置、基线、凭据与历史。
filewise follow /absolute/path/to/files
```

默认监听 `127.0.0.1:8000`，可通过 `--port` 修改。终端给出网址和工作台访问凭据；把凭据粘贴到页面的“连接”框。不要把服务日志写入关注文件集。配置不建立开机启动，终端进程需保持运行。

工作台“关注设置”默认“两种都启用”，也可选择保存后分析、仅受控修改或暂停。暂停时不接受新的受控任务，已生成的候选仍可审核处理。正在执行的任务不因暂停而终止。所选目录的 `.filewise/` 默认排除并通过内部 `.gitignore` 忽略，其中数据库保留版本字节，令牌文件只允许所有者读写。仅注册明确选择的目录，不扫描父目录。

`--include` 是项目相对路径匹配规则，可重复；`*.xlsx` 可匹配子目录，`**/*.md` 也包含根目录文件。固定排除 `.git`、`.filewise`、`.venv`、`node_modules`、凭据类文件和符号链接。首次 `--spec` 可附加依赖、检查、excludes/includes。后续不静默更改已有范围/契约，扩大范围应显式注册另一个项目；当前网页只切换处理模式，不编辑契约。

## 保存后分析

任意模型或编辑器照常修改原文件。服务每秒扫描，默认内容稳定 1 秒后保存候选：原字节版本 → 位置差分 → 声明依赖的影响路径 → 业务检查 → 人工审核。浏览器每两秒检查新事件，显示“发现文件变更”，不会替换你正在看的历史版本。服务重启后从持久化观察基线继续比较。

这是保存后的观察，不能阻止已经发生的写入，也无法仅凭文件变化识别真实写入者。连续短时修改可能合并为一个事件，保存又恢复到同一内容的短暂状态可能不被记录。删除最后一个文件仍留下删除报告，但空文件集不能发布。扫描/提取失败在关注状态中显示错误，不把失败当作成功或更新基线。

Office 解析、文本/JSON/表格位置差分与已有网关共用实现。数值、公式和约束措辞只形成确定性审核提示；影响路径以声明的依赖为准。未提供业务检查时，PASS 仅证明文件集完整性。

## 受控修改

在另一个终端运行新任务，保持当前目录为 Filewise 仓库（或使用已安装的 filewise）：

```bash
filewise run /absolute/path/to/files -- codex exec --skip-git-repo-check -s workspace-write "修改当前目录文件"
```

Codex 选项来自本机 CLI 帮助；未用云模型跑此测试。其他前台模型 CLI 或本地脚本同样放在 `--` 后面，不经 shell 拼接。例如不调用模型的检查：

```bash
filewise run /absolute/path/to/files -- python3 -c 'from pathlib import Path; Path("notes.md").write_text("new notes\n")'
```

任务收到选定范围内的完整文件副本。`FILEWISE_WORKTREE` 指向临时目录；Filewise 凭据相关环境变量移除。macOS `sandbox-exec` 拒绝子进程及其后代直接读写原目录、数据库及所在私有状态目录。命令需要以前台方式等待修改结束；不要让其他常驻进程代为改文件。任务输出照常转发，结束时 CLI 输出候选 JSON 并沿用子进程退出码。非零退出也可能留下部分修改，仍只成为待审核候选；无变化则不生成修改候选。临时工作副本随后删除。

回到页面点击“有待审核修改”，查看文本行、JSON Pointer 或 Excel 单元格的前后差异。检查 PASS 后人工“审核通过”，再点击“写回原文件并发布”。自动提交者与人工操作员身份不同；`follow` 的人工 `local-owner` 同时具备审核和发布角色，受限 Agent 凭据没有这些权限。提交者不能审核自己的版本。主动点击“检查目录改动”产生的人工候选仍需另一身份审核。

当前受控执行仅支持 macOS；观察与恢复需要 POSIX 文件锁（macOS/Linux）。其他平台明确拒绝未隔离的受控启动。不控制已有模型、独立 MCP 服务、其他终端或网络代理，也没有把任意恶意程序隔离在完整容器里；底层限制只是指定路径的读写边界。模型应使用工作副本相对路径，不能把工作目录改回原目录。

## 写回与恢复

写回前重新验证候选完整性、独立审批、当前证据权限、活动版本 CAS，以及原件是否仍与任务开始时一致。已经变化的原件返回 STALE，保留外部修改，需要以最新原件重新执行任务。失败检查和未审批版本不会写入原件。

每个目标文件用同目录临时文件、fsync 和原子替换落盘，拒绝路径/符号链接穿越并保留已有文件权限；新增文件默认 0600。原字节按候选写回，未进行 Office 文档重排或重算。仅管理文件内容与路径，不版本化扩展属性、Finder 元数据和目录权限。

多文件写回不是文件系统级事务：逐文件替换，SQLite 先记录意图。发生普通错误时恢复已知的旧内容；进程突然退出后，网页显示恢复按钮，或使用：

```bash
filewise recover /absolute/path/to/files RELEASE_ID
```

激活尚未记录则恢复原件、候选回到待写回；激活已经记录则保留新内容并核对状态。恢复遇到既不是旧版本也不是候选的外部修改时拒绝覆盖，需要人工处理。未恢复的写回事务会阻止新的观察和写回。每个项目的 Filewise 操作由进程间文件锁串行化；不锁住其他编辑器，外部程序在最终内容检查与替换之间写入仍存在竞态。因此写回/恢复时应暂停其他工具对同一文件集的修改。

回退发布指针仍只改变知识发布版本，不替换原文件；它与这里显式确认的候选写回是不同操作。

## 程序接入

使用操作员 Bearer token（Agent token 一律不能调用这些接口）：

| 请求 | 用途 |
| --- | --- |
| `GET /api/projects/{id}/watch` | 模式、观察基线、最后事件、错误与 worker 状态 |
| `GET /api/setup` | 当前服务是否开启本机设置功能 |
| `POST /api/setup/project` | 本机设置模式下显式注册 `{ "root": 绝对路径, "name": 名称, "includes": ["**"] }` |
| `GET /api/projects/{id}/connections` | 已安装配置、自检与实际工具事件状态 |
| `GET .../connections/{agent}/preview` | 只显示 Filewise 将增加的配置，不输出原配置中的其他信息 |
| `POST / DELETE .../connections/{agent}` | 本机设置模式下安装/断开项目接入，agent 为 codex、claude、pi |
| `POST .../connections/{agent}/check` | 实际隔离自检；不标记 Agent 已加载 |
| `PUT /api/projects/{id}/watch` | 保存 `{ "enabled": true, "mode": "both", "settle_seconds": 1 }` |
| `GET /api/projects/{id}/snapshots/{release_id}` | 差异、定位、依赖影响、检查与 writeback 状态 |
| `POST .../snapshots/{release_id}/approve` | 独立审核 |
| `POST .../snapshots/{release_id}/apply` | 传 `{ "expected_active": 当前release或null }` 写回并发布 |
| `POST .../snapshots/{release_id}/recover` | 恢复未完成的写回 |

`follow` 创建的项目 ID 为 `workspace`；`start` 按所选路径生成项目 ID。`last_event.kind` 区分 `after_save` 与 `before_write`，后者的 `original_files_changed` 为 false。用 `release_id` 去重，并读取不可变报告，可接自己的后续处理；这里只轮询取报告，没有执行用户自定义 webhook/shell hook。观测只保留最新通知，完整候选历史仍在项目快照列表中。API 不提供远程执行任意模型命令的端点；工具执行发生在本机 Agent 钩子或 `run` 启动的子进程中。

## 可重复验证

```bash
PYTHONPATH=src .venv/bin/python -m unittest discover -s tests -v
PYTHONPATH=src .venv/bin/python examples/check_middleware.py
.venv/bin/python examples/check_connections.py
```

前者包含去抖、重启/暂停、最后文件删除、Excel 检查传播、审批、冲突、失败恢复、激活后中断、符号链接、令牌权限及配置持久化；测试中的工作副本命令不启用 OS 隔离。后者在 macOS 上使用真实 follow 服务、run 命令和继承的 OS 限制：原件及私有状态读写被拒绝 → 副本修改 → 未审批写回拒绝 → 审批写回 → Agent 网关读取新字节。仅使用临时合成数据。

`check_connections.py` 使用已安装包执行生成的 Codex/Claude 钩子命令、真实 Shell 隔离及 Excel 审批写回。可传 `--pi-package /path/to/@earendil-works/pi-coding-agent --node /path/to/node` 加载已安装 Pi 的 TypeScript 扩展加载器、事件运行器和原生 write 工具。Codex/Claude 输入是协议测试事件；没有把它们说成完整客户端或云模型回合的验证。
