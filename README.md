# Filewise

**有证据约束的企业知识计算与发布内核。** 将已审核的业务对象、约束与依赖编译成不可变发布包，经过回归检查和独立审批，再供人员、系统或 Agent 固定版本读取。

本地单租户预览版，Python 3.11+、SQLite、FastAPI；无需模型、向量库、Node 服务或外部账号。全部演示和测试使用合成资料。

## 运行

在本仓库目录执行，需要 Python 3.11+ 和 [uv](https://docs.astral.sh/uv/)。

```bash
uv sync --locked --no-editable
uv run --no-sync filewise --db .filewise/demo.db demo
uv run --no-sync filewise auth-init
uv run --no-sync filewise --db .filewise/demo.db serve --tokens .filewise/tokens.json
```

打开 <http://127.0.0.1:8000>。在本地打开 `.filewise/tokens.json`，选择 `local-reader` 对应的键作为访问令牌。页面不会持久保存令牌。编辑、审批和发布分别使用 `local-editor`、`local-reviewer`、`local-publisher` 的令牌；服务器不接受客户端声明角色。

`demo` 要求数据库中不存在 `demo` 范围，重复运行会拒绝覆盖。它完成以下流程，并输出每个发布 ID：

1. 发布 100 kPa 的合成规则及操作程序。
2. 只将规则改为 120 kPa：影响传递至程序，一致性检查 **BLOCKED**。
3. 将程序同步改为 120 kPa：检查 **PASS**，独立审批并激活。
4. 回滚激活指针至 100 kPa；已固定的新版本仍返回 120 kPa。

回滚不等于撤销。撤销发布包、撤销其修订、撤权、来源失效或超出有效期，会阻止后续固定版本读取。

安装 PDF/Office 解析能力：

```bash
uv sync --locked --all-extras --no-editable
```

基础安装支持 UTF-8 TXT、Markdown、CSV；可选解析支持 PDF 文本、DOCX 段落/表格、XLSX 单元格、PPTX 文本/表格。坐标由解析器生成，原始字节保存在本地 SQLite。没有 OCR、图纸理解、音视频处理或自动业务语义抽取。扫描 PDF 无文本时明确拒绝。

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

Python API 和 CLI 是可信本地操作接口，身份由调用者提供；它们不是抵御本机数据库管理员的认证边界。HTTP 身份只由服务端配置决定。

完整请求模型和端点见带认证的 `/api/openapi.json`，或 [API 文档](docs/api.md)。控制台提供范围、文件导入、修订提交/审核、状态判定、构建、审批、发布及固定版本读取。

## 测试与打包

```bash
uv sync --locked --all-extras --no-editable
uv run --no-sync ruff check src tests
uv run --no-sync ruff format --check src tests
uv run --no-sync python -m unittest discover -s tests -v
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

[架构与状态语义](docs/architecture.md) · [安全边界](docs/security.md) · [REST API](docs/api.md)
