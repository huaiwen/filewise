import base64
import hashlib
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from fastapi.testclient import TestClient

from filewise.api import create_app
from filewise.engine import Engine, FilewiseError
from filewise.middleware import GUARD, Middleware, WatchConfig, replace_bytes
from filewise.models import Actor
from filewise.projects import Projects, scan


class MiddlewareTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve() / "files"
        self.root.mkdir()
        (self.root / "a.md").write_text("100")
        self.engine = Engine(self.root / ".filewise" / "filewise.db")
        self.projects = Projects(self.engine)
        self.owner = Actor(id="owner", roles={"reader", "editor", "reviewer", "publisher"})
        self.reader = Actor(id="agent", roles={"reader"}, audience="agent")
        self.projects.create(
            {"id": "test", "name": "Files", "includes": ["**/*.md", "*.json", "*.xlsx"]},
            self.owner,
            self.root,
        )
        self.middleware = Middleware(self.projects)
        self.middleware.configure("test", WatchConfig(settle_seconds=0.2), self.owner)

    def proposal(self, files):
        # Exercise the real working-copy command; OS isolation is separately checked by the macOS example.
        script = "from pathlib import Path\n" + "\n".join(
            f"Path({name!r}).write_bytes({body!r})" if body is not None else f"Path({name!r}).unlink()"
            for name, body in files.items()
        )
        with patch("filewise.middleware.sandbox_command", side_effect=lambda paths, command: command):
            return self.middleware.guard("test", [sys.executable, "-c", script], self.owner)["release_id"]

    def test_observation_debounce_pause_persistence_and_last_deletion(self):
        (self.root / "unwatched.bin").write_bytes(b"ignored")
        self.assertIsNone(self.middleware.tick("test", 0))
        (self.root / "a.md").write_text("120")
        self.assertIsNone(self.middleware.tick("test", 1))
        (self.root / "a.md").write_text("130")
        self.assertIsNone(self.middleware.tick("test", 1.3))
        self.assertIsNone(self.middleware.tick("test", 1.4))
        event = self.middleware.tick("test", 1.6)
        self.assertEqual(event["paths"], ["a.md"])
        self.assertEqual(event["writer"], "unknown")
        self.assertIsNone(Middleware(self.projects).tick("test", 3))
        self.middleware.configure("test", WatchConfig(enabled=False), self.owner)
        (self.root / "a.md").unlink()
        self.assertIsNone(self.middleware.tick("test", 4))
        self.middleware.configure("test", WatchConfig(settle_seconds=0.2), self.owner)
        self.middleware.tick("test", 5)
        deleted = self.middleware.tick("test", 6)
        self.assertEqual(deleted["decision"], "BLOCKED")
        snapshot = self.projects.inspect("test", deleted["release_id"], self.owner)
        self.assertEqual(snapshot["report"]["changes"][0]["kind"], "removed")
        self.assertEqual(snapshot["files"], [])
        with self.assertRaises(FilewiseError):
            self.projects.approve("test", deleted["release_id"], self.owner)

    def test_excel_observation_localizes_cell_and_propagates_blocked_check(self):
        from openpyxl import Workbook

        def excel(value):
            book = Workbook()
            book.active["B2"] = value
            buffer = io.BytesIO()
            book.save(buffer)
            return buffer.getvalue()

        (self.root / "requirement.json").write_text('{"pressure":100}')
        (self.root / "inspection.xlsx").write_bytes(excel(100))
        self.projects.create(
            {
                "id": "pressure",
                "name": "Pressure",
                "dependencies": {"inspection.xlsx": ["requirement.json"], "a.md": ["inspection.xlsx"]},
                "checks": [
                    {
                        "id": "pressure-match",
                        "file": "inspection.xlsx",
                        "pointer": "sheet:1/cell:B2",
                        "reference_file": "requirement.json",
                        "reference_pointer": "/pressure",
                    }
                ],
            },
            self.owner,
            self.root,
        )
        self.middleware.configure("pressure", WatchConfig(settle_seconds=0.2), self.owner)
        (self.root / "inspection.xlsx").write_bytes(excel(120))
        self.middleware.tick("pressure", 1)
        event = self.middleware.tick("pressure", 2)
        snapshot = self.projects.inspect("pressure", event["release_id"], self.owner)
        self.assertEqual(event["decision"], "BLOCKED")
        change = snapshot["report"]["changes"][0]
        self.assertEqual(change["path"], "inspection.xlsx")
        self.assertEqual(change["locations"][0]["after_locator"], "sheet:1/cell:B2")
        self.assertEqual(snapshot["report"]["impact"]["a.md"], ["inspection.xlsx", "a.md"])

    def test_guard_review_apply_and_agent_receipt(self):
        rid = self.proposal({"a.md": b"120", "new.json": b'{"ok":true}'})
        self.assertEqual((self.root / "a.md").read_text(), "100")
        self.assertFalse((self.root / "new.json").exists())
        event = self.middleware.config("test", self.owner)["last_event"]
        self.assertFalse(event["original_files_changed"])
        self.assertEqual(event["kind"], "before_write")
        with self.assertRaises(FilewiseError):
            self.middleware.apply("test", rid, None, self.owner)
        with self.assertRaises(FilewiseError):
            self.projects.approve("test", rid, Actor(id=GUARD.id, roles={"reviewer"}))
        self.projects.approve("test", rid, self.owner)
        result = self.middleware.apply("test", rid, None, self.owner)
        self.assertEqual(result["status"], "applied")
        self.assertEqual((self.root / "a.md").read_text(), "120")
        self.assertEqual(self.middleware.apply("test", rid, None, self.owner)["status"], "applied")
        session = self.projects.session("test", self.reader)
        read = self.projects.read("test", rid, "new.json", self.reader, session_id=session["session_id"])
        self.assertEqual(base64.b64decode(read["base64"]), b'{"ok":true}')
        self.assertIsNone(self.middleware.tick("test", 5))
        self.assertTrue(self.engine.audit("project.test", self.owner)["chain_valid"])

    def test_stale_original_and_active_conflict_refuse_without_overwrite(self):
        rid = self.proposal({"a.md": b"120"})
        self.projects.approve("test", rid, self.owner)
        with self.assertRaisesRegex(FilewiseError, "Active release changed"):
            self.middleware.apply("test", rid, "not-current", self.owner)
        (self.root / "a.md").write_text("external edit")
        with self.assertRaisesRegex(FilewiseError, "STALE"):
            self.middleware.apply("test", rid, None, self.owner)
        self.assertEqual((self.root / "a.md").read_text(), "external edit")
        self.assertEqual(self.middleware.proposal(rid)["status"], "pending")

    def test_failed_writeback_restores_modified_added_and_deleted_files(self):
        (self.root / "delete.md").write_text("keep")
        rid = self.proposal({"a.md": b"120", "new.md": b"new", "delete.md": None})
        self.projects.approve("test", rid, self.owner)
        with patch.object(
            self.engine, "activate", side_effect=FilewiseError("injected activation failure", 409)
        ):
            with self.assertRaisesRegex(FilewiseError, "injected"):
                self.middleware.apply("test", rid, None, self.owner)
        self.assertEqual((self.root / "a.md").read_text(), "100")
        self.assertEqual((self.root / "delete.md").read_text(), "keep")
        self.assertFalse((self.root / "new.md").exists())
        self.assertEqual(self.middleware.proposal(rid)["status"], "pending")
        self.middleware.apply("test", rid, None, self.owner)
        self.assertFalse((self.root / "delete.md").exists())

    def test_interrupted_recovery_preserves_unrelated_external_edit(self):
        rid = self.proposal({"a.md": b"120"})
        self.projects.approve("test", rid, self.owner)
        self.middleware._status(rid, "applying")
        (self.root / "a.md").write_text("external")
        with self.assertRaisesRegex(FilewiseError, "Recovery conflict"):
            self.middleware.recover("test", rid, self.owner)
        self.assertEqual((self.root / "a.md").read_text(), "external")
        with self.assertRaisesRegex(FilewiseError, "recovery"):
            self.middleware.tick("test")
        (self.root / "a.md").write_text("120")
        self.assertEqual(self.middleware.recover("test", rid, self.owner)["status"], "recovered")
        self.assertEqual((self.root / "a.md").read_text(), "100")
        # Process stopped just after activation: recovery keeps the committed new contents.
        self.middleware.apply("test", rid, None, self.owner)
        self.middleware._status(rid, "applying")
        self.assertEqual(self.middleware.recover("test", rid, self.owner)["status"], "applied")
        self.assertEqual((self.root / "a.md").read_text(), "120")

    def test_failure_after_activation_requires_reconciliation_not_content_rollback(self):
        rid = self.proposal({"a.md": b"120"})
        self.projects.approve("test", rid, self.owner)
        audit = self.engine._audit

        def fail_final_record(db, scope, actor, action, payload):
            if action == "guard.applied":
                raise OSError("injected journal failure")
            return audit(db, scope, actor, action, payload)

        with patch.object(self.engine, "_audit", side_effect=fail_final_record):
            with self.assertRaisesRegex(FilewiseError, "Activation was recorded"):
                self.middleware.apply("test", rid, None, self.owner)
        self.assertEqual((self.root / "a.md").read_text(), "120")
        self.assertEqual(self.middleware.proposal(rid)["status"], "recovery_required")
        self.middleware.recover("test", rid, self.owner)
        self.assertEqual(self.middleware.config("test", self.owner)["baseline"], rid)
        self.assertIsNone(self.middleware.tick("test", 2))

    def test_replace_refuses_symlinks_and_preserves_file_mode(self):
        outside = self.root.parent / "outside.md"
        outside.write_text("outside")
        (self.root / "link.md").symlink_to(outside)
        with self.assertRaises(OSError):
            replace_bytes(self.root, "link.md", b"attack", hashlib.sha256(b"outside").hexdigest())
        self.assertEqual(outside.read_text(), "outside")
        target = self.root / "a.md"
        target.chmod(0o640)
        replace_bytes(self.root, "a.md", b"120", hashlib.sha256(b"100").hexdigest())
        self.assertEqual(target.stat().st_mode & 0o777, 0o640)
        (self.root / "dir").symlink_to(self.root.parent, target_is_directory=True)
        with self.assertRaises(OSError):
            replace_bytes(self.root, "dir/outside.md", b"attack", hashlib.sha256(b"outside").hexdigest())
        self.assertEqual(set(scan(self.root, [".filewise"], ["**/*.md"])[0]), {"a.md"})

    def test_modes_and_http_authorization(self):
        self.middleware.configure("test", WatchConfig(mode="observe"), self.owner)
        with self.assertRaisesRegex(FilewiseError, "Enable guarded"):
            self.proposal({"a.md": b"120"})
        self.middleware.configure("test", WatchConfig(mode="guard"), self.owner)
        (self.root / "a.md").write_text("110")
        self.assertIsNone(self.middleware.tick("test", 1))
        tokens = {"o" * 32: self.owner, "a" * 32: self.reader, "r" * 32: Actor(id="reader", roles={"reader"})}
        app = create_app(self.engine, tokens)
        with TestClient(app) as client:
            owner = {"Authorization": "Bearer " + "o" * 32}
            agent = {"Authorization": "Bearer " + "a" * 32}
            self.assertTrue(client.get("/api/projects/test/watch", headers=owner).json()["worker_running"])
            for method in ("get", "put"):
                self.assertEqual(
                    getattr(client, method)("/api/projects/test/watch", headers=agent).status_code, 403
                )
            self.assertEqual(
                client.put(
                    "/api/projects/test/watch", headers={"Authorization": "Bearer " + "r" * 32}, json={}
                ).status_code,
                403,
            )
            self.assertEqual(
                client.put("/api/projects/test/watch", headers=owner, json={"mode": "unsafe"}).status_code,
                422,
            )
            rid = self.proposal({"a.md": b"120"})
            route = "/api/projects/test/snapshots/" + rid
            self.assertEqual(client.get(route, headers=owner).json()["writeback"]["status"], "pending")
            self.assertEqual(client.post(route + "/apply", headers=agent, json={}).status_code, 403)
            self.assertEqual(client.post(route + "/approve", headers=owner).status_code, 200)
            self.assertEqual(
                client.post(route + "/apply", headers=owner, json={"expected_active": None}).status_code, 200
            )
        self.assertFalse(app.state.middleware.thread.is_alive())

    def test_follow_cli_persists_configuration_and_credentials(self):
        from filewise.cli import main

        directory = self.root.parent / "followed"
        directory.mkdir()
        (directory / "a.md").write_text("one")
        with patch("uvicorn.run"), patch("sys.stderr", new_callable=io.StringIO) as output:
            self.assertEqual(main(["follow", str(directory), "--include", "*.md"]), 0)
            self.assertIn("http://127.0.0.1:8000", output.getvalue())
            credentials = (directory / ".filewise/tokens.json").read_bytes()
            self.assertEqual(main(["follow", str(directory)]), 0)
            self.assertEqual((directory / ".filewise/tokens.json").read_bytes(), credentials)
            self.assertEqual(len(json.loads(credentials)), 2)
            self.assertEqual(main(["follow", str(directory), "--include", "*.xlsx"]), 1)
        self.assertEqual((directory / ".filewise/tokens.json").stat().st_mode & 0o777, 0o600)


if __name__ == "__main__":
    unittest.main()
