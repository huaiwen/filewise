# Connect an agent · 连接 Agent

The skill uses the native Go project gateway. Build the current Go preview, set `FILEWISE_BIN` to its absolute `build/filewise` path, and check that `--version` includes `(Go)`. Go binaries are not yet publicly released; do not substitute historical v0.2.0. `init` checks the connection; it does not create an administrative credential or register a directory.

## Human operator: first connection

Run these steps in your own terminal, outside a restricted agent environment. Start with a separate practice directory. The gateway's private state must be outside both the managed folder and the agent's accessible filesystem.

```bash
FW="${FILEWISE_BIN:-$HOME/.local/bin/filewise}"
DEMO=$(mktemp -d "$HOME/filewise-agent-demo.XXXXXX")
mkdir -p "$DEMO/files"
printf '%s\n' '# Project notes' 'Budget: 120 units.' > "$DEMO/files/notes.md"
"$FW" --db "$DEMO/state/filewise.db" auth-init --out "$DEMO/state/tokens.json"
"$FW" --db "$DEMO/state/filewise.db" project add "$DEMO/files" --id demo --name "Filewise demo"
"$FW" --db "$DEMO/state/filewise.db" project sync demo
"$FW" --db "$DEMO/state/filewise.db" auth-agent demo --tokens "$DEMO/state/tokens.json" --read-only
```

The last command returns ONE scoped `FILEWISE_TOKEN`. Keep it in your operator terminal; do not paste it into chat or commit it. `--read-only` is the recommended starting point. To permit approved saves later, issue another project credential without `--read-only` and restart the gateway so it reads the updated token map.

Start the gateway in this same operator terminal:

```bash
"$FW" --db "$DEMO/state/filewise.db" serve --tokens "$DEMO/state/tokens.json" --port 8000
```

The browser service (`filewise start`, normally port 8733) is a separate default state/identity. Its `#token=` URL is an operator credential: NEVER use it as the agent credential. If port 8000 is occupied, choose a free port and use that exact origin below. Existing projects should be connected by their operator instead of registered twice.

## Launch the coding tool with its scoped environment

In the terminal that will launch Codex, Claude Code or Cursor, enter the single project token privately. This example uses Bash; it keeps the token out of shell command history and arguments:

```bash
export FILEWISE_URL=http://127.0.0.1:8000
read -r -s -p 'Project token: ' FILEWISE_TOKEN
printf '\n'
export FILEWISE_TOKEN
FW="${FILEWISE_BIN:-$HOME/.local/bin/filewise}"
"$FW" agent projects
```

Launch your coding tool from that environment. For an already-running IDE, configure the variable in its documented secret/environment mechanism and restart the relevant agent process. Do not save the token in project configuration, `AGENTS.md`, `SKILL.md` or a tracked `.env`.

Then invoke `$filewise init` in Codex or `/filewise init` in Claude Code/Cursor. Select `demo`; ask it to find the budget and cite its source. A read-only credential can search/read/compare but cannot save.

Remote/cloud coding sessions cannot reach your laptop through their own `127.0.0.1`. They need an explicitly configured HTTPS gateway reachable from that environment. This installer does not open ports or create tunnels.

The gateway enforces project roles. A skill is not a sandbox: use separate OS/container permissions for agents that must not access operator storage directly.

## 安装与初始化

- 安装 Skill 后重新加载开发工具。Codex 使用 `$filewise init`；Claude Code、Cursor 使用 `/filewise init`。
- 初次使用由操作员在自己的终端注册目录、建立初始版本并签发项目令牌。上面的示例创建独立练习目录，默认只读。
- 只向 Agent 环境提供这一条项目令牌和网关地址；不要提供完整凭据文件、数据库或浏览器工作台的操作员令牌，也不要在聊天里发送令牌。
- `init` 检查程序和连接、选择有权访问的项目并读取版本；不会自动增加权限、监控目录或修改原文件。
- 默认 CLI 网关为 8000 端口，浏览器工作台默认 8733，两者默认使用不同状态目录。云端 Agent 的 localhost 不是你的电脑。

Full native guide: https://github.com/huaiwen/filewise/blob/main/docs/go.md
