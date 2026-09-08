# zvec-grep 与 Filewise：定位比较与接入决策

## 结论

实现状态（2026-09-08 续接）：Filewise 已建立独立的来源存储、确定性内核、CLI/REST 和发布运行时；本文件所述 zvec-grep 适配仍未实际接入。基础运行和测试均不依赖 zvec-grep，不创建或刷新其持久索引。

吸收为可选的证据检索层，保持 Filewise 的状态、影响、验证与发布内核独立。优先通过上游公开接口组合，不 fork 全仓、不重写现成检索能力。

如果 Filewise 只定位为「本地文件 + 多模态检索 + Agent/MCP」，二者高度重叠。按照现有项目材料的「状态判定—影响计算—执行验证」定位，zvec-grep 主要覆盖底层的证据发现环节。

## 核验范围

阅读了 README、架构、检索流水线、MCP、路线图，以及公开类型、文件识别、图片提取、变化事件、授权、检索融合等源码。

- 仓库：https://github.com/zvec-ai/zvec-grep
- 固定提交：`6fa85a8e28c0b5a0f651c27f09f0247627c5d5c3`
- 该提交 package.json：`@zvec/zvec-grep` 0.2.1，Node >=22，Apache-2.0。
- 本次为静态源码审查；未安装或运行上游、未复现其性能基准、未接入 Filewise。

## 功能对照

| 维度 | zvec-grep 当前公开实现 | Filewise 的产品契约 |
| --- | --- | --- |
| 核心问题 | 哪些文件/段落最相关 | 哪个版本有效、变化影响什么、知识是否允许发布和使用 |
| 检索 | ripgrep、BM25/FTS、向量、多路 RRF | 可直接复用作候选证据发现 |
| 文档结构 | 代码符号、Markdown 标题、文件/行范围 | 扩展为业务对象、约束、原文证据锚点 |
| 变化 | 文件增删改、索引增量更新、freshness | 业务有效时间/记录时间、批准状态、约束差分和依赖传播 |
| 授权 | 本地端点认证、远程 embedding 外发授权 | 业务主体 ACL、派生证据权限、撤权重检、独立发布审批 |
| Agent 集成 | CLI、MCP、公开 TypeScript API | 人员/系统/Agent 共用同一计算内核和固定 release_id |
| 发布 | 当前检索索引及运行状态 | 不可变知识发布包、回归门禁、批准、激活、撤销、回滚 |

索引 `fresh` 只表示索引新鲜度，不能解释为条款已批准或业务上有效。文件 `ChangeSet` 是 watcher 事件集合，不是业务语义差分。检索 trace 描述召回和融合，不等于业务影响证明。

## 当前能力边界

1. `src/engine/file-type.ts` 明确跳过 PDF、DOC/DOCX、PPT/PPTX、XLS/XLSX 等二进制文档，也跳过常见音视频。Filewise 仍需自行负责这些格式的解析与证据映射。
2. 图片须显式纳入，并使用支持图片的 embedding。图片 extractor 将整图作为 `range.kind=file` 的片段；它本身不进行 OCR、表格恢复、图纸关系或业务约束抽取。
3. PDF/Office 等更广泛原生多模态支持、知识图谱检索和 GUI 出现在 roadmap，不能按已交付能力计算。
4. 当前为预览期代码，公开 CLI/MCP/索引兼容性仍在稳定过程中。集成应固定版本并留合同测试。

## 推荐接入路径

```text
企业文件 / 系统导出
    ↓ Filewise 解析、来源哈希、原文坐标、范围与权限划分
授权的证据投影 / 独立 workspace
    ↓ zvec-grep（可选）：精确 / 词法 / 语义检索
候选证据（不能直接成为批准事实）
    ↓ Filewise：身份、内容哈希、原文坐标、时态与权威验证
resolve → diff → impact → compile → verify
    ↓
知识发布包 → 独立审批 → 原子激活 → 固定版本消费
```

### 第一阶段：独立可选适配

- 先以官方 MCP 或公开 TypeScript `createZvecGrep` API 做合同验证。Python 内核无需迁移；若需要结构化结果，优先核验公开 SDK 的返回契约，而不是解析面向人的 CLI 紧凑文本。
- workspace/root 由 Filewise 配置，不能让客户端指定任意服务器目录。
- 只索引声明范围内、授权可见的证据投影。不同租户/安全域必须隔离；不能把所有资料先传给模型再做结果过滤。
- 固定发布版本的索引映射必须绑定 `release_id`，不能让已发布包读取持续变化的工作目录索引。
- 检索结果映射回 `source_id + source_hash + 原文坐标`；映射失败、哈希变化、来源撤销或权限失效时拒绝进入可信上下文。
- 上游 `freshness` 与 Filewise 的业务有效性作为两个独立字段。
- 默认本地 embedding；远程模型配置和外发授权显式开启。

### 第二阶段：用基准决定是否深化

冻结一个合成/获授权的中文规程变更数据集，对比原始检索与 zvec-grep：证据 Recall@k、原文锚点准确率、过期证据泄漏、撤权泄漏、延迟、安装成本与模型资源占用。检索效果达标后再决定长期使用 zvec-grep，还是直接使用底层 zvec 的 Python 绑定；不提前维护两套后端。

## 许可

Apache-2.0 允许使用、修改和商业分发。分发其代码时保留许可证和相关版权声明，修改文件注明修改，若上游分发含 NOTICE 则保留适用 NOTICE。模型权重与依赖分别核验许可；上游商标不在许可证授权范围内。

## 一句话分工

**zvec-grep 找到证据；Filewise 判定证据何时有效、变化影响什么，以及能否据此发布和行动。**

## 上游依据

- [架构](https://github.com/zvec-ai/zvec-grep/blob/6fa85a8e28c0b5a0f651c27f09f0247627c5d5c3/docs/05-architecture.md)
- [检索与格式支持](https://github.com/zvec-ai/zvec-grep/blob/6fa85a8e28c0b5a0f651c27f09f0247627c5d5c3/docs/04-pipeline.md)
- [MCP](https://github.com/zvec-ai/zvec-grep/blob/6fa85a8e28c0b5a0f651c27f09f0247627c5d5c3/docs/03-mcp.md)
- [路线图](https://github.com/zvec-ai/zvec-grep/blob/6fa85a8e28c0b5a0f651c27f09f0247627c5d5c3/docs/08-roadmap.md)
- [文件格式源码](https://github.com/zvec-ai/zvec-grep/blob/6fa85a8e28c0b5a0f651c27f09f0247627c5d5c3/src/engine/file-type.ts)
- [许可证](https://github.com/zvec-ai/zvec-grep/blob/6fa85a8e28c0b5a0f651c27f09f0247627c5d5c3/LICENSE)
