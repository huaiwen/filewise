# Agent Skills 与 npm 安装器

[English](#english) · [原生网关指南](go.md)

## 当前状态

npm 安装器与 Skill 已提供源码，**尚未发布到 npm**。Skill 已通过本地 Go CLI/HTTP 链路验证；当前 Go 二进制同样未公开发行。先构建 Go 程序，再仅安装 Skill，不回退到历史运行时。

## 本地试用

在 Filewise 源码根目录执行：

```bash
make build
export FILEWISE_BIN="$PWD/build/filewise"
PACK_DIR=$(mktemp -d)
npm pack --ignore-scripts --pack-destination "$PACK_DIR"
npm exec --yes --package "$PACK_DIR/filewise-0.3.0-dev.tgz" -- \
  filewise skills --providers codex --dir /absolute/path/to/your/project
```

需要 Node.js 22+。上述 `skills` 命令只安装 Skill，不下载程序。`FILEWISE_BIN` 指向已构建的 Go 二进制；不会注册文件夹、启动服务、写入令牌或修改开发工具 hooks。

Go 二进制与 npm 均实际发布后，完整安装入口为：

```bash
npx filewise install --providers codex,claude,cursor
```

终端交互模式省略 `--providers` 时会询问平台；脚本模式必须明确指定。默认项目级安装。所有平台共用一份 Skill 正文。

| 平台 | 项目级目录 | 调用 |
| --- | --- | --- |
| Codex | `.agents/skills/filewise/` | `$filewise init` 或 `/skills` |
| Claude Code | `.claude/skills/filewise/` | `/filewise init` |
| Cursor | `.cursor/skills/filewise/` | `/filewise init` |

用户级目录位于 `$HOME` 下同名路径；`--scope user` 显式选择。Cursor 也能发现 `.agents`/`.claude` 中的兼容 Skill；同时安装多个平台时可能看到重复入口，可只保留一个 Cursor 能发现的副本。

## 安装器命令

以下以源码调用表示，发布后可将 `node npm/cli.mjs` 换成 `npx filewise`：

```bash
node npm/cli.mjs skills --providers codex --scope user
# 仅在对应 Go 标签实际发布后运行：
node npm/cli.mjs install --providers claude --bin-dir "$HOME/bin" --native-version v0.3.0-dev
node npm/cli.mjs doctor
```

- `install`：调用随包分发的 Go 安装器，固定原生版本并校验下载；Go Release 不存在时失败，Skill 不改动。
- `skills`：只安装 Skill，不下载程序。已有内容完全相同则保留；内容不同则停止，先由用户备份并移走旧目录。无 `--force` 覆盖。
- `doctor`：检查指定路径的 `(Go)` 版本、Skill 文件和令牌是否设置，不读取数据库或发送网络请求。历史或无效程序退出 1。
- `--dir`：现有项目目录；与 `--scope user` 不可同时使用。
- `--bin-dir`：绝对安装路径；也可使用 `FILEWISE_INSTALL_DIR`。自定义安装路径时，给 Agent 环境设置 `FILEWISE_BIN` 为可执行文件的绝对路径。
- 升级二进制前停止所有 Filewise 实例，包括自定义数据库实例。原生安装失败时不改 Skill；已修改的 Skill 或不安全路径会在下载前拒绝。多目录安装不是跨目录原子事务，成功路径会逐条输出。

## 连接与权限

安装完成后，按 [操作员接入步骤](../skills/filewise/references/setup.md) 建立项目网关与专用凭证，再重新加载开发工具并调用 Skill。

`init` 是 Skill 的连接检查流程，不是原生 CLI 的 `filewise init` 子命令。它检查程序、验证已提供的项目令牌、选择有权访问的项目，并读取具体版本。缺少授权时由操作员完成接入，不会借用工作台操作员身份。

只把 `FILEWISE_URL` 和单个项目 `FILEWISE_TOKEN` 提供给 Agent。凭证不写进仓库；需要受限运行时，将数据库、完整凭据和非授权原件隔离到 Agent 无权访问的 OS/容器环境。Skill 本身不是权限沙箱。云端 Agent 的 localhost 不是用户电脑，远端网关必须使用 HTTPS。

Skill 支持 init、search、read、diff、save、verify；底层语法见 [命令参考](../skills/filewise/references/commands.md)。只读身份不能保存。预览和正式保存使用不同请求 ID；对同一次保存重试则保留原 ID 与完全相同的请求。

当前验证覆盖目录安装、包内容、原生 CLI/HTTP 流程；各开发工具中的真实模型调用需要分别验收，不等同于已完成所有宿主平台的端到端认证。

## 发布与官网

- npm 包名拟为 `filewise`；发布前确认名称归属、登录账户、包内容和用户授权。未新增许可证；许可安排应在对外分发前明确。
- 无自动 npm 发布钩子或工作流。本次本地打包不等于公开发布。
- 官网源码在 `site/`，根目录执行 `npm run site`，在本机预览。部署说明在 `site/README.md`。
- 官网 npm 标签在真实发布前保留“尚未发布”，不提供一键复制未上线的安装命令。

---

## English

The npm installer and portable Skill are **not yet published**; neither is the Go binary. Run `make build`, set `FILEWISE_BIN` to the absolute `build/filewise` path, pack with `npm pack --ignore-scripts`, then use `npm exec --package /absolute/path/to/filewise-0.3.0-dev.tgz -- filewise skills --providers codex --dir /absolute/project`.

Node.js 22+ is needed only for this distribution entrypoint. After a Go release exists, `install` downloads its pinned native binary through the Go-only Bash installer. Missing releases fail without falling back to Rust. No compilation, service startup, folder enrollment, profile edits, hooks or credential storage occurs.

Use `--providers codex,claude,cursor`, optionally `--scope user`, `--dir` for project scope, and `--bin-dir` for a binary location. `skills` does not download a runtime; `doctor` requires a `(Go)` version and performs no network request. Stop every instance before binary replacement. Identical Skill files are preserved; modified files, symlinks and unsafe directories are refused. An interrupted multi-directory install can leave previously reported successes.

Codex discovers `.agents/skills/filewise` and invokes `$filewise init`; Claude Code and Cursor use `.claude/skills/filewise` and `.cursor/skills/filewise`, with `/filewise init`. User scope uses the equivalent paths under HOME. Cursor can also discover compatible Codex/Claude skill directories, so keep one discoverable copy if duplicate entries appear.

Follow the [operator setup](../skills/filewise/references/setup.md) before running init. This is a Skill workflow, not a native `filewise init` command. It checks the executable, uses an already-provided project credential, selects an accessible project and reads its version. Never give an agent the browser's operator token or the complete credential map. A remote/cloud agent needs a reachable HTTPS gateway, not the laptop's localhost. Enforce OS/container isolation separately.

One canonical [command reference](../skills/filewise/references/commands.md) covers search, read, diff, save and verify. Preview and actual save require separate request IDs; retries of the same request keep the same ID and payload. Installation and CLI tests do not certify every host's real model invocation.

The bilingual static website lives in `site/`; use `npm run site` for loopback-only preview. No website deployment or npm publication is implicit. The package has no newly granted license; confirm distribution terms and package ownership before public release.
