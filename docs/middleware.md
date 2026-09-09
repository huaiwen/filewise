# Filewise 文件中间件

## 最短使用路径

先在 Filewise 仓库安装：`uv sync --locked --all-extras --no-editable`。下文的 `filewise` 在仓库内可替换成 `uv run --no-sync filewise`；安装后的可执行文件也位于 `.venv/bin/filewise`。Filewise 和模型运行环境应放在被关注目录之外。

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

回到页面点击“有待审核修改”，查看文本行、JSON Pointer 或 Excel 单元格的前后差异。检查 PASS 后人工“审核通过”，再点击“写回原文件并发布”。自动提交者与人工操作员身份不同；`follow` 的人工 `local-owner` 同时具备审核和发布角色，受限 Agent 凭据没有这些权限。提交者不能审核自己的版本。主动点击“同步目录”产生的人工候选仍需另一身份审核。

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
| `PUT /api/projects/{id}/watch` | 保存 `{ "enabled": true, "mode": "both", "settle_seconds": 1 }` |
| `GET /api/projects/{id}/snapshots/{release_id}` | 差异、定位、依赖影响、检查与 writeback 状态 |
| `POST .../snapshots/{release_id}/approve` | 独立审核 |
| `POST .../snapshots/{release_id}/apply` | 传 `{ "expected_active": 当前release或null }` 写回并发布 |
| `POST .../snapshots/{release_id}/recover` | 恢复未完成的写回 |

`follow` 创建的项目 ID 为 `workspace`。`last_event.kind` 区分 `after_save` 与 `before_write`，后者的 `original_files_changed` 为 false。用 `release_id` 去重，并读取不可变报告，可接自己的后续处理；这里只轮询取报告，没有执行用户自定义 webhook/shell hook。观测只保留最新通知，完整候选历史仍在项目快照列表中。API 不提供远程执行任意模型命令的端点，受控任务由本机 `run` 显式启动。

## 可重复验证

```bash
PYTHONPATH=src .venv/bin/python -m unittest discover -s tests -v
PYTHONPATH=src .venv/bin/python examples/check_middleware.py
```

前者包含去抖、重启/暂停、最后文件删除、Excel 检查传播、审批、冲突、失败恢复、激活后中断、符号链接、令牌权限及配置持久化；测试中的工作副本命令不启用 OS 隔离。后者在 macOS 上使用真实 follow 服务、run 命令和继承的 OS 限制：原件及私有状态读写被拒绝 → 副本修改 → 未审批写回拒绝 → 审批写回 → Agent 网关读取新字节。仅使用临时合成数据。
