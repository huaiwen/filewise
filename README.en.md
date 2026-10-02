<h1 align="center">Filewise</h1>

<p align="center">
  <strong>Version your files. Ground your agents.</strong><br />
  A local file workspace for people and AI agents
</p>

<p align="center">
  <a href="https://github.com/huaiwen/filewise/releases/latest"><img src="https://img.shields.io/github/v/release/huaiwen/filewise?color=2563eb" alt="Latest release" /></a>
  <a href="https://github.com/huaiwen/filewise/actions/workflows/ci.yml"><img src="https://github.com/huaiwen/filewise/actions/workflows/ci.yml/badge.svg?branch=main" alt="Go checks" /></a>
  <a href="#core-technology"><img src="https://img.shields.io/badge/primary-Go-00ADD8?logo=go" alt="Primary development language: Go" /></a>
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

**The runtime is now Go.** `0.3.0-dev` provides project storage, the Agent gateway, folder monitoring, the workbench, document parsing, data checks and independent publication. See [implementation and validation scope](docs/go-migration.md). This Go version is not publicly released; historical **v0.2.0** remains unchanged.

## Highlights

| | Capability | What it gives you |
| --- | --- | --- |
| 📁 | **Ongoing folder organization** | Configure rules once to extract titles, text excerpts, tags and JSON fields; keep names, confirm suggestions or rename automatically |
| 🕘 | **History for files and metadata** | Read captured versions, compare content and follow declared dependencies to identify affected files |
| 🔎 | **Search with sources attached** | English and Chinese keyword search returns file paths, versions and content locations you can check |
| 🤖 | **Project-scoped agent access** | Project credentials, write-baseline checks and task evidence bindings |
| ✅ | **Check data before use** | Declare quality rules, meaning and allowed uses for JSON, CSV and XLSX; check missing values, uniqueness, ranges and record counts |
| 📝 | **Record what a change means** | The Go development build stores Word quotations, quantity changes and paragraph evidence in `meta.semantic_change` |
| ⚙️ | **Native programs, running locally** | Go + SQLite/FTS5 with a bilingual workspace, CLI and HTTP service; normal builds disable CGO |

<details>
<summary><strong>Use cases</strong></summary>

- **Collecting and organizing material**: add PDFs, notes and Office files to a research or project folder and keep generating metadata and naming suggestions.
- **Iterating on reports and requirements**: compare captured versions, review content changes and see which declared dependencies need another check.
- **Preparing data for analysis**: check agreed data meaning, allowed uses and quality rules before handing tables to a script or agent.
- **Agent file workflows**: find material, read a specific version, prepare evidence-bound task context and check outputs against configured rules.

</details>

## Quick Start

### 1. Build and start

From the current checkout, with Go 1.27+:

```bash
make build
./build/filewise start
```

The executable stays at `build/filewise` and opens the local workspace. Go uses separate state and does not convert legacy databases.

Targets: macOS (Apple Silicon / Intel), Linux (ARM64 / x86-64). Windows is not implemented; minimum-OS and other-platform execution need separate verification.

[Install, upgrade and uninstall](docs/install.md) · [Historical releases](https://github.com/huaiwen/filewise/releases). The current installer accepts Go releases only, without legacy fallback.

### 2. Add a folder

1. Choose **Add folder** in the workspace and select a local directory.
2. Set file scope, analysis fields and naming rules. Original names are kept by default.
3. Drop in files. Once their content is stable, review processing results and naming suggestions in activity.

Automatic renaming changes filenames in the original directory, with conflict checks and undo.

### 3. Keep using it

Monitoring continues after you close the browser. Rules and history survive service restarts.

```bash
./build/filewise status
./build/filewise stop
./build/filewise start --no-open
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

Agents read and write files through the HTTP gateway with project-scoped access. [Configure projects and credentials](docs/go.md).

The **Agent Skill** supports Codex, Claude Code and Cursor. Use `$filewise init` in Codex or `/filewise init` in Claude Code/Cursor to check the project connection, then search, read and compare versions.

The npm installer is not published yet. [Local package testing, platform installation and authorization](docs/agent-skills.md).

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
| Native executable | Go · `cmd/filewise`; normal builds disable CGO |
| Documents and semantic meta | PDF / DOCX / XLSX / PPTX / UTF-8; extraction workers, exact changes and before/after quotations |
| Versions and permissions | SQLite, immutable originals, CAS/idempotency/recovery, source ACLs, audit and revocation |
| HTTP, monitoring and workspace | Go `net/http`, stability scanning, naming/undo and embedded bilingual HTML / CSS / JS |
| Retrieval, quality, tasks and publication | FTS5 / BM25, full-table checks, evidence-bound tasks and independent review/publication |

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
| [Go migration record](docs/go-migration.md) | Semantic meta, separate state, functional coverage and validation scope |
| [Installation and upgrades](docs/install.md) | Requirements, pinned versions, manual downloads, upgrades and uninstalling; English and Chinese |
| [Folder workspace](docs/folders.md) | Monitoring, naming, language, processing status and background operation; English and Chinese |
| [CLI and Agent](docs/go.md) | Projects, file operations, versions, retrieval, quality rules and permissions; currently in Chinese |
| [Agent Skills](docs/agent-skills.md) | npm installer, Codex / Claude Code / Cursor setup and authorization |
| [Releases](https://github.com/huaiwen/filewise/releases) | Binaries and release notes |
| [Historical prototype](README-python-reference.md) | The earlier Python reference implementation |

## Development and Feedback

Go 1.27+; race-detector checks also require a C toolchain. Website and installer checks use Node.js 22+. Runtime, builds, CI and Docker do not depend on Rust.

Website source: `site/`. Local preview: `npm run site`; installer and website tests: `npm test`.

```bash
git clone https://github.com/huaiwen/filewise.git
cd filewise
make build
make check
```

[Go usage guide](docs/go.md) · [Source build](docs/install.md#developers-build-from-source) · [Issues](https://github.com/huaiwen/filewise/issues) · [Pull Requests](https://github.com/huaiwen/filewise/pulls)
