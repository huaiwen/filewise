# 数据使用契约与“当时已知”视图

Filewise 将数据口径、质量检查和时间边界纳入版本、任务与验证。它保存观察到的文件状态，检查声明的条件；不会把“文件有历史”当成“回测没有未来信息”。

## 1. 给数据声明口径与检查

保存文件时，在 `meta.data` 中提交契约：

```json
{
  "data": {
    "meaning": {
      "entity_type": "company",
      "identifier_system": "internal_company_id",
      "unit": "CNY",
      "period": "2025-01",
      "calendar": "calendar_month",
      "population": "card_panel",
      "grain": "cardholder_month"
    },
    "allowed_uses": ["panel_spending"],
    "checks": [
      {"id": "rows", "op": "row_count", "minimum": 1},
      {"id": "identity", "op": "unique", "column": "id"},
      {"id": "amount-present", "op": "not_null", "column": "amount"},
      {"id": "amount-range", "op": "range", "column": "amount", "minimum": 0},
      {"id": "panel-growth", "op": "distinct_count_change", "column": "id", "max_change": 0.1}
    ]
  }
}
```

`meaning` 支持 `entity_type/entity_id/identifier_system/measure/unit/currency/period/calendar/grain/population/sampling/coverage`。按声明字符串精确比较，不自动换汇、换单位、映射公司/证券代码或推断财年。

**需要强制执行的规则，应在创建项目时放入 `ProjectSpec.data_contracts`**，格式为 `{"data.csv": <上面的 data 对象>}`。它与项目规格一起不可变，Agent 不能用自己的 metadata 降低规则；不同声明、缺少指定文件及失败检查都会阻止审批。`meta.data` 则是可版本化的文件声明：修改被记录，但不能代替操作员政策。

JSON 使用记录数组；嵌套数组设置 `rows_pointer`，例如 `/records`。CSV 第一行是唯一、非空列名。XLSX 使用 `sheet` 指定工作表序号（默认 1），第一行为列名；内层空白行保留，尾部无内容行不计入数据。公式作为公式文本，不计算、不执行 VBA，不相信缓存值已经更新。

检查读取完整的受限表，而不是 120 字段投影或检索片段：最多 20,000 条记录、512 列、40 条规则；XLSX 另受已有解析器的 200,000 单元格及 20,000 非空片段上限限制。超限或不可解析返回 BLOCKED，不能截断后报 PASS。数值规则拒绝空值、布尔值、NaN/Infinity、公式和非数值文本；不猜测千分符或地区数字格式。

- `not_null`：缺字段、null、空白文本失败。
- `unique`：重复值或缺值失败；JSON 值按类型区分，字符串不隐式去空格。
- `range`：数值满足 `minimum` / `maximum`。
- `row_count`：记录数量满足上下界；空表是否可用由此规则明确规定。
- `distinct_count_change`：与前一已完成快照的不同 ID 数量比较。`max_change=0.1` 表示数量相对变化不超过 10%；无基线、缺 ID 或无法比较明确失败。旧数量为零、新数量非零失败。

数量变化检查固化比较基线；同字节、同契约的重新同步不会把失败的比较改成“和自己比”。它检测数量变化，不自动证明样本代表性，也不把样本内消费增长推广为全市场增长。

`quality` 返回固定版本、规则、真实计数和前 20 个失败记录索引（从 0 开始；JSON 是数组下标，CSV/XLSX 数据行通常为索引 + 2），完整失败数不截断。无质量规则为 NEEDS_REVIEW；失败为 BLOCKED；PASS 仅表示声明规则通过。普通工作保存仍可保留失败数据，`--require-pass` 拒绝保存；两者均不发布。

```bash
filewise agent quality inspection --path data.csv
```

## 2. 按用途编译，而不只是找到相似文字

保存 `requirements.json`：

```json
{
  "data.csv": {
    "expect": {"unit": "CNY", "population": "card_panel", "period": "2025-01"},
    "purpose": "panel_spending",
    "require_quality": true
  }
}
```

```bash
filewise agent quality inspection --requirements requirements.json
filewise agent search inspection "消费支出" --requirements requirements.json
filewise agent compile inspection --goal "核对样本内消费" \
  --path data.csv --requirements requirements.json
filewise agent verify inspection --phase preflight --task-id TASK_ID
```

缺口径、口径不同、用途不在声明中、必需文件不在选中上下文内，均 BLOCKED；要求质量检查却没有质量契约，返回 NEEDS_REVIEW。`require_quality=false` 只是不额外要求一个缺失的质量契约，不能覆盖已有失败检查。文件改变却未刷新内容绑定声明，也不能继续提升为可信数据。

搜索保留相关结果，并附带 `data_guard`；“搜得到”不表示“可用于此任务”。compile 固化 requirements，preflight/postflight 重验。`quality`、`verify` 的 BLOCKED / NEEDS_REVIEW CLI 退出码为 2；创建任务和返回搜索结果本身不代表授权。

## 3. “当时已知”与“现在所见”分开

```bash
filewise agent read inspection data.csv --as-of 2026-03-15T00:00:00Z
filewise agent search inspection "收入" --mode lexical --as-of 2026-03-15T00:00:00Z
filewise agent compile inspection --goal "重看当时的数据" \
  --path data.csv --as-of 2026-03-15T00:00:00Z
```

- `latest + as_of` 选择截止时最后一个**已完成**的项目快照；不选择未来版本，也不回退绕过撤销。
- 显式快照/提交/HEAD/published 与 `as_of` 联用时，先解析该版本，再检查其完成时间。不会自动改成另一个版本。历史生产指针切换并非此选择器的定义。
- `as_of` 必须含时区且不在未来。支持 ls/read/search/quality/resolve/impact/compile/verify/trace；impact 的比较基线也不得越过截止时间。
- 回执由服务端记录在同一个完成事务中。待写回候选、dry-run、失败或尚未补齐完成状态的写入没有可用时间。记录过的时间不被重试改写，检测到时钟倒退会拒绝新增回执。
- `valid_from`、文件中的供应商日期等声明不能倒填服务端完成时间。旧数据库没有回执的历史状态可按显式版本普通读取，但不能充当可证明的 `as_of` 结果；重新同步只从今天建立观察记录。
- 当前 ACL、来源策略和撤销仍然生效。历史任务只读，验证继承任务固化的 cutoff，不能换时间后继续使用同一任务。

例：3 月 1 日 Filewise 完成接收 1 月收入 100；4 月 1 日完成接收更正 80。3 月 15 日的 `as_of` 返回 100；没有截止限制的 latest 返回 80。即使更正声明 `valid_from` 在 1 月，也不会回流到 3 月的历史视图。

这里证明的是 **Filewise 完成观察的整项目状态**：不自动补齐接入前的历史，不认证供应商真实送达时刻，不推断两次同步之间发生了什么，也不实现行级 point-in-time join。服务端时钟、SQLite 和操作员仍属于可信边界；回执哈希不是外部时间公证。

原有 `resolve --valid-time --transaction-time` 继续处理经过审核的双时间修订。它与快照 `as_of` 是不同语义，不允许混用来伪装完整金融回测。

## 4. 固定处理输入，不把依赖路径当成处理证明

文件 metadata 可声明：

```json
{
  "processing": "deterministic",
  "lineage": [
    {
      "version": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "path": "data.csv",
      "role": "data"
    }
  ]
}
```

把示例 version 换成实际已完成快照 ID。`processing` 为 source（默认）/deterministic/model；输入角色为 data/mapping/code/model。只接受本项目具体快照，不接受 latest 等浮动引用。服务器绑定输入 SHA-256、source_id 和完成时间，并递归检查现行权限、撤销和时间界限；输入证据进入输出版本。每文件最多 100 条直接声明、递归最多 1,000 条角色输入，同时受修订最多 100 条去重证据限制；超限拒绝，不省略输入。

数量变化检查也记录服务器选择的 baseline 输入。普通处理输入的已知质量失败沿声明链传播；baseline 是数量比较参照，不把基线自身的历史数量变化失败递归当作当前数据失败。

这是一份内容绑定的**处理声明**，不是执行捕获器。Filewise 不自动运行或截获任意外部脚本，不推断遗漏的映射/模型，也不证明输出确实由声明代码产生。已声明为处理产物却未声明输入，会要求复核。

历史任务遇到声明的模型产物/模型输入、语义模型排名，或 `compile --model-use extractive|generative`，标记 `model_hindsight_unverified`，验证为 NEEDS_REVIEW。普通数据通过数值检查、文件时间早于截止、提示模型“忘记未来”，均不能清除这个风险。未声明使用模型也不是模型训练记忆安全证明。

## 复验

```bash
PYTHONPATH=src .venv/bin/python -m unittest discover -s tests
PYTHONPATH=src .venv/bin/python examples/check_data.py
```

示例只创建临时合成数据，经真实回环 HTTP 和独立 CLI 验证更正时间隔离、口径冲突、样本数量变化、输入版本绑定及模型风险；不会登记业务目录或激活生产版本。
