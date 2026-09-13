# Filewise · Rust

**简体中文** · [English](README.en.md)

**供人员和 Agent 使用的文件版本与知识计算层。** Filewise 自己保存文件、metadata 和历史，提供检索、差分、影响分析、任务编译及检查。工作保存与人工审批、生产发布分开。

原生实现：**Rust + SQLite + Axum**，同一个 `filewise` 二进制提供本地管理、HTTP 服务和 Agent CLI。运行不需要 Python、uv、Node 或独立数据库服务。

## 快速上手

需要 Rust **1.86+**、C 编译工具链和 macOS/Linux。第一次构建下载公开 Cargo 依赖；项目文件不外发。

```bash
cd Filewise
cargo build --locked --release
./target/release/filewise --version
```

预期：`filewise 0.2.0`。

### 普通用户：后台监控与界面

```bash
./target/release/filewise start
```

在工作台添加文件夹，设置分析和命名规则。后续放入文件会自动处理，**关闭网页不影响后台监控**；重启服务后保留配置。界面支持中英文、跟随浏览器和手动切换。默认保留原名，可选择建议或自动改名。

使用边界、启动设置与 i18n 说明见 **[文件夹监控 / Folder monitoring](docs/folders.md)**。请先用练习文件夹。Office/PDF 正文解析尚未实现，其他格式仅有基础信息。

### 操作员与 Agent：底层网关

完整教程见 **[Rust 使用指南](docs/rust.md)**。以下是另一套独立练习状态，不是上面的默认后台实例。

用独立的合成目录启动服务：

```bash
FW="$(pwd)/target/release/filewise"
DEMO=$(mktemp -d "$HOME/filewise-rust-demo.XXXXXX")
mkdir -p "$DEMO/files"
printf '%s\n' '{"pressure_kpa":100}' > "$DEMO/files/requirement.json"

"$FW" --db "$DEMO/state/filewise.db" auth-init --out "$DEMO/state/tokens.json"
"$FW" --db "$DEMO/state/filewise.db" project add "$DEMO/files" --id tutorial --name Rust练习
"$FW" --db "$DEMO/state/filewise.db" project sync tutorial
"$FW" --db "$DEMO/state/filewise.db" auth-agent tutorial --tokens "$DEMO/state/tokens.json"
"$FW" --db "$DEMO/state/filewise.db" serve --tokens "$DEMO/state/tokens.json"
```

保留 `auth-agent` 返回的 **项目专用 `FILEWISE_TOKEN`**。在另一终端设置它：

```bash
export FILEWISE_URL=http://127.0.0.1:8000
export FILEWISE_TOKEN='替换为项目专用令牌'
./target/release/filewise agent ls tutorial
./target/release/filewise agent read tutorial requirement.json
```

不要把数据库或完整 `tokens.json` 交给 Agent。服务默认绑定本机；`Ctrl+C` 停止，练习目录和历史保留。

## 让 Filewise 保存文件

把 `ls` 返回的具体 `version` 替换进命令：

```bash
./target/release/filewise agent write tutorial requirement.json \
  --base '替换为64位版本ID' --request-id pressure-001 \
  --content '{"pressure_kpa":120}' \
  --meta '{"summary":"调整压力","facts":{"pressure_kpa":120}}' \
  -m '压力调整到120'

./target/release/filewise agent search tutorial pressure --mode lexical
./target/release/filewise agent versions tutorial
./target/release/filewise agent compile tutorial --path requirement.json --goal '核对压力'
```

保存返回 `saved: true`，原文件随之改变，历史原字节留在 SQLite；`published: false`。过期基线拒绝覆盖，同一请求 ID 与内容可安全重放。

## 本轮 Rust 迁移范围

| 能力 | Rust 状态 |
| --- | --- |
| 本地目录、原字节与 metadata 保存、增删改、历史读取 | 已实现 |
| CAS、幂等、dry-run、require-pass、补偿与中断恢复 | 已实现 |
| 项目令牌、现行来源 ACL/撤销、独立审批与发布 | 已实现 |
| 完成快照 `as_of`、固定输入及递归 lineage | 已实现 |
| 精确检索、中文/英文 FTS5/BM25、片段定位 | 已实现 |
| JSON/文本/CSV 差分、声明依赖影响、任务编译和只读结果校验 | 已实现 |
| JSON/CSV 数据契约、5 类质量规则、口径/用途检查 | 已实现 |
| 本地向量模型、Office/PDF 内容抽取 | 待移植；语义请求明确报错 |
| 持续目录监控、规则元信息、安全命名、本地工作台、中英文 i18n | 已实现；支持范围见目录指南 |
| 原生 Agent 钩子、上传项目、旧完整浏览器功能 | 待移植 |
| 命名提交、审核恢复历史文件、双时间对象引擎、生产只读会话 | 待移植 |
| 写入任务 postflight 来源校验、旧数据库导入 | 待移植；不返回伪造的 PASS |

这是已跑通的 **Rust 原生首版**，不是 Python 全功能版本的等价替换。Rust 使用独立数据库标识，拒绝直接打开旧数据库。旧源码、测试与未提交开发内容保留作迁移参考；不参与 Rust 构建或运行。

## 测试与部署

```bash
cargo fmt --all --check
cargo clippy --locked --all-targets -- -D warnings
cargo test --locked
node rust/ui/i18n.test.cjs
cargo build --locked --release
```

Node 仅用于开发时检查 i18n，不是运行依赖。原生测试使用合成文件，覆盖后台启停/重启和真实 CLI→HTTP→SQLite/原文件，以及版本、权限、数据质量、恢复与 macOS 进程文件隔离。Rust 验证记录和安全边界见 [使用指南](docs/rust.md#8-验证与运行边界)。

`Dockerfile` 与 GitHub CI 已改为 Rust 构建。Cargo 包和容器上下文使用文件白名单，排除旧 Python 源码、业务材料、数据库、令牌和工作记录。

[开始使用 Rust](docs/rust.md) · [Python 原型归档说明](README-python-reference.md)
