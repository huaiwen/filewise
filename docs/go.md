# Filewise Go 使用指南

普通用户请先看 **[后台文件夹监控与中英文界面](folders.md)**；[English overview](../README.en.md)。下文是底层 CLI/Agent 网关教程，保持原有独立练习目录。

## 1. 快速上手

Go 二进制提供操作员 CLI、HTTP 网关、Agent 客户端和后台工作台。SQLite/FTS5 使用纯 Go 驱动；普通构建关闭 CGO。

在当前源码目录构建，使用明确的 Go 程序路径：

```bash
make build
FW="$PWD/build/filewise"
"$FW" --version
```

当前版本为 `filewise 0.3.0-dev (Go)`，尚未公开发行。历史 `v0.2.0` 保持不变，不作为 Go 安装回退。构建与发行状态见[安装指南](install.md)。

### 操作员终端：建立独立练习项目

```bash
DEMO=$(mktemp -d "$HOME/filewise-go-demo.XXXXXX")
mkdir -p "$DEMO/files"
printf '%s\n' '{"pressure_kpa":100}' > "$DEMO/files/requirement.json"
printf '%s\n' '交付前核对压力要求。' > "$DEMO/files/delivery.md"
printf '%s\n' '机器故障：先断电，再检查连接。' > "$DEMO/files/machine.md"

printf '%s\n' '{"dependencies":{"delivery.md":["requirement.json"]},"checks":[{"id":"positive-pressure","file":"requirement.json","pointer":"/pressure_kpa","op":"gte","expected":0}]}' > "$DEMO/spec.json"

"$FW" --db "$DEMO/state/filewise.db" auth-init --out "$DEMO/state/tokens.json"
"$FW" --db "$DEMO/state/filewise.db" project add "$DEMO/files" \
  --id tutorial --name Go练习 --spec "$DEMO/spec.json"
"$FW" --db "$DEMO/state/filewise.db" project sync tutorial
"$FW" --db "$DEMO/state/filewise.db" auth-agent tutorial --tokens "$DEMO/state/tokens.json"
```

`sync` 返回初始具体版本和 `verification.decision: PASS`。这里的声明检查是压力不小于零。

`auth-agent` 返回 `FILEWISE_TOKEN` 和身份信息。**只把这一条项目凭据给 Agent**，保留状态目录和完整凭据文件在操作员环境。

```bash
printf '二进制：%s\n练习目录：%s\n' "$FW" "$DEMO"
"$FW" --db "$DEMO/state/filewise.db" serve --tokens "$DEMO/state/tokens.json"
```

保持这个终端运行。默认地址为 `http://127.0.0.1:8000`；`/health` 返回 `{"status":"ok","runtime":"go"}`。此底层 `serve` 的根地址提供运行信息；普通用户的工作台请用 `filewise start`（独立状态），或在仅绑定 `127.0.0.1` 的网关显式开启 `--local-ui`。

### Agent 终端：只有 HTTP 凭据

新开终端，使用同一个已安装程序，填入刚才返回的项目令牌：

```bash
FW='/absolute/path/to/filewise/build/filewise'
export FILEWISE_URL=http://127.0.0.1:8000
read -r -s -p 'Project token: ' FILEWISE_TOKEN
printf '\n'
export FILEWISE_TOKEN

"$FW" agent projects
"$FW" agent ls tutorial
"$FW" agent read tutorial requirement.json
```

预期读到压力 100，响应带 `version`、`source_id`、`sha256`、文本片段和回执。Agent 客户端只走网关，不打开本地数据库。

## 2. 保存、历史与差分

从 `ls` 复制具体版本，不使用 `latest` 作为写入基线：

```bash
BASE='替换为ls返回的64位version'
"$FW" agent write tutorial requirement.json --base "$BASE" \
  --request-id pressure-001 --content '{"pressure_kpa":120}' \
  --meta '{"summary":"调整压力","facts":{"pressure_kpa":120}}' \
  -m '压力调整到120'
```

成功返回 `saved: true`、`published: false` 和新版本。原文件已由 Filewise 修改；原字节、metadata、作者、消息与完成时间存入 SQLite。

```bash
"$FW" agent read tutorial requirement.json
"$FW" agent read tutorial requirement.json --version "$BASE"
"$FW" agent diff tutorial "$BASE" latest
"$FW" agent versions tutorial
```

最新值为 120，历史值仍为 100，差分定位 `/pressure_kpa`。

- `--file 路径` 或 `--file -` 保存原字节；读取时 `--output 新路径` 验证 SHA-256 后落盘，拒绝覆盖已有文件。
- metadata-only 写入使用 `--meta`，不传 `--content/--file`。
- 批量写入使用 `--request request.json`；此选项不能与单文件参数混用。
- 同一身份、项目和 `request_id` 重放相同内容返回原操作；变更内容会冲突。
- `--dry-run` 只留预览候选；`--require-pass` 要求检查 PASS 才写原件。预览与正式保存用不同请求 ID；重试同次操作保留原 ID 与完整载荷。普通保存可保留失败工作供修复。
- 自动推导的变化含义保存在 `meta.semantic_change`，与人工维护的 `metadata` 分开；前后哈希、原文、位置及复核状态一起保存。
- 漏传 metadata 会继承原声明。内容变化后，继承的内容绑定声明标记为过期，旧 facts 不作为有效声明通过检查。

批量请求结构如下，基线需要替换为当前具体版本：

```json
{
  "base_version": "替换为64位版本ID",
  "request_id": "batch-001",
  "message": "补充交付说明",
  "changes": {
    "delivery.md": {
      "text": "交付前按120 kPa核对。",
      "meta": {"depends_on": ["requirement.json"]}
    },
    "notes/new.md": {"text": "新增说明。"}
  },
  "require_pass": true
}
```

删除用 `{"delete":true}`；文本、Base64、删除三选一，metadata-only 需要已有文件。

### 文档正文与位置

PDF、DOCX、XLSX、PPTX 的原字节仍按文件保存；读取时返回 `extraction`（格式、引擎、范围、状态、说明）、`fragments` 和提取的 `text`。`--output` 导出原件，不是转出的文本。

| 格式 | 提取内容 | 位置示例 |
|---|---|---|
| PDF | 可解码的文字层，按物理页提取 | `page:2/line:3` |
| DOCX | 段落、表格、相关页眉/页脚/脚注/尾注；不包含删除修订文字 | `word/document.xml/table:1/row:1/cell:2/paragraph:3` |
| XLSX | 工作簿顺序、工作表名、稀疏单元格、原始数值与公式缓存 | `sheet:1/cell:B2` |
| PPTX | 演示顺序中的幻灯片文字、表格和讲者备注 | `slide:2/paragraph:1`、`slide:2/notes/paragraph:1` |

把练习文档 `report.pdf` 放入已登记目录后：

```bash
"$FW" agent sync tutorial
"$FW" agent read tutorial report.pdf
"$FW" agent read tutorial report.pdf --output ./report-copy.pdf
```

同一提取链路供正文读取、关键词检索、定位差分、引用锚点、任务上下文和后台分析使用。位置属于具体文件版本；它们不是跨版本不变的段落 ID。XLSX 读取另返回 `sheets` 的单元格类型、位置、数值和公式信息，数字以十进制字符串保存，避免 JSON 客户端将大整数舍入。

`extraction.status` 为 `text`、`partial`、`no_text`、`unsupported` 或 `failed`。`text` 表示所声明文本范围已提取，不是版式/OCR 完整性证明。图片、图表、嵌入对象、幻灯片继承版式、无法解码的字符、无字/失败 PDF 页和未计算公式会留下覆盖说明；部分或缺失提取进入任务 `frontier`，preflight 不会给完整性 PASS。无文字不能单凭解析区分空白页和扫描件。加密文件需另存为未加密副本；不接收密码，不执行宏、公式或外部关系。

内置 Go 解析器不需要 Office、Python 或转换工具。入口在处理请求前配置同一可执行文件的资源隔离子进程；内部包单元测试可直接调用有界解析器。原件采集独立于解析，损坏文档仍可保留和导出。

## 3. 检索、影响与任务

```bash
"$FW" agent search tutorial '机器故障' --mode lexical
"$FW" agent impact tutorial --path requirement.json
"$FW" agent compile tutorial --path delivery.md --direction reverse --goal '核对交付压力'
```

检索返回路径、位置、片段、内容哈希与排名依据。语料只包含当前身份可读的选定项目版本；FTS5 使用英文词干和中文二元词。`exact` 为子串匹配，`lexical` 为 BM25，当前 `hybrid` 为二者的 RRF 组合，并标记未配置向量推理。`semantic` 请求返回 503。

影响分析沿声明依赖遍历。任务编译固定版本、上下文、依赖、缺失边界和允许工具；没有显式路径时按目标检索。没有命中或上下文超限，preflight 阻断。

### 用可检查的输出条件验证结果

在 Agent 终端建立临时请求文件。这些文件无需放入受管理项目：

```bash
CLIENT=$(mktemp -d)
printf '%s\n' '[{"id":"pressure","object_id":"result","field":"pressure_kpa","op":"eq","expected":120}]' > "$CLIENT/checks.json"
printf '%s\n' '{"pressure_kpa":120}' > "$CLIENT/result.json"

"$FW" agent compile tutorial --path delivery.md --direction reverse \
  --goal '核对交付压力' --checks "$CLIENT/checks.json"
```

复制返回的 `task_id`：

```bash
TASK='替换为task_id'
"$FW" agent verify tutorial --phase preflight --task-id "$TASK"
"$FW" agent verify tutorial --phase postflight --task-id "$TASK" --result "$CLIENT/result.json"
```

两步预期 PASS。把结果改为 100，postflight 应返回 BLOCKED、退出码 2。没有可检查输出的 postflight 为 NEEDS_REVIEW。断言由任务提交者声明，PASS 只证明这些条件得到满足；输出检查不替代生产审批。

Go 当前校验只读结果、选定输出文件哈希和引用锚点。**写入操作的 postflight 仍返回 501**，避免把文件已保存误判为写入任务来源已验证。

## 4. 数据质量与用途

JSON 记录数组、CSV 和 XLSX 工作表支持 `not_null`、`unique`、`range`、`row_count`、`distinct_count_change`。以下内容可放在文件 `meta.data` 中；操作员强制政策放在创建项目的 `spec.data_contracts[相对路径]` 中。

```json
{
  "meaning": {"unit": "CNY", "population": "card_panel"},
  "allowed_uses": ["panel_spending"],
  "checks": [
    {"id": "rows", "op": "row_count", "minimum": 1},
    {"id": "identity", "op": "unique", "column": "id"},
    {"id": "amount", "op": "range", "column": "amount", "minimum": 0}
  ]
}
```

用途请求文件示例：

```json
{
  "spending.csv": {
    "expect": {"unit": "CNY", "population": "card_panel"},
    "purpose": "panel_spending",
    "require_quality": true
  }
}
```

在项目中保存相应数据与契约后执行：

```bash
"$FW" agent quality tutorial --path spending.csv --requirements requirements.json
```

口径按声明字符串精确比较。缺失口径、用途不符、已知质量失败或强制文件被删，均阻断；无质量规则要求复核。Agent 的声明不能降低操作员固定政策。检查读取完整受限表，报告完整失败数及前 20 个失败记录索引，不用检索前几行代替全表检查。

范围检查保留十进制值和边界的精度。JSON 重复键、溢出为非有限浮点量级的数值、CSV 重复表头及不齐行被拒绝；空值、布尔值和公式不能通过数值检查。JSON 嵌套记录用 RFC6901 `rows_pointer`。

XLSX 用契约中的 `sheet` 选择第几个工作表（从 1 开始，默认 1）。第一非空行是表头，要求从 A 列起连续、唯一、非空的文字列名；之后直到最后有值行是记录，空行/稀疏格按缺值处理，不把合并格自动填充为数据。`record_indices` 是零起始记录序号，不是 Excel 行号。公式及数组/共享公式范围的缓存、错误单元格按缺值检查；只检查行数不等于验证了公式。日期保留原序列与 1900/1904 日期系统或显式 ISO 字面量，不擅自解释 Excel 的序列 60，也不作为普通金额通过数值范围检查。

数量变化检查比较固定的已完成基线。同字节、同契约重扫保留原比较基线，不能用“与自身比较”消除失败；首次没有基线时明确失败。先建立观察基线，再声明数量变化规则。

## 5. 当时已知与处理输入

`versions` 和读响应提供服务端 `available_at`。把实际的完成时间作为截止值：

```bash
CUTOFF='替换为某个快照的available_at'
"$FW" agent read tutorial requirement.json --as-of "$CUTOFF"
"$FW" agent compile tutorial --path requirement.json \
  --goal '核对当时收到的压力要求' --as-of "$CUTOFF" --model-use generative
```

`latest + as_of` 选择当时最后一个已完成项目快照。显式版本与 `published` 必须满足截止时间；撤销的版本不会被悄悄替换。候选和未完成写入没有完成回执。历史任务不允许写入；验证继承任务的 cutoff，模型使用和模型输入要求复核。

处理产物可声明具体输入：

```json
{
  "processing": "deterministic",
  "lineage": [
    {"version": "替换为已完成的64位版本ID", "path": "spending.csv", "role": "data"}
  ]
}
```

服务器绑定输入哈希、source_id、完成时间，并递归重验权限、撤销、有效性和已知质量问题。角色为 data/mapping/code/model；数量比较的 baseline 由服务器记录。已声明处理、但没有输入的产物及其下游要求复核。

这描述完成快照和处理声明。供应商真实送达时间、行级 point-in-time join、外部代码执行捕获、模型训练记忆和统计代表性不在这些检查的证明范围内。

## 6. 审核、发布与恢复

这些是操作员命令，使用与服务相同的数据库。先从 `project status` 确认当前版本和活动发布：

```bash
"$FW" --db "$DEMO/state/filewise.db" project status tutorial
VERSION='替换为要审批的已完成版本'

"$FW" --db "$DEMO/state/filewise.db" --actor local-reviewer --roles reviewer \
  project review tutorial "$VERSION"
"$FW" --db "$DEMO/state/filewise.db" --actor local-publisher --roles publisher \
  project publish tutorial "$VERSION"
```

审核身份必须不同于写入作者，且声明检查 PASS。发布再次检查来源、有效性、输入和原件一致性。首次发布可省略 `--expected-active`；已有活动发布时必须传当前活动版本，防止覆盖并发变化。

本地 CLI 是持有私有数据库的可信管理接口。通过 HTTP 操作时，身份和角色由服务端令牌固定；Agent 不能自报 reviewer/publisher。

当普通编辑器在原目录保存后，执行 `project sync tutorial` 或 `agent sync tutorial` 捕获变化；`latest` 不会暗中重扫磁盘。

失败写回优先补偿已经改动的文件。进程中断留下 `applying/recovery_required` 时，新的保存和同步暂停；原写入身份执行：

```bash
"$FW" agent recover tutorial --version '替换为中断写入的版本ID'
```

恢复到基线后，使用相同请求 ID 与内容重试。发现不同内容的外部修改时保留原件并报告冲突。这里的 recover 是**中断写入恢复**，不是命名提交的历史还原；后者待移植。

停止服务后备份整个私有状态目录和项目原目录，连同 SQLite 的 WAL/SHM 文件一起保留。重启时复用原 `--db` 和 `--tokens` 路径；重新发凭据后重启服务加载新令牌。

## 7. REST 入口

业务请求使用 `Authorization: Bearer TOKEN`；JSON 拒绝重复键和未知字段。接口模型见 `internal/workspace/model.go`；客户端不直接打开数据库。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/health` | 原生运行状态 |
| GET | `/api/me`、`/api/projects` | 当前身份、可见项目 |
| GET | `/api/workspaces/{project}/versions` | 快照与活动发布 |
| POST | `/api/workspaces/{project}/ls`、`read`、`search` | 版本文件与检索 |
| POST | 同上，`write`、`sync`、`recover` | 工作保存与恢复 |
| POST | 同上，`diff`、`impact`、`resolve`、`compile`、`verify`、`quality`、`trace` | 计算与回执 |
| POST | `/api/projects/{project}/versions/{version}/approve`、`publish`、`revoke` | 操作员审批/发布/撤销 |
| PUT | `/api/projects/{project}/sources/{source}/policy` | 操作员设置来源 ACL 与撤销 |

`read` 的最小请求为 `{"path":"requirement.json"}`；默认 `version=latest`。`sync` 使用空对象。`publish` 请求为 `{"expected_active":null}` 或指定当前活动版本。来源政策为 `{"acl":["reader","editor","reviewer","publisher"],"revoked":false}`。

CLI 成功输出 JSON；输入或 HTTP 错误退出 1。`quality/verify` 的 BLOCKED、NEEDS_REVIEW，以及被检查拒绝的候选保存退出 2。普通保存即使留下失败工作也可退出 0，需要读取返回的 verification。

## 8. 验证与运行边界

原生测试：

```bash
make check
make build
FILEWISE_TEST_BINARY="$PWD/build/filewise" npm test
bash tests/install.sh "$PWD/build/filewise"
```

Go 测试覆盖版本/CAS/幂等、权限与撤销、完整表格质量、lineage、审计、检索和任务、独立发布、补偿恢复、监控规则/命名/撤销/重放及真实 CLI/HTTP 生命周期。完整对齐清单见 [Go 迁移记录](go-migration.md)。

工作台沿用原有 165 项双语资源，检查键/占位符、回退、复数与 Intl 格式。CLI/API 标识和原始诊断保持英文；真实 Ollama 模型质量、目录选择弹窗、实际退出/登录以及最低系统版本需要分别验证。

文档测试覆盖 PDF 页/加密/树完整性、Office 段落/表格/备注/顺序、精确数值和公式/日期、恶意 ZIP/XML、共享检索/差分/任务/引用撤销，以及原件保留和坏文件隔离。worker 测试覆盖输入/输出/超时、RSS 采样终止、格式校验、环境清理和进程回收。Go 与旧版分配器的资源策略不同，见下文。

原生发行包只包含 Go 可执行文件；工作台和第三方许可文本已嵌入，可用 `--licenses` 查看。npm 仅分发安装器与 Skill。原生进程测试使用临时 HOME、合成文件及受控环境；不会自动为 Agent 建立操作系统沙箱。

主要上限与边界：

- 默认排除工具/状态目录、凭据、私钥和 SQLite 文件；敏感文件类型不因更改大小写而绕过默认排除。
- 每项目 1,000 文件、每文件 10 MiB、总字节 50 MiB。文本抽取最多 200 万字符、20,000 个非空行片段；完整表格检查另有行列上限。超限明确拒绝。
- 文档提取最多 200 万字符、20,000 片段；PDF/PPTX 最多 500 页/幻灯片；PDF 声明对象数最多 100,000。Office ZIP 最多 4,096 部件、展开总量 ≤32 MiB、单 XML ≤8 MiB；整包合计 200,000 个元素及属性、128 层，禁止 DTD、路径越界、重复成员和加密 ZIP。
- XLSX 最多 100 工作表、全工作簿 20,000 个声明单元格；位置不超过第 20,001 行和第 512 列，公式范围最多 1,000 个/表。不按 `dimension` 创建巨型矩阵。
- 每次解析子进程限 20 秒墙钟、15 秒 CPU、24 MiB 返回值，并禁止 core dump。Go 软内存目标 256 MiB；父进程每 25 ms 采样 RSS，超过 384 MiB 时终止并回收；Linux 另设 384 MiB RLIMIT_DATA。这不是硬 RSS 上限或文件/网络权限沙箱。按格式和原件哈希缓存最多 16 个结果、合计 8 MiB 序列化内容，不缓存失败。
- 数据检查最多 20,000 记录、512 列、40 规则，仍受文件/抽取上限约束。十进制文本最多 128 字符、指数绝对值最多 1,024；JSON 数值另要求有限的通用浮点量级。
- 检索最多 20,000 个 480 字符块；任务上下文最多 50,000 字符，超限不报告证据完整。
- 一库一个跨进程锁，Filewise 操作串行；策略更新在取得同一锁后生效。每文件临时写入、哈希检查、fsync 与 rename，不是全目录原子事务。其他编辑器不遵守此锁，写回时应暂停它们；严格排他需要操作系统权限隔离。
- 崩溃恢复按前后字节判断文件状态；无法区分内容完全相同的外部写入。需要排他文件归属的场景应同时限制其他进程写目录。
- 审计中的 task/model/tool 是客户端声明，actor 来自固定身份。
- 数据库和凭据要求私有普通文件；使用其他用户不可写的专用状态目录。数据库管理员、操作员环境和服务端时钟属于可信边界，审计哈希不是外部签名公证。
- CLI 直接连接显式配置的网关，禁用环境代理和 HTTP 跳转；无效代理环境下的本地调用已测试。
- 默认仅本机 HTTP。远程 Agent 客户端要求 HTTPS；服务端跨机部署由 TLS 反向代理和网络访问控制提供保护。

当前 Go 构建与测试的证据和未验证范围集中记录在 [Go 迁移记录](go-migration.md)。历史 `v0.2.0` 的 CI 和二进制保留原样，不能代替当前 Go 验证。Go race 检查需要匹配的 C SDK；普通 CGO0 构建不需要 C 编译器。

## 9. 迁移清单

项目概览见 [README](../README.md)；本指南记录原生 CLI 的使用方式与运行边界。

后续能力：OCR、旧二进制 Office 与本地语义模型、原有浏览器及 Agent 钩子、命名提交与审核历史还原、对象级双时间解析、生产只读会话、写入任务 postflight 和经过验证的旧库导入。当前 `resolve` 选择快照文件，并非旧的对象级双时间引擎；文本与文档按提取位置比较，JSON 对象递归到 Pointer、数组作为整体比较。

底层 CLI 默认数据库为 `.filewise-go/filewise.db`，后台默认目录见[文件夹指南](folders.md)。Go 状态使用 `filewise-go-v1`；拒绝直接打开旧数据库，不隐式转换、不重写历史哈希、不回填时间。Rust 源码和原发行物保留在不可变的历史标签中；Python 仅保留为参考。
