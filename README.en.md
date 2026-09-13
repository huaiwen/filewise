# Filewise · Rust

[简体中文](README.md) · **English**

A local versioned file and knowledge layer for people and Agents. Filewise stores original bytes, metadata and history, and provides retrieval, diff, impact analysis and checkable task contracts. Working saves are separate from approval and publication.

**Rust + SQLite + Axum.** One native binary; no Python, Node or standalone database server is needed at runtime.

## Folder workspace

Requires Rust 1.86+, a C toolchain, and macOS or Linux.

```bash
cargo build --locked --release
./target/release/filewise start
```

Add a folder in the browser UI, configure analysis and naming rules, then drop in files. The Rust background process detects stable changes and processes them without manual sync. **Closing the browser does not stop monitoring.**

- English / Simplified Chinese, browser-language detection and a persistent language selector.
- Local extractive metadata for text, Markdown, JSON and CSV; explicitly configured local Ollama is optional.
- Keep names, suggest names for confirmation, or rename automatically with collision protection and undo.
- Persistent watches, pause/resume, retry and restart detection. Optional macOS login startup; Linux login startup is not yet implemented.
- No automatic production approval or publication. No silent cloud analysis or Python fallback.

See **[Folder monitoring and safety limits](docs/folders.md)**. Office/PDF/OCR content extraction is not implemented. Folder renames preserve bytes, the inode, permissions and extended attributes; moves that cannot preserve them safely are refused. Start with a practice folder.

```bash
./target/release/filewise status
./target/release/filewise stop
```

## Agent and data operations

The native gateway supports project-scoped credentials, guarded file/metadata writes, concrete-version CAS, idempotency, completed snapshot history and `as_of`, exact/FTS5 retrieval, dependency impact, task compilation and read-result verification, JSON/CSV data contracts and pinned lineage.

Use a **project-specific Agent token**, never the local workspace's operator token or raw SQLite/token files. The detailed [Rust operator and Agent guide](docs/rust.md) is currently in Chinese; API and CLI names are English.

Native vector inference, Office/PDF extraction, named commits, reviewed historical restoration, upload projects, native Agent hooks, object bitemporality and write-postflight verification are not migrated. Unsupported operations fail explicitly. The old Python code is preserved as a reference, not a runtime backend.

## Development checks

```bash
cargo fmt --all --check
cargo clippy --locked --all-targets -- -D warnings
cargo test --locked
node rust/ui/i18n.test.cjs
```

Node is development-only for the internationalization check. Native tests use synthetic data and exercise actual background CLI/HTTP workflows. Packaging is allowlisted; no business documents, credentials or runtime state are included. Local validation does not imply that remote CI, Docker or Linux have been executed.
