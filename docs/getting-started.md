# Filewise 使用指南 · Python 原型归档

> 当前已切换 Rust 原生实现。请使用 **[Rust 使用指南](rust.md)**；本页保留此前教程，不用于启动当前 Rust 服务。

**从快速上手开始，完成一次真实的文件读写、版本比较、知识检索和任务验证。**

Filewise 为文件增加版本、声明、来源与检查。你可以在浏览器中查看和审核，也可以让 Agent 通过认证 CLI 直接读写。下面先体验合成演示，再用一个独立练习目录走完整个流程。

## 1. 快速上手

### 1.1 准备环境

需要 **Python 3.11+** 和 **uv**，推荐 Python 3.12。本教程的命令使用 macOS/Linux Shell；完整本机验证环境为 macOS，原生 Agent 受控连接目前也以 macOS 为运行平台。

如果还没有 uv，先按 [uv 安装说明](https://docs.astral.sh/uv/getting-started/installation/) 安装。然后进入你拿到的 Filewise 源码目录：这里应能看到 `pyproject.toml`、`uv.lock`、`src/` 和 `examples/`。

```bash
cd Filewise
uv sync --locked --all-extras --no-editable --python 3.12
```

这会创建 `.venv`，安装服务、文档解析器和可选检索运行库。**此步不下载语义模型权重，也不需要大模型 API Key。** 首次安装依赖需要联网。

> 已经安装过、现在更新了源码？改用 `uv sync --locked --all-extras --no-editable --reinstall-package filewise-engine`，随后重启服务。

### 1.2 启动演示

```bash
uv run --no-sync filewise showcase
```

手动打开 **http://127.0.0.1:8765**。保持终端运行。

你会看到一个“合成演示”项目，包含需求文件、Excel 检验计划和交付资料。按下面顺序操作：

| 操作 | 应该看到什么 |
| --- | --- |
| 查看初始文件 | 需求和检验计划均为 100 kPa，检查通过 |
| 点击「1. 修改需求」 | 需求变成 120 kPa，检验计划仍为 100，检查变为 BLOCKED |
| 打开「变更」「影响路径」 | 看到具体字段/单元格的变化，以及声明的依赖关系 |
| 点击「2. 修复检验计划」 | Excel 更新为 120 kPa，一致性检查恢复 PASS |
| 右上角切换「审核者」，点击「审核通过」 | 当前候选获得独立审核 |
| 切换「发布者」，点击「发布此版本」 | 新版本成为当前生产发布 |
| 点击「验证 Agent 读取」 | 获得固定发布版本的文件及读取回执 |

也可以点「记录一次提交」，给选中版本写一句说明，再到「提交历史」查看。

**完成这一轮，你已经看到了 Filewise 的主线：文件变化 → 定位差异 → 检查影响 → 独立审核 → 发布。**

按 `Ctrl+C` 停止演示；演示使用临时合成资料，停止后删除。若端口被占用，可运行 `uv run --no-sync filewise showcase --port 8766` 并打开对应地址。

### 1.3 想先验证命令行？

在源码目录另开终端：

```bash
uv run --no-sync python examples/check_workspace.py
uv run --no-sync python examples/check_data.py
```

两个脚本会自行创建临时项目、启动本地 HTTP 服务并调用独立 CLI。成功时输出包含 `"PASS"` 的 JSON；结束后关闭自己的服务并清理测试目录。

---

## 阅读路线

- [2. 选择使用方式](#2-选择使用方式)
- [3. 建立第一个自己的项目](#3-建立第一个自己的项目)
- [4. 查看版本、差分和影响](#4-查看版本差分和影响)
- [5. 搜索相关内容](#5-搜索相关内容)
- [6. 编译并验证一个任务](#6-编译并验证一个任务)
- [7. 检查数据是否适合某种用途](#7-检查数据是否适合某种用途)
- [8. 提交、审核、发布与恢复](#8-提交审核发布与恢复)
- [9. 日常运行与程序接入](#9-日常运行与程序接入)
- [10. 常见问题](#10-常见问题)
- [11. 深入阅读](#11-深入阅读)

## 2. 选择使用方式

### 人员：浏览器工作台

停止演示后，可以启动日常工作台：

```bash
uv run --no-sync filewise --db .filewise/filewise.db start
```

默认打开 http://127.0.0.1:8000 并完成本机登录。按引导关注一个资料文件夹；首次练习建议选择测试副本。

| 页面 | 用来做什么 |
| --- | --- |
| 文件 | 查看当前选中版本的文件和来源位置 |
| 变更 | 查看新增、删除、正文和 metadata 变化 |
| 搜索知识 | 按问题查找片段，回到对应文件位置 |
| 影响路径 | 查看声明依赖上的传播关系 |
| 提交历史 | 给版本命名、查看累计改动、提出历史恢复候选 |
| 发布检查 | 查看规则结果，执行独立审核与发布 |

「Agent 连接」用于安装 Codex、Claude Code、Pi 的项目级受控配置：受支持的工具在审核副本内修改，由人员决定何时写回。只使用浏览器或下面的 `filewise agent` 命令，不需要先安装这些连接。

### Agent：认证 CLI

Agent 通过 Filewise HTTP 服务获取文件或提交修改，由 Filewise 完成保存、版本记录与检查。

```text
人员 / Agent
    ↓ 浏览器或认证 CLI
Filewise
    ├─ 原文件与不可变版本
    ├─ metadata、来源与声明依赖
    ├─ 搜索 / 差分 / 影响 / 数据质量
    └─ 任务编译 / 验证 / 审核发布
```

可以先亲自在终端操作这些命令，理解结果后再交给 Agent。**不需要启动大模型，才能使用 Filewise。**

### 先记住三个区别

| 概念 | 含义 |
| --- | --- |
| 工作保存 | 原生 `agent write` 保存关注文件并创建版本，供继续工作 |
| 命名提交 | 给一个完整文件版本附上说明，方便回看；不移动生产指针 |
| 生产发布 | 候选检查通过、独立审核后，由发布者启用 |

普通工作保存允许保留失败状态以便修复；加 `--require-pass` 才要求检查通过后保存。PASS 表示已声明检查通过，生产使用仍走发布流程。

## 3. 建立第一个自己的项目

下面用一个全新的练习目录，不与 `start` 的默认数据库混用。需要两个终端，**都先进入 Filewise 源码目录**。

- **终端 A：操作员。** 创建项目、签发凭据、运行服务。
- **终端 B：客户端。** 模拟 Agent 进行读写和计算。

### 3.1 终端 A：准备三份文件

```bash
TUTORIAL_DIR=$(mktemp -d "$HOME/filewise-tutorial.XXXXXX")
TUTORIAL_DIR=$(cd "$TUTORIAL_DIR" && pwd -P)
mkdir -p "$TUTORIAL_DIR/files"

printf '%s\n' '{"pressure_kpa":100}' > "$TUTORIAL_DIR/files/requirement.json"
printf '%s\n' '交付前请根据需求文件复核压力要求。' > "$TUTORIAL_DIR/files/delivery.md"
printf '%s\n' '发生非计划停机时，先切断设备电源，挂牌锁定，再由维修人员排查故障。' > "$TUTORIAL_DIR/files/machine.md"

export FILEWISE_DB="$TUTORIAL_DIR/state/filewise.db"
printf '本次练习目录：%s\n' "$TUTORIAL_DIR"
```

保留最后打印的目录路径。它包含练习原件与独立状态库，关闭服务后仍保留。

### 3.2 终端 A：注册项目并授权

```bash
uv run --no-sync filewise auth-init --out "$TUTORIAL_DIR/state/tokens.json"
uv run --no-sync filewise project add "$TUTORIAL_DIR/files" \
  --id tutorial --name "第一次使用 Filewise"
uv run --no-sync filewise project sync tutorial
uv run --no-sync filewise auth-agent tutorial \
  --tokens "$TUTORIAL_DIR/state/tokens.json"
```

最后一条命令输出 `FILEWISE_TOKEN`。**只复制这个字段的值**，不要把整份 JSON 或 `tokens.json` 交给 Agent。

现在启动服务：

```bash
uv run --no-sync filewise serve \
  --tokens "$TUTORIAL_DIR/state/tokens.json" --port 8010
```

保持终端 A 运行。这里使用 **8010**，与前面的演示、默认工作台分开。

这次的身份边界是：

- `auth-init` 创建操作员及兼容只读凭据。
- `auth-agent tutorial` 另签发只访问 tutorial 项目的 Agent 凭据。
- 该 Agent 可以读写工作文件，但不能审批或发布。
- 服务启动时加载令牌文件；以后新增/轮换凭据，需要重启服务。

### 3.3 终端 B：读文件并由 Filewise 保存修改

把下面令牌占位文字换成刚才复制的值：

```bash
export FILEWISE_URL=http://127.0.0.1:8010
export FILEWISE_TOKEN='把刚才的FILEWISE_TOKEN值粘贴到这里'
CLIENT_DIR=$(mktemp -d)

uv run --no-sync filewise agent projects
uv run --no-sync filewise agent ls tutorial
uv run --no-sync filewise agent read tutorial requirement.json
```

应看到三份文件，以及 `pressure_kpa` 为 100 的原文。`CLIENT_DIR` 用来保存本教程的客户端请求与响应，和服务器状态目录分开。

保存初始版本，并自动取出具体版本 ID：

```bash
uv run --no-sync filewise agent ls tutorial > "$CLIENT_DIR/before.json"
BASE_VERSION=$(uv run --no-sync python -c \
  'import json,sys; print(json.load(sys.stdin)["version"])' < "$CLIENT_DIR/before.json")
```

提交第一次修改：

```bash
uv run --no-sync filewise agent write tutorial requirement.json \
  --base "$BASE_VERSION" --request-id first-pressure-change \
  --content '{"pressure_kpa":120}' \
  --meta '{"summary":"压力要求","tags":["压力"],"facts":{"pressure_kpa":120}}' \
  -m "将压力要求调整为120 kPa" > "$CLIENT_DIR/saved.json"

NEW_VERSION=$(uv run --no-sync python -c \
  'import json,sys; print(json.load(sys.stdin)["version"])' < "$CLIENT_DIR/saved.json")

uv run --no-sync python -m json.tool "$CLIENT_DIR/saved.json"
uv run --no-sync filewise agent read tutorial requirement.json
```

**预期结果：**

- `saved` 为 `true`，`published` 为 `false`。
- 再读文件得到 120。
- 练习目录中的原件也已经变成 120；这不是只写进了搜索索引。
- 旧版本仍保存着 100。

写操作的几个参数：

| 参数 | 作用 |
| --- | --- |
| `--base` | 指定刚读到的具体版本，防止覆盖别人的新修改 |
| `--request-id` | 同一请求的重试标识；同键同内容不重复保存 |
| `--content` | 提交 UTF-8 文本；二进制/本地文件用 `--file` |
| `--meta` | 声明文件摘要、事实、标签、依赖或数据契约 |
| `-m` | 本次保存说明 |

后续代码块请继续在**同一个终端 B**按顺序执行，保留版本变量和 `CLIENT_DIR`。

## 4. 查看版本、差分和影响

### 4.1 读旧版本，让服务端比较差异

```bash
uv run --no-sync filewise agent read tutorial requirement.json --version "$BASE_VERSION"
uv run --no-sync filewise agent diff tutorial "$BASE_VERSION" "$NEW_VERSION"
uv run --no-sync filewise agent versions tutorial
```

旧版本返回 100；diff 返回文件变化、JSON 字段位置及前后值。Agent 不需要先下载两份完整文件再自行比较。

版本选择器：

| 写法 | 选择什么 |
| --- | --- |
| `latest` | 默认，最近完成保存/同步的工作版本 |
| 具体 version ID | 指定的历史快照 |
| `HEAD` 或 commit ID | 操作员记录的命名提交 |
| `published` | 当前生产发布指针 |

本练习此时尚未发布，也没有命名提交，暂时使用 `latest` 和具体 version ID。

### 4.2 给交付说明声明依赖

```bash
uv run --no-sync filewise agent write tutorial delivery.md \
  --base "$NEW_VERSION" \
  --meta '{"summary":"交付说明","depends_on":["requirement.json"]}' \
  -m "将交付说明关联到压力要求"

uv run --no-sync filewise agent impact tutorial --path requirement.json
```

这次只修改 metadata，正文不变。影响结果应包含 `delivery.md`，因为现在明确声明了：**交付说明依赖需求文件**。

`meta` 是整份声明替换。省略时继承；如果正文改变却没有刷新相关声明，Filewise 会标记 `metadata_current=false`，旧内容绑定事实不再当作新事实使用。

### 4.3 查询“当时已知”的状态

从真正的初始回执提取截止时间，不需要手写一个可能早于接入的日期：

```bash
AS_OF=$(uv run --no-sync python -c \
  'import json,sys; print(json.load(sys.stdin)["temporal"]["availability"]["available_at"])' \
  < "$CLIENT_DIR/before.json")

uv run --no-sync filewise agent read tutorial requirement.json --as-of "$AS_OF"
```

预期仍为 100。Filewise 按服务端完成回执选择截止时最后完成的整项目快照；后来改成 120，不会倒灌到这个历史视图。

例如，3月收到收入100，4月收到更正80：3月的已知视图保留100，现在的工作视图可以是80。文件内声明一个更早的业务日期，不会倒填服务端可用时间。时间查询使用当前权限与撤销状态。

## 5. 搜索相关内容

### 5.1 先用无需模型的检索

终端 B：

```bash
uv run --no-sync filewise agent search tutorial "压力" --mode exact
uv run --no-sync filewise agent search tutorial "切断 电源" --mode lexical
```

返回的每个命中包含文件路径、片段、原文位置、来源与哈希、排序分数及命中通道。上层同时给出具体版本和读取范围。

| 模式 | 适用场景 |
| --- | --- |
| `exact` | 精确字符串、编号、字段名 |
| `lexical` | 中文/英文词汇检索，使用 BM25 |
| `semantic` | 换一种说法查找，需要配置本地模型 |
| `hybrid` | 默认；组合已启用通道并排序 |

可追加 `--path machine.md`、`--tag 压力`、`--version VERSION`、`--limit 5` 等过滤。标签来自版本化 metadata。

### 5.2 可选：启用本地语义检索

在**终端 A**按 `Ctrl+C` 停止教程服务，再执行：

```bash
uv run --no-sync filewise --db "$TUTORIAL_DIR/state/filewise.db" retrieval setup
uv run --no-sync filewise --db "$TUTORIAL_DIR/state/filewise.db" retrieval status
uv run --no-sync filewise serve \
  --tokens "$TUTORIAL_DIR/state/tokens.json" --port 8010
```

首次 setup 下载公共多语言模型，模型权重约 224 MiB；实际耗时取决于网络。之后文档与查询在本机推理，缓存可复用。

终端 B：

```bash
uv run --no-sync filewise agent search tutorial \
  "机器突然不运转该如何处理？" --mode semantic
```

这个问题没有直接照抄文件内容，但可以找到 `machine.md` 的停机处理原文。仍应阅读片段与来源，不把排名当成事实批准。

未配置模型时，`hybrid` 明确使用已启用的通道；显式 `semantic` 会要求先 setup。完整配置见 [相关内容检索](retrieval.md)。

## 6. 编译并验证一个任务

**compile 的作用是准备任务上下文和检查条件。** 它返回相关文件、固定版本、证据、声明依赖、可用操作及验证要求；真正执行任务或生成回答的是你接入的 Agent。

### 6.1 定义一个可以检查的结果

继续在终端 B 执行：

```bash
printf '%s\n' '[{"id":"pressure-result","object_id":"result","field":"pressure_kpa","op":"eq","expected":120}]' \
  > "$CLIENT_DIR/checks.json"

uv run --no-sync filewise agent compile tutorial \
  --path delivery.md --goal "根据需求核对交付压力" \
  --checks "$CLIENT_DIR/checks.json" > "$CLIENT_DIR/task.json"

TASK_ID=$(uv run --no-sync python -c \
  'import json,sys; print(json.load(sys.stdin)["task_id"])' < "$CLIENT_DIR/task.json")

uv run --no-sync python -m json.tool "$CLIENT_DIR/task.json"
```

预期上下文包含交付说明及其依赖的需求文件，结果断言要求 `pressure_kpa=120`。

不指定路径时，也可以让问题先发现相关文件：

```bash
uv run --no-sync filewise agent compile tutorial \
  --goal "切断设备电源后如何处理停机？" --retrieval-mode lexical
```

compile 会保存检索依据，再补齐声明依赖。检索无结果时明确返回缺口，任务预检不放行。

### 6.2 执行前验证

```bash
uv run --no-sync filewise agent verify tutorial \
  --phase preflight --task-id "$TASK_ID"
```

在按本教程完成前序操作的情况下，应得到 PASS。它检查任务身份、版本、证据、上下文完整性和相关约束。

### 6.3 执行后验证

用一个合成回答模拟 Agent 输出：

```bash
printf '%s\n' '{"pressure_kpa":120}' > "$CLIENT_DIR/result.json"
uv run --no-sync filewise agent verify tutorial \
  --phase postflight --task-id "$TASK_ID" --result "$CLIENT_DIR/result.json"
```

预期 PASS。将 `result.json` 的数值改为 100 后再次验证，就会得到 BLOCKED。

| 状态 | 应如何处理 |
| --- | --- |
| `PASS` | 此阶段声明的检查已通过 |
| `BLOCKED` | 先修复具体失败项，例如错误数值、失效证据或不匹配的数据口径 |
| `NEEDS_REVIEW` | 补充可检查的依据或人工复核，例如没有结果断言、模型未来知识风险 |

`quality` / `verify` 返回 BLOCKED 或 NEEDS_REVIEW 时，JSON 仍会输出到 stdout，CLI 退出码为 **2**。HTTP/输入错误退出码为 **1**。

对文件写入任务，postflight 还可以检查输出文件哈希和实际完成的 Filewise 写入。参数及完整契约见 [Agent 使用说明](agent.md)。

## 7. 检查数据是否适合某种用途

现在加入一份样本消费数据，练习两件事：**数值是否合格，口径是否适用。**

### 7.1 保存数据及质量契约

终端 B：

```bash
printf 'id,amount\na,100\nb,80\n' > "$CLIENT_DIR/spending.csv"
printf '%s\n' '{
  "summary": "样本内消费数据",
  "data": {
    "meaning": {"unit":"CNY","population":"card_panel"},
    "allowed_uses": ["panel_spending"],
    "checks": [
      {"id":"rows","op":"row_count","minimum":1},
      {"id":"identity","op":"unique","column":"id"},
      {"id":"amount","op":"range","column":"amount","minimum":0}
    ]
  }
}' > "$CLIENT_DIR/data-meta.json"

DATA_META=$(uv run --no-sync python -c \
  'import pathlib,sys; print(pathlib.Path(sys.argv[1]).read_text())' "$CLIENT_DIR/data-meta.json")
BASE_FOR_DATA=$(uv run --no-sync filewise agent ls tutorial | \
  uv run --no-sync python -c 'import json,sys; print(json.load(sys.stdin)["version"])')

uv run --no-sync filewise agent write tutorial spending.csv \
  --base "$BASE_FOR_DATA" --file "$CLIENT_DIR/spending.csv" \
  --meta "$DATA_META" -m "加入样本消费数据和检查规则"

uv run --no-sync filewise agent quality tutorial --path spending.csv
```

预期质量检查 PASS：至少一条记录、ID唯一、金额非负。规则检查的是受限范围内的完整表，不是搜索命中的几个片段。

支持的质量规则包括缺值、唯一性、数值范围、记录数量和不同 ID 数量变化。JSON 使用记录数组；CSV/XLSX 使用表头列名。公式作为文本，不执行计算或 VBA。

### 7.2 声明这次任务的用途

```bash
printf '%s\n' '{"spending.csv":{"expect":{"unit":"CNY","population":"card_panel"},"purpose":"panel_spending","require_quality":true}}' \
  > "$CLIENT_DIR/requirements.json"

uv run --no-sync filewise agent quality tutorial \
  --path spending.csv --requirements "$CLIENT_DIR/requirements.json"
```

预期 PASS。接着故意要求错误币种和未声明的用途：

```bash
printf '%s\n' '{"spending.csv":{"expect":{"unit":"USD"},"purpose":"total_market_sales"}}' \
  > "$CLIENT_DIR/requirements-bad.json"

uv run --no-sync filewise agent quality tutorial \
  --path spending.csv --requirements "$CLIENT_DIR/requirements-bad.json"
```

预期 **BLOCKED，退出码2**：这份数据声明的是人民币、样本内消费，不能直接拿来满足美元口径的全市场销售任务。

同一份 requirements 也可以传给 compile；之后 verify 会按任务固化的要求重验：

```bash
uv run --no-sync filewise agent compile tutorial \
  --goal "核对样本内消费" --path spending.csv \
  --requirements "$CLIENT_DIR/requirements.json"
```

### 7.3 哪些规则应该由操作员固定？

文件的 `meta.data` 是可版本化声明。必须强制执行、不能由 Agent 降低的规则，放入项目创建规格的 `data_contracts`，由操作员通过 `project add --spec ...` 配置。项目规格在创建时固定；本教程项目没有预设这类强制数据规则。

例如，样本 ID 数量从2变3，可以配置 `distinct_count_change` 检测50%的数量变化。规则能提示数量变化；样本是否代表总体，需要对应的业务和统计设计。

### 7.4 处理输入与模型风险

对于数据处理产物，可以用 metadata 的 `processing` 和 `lineage` 声明它使用了哪些具体版本：

| 输入角色 | 示例 |
| --- | --- |
| `data` | 原始数据文件 |
| `mapping` | 公司标识映射、分类表 |
| `code` | 处理脚本 |
| `model` | 模型文件或版本描述资料 |

服务端绑定声明输入的版本、来源、内容哈希与可用时间，递归检查权限和撤销。这记录的是声明的处理依据，不自动证明任意外部程序确实按该代码运行。

历史任务若涉及声明的模型输入/产物、语义模型排名，或显式设置 `--model-use generative`，会要求复核模型未来知识风险。完整 JSON、时间语义和数量规则见 [数据契约](data.md)。

## 8. 提交、审核、发布与恢复

### 给工作版本命名

在工作台选择版本，点击「记录一次提交」，填写例如“完成压力要求和交付说明更新”。也可以由可信操作员执行：

```bash
uv run --no-sync filewise project commit PROJECT_ID VERSION_ID \
  -m "完成第一轮资料更新" --expected-parent NONE
```

这里的 `PROJECT_ID`、`VERSION_ID` 需替换为实际值；操作员须指向该项目的数据库。`NONE` 只用于首次提交，后续使用最新 commit ID。记录提交与 Git commit 是不同操作，文件不会因此上传 GitHub。

### 从工作状态到生产使用

```text
工作文件保存
    ↓
查看差异、影响与检查
    ↓
独立审核者批准
    ↓
发布者启用
    ↓
生产消费者读取固定发布版本
```

演示里的身份下拉框仅用于演示。真实项目应使用各自的凭据；Agent 工作区凭据不能批准或发布。

原生 `agent write` 已经保存工作原件，所以审核的是这个工作版本能否进入生产使用。受控副本模式则在批准后才写回原件；两条路径的保存时机不同，发布门禁相同。

### 恢复历史文件

1. 在「提交历史」或版本选择器中打开目标版本。
2. 点击「从此版本恢复文件」，生成候选。
3. 核对相对当前原件的新增、修改和删除。
4. 独立审核后写回，再按需要记录新的提交。

恢复生成一个新的版本，保留旧历史。**恢复文件**会改变原件；**仅切换发布指针**不会改变原件。完整命令见 [README 的历史恢复部分](../README.md#用类似-git-的方式管理知识)。

## 9. 日常运行与程序接入

### 状态存在哪里？

| 启动方式 | 状态与生命周期 |
| --- | --- |
| `showcase` | 临时合成项目，正常停止后删除 |
| `start` | 本教程指定源码目录下 `.filewise/filewise.db`；保留关注列表和历史 |
| 第3节的教程服务 | `$TUTORIAL_DIR/state/`，原件在 `$TUTORIAL_DIR/files/`；停止服务不删除 |
| `follow /path/to/files` | 状态在该资料目录的 `.filewise/` |

`latest` 是最后一次完成保存/同步的状态。若你直接用外部编辑器改了文件，使用 `agent sync PROJECT_ID` 或工作台「检查目录改动」重新捕获；配置了保存后观察时则由观察器处理。

### 升级与重启

在源码目录执行：

```bash
uv sync --locked --all-extras --no-editable --reinstall-package filewise-engine
```

然后停止旧服务，以**原来的数据库、令牌路径与端口**重新启动。浏览器刷新页面。单纯关掉浏览器不会关闭服务。

本教程的终端 A 已保留 `FILEWISE_DB` 和 `TUTORIAL_DIR`；换了终端时，要先恢复它们，不能假定新的终端自动知道旧路径。

### REST 调用

已经配置终端 B 的环境变量后，可以直接调用同一个服务：

```bash
curl -sS "$FILEWISE_URL/api/workspaces/tutorial/read" \
  -H "Authorization: Bearer $FILEWISE_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"path":"requirement.json","version":"latest"}'
```

实际输入模式在带认证的 `/api/openapi.json`。程序接入说明见 [REST API](api.md)。

### 备份与访问边界

- 原件、私有 SQLite 状态和凭据分别保管；客户端只获得项目专用令牌。
- 日常备份先停止写入服务再复制状态，或使用 SQLite backup API；不要在 WAL 写入期间只复制主 `.db` 文件。
- 远程访问使用 HTTPS。真实 Agent 应在受控环境获得令牌；提示它“只用 Filewise”并不等于操作系统隔离。
- 本教程由同一位使用者在本机操作两个终端，目的是理解接口，不是在演示账号/进程隔离。

## 10. 常见问题

| 问题 | 处理方式 |
| --- | --- |
| `uv: command not found` | 先安装 uv，重开终端确认 PATH |
| `filewise: command not found` | 在源码目录使用本教程的 `uv run --no-sync filewise ...` |
| 更新源码后没有 quality/as_of 等新参数 | 强制重装非可编辑包，再重启旧服务 |
| 地址打不开或 Gateway unavailable | 检查服务终端是否仍运行、端口是否一致；本教程客户端是8010 |
| 401 / 403 | 检查令牌；使用 auth-agent 生成的项目凭据，而非默认的旧生产只读凭据；新增凭据后重启服务 |
| 提示凭据文件已存在 | auth-init 会拒绝覆盖；已有项目沿用原凭据，新练习使用新的目录 |
| `STALE` / latest version changed | 重新 ls/read/diff，用最新具体版本提交；不要盲目覆盖 |
| 同一个 request-id 冲突 | 同键只用于同一请求的重试；新操作使用新键 |
| 外部改了文件，latest 仍旧 | 显式 sync 或启用保存后观察 |
| semantic 不可用 | 在服务器的同一数据库上执行 retrieval setup，并确认本地模型配置 |
| 没有数据契约时 quality 是 NEEDS_REVIEW | 需要可检查的质量规则；文件能读取并不等于数据质量已验证 |
| as_of 找不到版本 | 截止时可能尚未接入或没有完成回执；先用第4.3节的真实回执时间练习 |
| preflight 提示上下文不完整 | 缩小任务路径，或调整 --max-chars；最高50,000字符 |
| postflight 没有 PASS | 检查是否提供了可核验的结果/引用/输出，以及数值是否满足任务断言 |
| 发布不能点击 | 检查结果、审核身份、独立审批和原件状态；工作保存不会自动发布 |

当前 Filewise 以本地单租户文件项目为单位；支持最多1,000文件、50 MiB原件总量、10 MiB单文件。检索、数据检查和解析各有额外上限，详见专题文档。

- 文本、JSON、CSV，以及可解析的 Office/PDF 有内容定位；扫描 PDF、图片和旧二进制 Office 主要管理原件/声明，没有通用视觉理解。
- 影响分析依据显式依赖；时间查询依据已完成快照。自动实体映射、行级 point-in-time join 和统计偏差校正需要专门能力。
- 正式企业上线还应配置身份生命周期、备份、网络与资源隔离；当前验证范围见 [验证记录](validation.md)。

## 11. 深入阅读

| 文档 | 内容 |
| --- | --- |
| [项目 README](../README.md) | 总体能力、安装与代码入口 |
| [Agent 使用说明](agent.md) | 批量写入、metadata、版本、任务验证和恢复 |
| [相关内容检索](retrieval.md) | 本地模型、通道、排名、缓存与限制 |
| [数据契约](data.md) | 用途、质量规则、as_of、处理输入与模型风险 |
| [接入与中间件](middleware.md) | 原生 Agent 连接、保存后观察、审核副本 |
| [REST API](api.md) | 请求和响应模型、端点、错误语义 |
| [架构与状态语义](architecture.md) | 内核对象与计算/发布流程 |
| [安全边界](security.md) | 权限、凭据、数据、进程与部署 |
| [验证记录](validation.md) | 已执行测试与环境范围 |

**建议第一轮完成到第6节：先亲自读写一个文件，看到版本和差分，再理解任务验证。第7节用于进一步检查数据用途。**
