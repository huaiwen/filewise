"""Project-scoped native Agent connections and review copies. No model service required."""

import copy
import hashlib
import json
import os
import re
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from contextlib import ExitStack
from pathlib import Path

from .agent import sandbox_command
from .engine import FilewiseError, canonical, require
from .middleware import GUARD, hashes, replace_bytes
from .models import now
from .projects import DEFAULT_EXCLUDES, ProjectSpec, excluded, matches, relative_path, scan

AGENTS = {
    "codex": ("Codex", ".codex/hooks.json"),
    "claude": ("Claude Code", ".claude/settings.local.json"),
    "pi": ("Pi", ".pi/extensions/filewise.ts"),
}
EVENTS = ("SessionStart", "PreToolUse", "PostToolUse")
INSTRUCTION = "Filewise 已接入：关注文件的操作会进入审核副本。继续使用原来的相对文件名，修改完成后告诉用户到 Filewise 审核写回。不要直接修改原件、Filewise 私有状态或绕过受控工具。"


def fingerprint(body):
    return hashlib.sha256(body).hexdigest()


def configuration(body):
    try:
        value = json.loads(body or b"{}")
    except ValueError as exc:
        raise FilewiseError("已有 Agent 配置不是有效 JSON，未修改配置。") from exc
    if not isinstance(value, dict) or not isinstance(value.get("hooks", {}), dict):
        raise FilewiseError("Agent 配置的 hooks 必须为对象，未修改配置。")
    if any(not isinstance(items, list) for items in value.get("hooks", {}).values()):
        raise FilewiseError("Agent 的已有 hooks 不是列表，未修改配置。")
    return value


def managed_hooks(row):
    original = configuration(row["original"]).get("hooks", {})
    return {
        phase: [item for item in entries if item not in original.get(phase, [])]
        for phase, entries in configuration(row["installed"]).get("hooks", {}).items()
    }


class Connections:
    def __init__(self, middleware):
        self.middleware = middleware
        self.projects, self.engine = middleware.projects, middleware.engine
        with self.engine.connect(True) as db:
            db.executescript("""
                CREATE TABLE IF NOT EXISTS agent_connections(project_id TEXT NOT NULL, agent TEXT NOT NULL,
                    entry TEXT NOT NULL, original BLOB, installed BLOB NOT NULL, installed_at TEXT NOT NULL,
                    last_seen TEXT, last_tool TEXT, error TEXT, self_test TEXT, PRIMARY KEY(project_id,agent));
                CREATE TABLE IF NOT EXISTS agent_workspaces(id TEXT PRIMARY KEY, project_id TEXT NOT NULL,
                    agent TEXT NOT NULL, directory TEXT NOT NULL, baseline TEXT NOT NULL,
                    captured TEXT NOT NULL, last_release TEXT, created_at TEXT NOT NULL);
                CREATE TABLE IF NOT EXISTS agent_candidates(release_id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL);
            """)

    def project(self, project_id, actor):
        with self.engine.connect() as db:
            project = self.projects._project(db, project_id, actor)
        if not project["root"]:
            raise FilewiseError("连接 Agent 需要本地文件夹；上传项目只能观察上传后的文件版本。")
        return project

    def command(self, project_id, agent):
        return [
            str(Path(sys.executable).absolute()),
            "-m",
            "filewise",
            "--db",
            str(self.engine.path.resolve()),
            "agent-hook",
            project_id,
            agent,
        ]

    def generated(self, project_id, agent):
        if agent not in AGENTS:
            raise FilewiseError("Unknown Agent", 404)
        command = self.command(project_id, agent)
        if agent == "pi":
            return """// Filewise managed connection. Remove through the Filewise workbench.
import { execFileSync } from "node:child_process";
const command = CONFIG;
export default function(pi) {
  function call(event, ctx, phase) {
    const payload = {hook_event_name: phase, session_id: ctx.sessionManager.getSessionId(), cwd: ctx.cwd,
      tool_name: event.toolName, tool_use_id: event.toolCallId, tool_input: event.input};
    return JSON.parse(execFileSync(command[0], command.slice(1), {
      input: JSON.stringify(payload), encoding: "utf8", timeout: 30000, maxBuffer: 2 * 1024 * 1024
    }));
  }
  pi.on("session_start", async (event, ctx) => {
    try { call(event, ctx, "SessionStart"); ctx.ui.setStatus("filewise", "Filewise · 文件修改待审核"); }
    catch (error) { ctx.ui.setStatus("filewise", "Filewise · 连接异常"); }
  });
  pi.on("tool_call", async (event, ctx) => {
    const result = call(event, ctx, "PreToolUse");
    if (result.block) return {block: true, reason: result.reason};
    if (result.updatedInput) Object.assign(event.input, result.updatedInput);
  });
  pi.on("tool_result", async (event, ctx) => {
    const result = call(event, ctx, "PostToolUse");
    if (result.message) return {content: [...event.content, {type: "text", text: result.message}]};
  });
}
""".replace("CONFIG", json.dumps(command, ensure_ascii=False)).encode()
        return {
            phase: [
                {
                    "matcher": ".*",
                    "hooks": [
                        {
                            "type": "command",
                            "command": shlex.join(command)
                            + ' || { echo "Filewise hook failed; operation blocked" >&2; exit 2; }',
                            "timeout": 30,
                        }
                    ],
                }
            ]
            for phase in EVENTS + (("PostToolUseFailure",) if agent == "claude" else ())
        }

    def _read_config(self, project, agent):
        path = Path(project["root"], AGENTS[agent][1])
        if path.resolve() != path.absolute():
            raise FilewiseError("Agent 配置不能通过符号链接指向其他位置。", 409)
        if path.exists() and path.stat().st_size > 1024 * 1024:
            raise FilewiseError("Agent 配置超过 1 MiB。", 413)
        body = path.read_bytes() if path.exists() else None
        return path, body

    def status(self, project_id, actor):
        project = self.project(project_id, actor)
        with self.engine.connect() as db:
            rows = {
                r["agent"]: dict(r)
                for r in db.execute("SELECT * FROM agent_connections WHERE project_id=?", (project_id,))
            }
        items = []
        for agent, (title, entry) in AGENTS.items():
            row = rows.get(agent)
            configured, error = False, None
            if row:
                try:
                    _, body = self._read_config(project, agent)
                    configured = self._contains(project_id, agent, body)
                    if not configured:
                        error = "接入配置已缺失或改变，请重新连接。"
                except (ValueError, OSError, FilewiseError) as exc:
                    error = str(exc)
            items.append(
                {
                    "agent": agent,
                    "name": title,
                    "entry": entry,
                    "available": bool(shutil.which(agent)),
                    "installed": bool(row),
                    "configured": configured,
                    "state": "error"
                    if error or (row and row["error"])
                    else "verified"
                    if configured and row["last_tool"]
                    else "loaded"
                    if configured and row["last_seen"]
                    else "waiting"
                    if configured
                    else "not_connected",
                    "last_seen": row["last_seen"] if row else None,
                    "last_tool": row["last_tool"] if row else None,
                    "error": error or (row["error"] if row else None),
                    "self_test": json.loads(row["self_test"]) if row and row["self_test"] else None,
                }
            )
        return {
            "project_id": project_id,
            "root": project["root"],
            "agents": items,
            "coverage": "原生文件工具和前台 Shell；其他 MCP、已启动后台进程及手工终端操作不在此接入范围内。",
            "supported": sys.platform == "darwin",
        }

    def _contains(self, project_id, agent, body):
        generated = self.generated(project_id, agent)
        if agent == "pi":
            return body == generated
        config = configuration(body)
        return all(
            entry in config.get("hooks", {}).get(phase, [])
            for phase, entries in generated.items()
            for entry in entries
        )

    def preview(self, project_id, agent, actor):
        require(actor, "editor")
        project = self.project(project_id, actor)
        generated = self.generated(project_id, agent)
        self._read_config(project, agent)
        return {
            "agent": agent,
            "entry": AGENTS[agent][1],
            "scope": project["root"],
            "addition": generated.decode() if agent == "pi" else {"hooks": generated},
            "restart": "在这个文件夹中新开任务；Pi 也可以执行 /reload。",
            "reversible": True,
        }

    def install(self, project_id, agent, actor):
        require(actor, "editor")
        sandbox_command([], ["true"])
        project = self.project(project_id, actor)
        generated = self.generated(project_id, agent)
        with self.middleware.lock(project_id):
            _, original = self._read_config(project, agent)
            with self.engine.connect() as db:
                previous = db.execute(
                    "SELECT * FROM agent_connections WHERE project_id=? AND agent=?", (project_id, agent)
                ).fetchone()
            if self._contains(project_id, agent, original):
                if previous:
                    return self.status(project_id, actor)
            if agent == "pi":
                if (
                    original is not None
                    and original != generated
                    and (not previous or original != previous["installed"])
                ):
                    raise FilewiseError("已有同名 Pi 扩展，保留原文件；请先改名后再连接。", 409)
                body = generated
            else:
                config = configuration(original)
                config.setdefault("hooks", {})
                if previous:
                    for phase, entries in managed_hooks(previous).items():
                        if phase in config["hooks"]:
                            config["hooks"][phase] = [
                                item for item in config["hooks"][phase] if item not in entries
                            ]
                for phase, entries in generated.items():
                    current = config["hooks"].setdefault(phase, [])
                    current.extend(entry for entry in entries if entry not in current)
                body = (json.dumps(config, indent=2, ensure_ascii=False) + "\n").encode()
            # Persist the original before touching configuration so a failed install can be retried/reversed.
            with self.engine.connect(True) as db:
                db.execute(
                    "INSERT INTO agent_connections VALUES(?,?,?,?,?,?,NULL,NULL,NULL,NULL) ON CONFLICT(project_id,agent) DO UPDATE SET installed=excluded.installed,error=NULL",
                    (
                        project_id,
                        agent,
                        AGENTS[agent][1],
                        previous["original"] if previous else original,
                        body,
                        now(),
                    ),
                )
            replace_bytes(
                project["root"],
                AGENTS[agent][1],
                body,
                fingerprint(original) if original is not None else None,
            )
            with self.engine.connect(True) as db:
                self.engine._audit(
                    db,
                    project["scope_id"],
                    actor,
                    "agent.connected",
                    {"agent": agent, "entry": AGENTS[agent][1]},
                )
        return self.status(project_id, actor)

    def disconnect(self, project_id, agent, actor):
        require(actor, "editor")
        project = self.project(project_id, actor)
        with self.middleware.lock(project_id):
            with self.engine.connect() as db:
                row = db.execute(
                    "SELECT * FROM agent_connections WHERE project_id=? AND agent=?", (project_id, agent)
                ).fetchone()
            if not row:
                raise FilewiseError("此 Agent 尚未连接。", 404)
            _, body = self._read_config(project, agent)
            if agent == "pi":
                if body is not None and body != row["installed"]:
                    raise FilewiseError("扩展已有其他修改，未删除。", 409)
                restored = row["original"]
            elif body == row["installed"]:
                restored = row["original"]
            else:
                config = configuration(body)
                for phase, entries in managed_hooks(row).items():
                    if phase in config.get("hooks", {}):
                        config["hooks"][phase] = [
                            entry for entry in config["hooks"][phase] if entry not in entries
                        ]
                        if not config["hooks"][phase]:
                            del config["hooks"][phase]
                if config.get("hooks") == {}:
                    del config["hooks"]
                restored = (json.dumps(config, indent=2, ensure_ascii=False) + "\n").encode()
            replace_bytes(
                project["root"], AGENTS[agent][1], restored, fingerprint(body) if body is not None else None
            )
            with self.engine.connect(True) as db:
                db.execute(
                    "DELETE FROM agent_connections WHERE project_id=? AND agent=?", (project_id, agent)
                )
                self.engine._audit(db, project["scope_id"], actor, "agent.disconnected", {"agent": agent})
        return self.status(project_id, actor)

    def self_test(self, project_id, agent, actor):
        require(actor, "editor")
        status = next(item for item in self.status(project_id, actor)["agents"] if item["agent"] == agent)
        if not status["configured"]:
            raise FilewiseError("先连接 Agent，再运行连接自检。", 409)
        session_id = "self-test-" + os.urandom(12).hex()
        project = self.project(project_id, actor)
        event = {
            "hook_event_name": "PreToolUse",
            "session_id": session_id,
            "cwd": project["root"],
            "tool_name": "bash" if agent == "pi" else "Bash",
            "tool_input": {"command": "pwd"},
        }
        result = self.handle(project_id, agent, event, probe=True)
        updated = (
            result.get("updatedInput")
            if agent == "pi"
            else result.get("hookSpecificOutput", {}).get("updatedInput")
        )
        if not updated:
            raise FilewiseError("连接自检未能建立审核副本。", 409)
        key = fingerprint((project_id + "\0" + agent + "\0" + session_id).encode())
        with self.engine.connect() as db:
            workspace = dict(db.execute("SELECT * FROM agent_workspaces WHERE id=?", (key,)).fetchone())
        work = Path(workspace["directory"])
        script = "from pathlib import Path\nimport os\n"
        for path in (self.engine.path.resolve(), Path(project["root"])):
            script += (
                "try: os.open("
                + repr(str(path))
                + ", os.O_RDONLY)\nexcept PermissionError: pass\nelse: raise AssertionError('原路径读取未被限制')\n"
            )
        script += "p=Path('.filewise-tmp'); p.mkdir(exist_ok=True); (p/'probe').write_text('ok'); assert (p/'probe').read_text() == 'ok'\n"
        try:
            completed = subprocess.run(
                sandbox_command(
                    [project["root"], self.engine.path.parent],
                    [str(Path(sys.executable).absolute()), "-c", script],
                    worktree=work,
                ),
                cwd=work,
                capture_output=True,
                text=True,
                timeout=15,
            )
            if completed.returncode:
                raise FilewiseError("隔离检查未通过：" + completed.stderr[-600:], 409)
            report = {
                "passed": True,
                "at": now(),
                "checks": [
                    "项目配置已安装",
                    "工具操作已指向副本",
                    "原目录和私有数据库读取被拒绝",
                    "副本可以正常写入",
                ],
                "native_event_verified": False,
            }
            with self.engine.connect(True) as db:
                db.execute(
                    "UPDATE agent_connections SET self_test=?,error=NULL WHERE project_id=? AND agent=?",
                    (canonical(report), project_id, agent),
                )
            return report
        finally:
            shutil.rmtree(work)
            with self.engine.connect(True) as db:
                db.execute("DELETE FROM agent_workspaces WHERE id=?", (key,))

    def workspace(self, project_id, agent, session_id):
        if not isinstance(session_id, str) or not session_id or len(session_id) > 300:
            raise FilewiseError("Agent 没有提供有效的会话标识。")
        project = self.project(project_id, GUARD)
        key = fingerprint((project_id + "\0" + agent + "\0" + session_id).encode())
        with self.engine.connect() as db:
            row = db.execute("SELECT * FROM agent_workspaces WHERE id=?", (key,)).fetchone()
        spec = ProjectSpec.model_validate_json(project["spec"])
        if row:
            row = dict(row)
            with self.engine.connect() as db:
                active = db.execute(
                    "SELECT a.release_id FROM active a JOIN agent_candidates c ON c.release_id=a.release_id WHERE a.scope_id=? AND c.workspace_id=?",
                    (project["scope_id"], key),
                ).fetchone()
                if active and active[0] != row["baseline"]:
                    row["baseline"] = active[0]
                _, _, _, _, before = self.projects._snapshot(db, project_id, row["baseline"], GUARD)
            self.projects._live(project, before)
            work = Path(row["directory"])
            if not work.is_dir() or work.resolve() != work.absolute():
                raise FilewiseError("审核副本不可用，请在原文件夹新开 Agent 任务。", 409)
            with self.engine.connect(True) as db:
                db.execute("UPDATE agent_workspaces SET baseline=? WHERE id=?", (row["baseline"], key))
            return project, row
        originals, _ = scan(project["root"], DEFAULT_EXCLUDES + spec.excludes, spec.includes)
        parent = Path(project["root"], ".filewise", "workspaces")
        if parent.resolve() != parent.absolute():
            raise FilewiseError("审核副本目录不能是符号链接。", 409)
        parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        work = Path(tempfile.mkdtemp(prefix=agent + "-", dir=parent))
        try:
            for path, body in originals.items():
                replace_bytes(work, path, body, None)
                if Path(project["root"], path).stat().st_mode & 0o111:
                    (work / path).chmod(0o700)
            baseline_id = None
            with self.engine.connect() as db:
                candidates = db.execute(
                    "SELECT release_id,manifest FROM project_snapshots WHERE project_id=? AND "
                    "(release_id=(SELECT release_id FROM active WHERE scope_id=?) OR "
                    "release_id=(SELECT baseline FROM watches WHERE project_id=?)) ORDER BY created_at DESC",
                    (project_id, project["scope_id"], project_id),
                )
                for candidate in candidates:
                    previous = {p: value["sha256"] for p, value in json.loads(candidate["manifest"]).items()}
                    if previous == hashes(originals):
                        baseline_id = candidate["release_id"]
                        break
            if baseline_id is None:
                baseline_id = self.projects.snapshot(project_id, GUARD, originals, captured=True)[
                    "release_id"
                ]
            row = {
                "id": key,
                "project_id": project_id,
                "agent": agent,
                "directory": str(work),
                "baseline": baseline_id,
                "captured": canonical(hashes(originals)),
                "last_release": None,
                "created_at": now(),
            }
            with self.engine.connect(True) as db:
                db.execute(
                    "INSERT INTO agent_workspaces VALUES(:id,:project_id,:agent,:directory,:baseline,:captured,:last_release,:created_at)",
                    row,
                )
            return project, row
        except Exception:
            shutil.rmtree(work)
            raise

    def capture(self, project, workspace):
        spec = ProjectSpec.model_validate_json(project["spec"])
        files, _ = scan(workspace["directory"], DEFAULT_EXCLUDES + spec.excludes, spec.includes)
        current = canonical(hashes(files))
        if current == workspace["captured"]:
            return None
        snapshot = self.middleware.capture(
            project["id"], workspace["baseline"], files, GUARD, writer=workspace["agent"]
        )
        with self.engine.connect(True) as db:
            db.execute(
                "UPDATE agent_workspaces SET captured=?,last_release=? WHERE id=?",
                (current, snapshot["release_id"], workspace["id"]),
            )
            db.execute("INSERT INTO agent_candidates VALUES(?,?)", (snapshot["release_id"], workspace["id"]))
        return (
            "Filewise 已记录候选 "
            + snapshot["release_id"][:10]
            + "（"
            + snapshot["verification"]["decision"]
            + "）。原文件尚未改变，请到工作台审核写回。"
        )

    def rewrite(self, project, workspace, tool, inputs, cwd, *, prepare=True):
        root, work = Path(project["root"]), Path(workspace["directory"])
        spec = ProjectSpec.model_validate_json(project["spec"])
        result = copy.deepcopy(inputs)

        def mapped(value, directory=False):
            if not isinstance(value, str) or "\x00" in value:
                raise FilewiseError("工具没有提供有效的文件路径。")
            path = Path(os.path.abspath(Path(cwd, value).expanduser()))
            if path.is_relative_to(work):
                relative = path.relative_to(work)
            elif path.is_relative_to(root):
                relative = path.relative_to(root)
            else:
                return value
            if relative == Path(".") and directory:
                return str(work)
            name = relative_path(relative.as_posix())
            if excluded(name, DEFAULT_EXCLUDES + spec.excludes) or (
                not directory and not matches(name, spec.includes)
            ):
                raise FilewiseError("此路径不在 Filewise 关注范围内：" + name)
            target = work / relative
            if target.resolve() != target.absolute():
                raise FilewiseError("拒绝通过审核副本中的符号链接访问其他路径。", 409)
            return str(target)

        name = tool.rsplit("__", 1)[-1]
        if name in ("Bash", "bash", "exec_command", "shell", "shell_command"):
            command = inputs.get("command", inputs.get("cmd"))
            if isinstance(command, list):
                command = shlex.join(command)
            if not isinstance(command, str):
                raise FilewiseError("Shell 工具参数不受支持，未执行。")
            if inputs.get("run_in_background"):
                raise FilewiseError("关注项目请使用前台命令；后台写入无法在工具结束时可靠捕获。")
            shell_cwd = inputs.get("workdir", cwd)
            mapped_cwd = mapped(shell_cwd, True)
            if not Path(mapped_cwd).is_relative_to(work):
                raise FilewiseError("关注项目的 Shell 必须在项目或审核副本中运行。")
            scratch = work / ".filewise-tmp"
            if scratch.resolve() != scratch.absolute():
                raise FilewiseError("临时目录不能是符号链接。", 409)
            scratch.mkdir(exist_ok=True, mode=0o700)
            job = Path(tempfile.mkdtemp(prefix="shell-", dir=scratch)) if prepare else scratch / "probe"
            cli = self.command(project["id"], workspace["agent"])[:3]
            worker_pid = 0
            if prepare:
                # macOS hosts can forbid applying a nested sandbox. Prepare outside the tool sandbox,
                # but wait for the permitted tool's start signal before executing its command.
                with ExitStack() as stack:
                    streams = [
                        stack.enter_context(
                            os.fdopen(
                                os.open(
                                    job / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600
                                ),
                                "wb",
                            )
                        )
                        for name in ("stdout", "stderr", "done")
                    ]
                    done_fd = streams[2].fileno()
                    worker = subprocess.Popen(
                        [
                            *cli,
                            "agent-worker",
                            str(root),
                            str(work),
                            str(self.engine.path.parent.resolve()),
                            mapped_cwd,
                            str(job),
                            str(done_fd),
                            command,
                        ],
                        stdin=subprocess.DEVNULL,
                        stdout=streams[0],
                        stderr=streams[1],
                        start_new_session=True,
                        close_fds=True,
                        pass_fds=(done_fd,),
                    )
                    worker_pid = worker.pid
            wrapped = shlex.join([*cli, "agent-shell", str(job), str(worker_pid)])
            result["command" if "command" in inputs else "cmd"] = wrapped
            return result
        if name == "apply_patch":
            key = next((k for k in ("command", "patch", "input") if isinstance(inputs.get(k), str)), None)
            if key is None:
                raise FilewiseError("无法识别 apply_patch 参数，未执行。")
            paths = re.findall(
                r"(?m)^\*\*\* (?:Add File|Update File|Delete File|Move to): (.+)$", inputs[key]
            )
            inside = [Path(os.path.abspath(Path(cwd, value))).is_relative_to(root) for value in paths]
            if any(inside) and not all(inside):
                raise FilewiseError("请把关注目录和其他目录的修改拆成不同补丁。")
            if paths and not any(inside):
                return None
            result[key] = re.sub(
                r"(?m)^(\*\*\* (?:Add File|Update File|Delete File|Move to): )(.+)$",
                lambda m: m[1] + mapped(m[2]),
                inputs[key],
            )
            return result
        if name in (
            "Read",
            "Write",
            "Edit",
            "MultiEdit",
            "NotebookEdit",
            "read",
            "write",
            "edit",
            "Grep",
            "Glob",
            "grep",
            "find",
            "ls",
        ):
            directory = name in ("Grep", "Glob", "grep", "find", "ls")
            key = next((k for k in ("file_path", "path", "notebook_path") if k in result), "path")
            result[key] = mapped(result.get(key, "."), directory)
            return result if result != inputs else None
        if name in ("PowerShell", "powershell"):
            raise FilewiseError("本机 Filewise 连接尚不支持 PowerShell，请使用 Bash。")
        return None

    def handle(self, project_id, agent, event, *, probe=False):
        if agent not in AGENTS or not isinstance(event, dict):
            raise FilewiseError("Invalid hook request")
        phase, tool = event.get("hook_event_name"), event.get("tool_name", "")
        message, updated = None, None
        try:
            with self.middleware.lock(project_id):
                self.middleware.require_recovered(project_id)
                with self.engine.connect() as db:
                    installed = db.execute(
                        "SELECT 1 FROM agent_connections WHERE project_id=? AND agent=?", (project_id, agent)
                    ).fetchone()
                if not installed:
                    raise FilewiseError("Filewise 连接已断开，请重载 Agent 配置后继续。")
                config = self.middleware.config(project_id, GUARD)["config"]
                if not config["enabled"] or config["mode"] == "observe":
                    # Keep an installed write gate fail-closed until explicitly disconnected.
                    raise FilewiseError("Filewise 受控修改已暂停。请在工作台启用，或断开该 Agent 连接。")
                project, workspace = self.workspace(project_id, agent, event.get("session_id"))
                if phase == "SessionStart":
                    message = INSTRUCTION
                elif phase == "PreToolUse":
                    if not isinstance(event.get("tool_input"), dict):
                        raise FilewiseError("无法识别工具参数，未执行。")
                    updated = self.rewrite(
                        project,
                        workspace,
                        tool,
                        event["tool_input"],
                        event.get("cwd") or project["root"],
                        prepare=not probe,
                    )
                elif phase in ("PostToolUse", "PostToolUseFailure"):
                    message = self.capture(project, workspace)
                else:
                    raise FilewiseError("Unsupported hook event")
                if not probe:
                    with self.engine.connect(True) as db:
                        db.execute(
                            "UPDATE agent_connections SET last_seen=?,last_tool=COALESCE(?,last_tool),error=NULL WHERE project_id=? AND agent=?",
                            (now(), tool if updated else None, project_id, agent),
                        )
            if agent == "pi":
                return {"updatedInput": updated, "message": message}
            output = {"hookEventName": phase}
            if updated:
                output["updatedInput"] = updated
                if agent == "codex":
                    output["permissionDecision"] = "allow"
            if message:
                output["additionalContext"] = message
            return {"hookSpecificOutput": output}
        except (FilewiseError, ValueError, OSError) as exc:
            if not probe:
                with self.engine.connect(True) as db:
                    db.execute(
                        "UPDATE agent_connections SET error=? WHERE project_id=? AND agent=?",
                        (str(exc)[:1000], project_id, agent),
                    )
            if phase != "PreToolUse":
                raise
            if agent == "pi":
                return {"block": True, "reason": str(exc)}
            return {
                "hookSpecificOutput": {
                    "hookEventName": "PreToolUse",
                    "permissionDecision": "deny",
                    "permissionDecisionReason": str(exc),
                }
            }


def shell_worker(root, work, private_state, cwd, job, done_fd, command):
    """One prepared command, started only when its permitted native tool signals readiness."""
    code = 1
    try:
        root, work, cwd, job = Path(root), Path(work), Path(cwd), Path(job)
        if job.parent != work / ".filewise-tmp" or job.resolve() != job.absolute():
            raise FilewiseError("Shell 任务位置无效。", 403)
        deadline = time.monotonic() + 600
        while not (job / "start").exists():
            if time.monotonic() > deadline:
                raise FilewiseError("等待工具许可已超时，请重新执行工具。")
            time.sleep(0.1)
        if not work.is_relative_to(root / ".filewise/workspaces") or work.resolve() != work.absolute():
            raise FilewiseError("审核副本位置无效。", 403)
        if not cwd.is_relative_to(work) or cwd.resolve() != cwd.absolute():
            raise FilewiseError("Shell 目录超出审核副本。", 403)
        invocation = sandbox_command([root, private_state], ["/bin/sh", "-c", command], worktree=work)
        env = {k: v for k, v in os.environ.items() if not k.startswith("FILEWISE_")}
        env.update(TMPDIR=str(work / ".filewise-tmp"), PYTHONDONTWRITEBYTECODE="1")
        sender_fd = os.open(job / "start", os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(sender_fd) as sender:
            sender_pid = int(sender.read(32))
        if sender_pid <= 1:
            raise FilewiseError("无效的工具进程标识。")
        with subprocess.Popen(invocation, cwd=cwd, env=env, start_new_session=True) as child:
            while child.poll() is None:
                try:
                    os.kill(sender_pid, 0)
                except ProcessLookupError:
                    os.killpg(child.pid, signal.SIGTERM)
                    try:
                        child.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        os.killpg(child.pid, signal.SIGKILL)
                    break
                time.sleep(0.1)
            code = child.wait()
    except Exception as exc:
        print(str(exc), file=sys.stderr, flush=True)
    finally:
        # All output descriptors were opened before returning control to the native tool.
        os.write(done_fd, str(code).encode())
        os.close(done_fd)
    return code


def run_shell(job, worker_pid):
    """Native tool side: signal execution, stream output, never open private DB or apply a sandbox."""
    job = Path(job)
    if job.resolve() != job.absolute() or job.parent.name != ".filewise-tmp":
        raise FilewiseError("Shell 任务位置无效。", 403)
    replace_bytes(job, "start", str(os.getpid()).encode(), None)
    positions = {"stdout": 0, "stderr": 0}
    while True:
        done = (job / "done").stat().st_size > 0
        for name, output in (("stdout", sys.stdout), ("stderr", sys.stderr)):
            try:
                fd = os.open(job / name, os.O_RDONLY | os.O_NOFOLLOW)
            except FileNotFoundError:
                continue
            with os.fdopen(fd, "rb") as stream:
                stream.seek(positions[name])
                data = stream.read(65536)
                positions[name] += len(data)
            output.buffer.write(data)
            output.flush()
        if done:
            # Drain remaining output before returning the child's status.
            if any((job / name).stat().st_size > offset for name, offset in positions.items()):
                continue
            return int((job / "done").read_text())
        try:
            os.kill(worker_pid, 0)
        except ProcessLookupError as exc:
            raise FilewiseError("Filewise 执行进程已退出，副本未自动写回，请重新执行工具。") from exc
        except PermissionError:
            pass  # Some native sandboxes deny process metadata; the host still controls tool timeout.
        time.sleep(0.1)
