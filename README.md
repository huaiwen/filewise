# Filewise

**简体中文** · [English](README.en.md)

**为人和 AI Agent 提供可追溯的本地文件工作空间。**

文件不断更新，使用它们的人和 Agent 需要知道：读的是哪一版，内容改了什么，引用来自哪里。Filewise 在现有目录上保存文件与元信息的版本，把自动整理、检索和变更检查连接起来。

通过网页管理日常文件夹，通过 CLI 和 HTTP API 接入 Agent 工作流。文件历史保存在本地，默认分析无需云服务。

## 可以用它做什么

- **持续整理文件夹。** 设置一次规则，后台自动提取标题、原文节选、标签和 JSON 字段。保留原名、确认后改名或自动命名，由你选择；改名支持冲突检查和撤销。
- **保留文件的变化过程。** 保存原始内容和元信息，读取历史版本、比较前后差异，并沿声明的依赖分析哪些文件会受影响。
- **找到资料，也找到依据。** 中英文关键词检索返回文件路径、版本和内容位置，方便回到原文核对。
- **让 Agent 有边界地读写。** 项目专用凭据控制访问范围；修改时检查基线版本，任务上下文绑定具体文件和证据。
- **在使用前检查数据。** 为 JSON/CSV/XLSX 声明质量规则、数据口径和允许用途；日常保存与人工审核、发布分开。

默认从本地文本提取元信息，也可选择本机 Ollama 生成标题、摘要和标签。网页支持简体中文与 English。

## 开始使用

从源码构建需要 **Rust 1.86+** 和 C 编译工具链。当前建议在 macOS 上试用。

```bash
git clone https://github.com/huaiwen/filewise.git
cd filewise
cargo build --locked --release
./target/release/filewise start
```

浏览器打开后：

1. 添加一个练习文件夹。
2. 选择分析字段和命名规则，默认保留原名。
3. 放入文件，在工作台查看处理记录与改名建议。

关闭网页后，后台仍会继续监控。停止服务可运行 `./target/release/filewise stop`。

## 当前范围

Filewise 处于开发预览阶段，面向小型本地工作目录。正文处理支持 **文本、Markdown、JSON、CSV、带文字层的 PDF，以及 DOCX、XLSX、PPTX**，可定位到页、段落、工作表单元格和幻灯片。OCR、旧版 DOC/XLS/PPT 和向量语义检索尚未实现。

当前每个文件夹最多 1,000 个文件、总计 50 MiB，单文件最多 10 MiB。改名直接作用于原目录中的文件。

核心由 **Rust + SQLite** 实现，一个原生程序提供工作台、CLI 和 HTTP 服务，运行无需 Python、Node 或独立数据库服务器。

## 深入了解

- [文件夹工作台](docs/folders.md)：监控、命名规则、语言和后台运行设置，中英双语。
- [CLI 与 Agent 使用指南](docs/rust.md)：项目接入、读写、检索、任务检查和权限。
- [历史原型](README-python-reference.md)：保留的参考实现，不参与当前 Rust 运行。

欢迎在 [Issues](https://github.com/huaiwen/filewise/issues) 分享使用场景、问题和建议。
