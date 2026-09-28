<h1 align="center">Filewise</h1>

<p align="center">
  <strong>Version your files. Ground your agents.</strong><br />
  A local file workspace for people and AI agents
</p>

<p align="center">
  <a href="https://github.com/huaiwen/filewise/releases/latest"><img src="https://img.shields.io/github/v/release/huaiwen/filewise?color=2563eb" alt="Latest release" /></a>
  <a href="https://github.com/huaiwen/filewise/actions/workflows/ci.yml"><img src="https://github.com/huaiwen/filewise/actions/workflows/ci.yml/badge.svg?branch=main" alt="Rust CI" /></a>
  <a href="#core-technology"><img src="https://img.shields.io/badge/built_with-Rust-000000?logo=rust" alt="Built with Rust" /></a>
  <a href="docs/install.md"><img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux-64748b" alt="macOS and Linux" /></a>
</p>

<p align="center">
  <a href="#highlights">Highlights</a> ·
  <a href="#quick-start">Quick Start</a> ·
  <a href="#files-and-versions">Files & Versions</a> ·
  <a href="#agent-access">Agent Access</a> ·
  <a href="#documentation">Documentation</a> ·
  <a href="#development-and-feedback">Development</a>
</p>

<p align="center"><a href="README.md">简体中文</a> · <strong>English</strong></p>

---

**Filewise** is a local file workspace for people and AI agents, with automatic organization, version history, traceable retrieval and controlled read/write access.

Manage folders in the browser and connect agents through the CLI and HTTP API. File versions and metadata are stored locally.

## Highlights

| | Capability | What it gives you |
| --- | --- | --- |
| 📁 | **Ongoing folder organization** | Configure rules once to extract titles, text excerpts, tags and JSON fields; keep names, confirm suggestions or rename automatically |
| 🕘 | **History for files and metadata** | Read captured versions, compare content and follow declared dependencies to identify affected files |
| 🔎 | **Search with sources attached** | English and Chinese keyword search returns file paths, versions and content locations you can check |
| 🤖 | **Project-scoped agent access** | Project credentials, write-baseline checks and task evidence bindings |
| ✅ | **Check data before use** | Declare quality rules, meaning and allowed uses for JSON, CSV and XLSX; check missing values, uniqueness, ranges and record counts |
| 🦀 | **One native program, running locally** | Rust + SQLite with a bilingual workspace, CLI, HTTP service and document parsing |

<details>
<summary><strong>Use cases</strong></summary>

- **Collecting and organizing material**: add PDFs, notes and Office files to a research or project folder and keep generating metadata and naming suggestions.
- **Iterating on reports and requirements**: compare captured versions, review content changes and see which declared dependencies need another check.
- **Preparing data for analysis**: check agreed data meaning, allowed uses and quality rules before handing tables to a script or agent.
- **Agent file workflows**: find material, read a specific version, prepare evidence-bound task context and check outputs against configured rules.

</details>

## Quick Start

### 1. Install and start

```bash
curl -fsSL https://github.com/huaiwen/filewise/releases/latest/download/install.sh | bash
export PATH="$HOME/.local/bin:$PATH"
filewise start
```

Install location: `~/.local/bin`. The installer selects the OS and architecture, verifies SHA-256, and installs the binary. Starting Filewise opens the local workspace.

| System | Architecture | Build baseline |
| --- | --- | --- |
| macOS | Apple Silicon / Intel | macOS 13+ |
| Linux | ARM64 / x86_64 | glibc 2.35+, such as Ubuntu 22.04 |

[Download binaries](https://github.com/huaiwen/filewise/releases/latest) · [Install, upgrade and uninstall](docs/install.md)

Windows and Alpine/musl are not currently supported.

### 2. Add a folder

1. Choose **Add folder** in the workspace and select a local directory.
2. Set file scope, analysis fields and naming rules. Original names are kept by default.
3. Drop in files. Once their content is stable, review processing results and naming suggestions in activity.

Automatic renaming changes filenames in the original directory, with conflict checks and undo.

### 3. Keep using it

Monitoring continues after you close the browser. Rules and history survive service restarts.

```bash
filewise status
filewise stop
filewise start --no-open
```

The workspace supports English and Simplified Chinese. macOS login startup is opt-in; see the [folder workspace guide](docs/folders.md).

## Files and Versions

The workspace handles **folder monitoring, organization rules and processing activity**. Historical reads, search, diffs and data checks are available through the **CLI / HTTP API**.

| File type | Extracted content and locations |
| --- | --- |
| Text, Markdown, JSON, CSV | Text fragments, structured fields and tabular data |
| PDF | Text-layer content with page and line locations |
| DOCX | Paragraphs, tables and related headers, footers, footnotes and endnotes |
| XLSX | Worksheets and cells; preserved numeric precision, with formulas and date types identified |
| PPTX | Slide text, tables and speaker notes in presentation order |

Default analysis uses local text excerpts. An installed local Ollama model can generate titles, summaries and tags.

Original content and metadata are saved with captured versions. Incomplete extraction shows its coverage, and tasks containing missing or partially extracted content cannot pass completeness checks.

<details>
<summary><strong>Development preview: processing scope and capacity</strong></summary>

- Designed for small local workspaces: up to **1,000 files / 50 MiB per folder**, with a **10 MiB per-file** limit.
- Document processing is text-focused: no OCR, visual-layout reconstruction or legacy DOC/XLS/PPT parsing; macros and formulas are not executed.
- Search combines exact matching and keyword full-text retrieval; vector semantic search is not yet available.
- Version history accumulates locally with no automatic cleanup policy. Stop the service before backing up its database.

</details>

## Agent Access

Agents read and write files through the HTTP gateway with project-scoped access. [Configure projects and credentials](docs/rust.md).

| Command | Purpose |
| --- | --- |
| `filewise agent versions PROJECT` | List captured versions |
| `filewise agent search PROJECT "query"` | Search content and return checkable locations |
| `filewise agent read PROJECT report.pdf` | Read a file and its extracted content; optionally select a historical version |
| `filewise agent diff PROJECT BEFORE_VERSION latest` | Compare two versions |
| `filewise agent impact PROJECT --path report.md` | Follow declared dependencies to assess impact |
| `filewise agent quality PROJECT` | Check configured data-quality rules |
| `filewise agent compile PROJECT --goal "review"` | Prepare version- and evidence-bound task context |

Writes validate baseline versions, original-file state, permissions and request identifiers. Working saves, review and publication are separate operations.

## Core Technology

| Layer | Implementation |
| --- | --- |
| Native core and CLI | Rust · Clap |
| HTTP service and background work | Axum · Tokio |
| Versions, metadata and audit | Embedded SQLite |
| Retrieval | Exact matching · SQLite FTS5 / BM25 |
| Document parsing | Native Rust PDF / OOXML parsers in a resource-limited child process |
| Browser workspace | Embedded HTML / CSS / JavaScript with English and Chinese support |

## Data and Access Boundaries

- **Local storage**: file history and default analysis stay on your machine; optional model analysis connects only to an explicitly configured local Ollama endpoint.
- **Project-level authorization**: restricted agents reach approved projects through the gateway. Current source permissions and revocations also apply to historical content.
- **Separate identities**: workspace URLs contain a local operator token. Do not give that credential to restricted agents.
- **Explicit publication**: automatic organization creates working saves. Publication requires the appropriate role, independent review and checks.

## Future Directions

- [ ] OCR and broader document extraction coverage.
- [ ] Local vector semantic search.
- [ ] Named versions and reviewed historical-content writeback.

## Documentation

| Guide | Contents |
| --- | --- |
| [Installation and upgrades](docs/install.md) | Requirements, pinned versions, manual downloads, upgrades and uninstalling; English and Chinese |
| [Folder workspace](docs/folders.md) | Monitoring, naming, language, processing status and background operation; English and Chinese |
| [CLI and Agent](docs/rust.md) | Projects, file operations, versions, retrieval, quality rules and permissions; currently in Chinese |
| [Releases](https://github.com/huaiwen/filewise/releases) | Binaries and release notes |
| [Historical prototype](README-python-reference.md) | An earlier reference implementation, separate from the current Rust runtime |

## Development and Feedback

Requirements: Rust 1.86+, a C toolchain, and Node.js 22 for UI language checks.

```bash
git clone https://github.com/huaiwen/filewise.git
cd filewise
cargo build --locked --release
cargo test --locked
```

[Developer guide](docs/install.md#developers-build-from-source) · [Issues](https://github.com/huaiwen/filewise/issues) · [Pull Requests](https://github.com/huaiwen/filewise/pulls)
