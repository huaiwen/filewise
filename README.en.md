# Filewise

[简体中文](README.md) · **English**

**A local file workspace with traceable history for people and AI agents.**

Files change. The people and agents using them need to know which version they read, what changed, and where a reference came from. Filewise works with existing folders, keeping versions of files and metadata alongside tools for organization, retrieval and change analysis.

Use the browser workspace for everyday folder organization, or connect agents through the CLI and HTTP API. File history stays local, and default analysis needs no cloud service.

## What you can do

- **Keep folders organized.** Configure rules once to extract titles, text excerpts, tags and JSON fields in the background. Keep original names, confirm suggestions or rename automatically, with conflict checks and undo.
- **Follow changes over time.** Preserve original content and metadata, read earlier versions, compare changes and follow declared dependencies to see which files are affected.
- **Find material you can check.** Search English and Chinese text and get file paths, versions and content locations that lead back to the source.
- **Give agents scoped file access.** Project-specific credentials limit access. Writes check their baseline version, and task context is bound to specific files and evidence.
- **Check data before use.** Declare quality rules, data meaning and allowed uses for JSON/CSV/XLSX. Everyday saves remain separate from human approval and publication.

Metadata is extracted from local text by default. You can optionally use a local Ollama model for titles, summaries and tags. The browser UI supports English and Simplified Chinese.

## Get started

Building from source requires **Rust 1.86+** and a C toolchain. macOS is currently recommended for trying Filewise.

```bash
git clone https://github.com/huaiwen/filewise.git
cd filewise
cargo build --locked --release
./target/release/filewise start
```

When the browser opens:

1. Add a practice folder.
2. Choose analysis fields and naming rules. Original names are kept by default.
3. Drop in files and review processing activity and suggested names in the workspace.

Monitoring continues after you close the browser. Run `./target/release/filewise stop` to stop the service.

## Current scope

Filewise is a development preview for small local workspaces. Content processing supports **text, Markdown, JSON, CSV, text-based PDF, DOCX, XLSX and PPTX**, with page, paragraph, worksheet-cell and slide locations. OCR, legacy DOC/XLS/PPT and vector semantic search are not yet implemented.

Each folder is limited to 1,000 files and 50 MiB total, with a 10 MiB limit per file. Renames apply to files in the original folder.

Built with **Rust + SQLite**, one native program provides the workspace, CLI and HTTP service. No Python, Node or standalone database server is needed at runtime.

## Learn more

- [Folder workspace guide](docs/folders.md): monitoring, naming, language and background settings, in English and Chinese.
- [CLI and Agent guide](docs/rust.md): project setup, file operations, retrieval, task checks and permissions; currently in Chinese.
- [Historical prototype](README-python-reference.md): a retained reference implementation, separate from the Rust runtime.

Share use cases, problems and suggestions in [Issues](https://github.com/huaiwen/filewise/issues).
