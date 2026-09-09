import io
import json
import shlex
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from fastapi.testclient import TestClient

from filewise.api import create_app
from filewise.connections import AGENTS, Connections
from filewise.engine import Engine, FilewiseError
from filewise.middleware import Middleware, WatchConfig
from filewise.models import Actor
from filewise.projects import Projects


class ConnectionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve() / "files"
        self.root.mkdir()
        (self.root / "notes.md").write_text("100")
        self.owner = Actor(id="owner", roles={"reader", "editor", "reviewer", "publisher"})
        self.engine = Engine(self.root / ".filewise/filewise.db")
        self.projects = Projects(self.engine)
        self.projects.create({"id": "test", "name": "Files"}, self.owner, self.root)
        self.middleware = Middleware(self.projects)
        self.middleware.configure("test", WatchConfig(), self.owner)
        self.connections = Connections(self.middleware)
        self.addCleanup(self.middleware.close)
        self.worker = self.enterContext(patch("filewise.connections.subprocess.Popen"))
        self.worker.return_value.pid = 123456

    def install(self, agent):
        # Configuration tests are portable; the example exercises the actual macOS sandbox.
        with patch("filewise.connections.sandbox_command", return_value=["true"]):
            return self.connections.install("test", agent, self.owner)

    def event(self, agent, phase, tool="Write", inputs=None, session="task-1"):
        return self.connections.handle(
            "test",
            agent,
            {
                "hook_event_name": phase,
                "session_id": session,
                "cwd": str(self.root),
                "tool_name": tool,
                "tool_input": inputs or {"file_path": str(self.root / "notes.md"), "content": "120"},
            },
        )

    def test_three_native_protocols_route_repeat_edits_and_publish(self):
        for agent in AGENTS:
            with self.subTest(agent=agent):
                self.install(agent)
                before = len(self.projects.detail("test", self.owner)["snapshots"])
                self.event(agent, "SessionStart")
                self.assertEqual(len(self.projects.detail("test", self.owner)["snapshots"]), before)
                inputs = (
                    {"path": str(self.root / "notes.md"), "content": "120"}
                    if agent == "pi"
                    else {"file_path": str(self.root / "notes.md"), "content": "120"}
                )
                result = self.event(
                    agent, "PreToolUse", tool="write" if agent == "pi" else "Write", inputs=inputs
                )
                updated = (
                    result["updatedInput"] if agent == "pi" else result["hookSpecificOutput"]["updatedInput"]
                )
                target = Path(updated.get("path", updated.get("file_path")))
                self.assertTrue(target.is_relative_to(self.root / ".filewise/workspaces"))
                target.write_text("120")
                self.event(agent, "PostToolUse")
                self.assertEqual((self.root / "notes.md").read_text(), "100")
                repeated = self.event(
                    agent, "PreToolUse", tool="read" if agent == "pi" else "Read", inputs=inputs
                )
                next_input = (
                    repeated["updatedInput"]
                    if agent == "pi"
                    else repeated["hookSpecificOutput"]["updatedInput"]
                )
                self.assertEqual(next_input, updated)
                target.write_text("130")
                self.event(agent, "PostToolUse")
                rid = self.middleware.config("test", self.owner)["last_event"]["release_id"]
                self.projects.approve("test", rid, self.owner)
                active = self.projects.detail("test", self.owner)["active_release"]
                self.middleware.apply("test", rid, active, self.owner)
                self.assertEqual((self.root / "notes.md").read_text(), "130")
                # The same Agent session can continue after its candidate has been written back.
                self.event(agent, "PreToolUse", inputs=inputs)
                target.write_text("100")
                self.event(agent, "PostToolUse")
                reset = self.middleware.config("test", self.owner)["last_event"]["release_id"]
                self.projects.approve("test", reset, self.owner)
                self.middleware.apply("test", reset, rid, self.owner)
                status = next(
                    a for a in self.connections.status("test", self.owner)["agents"] if a["agent"] == agent
                )
                self.assertEqual(status["state"], "verified")

    def test_install_disconnect_preserves_existing_and_later_configuration(self):
        for agent in ("codex", "claude"):
            with self.subTest(agent=agent):
                path = self.root / AGENTS[agent][1]
                path.parent.mkdir(parents=True, exist_ok=True)
                existing = {
                    "hooks": {
                        "PreToolUse": [
                            {"matcher": "Bash", "hooks": [{"type": "command", "command": "echo preserve"}]}
                        ]
                    },
                    "unrelated": True,
                }
                original = json.dumps(existing).encode()
                path.write_bytes(original)
                self.install(agent)
                first = path.read_bytes()
                self.install(agent)
                self.assertEqual(path.read_bytes(), first)
                self.assertNotIn("permissions", json.loads(first))
                self.connections.disconnect("test", agent, self.owner)
                self.assertEqual(path.read_bytes(), original)
                self.install(agent)
                changed = json.loads(path.read_text())
                changed["later-setting"] = "keep"
                path.write_text(json.dumps(changed))
                self.connections.disconnect("test", agent, self.owner)
                self.assertEqual(json.loads(path.read_text()), {**existing, "later-setting": "keep"})
                # Relocating Filewise must replace its old command, not leave two active write gates.
                self.install(agent)
                relocated = self.connections.command("test", agent)
                relocated[0] = "/new-filewise/bin/python"
                with patch.object(self.connections, "command", return_value=relocated):
                    self.install(agent)
                    configured = json.loads(path.read_text())
                    self.assertEqual(len(configured["hooks"]["PreToolUse"]), 2)
                    self.assertIn(
                        "/new-filewise/bin/python",
                        configured["hooks"]["PreToolUse"][1]["hooks"][0]["command"],
                    )
                configured["another-setting"] = True
                path.write_text(json.dumps(configured))
                self.connections.disconnect("test", agent, self.owner)
                self.assertEqual(
                    json.loads(path.read_text()),
                    {**existing, "later-setting": "keep", "another-setting": True},
                )
        path = self.root / AGENTS["pi"][1]
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("existing user's extension")
        with self.assertRaises(FilewiseError):
            self.install("pi")
        self.assertEqual(path.read_text(), "existing user's extension")
        path.unlink()
        self.install("pi")
        relocated = self.connections.command("test", "pi")
        relocated[0] = "/new-filewise/bin/python"
        with patch.object(self.connections, "command", return_value=relocated):
            self.install("pi")
            self.assertIn("/new-filewise/bin/python", path.read_text())
        self.connections.disconnect("test", "pi", self.owner)
        self.assertFalse(path.exists())

    def test_block_private_paths_mixed_patch_and_symlink_without_modifying_origins(self):
        self.install("codex")
        result = self.event(
            "codex",
            "PreToolUse",
            tool="apply_patch",
            inputs={"command": "*** Begin Patch\n*** Update File: notes.md\n@@\n-100\n+120\n*** End Patch"},
        )
        self.assertIn("/.filewise/workspaces/", result["hookSpecificOutput"]["updatedInput"]["command"])
        for path in (".filewise/tokens.json", "../../../outside.md"):
            result = self.event(
                "codex",
                "PreToolUse",
                tool="apply_patch",
                inputs={
                    "command": "*** Begin Patch\n*** Add File: notes.md\n+x\n*** Add File: "
                    + path
                    + "\n+x\n*** End Patch"
                },
            )
            self.assertEqual(result["hookSpecificOutput"]["permissionDecision"], "deny")
        workspace = self.connections.workspace("test", "codex", "task-1")[1]
        link = Path(workspace["directory"], "link.md")
        link.symlink_to(self.root / "notes.md")
        result = self.event("codex", "PreToolUse", inputs={"file_path": str(link), "content": "unsafe"})
        self.assertEqual(result["hookSpecificOutput"]["permissionDecision"], "deny")
        self.assertEqual((self.root / "notes.md").read_text(), "100")

    def test_invalid_configuration_failed_install_and_credentials_keep_originals(self):
        from filewise.cli import local_tokens

        path = self.root / AGENTS["codex"][1]
        path.parent.mkdir()
        for body in (b"not json", b"[]", b'{"hooks":{"PreToolUse":null}}'):
            path.write_bytes(body)
            with self.assertRaises(FilewiseError):
                self.install("codex")
            self.assertEqual(path.read_bytes(), body)
        original = b'{"unrelated": true}\n'
        path.write_bytes(original)
        with patch("filewise.connections.replace_bytes", side_effect=OSError("interrupted install")):
            with self.assertRaises(OSError):
                self.install("codex")
        self.assertEqual(path.read_bytes(), original)
        self.install("codex")
        self.connections.disconnect("test", "codex", self.owner)
        self.assertEqual(path.read_bytes(), original)
        (self.engine.path.parent / "tokens.json").symlink_to(self.root / "notes.md")
        with self.assertRaises(FilewiseError):
            local_tokens(self.engine.path.parent)
        self.assertEqual((self.root / "notes.md").read_text(), "100")

    def test_shell_quoting_and_scope_are_fixed_not_selected_by_model(self):
        self.install("codex")
        command = "printf '%s' 'a; $HOME `echo unsafe`'"
        result = self.event("codex", "PreToolUse", tool="Bash", inputs={"command": command})
        wrapped = shlex.split(result["hookSpecificOutput"]["updatedInput"]["command"])
        self.assertEqual(wrapped[:3], self.connections.command("test", "codex")[:3])
        self.assertEqual(wrapped[3], "agent-shell")
        self.assertTrue(Path(wrapped[4]).is_relative_to(self.root / ".filewise/workspaces"))
        self.assertNotIn(str(self.engine.path), wrapped)
        self.assertEqual(self.worker.call_args.args[0][-1], command)
        self.assertEqual(self.worker.call_args.args[0][4], str(self.root))
        blocked = self.event(
            "codex", "PreToolUse", tool="Bash", inputs={"command": "echo unsafe", "run_in_background": True}
        )
        self.assertEqual(blocked["hookSpecificOutput"]["permissionDecision"], "deny")

    def test_stale_original_and_paused_connection_block_before_use(self):
        self.install("pi")
        self.event("pi", "SessionStart")
        (self.root / "notes.md").write_text("external")
        result = self.event("pi", "PreToolUse", tool="write", inputs={"path": "notes.md", "content": "120"})
        self.assertTrue(result["block"])
        self.assertIn("STALE", result["reason"])
        self.middleware.configure("test", WatchConfig(enabled=False), self.owner)
        result = self.event("pi", "PreToolUse", session="other")
        self.assertTrue(result["block"])
        self.assertEqual((self.root / "notes.md").read_text(), "external")

    def test_local_setup_is_authenticated_opt_in_and_config_does_not_mean_verified(self):
        owner = {"Authorization": "Bearer " + "o" * 32}
        agent = {"Authorization": "Bearer " + "a" * 32}
        tokens = {"o" * 32: self.owner, "a" * 32: Actor(id="agent", roles={"reader"}, audience="agent")}
        client = TestClient(create_app(self.engine, tokens))
        self.assertEqual(
            client.post("/api/setup/project", headers=owner, json={"root": str(self.root)}).status_code, 403
        )
        self.assertEqual(client.get("/api/projects/test/connections", headers=agent).status_code, 403)
        client = TestClient(create_app(self.engine, tokens, local_setup=True))
        self.assertEqual(
            client.post(
                "/api/setup/project", headers=owner, json={"root": str(self.root / "missing")}
            ).status_code,
            400,
        )
        directory = self.root.parent / "second"
        directory.mkdir()
        (directory / "hello.md").write_text("hello")
        response = client.post("/api/setup/project", headers=owner, json={"root": str(directory)})
        self.assertEqual(response.status_code, 201)
        self.assertTrue(response.json()["snapshots"])
        self.install("claude")
        status = next(
            a for a in self.connections.status("test", self.owner)["agents"] if a["agent"] == "claude"
        )
        self.assertEqual(status["state"], "waiting")
        self.event("claude", "SessionStart")
        status = next(
            a for a in self.connections.status("test", self.owner)["agents"] if a["agent"] == "claude"
        )
        self.assertEqual(status["state"], "loaded")

    def test_hook_failure_exit_is_blocking_and_start_opens_no_external_service(self):
        from filewise.cli import main

        with (
            patch("sys.stdin", io.TextIOWrapper(io.BytesIO(b'{"hook_event_name":"PreToolUse"}'))),
            patch("sys.stderr", new_callable=io.StringIO),
            patch("sys.stdout", new_callable=io.StringIO),
        ):
            self.assertEqual(main(["--db", str(self.engine.path), "agent-hook", "test", "codex"]), 0)
        # Valid pre event with missing connection returns a deny JSON, rather than fail-open exit 1.
        with (
            patch("sys.stdin", io.TextIOWrapper(io.BytesIO(b"malformed"))),
            patch("sys.stderr", new_callable=io.StringIO),
        ):
            self.assertEqual(main(["--db", str(self.engine.path), "agent-hook", "test", "codex"]), 2)
        with patch("uvicorn.Server") as server, patch("sys.stderr", new_callable=io.StringIO) as output:
            self.assertEqual(
                main(["--db", str(self.root.parent / "start-state/db"), "start", "--no-open"]), 0
            )
            self.assertEqual(server.call_args.args[0].host, "127.0.0.1")
            self.assertIn("/#connect=", output.getvalue())
        with (
            patch("uvicorn.Server") as server,
            patch("threading.Thread") as thread,
            patch("webbrowser.open") as browser,
            patch("sys.stderr", new_callable=io.StringIO),
        ):
            main(["--db", str(self.root.parent / "start-state/db"), "start"])
            server.return_value.started = False
            server.return_value.should_exit = True
            thread.call_args.kwargs["target"]()
            browser.assert_not_called()
            server.return_value.started = True
            thread.call_args.kwargs["target"]()
            self.assertTrue(browser.call_args.args[0].startswith("http://127.0.0.1:8000/#connect="))


if __name__ == "__main__":
    unittest.main()
