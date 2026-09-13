# 在 Filewise 中找到相关内容

Filewise 同时负责**找到知识**和**管理知识版本与使用条件**。搜索直接使用已保存的原文片段和 metadata，结果进入同一套 read / compile / verify 流程。

## 直接搜索

```bash
filewise agent search PROJECT "设备交付前如何核对压力？"
filewise agent search PROJECT "pressure limit" --mode lexical
filewise agent search PROJECT "pressure_kpa" --mode exact
filewise agent search PROJECT "机器突然不运转该如何处理？" --mode semantic
filewise agent search PROJECT "质量要求" --tag quality --limit 10
filewise agent search PROJECT "质量要求" --path procedure.md --version HISTORICAL_VERSION
```

工作台的 **搜索知识** 页使用同一服务，可选择混合、关键词、精确或语义检索，点击结果回到选中版本的原文件及位置。侧栏“筛选文件”仍只筛文件名。

### 检索通道

| 模式 | 机制 |
| --- | --- |
| exact | Unicode 规范化后的大小写不敏感匹配，覆盖内容与文件名 |
| lexical | SQLite FTS5 / BM25；英文词干、代码标识符拆词、中文相邻双字词元 |
| semantic | 本机 FastEmbed / ONNX 多语言模型，真实向量与余弦相似度 |
| hybrid（默认） | 精确、BM25、已配置的语义通道通过 RRF 融合，精确通道权重2，其他为1 |

首次未配置模型时，hybrid 明确返回 exact + bm25 和 `semantic=disabled`；semantic 模式返回503并要求配置。已配置模型损坏或缺少依赖时，hybrid/semantic 都报错，不悄悄变成关键词搜索；可以显式选择 lexical。

## 配置真实本地语义模型

在服务端安装 retrieval extra，并使用与服务相同的数据库执行一次：

```bash
uv sync --locked --all-extras --no-editable
filewise --db .filewise/filewise.db retrieval setup
filewise --db .filewise/filewise.db retrieval status
```

默认模型为 `sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2` 的 Qdrant 量化 ONNX 版本，384维、约50种语言、Apache-2.0，模型权重约224 MiB（另含 tokenizer/config）。它支持中英文语义与跨语言查询。可通过 `--model BAAI/bge-small-zh-v1.5` 选择中文模型；其他 FastEmbed 支持模型也可配置，许可和维度随配置记录。

`setup --cache-dir /absolute/model-cache` 可复用模型下载缓存。**只有明确执行 setup 才下载公共模型文件。** 配置固定本地模型目录、全部文件 SHA-256、模型名、维度、FastEmbed/ONNX Runtime 版本。服务查询只加载该目录，禁止模型自动下载；文档和问题均在本机计算，不上传远程 embedding 服务。依赖升级后重新 setup，避免混用不同运行时的向量缓存。

模型配置写入同一私有 SQLite，下一次查询即生效。`retrieval disable` 关闭语义通道，不删除来源、历史或已编译任务。原生 Agent HTTP 令牌不能操作模型配置；这三个命令属于可信本地操作员接口。

## 返回内容与证据

每个 hit 包含：

- `path / source_id / sha256 / revision_id / chunk_id`，响应顶层有具体 `version`。
- `text / locator / parts`：原始片段、原文坐标、片段内起始偏移。
- `evidence`：可回查的 source_id + locator + quote。
- `score / matches`：RRF 分数和各通道排名、原始分数。分数是相关性，不是正确率或批准概率。
- `kind`：content、metadata 或 filename。

默认每个文件最多3条，合并重叠窗口，避免一个长文件占满结果。可用 `--max-per-file` 调整；总返回1–100条。`--min-similarity` 调整语义余弦门槛，默认0.45，应按具体模型/资料校准。

metadata 搜索覆盖 summary、tags、entities、owner、facts。只有与当前版本内容哈希绑定的声明参与召回；历史版本仍使用其自己的声明。metadata 命中以不可变版本/修订和文件哈希为依据，明确标记为声明，不伪造正文引用。文件标签过滤按该版本的 tags 执行。

JSON、文本、代码和已经提取文本的 PDF/DOCX/XLSX/PPTX/CSV 都可搜索。图片、扫描 PDF 和无法提取正文的二进制可按文件名及显式 metadata 查找；没有加入 OCR 或图像理解。

## 搜索驱动 compile

不必先知道文件名：

```bash
filewise agent compile PROJECT --goal "机器突然不运转该如何处理？" \
  --retrieval-mode semantic --retrieval-limit 3
filewise agent verify PROJECT --task-id TASK_ID --phase preflight
```

未提供 `--path` 或 `--base-version` 时，compile 使用 goal 的前200字符检索，再沿相关文件的声明依赖构建任务上下文。`--query` 可单独提供检索问题；与 `--path` 同时提供时只在这些文件中发现证据。

- `discovery` 保存问题、版本、命中、分数和模型指纹；命中文件使用检索片段，依赖文件补充上下文。
- `context[].selection` 区分 retrieved_evidence 与 dependency_file。检索片段不是完整文件，Agent 可继续调用 read。
- 没找到内容时返回 `no_relevant_evidence` frontier，preflight BLOCKED，不自动塞入全部文件。
- 原有显式路径/基线编译保持原语义；检索不替代声明依赖、审批或输出验证。结果少于请求数量不代表整个业务范围已穷尽。

## 版本、安全和资源边界

先验证项目身份、具体版本和当前证据权限，再构建检索语料；推理结束后再次检查撤销与权限。历史查询不会混入最新文件，删除只影响新版本，旧 Agent 发布会话继续执行原有发布/原件漂移检查。

BM25 使用本次授权版本和路径/标签过滤后的独立内存 FTS5 语料，避免其他项目、历史内容影响词频统计。无需刷新一个持续变化的全局文件索引；write/sync 后查询新版本即使用新内容。

向量持久化在原私有 SQLite 的派生缓存中，按模型指纹与输入文本哈希复用；相同内容跨版本不重复编码，变更只编码新输入。缓存有校验和，损坏可从模型与来源重算；`search --rebuild` 强制重算本次语料向量。模型文件、向量缓存均不进入 Git 或包。

当前每次最多20,000个片段，超出返回413，使用 path/tag 缩小范围。片段最多480字符，长原文保留重叠和坐标，模型仍有自身 token 上限。向量使用 NumPy 的精确余弦扫描，适合当前有界本地项目；不是 ANN 大规模索引。首次语义检索需计算该语料的向量，CLI search/compile 超时为300秒，后续复用缓存。CPU 推理串行，避免并发加载/编码占满本机。

## 可重复检查

```bash
# 关键词、版本、安全和真实 HTTP/CLI；无需下载模型
uv run --no-sync python -m unittest discover -s tests -p test_retrieval.py -v
uv run --no-sync python examples/check_retrieval.py
# 明确下载/复用公共模型，检查实际中文改写和中英跨语言召回
uv run --no-sync python examples/check_retrieval.py --semantic --model-cache /absolute/model-cache
```

语义检查使用合成机器故障、消防、休假和发货文档，验证关键词零命中时的向量召回、离线推理、缓存复用/修复、历史隔离和检索驱动编译。该样例证明机制实际执行，不代表真实企业语料上的召回率基准。
