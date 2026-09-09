# TeamAI 与 Filewise 的关系

结论：**TeamAI 归入 Filewise 的 Agent 接入与团队知识协作层**。它提供团队经验、配置和知识图谱的生产与分发方式；Filewise 管理这些输入对应哪些真实文件、哪个版本、什么证据，以及是否可用于当前任务。

本次静态评估固定于 [Tencent/teamai-cli 提交 24260bd](https://github.com/Tencent/teamai-cli/tree/24260bd5f7039a0dbd8b82577667b78b744a5529)，包版本 0.22.0，MIT 许可。未安装或执行上游代码，没有复制上游实现，也没有上传本地项目资料。[package.json](https://github.com/Tencent/teamai-cli/blob/24260bd5f7039a0dbd8b82577667b78b744a5529/package.json) · [LICENSE](https://github.com/Tencent/teamai-cli/blob/24260bd5f7039a0dbd8b82577667b78b744a5529/LICENSE)

## 归类

| TeamAI 能力 | 在 Filewise 中的位置 | 可以吸收什么 | Filewise 保留的责任 |
| --- | --- | --- | --- |
| skills、rules、agents、hooks、MCP 的团队分发 | Agent 接入与协作配置 | Git 版本、变更评审、按需同步 | 配置变更也是候选知识；同步完成不等于批准使用 |
| learnings、teamwiki、会话经验沉淀 | 候选知识生产 | 将经验保存为可审查文件，标明来源 | 经验须关联真实文件和适用范围；不能自动提升为权威规则 |
| codebase AST / heuristic 抽取和图索引 | 依赖发现、影响分析的输入 | 分开保留关系类型、抽取方法、置信度、证据 | 人工确认关系含义与方向；构建有边界的业务影响图 |
| recall 与关键词、BM25、图排序 | 可选证据检索 | 减少 Agent 寻找资料的成本 | 命中只用于发现；最终引用须通过版本、ACL、来源和撤销检查 |
| session / friction / digest 改进反馈 | 使用后的反馈与复盘 | 用实际任务失败产生下一轮变更提案 | 反馈进入新候选版本，不在后台静默改写已发布知识 |

以上能力归纳来自上游 [README](https://github.com/Tencent/teamai-cli/blob/24260bd5f7039a0dbd8b82577667b78b744a5529/README.md)。右侧 Filewise 分类和组合方式是本项目的架构判断。

## “类似 Git 管理知识”的落点

TeamAI 的协作流程包括修改本地资源、推到分支、合并请求评审，再拉取资源。Filewise 可以沿用这套用户习惯，把提交单位扩展为“文件字节 + 可定位内容 + 依赖 + 检查结果 + 人工批准”。[上游工作流](https://github.com/Tencent/teamai-cli/blob/24260bd5f7039a0dbd8b82577667b78b744a5529/README.md)

```text
真实文件 / 团队知识文件
        ↓ 导入、同步
候选快照 → 位置差分 → 语义审核任务 → 依赖影响 → 业务检查
        ↓ 独立审核与发布
不可变 release_id + 当前发布指针
        ↓ 认证会话 / 每次重新检查
Codex、其他 Agent、人员
        ↓ 使用反馈
下一次知识变更提案
```

这里的 commit 是 Filewise 内容寻址的发布包；status 是当前发布与工作文件状态；diff 包含文本位置和 Excel 单元格；review 是独立身份审核；publish 是带并发比较的指针切换；rollback 保留历史并回退指针，revoke 阻止旧版本再被读取。当前实现没有 Git 分支合并、远程 push/pull 或自动解决知识冲突，因此不宣称提供完整 Git 等价物。

## 不能直接复用为业务权威的部分

1. **图的置信度与新鲜度。** `graph-index.schema.ts` 保留节点 confidence、边 relation/source/evidence/weight，值得采用。该版本健康摘要里的 freshness 以是否存在节点作占位计算，不能代表文件已同步、业务有效期满足或批准仍有效。[图索引源码](https://github.com/Tencent/teamai-cli/blob/24260bd5f7039a0dbd8b82577667b78b744a5529/src/wiki-engine/core/graph-index.schema.ts)
2. **关系端点。** 代码图节点 slug 使用事实类型/名称；部分边以文件路径为端点，启发式解析还使用模糊目标匹配。导入 Filewise 必须明确映射到项目相对路径或业务对象，并保留原始来源；不能把所有边直接视作审核过的业务依赖。[代码图源码](https://github.com/Tencent/teamai-cli/blob/24260bd5f7039a0dbd8b82577667b78b744a5529/src/wiki-engine/code-knowledge/code-graph.ts)
3. **同步与准入。** SessionStart 等 hooks 可以自动同步配置，但“最新”只是分发状态。Filewise 的 Agent 会话固定 release_id，若原文件变化、来源撤权或发布撤销，读取会被拒绝，必须重新完成审核流程。[hooks](https://github.com/Tencent/teamai-cli/blob/24260bd5f7039a0dbd8b82577667b78b744a5529/src/hook-handlers.ts)
4. **Agent 配置。** 上游已有 Codex TOML 的 MCP 配置转换，包括凭据环境变量。可借鉴配置兼容策略；本次用认证 CLI 实现实际文件入口，没有为了相似性额外引入 MCP 服务。[配置转换源码](https://github.com/Tencent/teamai-cli/blob/24260bd5f7039a0dbd8b82577667b78b744a5529/src/resources/mcp-format.ts)

## 本次实际吸收与后续接入条件

已落实到 Filewise 的产品流程：项目文件树与版本历史、完整文件集快照、位置差分和审核提示、显式依赖和检查、独立审核与发布、Agent 专用凭据与固定版本读据。本次评估没有把这些能力说成上游代码集成。

TeamAI 的资源文件可以作为普通文件进入 Filewise 管理；自动图谱导入适配器暂未实现。只有在真实集成需求出现时增加一个小适配器：输出带原始 commit、文件位置、抽取方法和端点映射的候选关系，由编辑/审核者接受后形成新范围契约。未识别端点、弱启发式关系和缺少证据的条目应留在待审核列表。zvec-grep 同样保持为可选检索输入，不能越过文件发布入口。
