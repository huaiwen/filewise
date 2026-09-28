<h1 align="center">Filewise</h1>

<p align="center">
  <strong>让文件有版本，让 Agent 有依据。</strong><br />
  面向人和 AI Agent 的本地文件工作空间
</p>

<p align="center">
  <a href="https://github.com/huaiwen/filewise/releases/latest"><img src="https://img.shields.io/github/v/release/huaiwen/filewise?color=2563eb" alt="最新版本" /></a>
  <a href="https://github.com/huaiwen/filewise/actions/workflows/ci.yml"><img src="https://github.com/huaiwen/filewise/actions/workflows/ci.yml/badge.svg?branch=main" alt="Rust CI" /></a>
  <a href="#核心技术"><img src="https://img.shields.io/badge/built_with-Rust-000000?logo=rust" alt="使用 Rust 构建" /></a>
  <a href="docs/install.md"><img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux-64748b" alt="macOS 与 Linux" /></a>
</p>

<p align="center">
  <a href="#亮点">亮点</a> ·
  <a href="#快速开始">快速开始</a> ·
  <a href="#文件与版本">文件与版本</a> ·
  <a href="#agent-接入">Agent 接入</a> ·
  <a href="#文档">文档</a> ·
  <a href="#开发与反馈">开发与反馈</a>
</p>

<p align="center"><strong>简体中文</strong> · <a href="README.en.md">English</a></p>

---

**Filewise** 是面向人和 AI Agent 的本地文件工作空间，支持自动整理、版本管理、检索溯源和受控读写。

通过浏览器管理文件夹，通过 CLI 和 HTTP API 接入 Agent。文件版本与元信息保存在本机。

## 亮点

| | 能力 | 带来的变化 |
| --- | --- | --- |
| 📁 | **持续整理文件夹** | 设置一次规则，后台提取标题、原文节选、标签和 JSON 字段；支持保留原名、确认建议或自动改名 |
| 🕘 | **文件与元信息一起留历史** | 读取已采集的历史版本，比较前后内容，并沿声明的依赖分析受影响的文件 |
| 🔎 | **检索结果带出处** | 中英文关键词检索返回文件路径、版本和正文位置，方便核对引用 |
| 🤖 | **Agent 按项目接入** | 项目专用凭据、写入基线检查与任务证据绑定 |
| ✅ | **数据使用前先检查** | 为 JSON、CSV、XLSX 声明质量规则、数据含义与允许用途，检查缺失值、唯一性、范围和记录数量 |
| 🦀 | **一个原生程序，本地运行** | Rust + SQLite，内置中英文工作台、CLI、HTTP 服务与文档解析 |

<details>
<summary><strong>使用场景</strong></summary>

- **资料收集与整理**：把 PDF、笔记和 Office 文件放进研究或项目目录，持续生成元信息与命名建议。
- **报告与需求迭代**：对比已保存的前后版本，核对哪些内容变化、哪些声明依赖需要重新检查。
- **数据分析准备**：在把表格交给脚本或 Agent 前，检查约定的数据口径、用途与质量规则。
- **Agent 文件工作流**：查找资料、读取指定版本、准备带证据的任务上下文，并检查输出是否满足已配置规则。

</details>

## 快速开始

### 1. 安装并启动

```bash
curl -fsSL https://github.com/huaiwen/filewise/releases/latest/download/install.sh | bash
export PATH="$HOME/.local/bin:$PATH"
filewise start
```

安装目录：`~/.local/bin`。安装器自动匹配系统与架构并校验 SHA-256，启动后打开本地工作台。

| 系统 | 架构 | 构建基线 |
| --- | --- | --- |
| macOS | Apple Silicon / Intel | macOS 13+ |
| Linux | ARM64 / x86_64 | glibc 2.35+，如 Ubuntu 22.04 |

[下载安装包](https://github.com/huaiwen/filewise/releases/latest) · [安装、升级与卸载](docs/install.md)

Windows 与 Alpine/musl 暂不支持。

### 2. 添加文件夹

1. 在工作台点击“添加文件夹”，选择本地目录。
2. 设置文件范围、分析字段和命名规则，默认保留原名。
3. 放入文件，等待内容稳定后，在活动记录中查看处理结果与改名建议。

自动改名修改原目录中的文件名，支持冲突检查与撤销。

### 3. 持续使用

关闭网页后，后台仍会继续监控；服务重启后保留规则和历史。

```bash
filewise status
filewise stop
filewise start --no-open
```

工作台支持简体中文与 English。macOS 可按需开启登录启动，详见[文件夹工作台指南](docs/folders.md)。

## 文件与版本

工作台负责**文件夹监控、整理规则与处理记录**；历史读取、检索、差分和数据校验通过 **CLI / HTTP API** 使用。

| 文件类型 | 可以提取与定位的内容 |
| --- | --- |
| 文本、Markdown、JSON、CSV | 文本片段、结构化字段和表格数据 |
| PDF | 文字层正文，定位到页和行 |
| DOCX | 段落、表格，以及相关页眉、页脚、脚注和尾注 |
| XLSX | 工作表和单元格；保留数值精度，标记公式与日期类型 |
| PPTX | 按演示顺序提取幻灯片文字、表格和备注 |

默认分析使用本地文本节选，支持接入本机已安装的 Ollama 模型生成标题、摘要和标签。

原始内容和元信息随采集版本保存。文档提取不完整时会显示覆盖情况，包含缺失或部分提取内容的任务不会通过完整性检查。

<details>
<summary><strong>开发预览版：处理范围与容量</strong></summary>

- 面向小型本地工作目录：每个文件夹最多 **1,000 个文件 / 50 MiB**，单文件最多 **10 MiB**。
- 文档处理以文字为主；不做 OCR、视觉版式重建或旧版 DOC/XLS/PPT 解析，不执行宏和公式。
- 检索使用精确匹配与关键词全文检索，尚未提供向量语义检索。
- 版本记录持续保存在本地，目前没有自动清理策略；备份数据库前应停止服务。

</details>

## Agent 接入

Agent 通过 HTTP 网关按项目读写文件。[配置项目与访问凭据](docs/rust.md)。

| 命令 | 用途 |
| --- | --- |
| `filewise agent versions PROJECT` | 查看已采集的版本 |
| `filewise agent search PROJECT "query"` | 检索正文并返回可核对的位置 |
| `filewise agent read PROJECT report.pdf` | 读取文件及提取内容；可指定历史版本 |
| `filewise agent diff PROJECT BEFORE_VERSION latest` | 比较两个版本 |
| `filewise agent impact PROJECT --path report.md` | 沿声明的依赖分析影响范围 |
| `filewise agent quality PROJECT` | 检查已配置的数据质量规则 |
| `filewise agent compile PROJECT --goal "review"` | 准备带版本与证据的任务上下文 |

写入校验基线版本、原件状态、权限与请求标识。工作保存、审核与发布分别执行。

## 核心技术

| 层级 | 实现 |
| --- | --- |
| 原生核心与 CLI | Rust · Clap |
| HTTP 服务与后台任务 | Axum · Tokio |
| 版本、元信息与审计 | 内嵌 SQLite |
| 检索 | 精确匹配 · SQLite FTS5 / BM25 |
| 文档解析 | 原生 Rust PDF / OOXML 解析，使用资源受限的子进程 |
| 浏览器工作台 | 内嵌 HTML / CSS / JavaScript，支持中英文 |

## 数据与访问边界

- **本地保存**：文件历史与默认分析留在本机；可选模型分析仅连接显式配置的本机 Ollama。
- **按项目授权**：受限 Agent 通过网关访问获准项目，历史内容同样受当前来源权限与撤销状态约束。
- **区分身份**：浏览器工作台链接包含本地操作员令牌，不要把它交给受限 Agent。
- **明确发布**：自动整理产生工作保存；正式发布需要相应角色、独立审核和检查结果。

## 后续方向

- [ ] OCR 与更广的文档提取覆盖。
- [ ] 本地向量语义检索。
- [ ] 命名版本与经审核的历史内容写回。

## 文档

| 文档 | 内容 |
| --- | --- |
| [安装与升级](docs/install.md) | 系统要求、指定版本、手动下载、升级与卸载；中英双语 |
| [文件夹工作台](docs/folders.md) | 监控、命名、语言、处理状态与后台运行；中英双语 |
| [CLI 与 Agent](docs/rust.md) | 项目接入、文件操作、版本、检索、质量规则和权限 |
| [版本发布](https://github.com/huaiwen/filewise/releases) | 安装包与版本说明 |
| [历史原型](README-python-reference.md) | 保留的早期参考实现，与当前 Rust 运行时分开 |

## 开发与反馈

环境：Rust 1.86+、C 编译工具链；界面语言检查使用 Node.js 22。

```bash
git clone https://github.com/huaiwen/filewise.git
cd filewise
cargo build --locked --release
cargo test --locked
```

[开发指南](docs/install.md#开发者从源码构建) · [Issues](https://github.com/huaiwen/filewise/issues) · [Pull Requests](https://github.com/huaiwen/filewise/pulls)
