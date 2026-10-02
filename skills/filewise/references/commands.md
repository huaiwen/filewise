# Native commands · 原生命令

Requires the Filewise Go runtime (`--version` includes `(Go)`) and a project-scoped credential in `FILEWISE_TOKEN`. Set `FILEWISE_URL` to the operator-provided origin. Resolve `FW` for every shell invocation; examples use shell placeholders, never guessed project/version IDs.

```bash
FW="${FILEWISE_BIN:-$HOME/.local/bin/filewise}"
"$FW" agent projects
"$FW" agent ls PROJECT
"$FW" agent versions PROJECT
"$FW" agent search PROJECT "budget" --mode hybrid --limit 10
"$FW" agent read PROJECT notes.md --version VERSION
"$FW" agent diff PROJECT BEFORE AFTER
"$FW" agent compile PROJECT --goal "Review the budget with cited evidence" --query "budget"
"$FW" agent quality PROJECT
```

`projects` returns an array. `ls` returns the concrete `version` and files. Search results and reads include source/fragment information; preserve the actual returned identifiers. For tasks use the returned task ID and version. Consult `agent verify --help` before selecting its checks/result fields; do not invent successful verification.

## Authorized edits

Read current content and obtain its concrete 64-character `version`. Confirm what original file will change. For a new file, use a unique user-approved path. For replacement, preserve any human-authored declarations and dependencies.

```bash
"$FW" agent write PROJECT notes.md --base VERSION \
  --request-id UNIQUE-preview --content "Approved text" \
  --dry-run --require-pass -m "Explain the authorized change"
```

Inspect `verification`, `saved`, `status` and coverage. A dry run stores an unsaved candidate. A passing preview does not lock originals; the final request still checks for drift.

```bash
"$FW" agent write PROJECT notes.md --base VERSION \
  --request-id UNIQUE-save --content "Approved text" \
  --require-pass -m "Explain the authorized change"
```

Preview and actual save MUST have different request IDs. Reusing an ID with changed content or flags fails. Retry a lost-response save using that save's exact ID and payload, not a new ID. Don't weaken checks to get a save through. A stale base requires another read and review; it is not permission to overwrite a human edit.

Use `--file PATH` for approved original bytes. A bulk JSON request uses `--request PATH`, with `base_version`, `request_id`, `message`, `changes`, and `require_pass`; it cannot be mixed with single-file flags. Its own JSON `dry_run` controls preview behavior.

## Decisions and evidence

- Exit 0 alone is not a completeness certificate. Inspect the structured decision and extraction fields.
- `BLOCKED`, `NEEDS_REVIEW`, `partial`, `no_text`, `unsupported` and `failed` must remain visible in your answer.
- Store a quoted result with project, path, concrete version, source ID and the exact returned locator. Do not transform a worksheet-cell locator into a guessed PDF page.
- Historical tasks are read-only. Model hindsight remains a separate issue.
- `agent read ... --output NEW_PATH` exports original bytes with a hash check and refuses overwrite. Ask before writing exports.
- `agent sync` captures authorized current originals. It is not a search precondition to run silently every time.
- Native historical restoration, vector semantic search and write-postflight verification are not available in the Go preview. Approval/publication are separate operator workflows.

## 中文要点

先通过网关读取具体版本，再检索、引用、比较或保存。预览与正式保存使用不同的请求 ID；同一请求重试保留原 ID 和完全相同的内容。明确报告提取不完整、证据缺失和检查未通过。自动保存是工作版本，不是正式发布。
