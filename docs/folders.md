# Folder monitoring / 文件夹监控

## English

### Start once

Build with Rust 1.86+ and a C toolchain, then run:

```bash
cargo build --locked --release
./target/release/filewise start
```

The command opens the **local browser workspace** and returns while the Rust background process keeps running. Closing the browser does not stop monitoring. No Python or Node runtime is required.

1. Choose **Add folder** and select or paste a local path. Start with synthetic files.
2. Set file scope, whether to process existing files, analysis fields and naming rules.
3. Drop files into the folder. Filewise waits for stable content, captures a version and processes it automatically.
4. Review metadata, confirm suggested names, retry failures, pause monitoring or undo a rename in the UI.

The default naming mode is **Keep original name**. Suggested naming saves metadata first and waits for confirmation before changing a path. Automatic naming modifies file paths without a production approval; it never approves or publishes a release. If a target exists, Filewise chooses a suffix instead of overwriting it. Renames preserve the original inode, file mode, ACLs and extended attributes (including macOS quarantine). They use an exclusive hard-link/unlink move on the same filesystem, with a durable recovery intent; a short-lived second name is possible. Unsupported/cross-filesystem moves fail rather than falling back to an attribute-losing copy. External links may need updating. Undo refuses conflicts rather than discarding later edits.

### Language

The workspace supports **English** and **Simplified Chinese**. Use the language selector in the header or a dialog. The default follows the browser's supported language preferences; otherwise it falls back to English. Your explicit choice is stored in this browser's local storage. It survives reloads and does not change folder rules, filenames, summaries or user-entered fields.

Messages live in `rust/ui/strings.json`; date, number and plural formatting uses native `Intl`. API keys, statuses and diagnostic messages remain language-neutral/English; recognized error codes are translated in the UI. Changing UI language does not request translation of document content or model output.

```bash
node rust/ui/i18n.test.cjs
```

Node is needed only for this development check, not to run Filewise. The check covers key/placeholder parity, locale fallback, interpolation, plural rules and formatting.

### Lifecycle and private state

```bash
./target/release/filewise status
./target/release/filewise stop
./target/release/filewise start --no-open
```

Default state: `~/Library/Application Support/Filewise/filewise.db` on macOS; `~/.local/share/filewise/filewise.db` on Linux. A custom global `--db` selects another instance. Unlike `start/status/stop/autostart`, the lower-level project/serve commands keep their existing `.filewise-rust/filewise.db` default: always pass the same explicit DB path when mixing them.

macOS login startup is opt-in under **Startup settings**, or:

```bash
./target/release/filewise autostart --enable
./target/release/filewise autostart --disable
```

Changes take effect at the next login. This writes only a per-user LaunchAgent, without administrator access; it is not a crash supervisor. Keep the executable and data directory at their configured paths. Linux login startup is not implemented. While the computer is off/asleep or the service is stopped, scanning pauses; restarting restores configured watches and detects offline changes.

The start URL contains a local **operator editing token**. Do not share it with restricted Agents. Tokens, the runtime pointer and logs are private files. UI controls are loopback-only and require the exact Host/Origin plus an operator Bearer token; Agents cannot register arbitrary folders. Existing project-specific Agent access continues through the gateway. Optional model analysis sends only extracted text to an explicitly configured loopback Ollama endpoint; no remote endpoint or implicit provider fallback is accepted.

### Scope and current limits

- Local extraction: UTF-8 text, Markdown, JSON, CSV, text-based PDF, DOCX, XLSX and PPTX. PDF has page/line locations; Word has paragraphs/tables and related headers/footnotes; Excel has worksheet/cell addresses; PowerPoint has slide text, tables and speaker notes in presentation order. Titles use a heading, JSON title/name or the first extracted document line; summaries are original excerpts, not model-generated understanding. Configured tags and scalar JSON-pointer fields remain supported (JSON fields are not Office field mappings).
- **Coverage is explicit.** Partial extraction is labeled in activity/details. Images/charts/embedded objects, inherited slide layouts and uncalculated formulas require review; a task containing partial or missing text cannot pass completeness checks. Blank/image-only PDFs get a no-text error, not an invented summary. Encrypted, malformed or over-limit documents get an extraction error; their original bytes are preserved and other jobs continue.
- No OCR, visual-layout reconstruction or legacy DOC/XLS/PPT parsing. Other formats retain their bytes and get basic filename/type metadata. No macros, formulas or external document relationships are executed/fetched. Built-in Rust parsers need no Office, Python, converter or extra installation. The bounded parsing child is resource containment, not an OS permissions sandbox.
- Ollama requires an already installed model. Text input is limited to 20,000 characters; generated output is labeled as model output. No model is downloaded automatically. Real model quality is not established by mock/transport tests.
- Default polling/stability: 3 seconds each. This is a bounded content-hash heuristic, not proof that a writer finished. Partial-download extensions are excluded; raise the delay for slow downloads.
- Per folder: 1,000 files / 50 MiB total; 10 MiB per file. Full-content polling and serialized jobs favor small workspaces; a constantly changing folder can delay the whole-folder stable snapshot.
- File scope is fixed at registration. Rule changes affect future processing, not every already-completed file. Pending work based on older rules must be retried. Existing overlapping project roots are refused.
- Manual metadata, data contracts, current source policies and revocations remain effective. Automation does not silently refresh human declarations or bypass failed project checks.
- No file retention/garbage-collection policy is implemented: versions and job history accumulate locally. Back up the private DB while the service is stopped. The UI shows the latest 200 jobs.

## 中文

运行 `filewise start`，在工作台添加文件夹并设置一次规则。之后放入文件即可，**关闭网页不影响后台监控**；服务重启后保留规则、历史，并检测停机期间的变化。

- **语言**：界面支持 English / 简体中文，可在页头和弹窗切换；默认跟随浏览器，其他语言回退英文。选择保存在浏览器，不改写文件内容或已填表单。日期、数字和复数通过 `Intl` 格式化。
- **默认不改名**：保留原名仅写入元信息；建议模式先保存元信息，再等待确认；自动模式可直接修改路径。所有模式都不会自动审批或发布。
- **改名与撤销**：碰撞加后缀、不覆盖已有文件；检测到外部修改就拒绝撤销。改名保留原 inode、权限、ACL 和扩展属性（含 macOS 隔离标记）；使用同文件系统的排他硬链接/删除旧名与持久化恢复记录，短暂可能出现两个名字。无法安全移动时拒绝，不退化为丢失属性的复制。外部链接可能需要更新。
- **分析范围**：支持文本、Markdown、JSON、CSV、带文字层的 PDF、DOCX、XLSX、PPTX。Word 提取段落/表格及相关页眉脚注，Excel 保留工作表和单元格位置，PowerPoint 按演示顺序提取文字、表格和备注。JSON 字段配置仍只适用于 JSON。可明确选择已有的本机 Ollama 模型，不下载模型、不暗中调用云服务。
- **提取状态**：部分提取在记录和详情中明确提示；图片/图表、嵌入对象、幻灯片继承版式和未计算公式需要核对。无文字 PDF、加密、损坏或超限文档显示具体问题，仍保留原字节，不妨碍其他文件处理。任务上下文含缺失或部分提取内容时，完整性检查不会 PASS。
- **文档边界**：不做 OCR、视觉版式重建或旧版 DOC/XLS/PPT 解析，不执行宏/公式、不跟随外部关系。内置 Rust 解析无需 Office、Python 或转换软件；解析子进程有资源限额，但不是操作系统权限沙箱。
- **后台生命周期**：`status` 查看，`stop` 停止，`start` 恢复。macOS 可选择登录启动，下次登录生效；不是崩溃自动拉起。Linux 登录启动尚未实现。
- **边界**：3 秒扫描和稳定等待是启发式保护，不证明下载结束；慢速下载应调大。每文件 ≤10 MiB，每文件夹 ≤1,000 文件 / 50 MiB。持续变化可能延迟整个目录的稳定采集。
- **权限**：工作台链接包含操作员编辑令牌，不要给受限 Agent。人工声明、固定数据契约、当前来源 ACL 和撤销仍有效。历史和任务记录本地累积，尚无自动清理。

原生 CLI、历史、检索和数据校验详见 [Rust 使用指南](rust.md)。
