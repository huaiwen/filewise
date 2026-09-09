import base64
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from fastapi.testclient import TestClient

from filewise.api import create_app
from filewise.engine import Engine, FilewiseError
from filewise.middleware import RESTORER, Middleware, WatchConfig
from filewise.models import Actor
from filewise.projects import Projects
from filewise.showcase import Showcase


class RestoreTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve() / "files"
        (self.root / "folder").mkdir(parents=True)
        self.originals = {"a.md": b"100\n", "folder/lost.bin": b"\x00\xfforiginal\x80"}
        for path, body in self.originals.items():
            (self.root / path).write_bytes(body)
        (self.root / ".env").write_text("synthetic secret")
        (self.root / "ignore.md").write_text("unmanaged")
        self.engine = Engine(Path(self.tmp.name) / "state" / "filewise.db")
        self.projects = Projects(self.engine)
        self.middleware = Middleware(self.projects)
        self.actors = {role: Actor(id=role, roles={role}) for role in ("editor", "reviewer", "publisher")}
        self.actors["agent"] = Actor(id="agent", roles={"reader"}, audience="agent")
        self.tokens = {(role + "-") * 32: actor for role, actor in self.actors.items()}
        self.client = TestClient(create_app(self.engine, self.tokens))
        self.addCleanup(self.client.close)
        self.projects.create(
            {"id": "files", "name": "Files", "excludes": ["ignore.md"]}, self.actors["editor"], self.root
        )
        self.target = self.projects.snapshot("files", self.actors["editor"])["release_id"]

    def call(self, method, path="", role="editor", status=200, **kwargs):
        response = self.client.request(
            method,
            "/api/projects/files" + path,
            headers={"Authorization": "Bearer " + (role + "-") * 32},
            **kwargs,
        )
        self.assertEqual(response.status_code, status, response.text)
        return response.json()

    def restore(self, status=201, role="editor"):
        return self.call("POST", f"/snapshots/{self.target}/restore", role, status)

    def test_full_history_restore_review_writeback_and_receipt(self):
        first = self.call(
            "POST",
            "/commits",
            status=201,
            json={"release_id": self.target, "expected_parent": None, "message": "Initial files"},
        )
        self.projects.approve("files", self.target, self.actors["reviewer"])
        self.projects.activate("files", self.target, None, self.actors["publisher"])
        count = len(self.call("GET")["snapshots"])
        self.restore(status=409)
        self.assertEqual(len(self.call("GET")["snapshots"]), count)
        (self.root / "a.md").write_text("120\n")
        (self.root / "folder/lost.bin").unlink()
        (self.root / "new.md").write_text("remove on restore")
        current = self.projects.snapshot("files", self.actors["editor"])["release_id"]
        self.call(
            "POST",
            "/commits",
            status=201,
            json={"release_id": current, "expected_parent": first["id"], "message": "120 files"},
        )
        history = self.call("GET")["commits"]
        # Neither the current release nor the last commit contains this unsaved-to-Filewise edit.
        (self.root / "a.md").write_text("130\n")
        self.middleware.configure("files", WatchConfig(enabled=False), self.actors["editor"])
        snapshot = self.restore()
        rid, report = snapshot["release_id"], snapshot["report"]
        self.assertEqual(report["restore_from"], self.target)
        self.assertNotIn(report["base_release"], (self.target, current))
        self.assertEqual(
            {c["path"]: c["kind"] for c in report["changes"]},
            {"a.md": "modified", "folder/lost.bin": "added", "new.md": "removed"},
        )
        change = next(c for c in report["changes"] if c["path"] == "a.md")
        self.assertEqual((change["locations"][0]["before"], change["locations"][0]["after"]), ("130", "100"))
        event = self.middleware.config("files", self.actors["editor"])["last_event"]
        self.assertEqual((event["kind"], event["writer"]), ("restore", "editor"))
        self.assertFalse(event["original_files_changed"])
        self.assertEqual((self.root / "a.md").read_text(), "130\n")
        self.assertFalse((self.root / "folder/lost.bin").exists())
        self.assertEqual(self.call("GET")["commits"], history)
        self.assertEqual(self.call("GET")["active_release"], self.target)
        route = f"/snapshots/{rid}"
        self.assertEqual(self.call("GET", route)["writeback"]["status"], "pending")
        self.call("POST", route + "/apply", "publisher", 409, json={"expected_active": self.target})
        with self.assertRaisesRegex(FilewiseError, "author cannot approve"):
            self.projects.approve("files", rid, Actor(id=RESTORER.id, roles={"reviewer"}))
        self.call("POST", route + "/approve", "reviewer")
        self.call("POST", route + "/apply", "publisher", 409, json={"expected_active": None})
        (self.root / "a.md").write_text("external edit")
        self.call("POST", route + "/apply", "publisher", 409, json={"expected_active": self.target})
        self.assertEqual((self.root / "a.md").read_text(), "external edit")
        (self.root / "a.md").write_text("130\n")
        self.call("POST", route + "/apply", "publisher", json={"expected_active": self.target})
        for path, body in self.originals.items():
            self.assertEqual((self.root / path).read_bytes(), body)
        self.assertFalse((self.root / "new.md").exists())
        self.assertEqual((self.root / ".env").read_text(), "synthetic secret")
        self.assertEqual((self.root / "ignore.md").read_text(), "unmanaged")
        self.assertEqual(self.call("GET")["commits"], history)
        session = self.call("POST", "/sessions", "agent", 201)
        data = self.projects.read(
            "files", rid, "folder/lost.bin", self.actors["agent"], session_id=session["session_id"]
        )
        self.assertEqual(base64.b64decode(data["base64"]), self.originals["folder/lost.bin"])
        self.restore(status=409)
        audit = self.engine.audit("project.files", self.actors["reviewer"])
        self.assertTrue(audit["chain_valid"])
        self.assertTrue(any(e["payload"].get("restore_from") == self.target for e in audit["events"]))

    def test_permissions_local_boundary_revocation_and_integrity(self):
        (self.root / "a.md").write_text("120")
        for role in ("agent", "reviewer", "publisher"):
            self.restore(status=403, role=role)
        self.projects.create({"id": "uploads", "name": "Uploads"}, self.actors["editor"])
        uploaded = self.projects.snapshot("uploads", self.actors["editor"], {"a.md": b"old"})["release_id"]
        self.call("POST", f"/snapshots/{uploaded}/restore", status=404)
        with self.assertRaisesRegex(FilewiseError, "local directory"):
            self.middleware.restore("uploads", uploaded, self.actors["editor"])
        rid = self.restore()["release_id"]
        self.call("POST", f"/snapshots/{rid}/approve", "reviewer")
        self.engine.revoke(self.target, self.actors["publisher"])
        self.restore(status=409)
        self.call("POST", f"/snapshots/{rid}/apply", "publisher", 409, json={"expected_active": None})
        self.assertEqual((self.root / "a.md").read_text(), "120")
        self.assertEqual(self.middleware.proposal(rid)["status"], "pending")
        with self.engine.connect(True) as db:
            db.execute("UPDATE project_snapshots SET report='{}' WHERE release_id=?", (rid,))
        self.call("POST", f"/snapshots/{rid}/apply", "publisher", 409, json={"expected_active": None})

    def test_unavailable_sources_and_interrupted_writeback_block_restoration(self):
        (self.root / "a.md").write_text("120")
        source = next(
            f
            for f in self.projects.inspect("files", self.target, self.actors["editor"])["files"]
            if f["path"] == "a.md"
        )["source_id"]
        self.engine.source_policy(source, {"reviewer"}, False, self.actors["reviewer"])
        self.restore(status=403)
        self.engine.source_policy(
            source, {"editor", "reviewer", "publisher", "reader"}, False, self.actors["reviewer"]
        )
        rid = self.restore()["release_id"]
        self.middleware._status(rid, "applying")
        self.restore(status=409)
        self.middleware.recover("files", rid, self.actors["publisher"])
        self.call("POST", f"/snapshots/{rid}/approve", "reviewer")
        self.engine.source_policy(source, {"reviewer"}, True, self.actors["reviewer"])
        self.restore(status=403)
        self.call("POST", f"/snapshots/{rid}/apply", "publisher", 403, json={"expected_active": None})
        self.assertEqual((self.root / "a.md").read_text(), "120")

    def test_cli_restore_keeps_excel_checks_and_requires_review(self):
        demo = Showcase(Path(self.tmp.name) / "demo")
        blocked = demo.step("change")["release_id"]
        demo.step("repair")
        excel = (demo.root / "inspection.xlsx").read_bytes()
        command = [sys.executable, "-m", "filewise", "--db", str(demo.engine.path)]
        result = subprocess.run(
            command + ["project", "restore", "hydraulic-demo", blocked],
            check=True,
            capture_output=True,
            text=True,
        )
        candidate = json.loads(result.stdout)
        rid = candidate["release_id"]
        self.assertEqual(candidate["verification"]["decision"], "BLOCKED")
        changes = candidate["report"]["changes"]
        self.assertEqual(changes[0]["path"], "inspection.xlsx")
        self.assertEqual(changes[0]["locations"][0]["after_locator"], "sheet:1/cell:B2")
        denied = subprocess.run(
            command
            + ["--actor", "reviewer", "--roles", "reviewer", "project", "review", "hydraulic-demo", rid],
            capture_output=True,
            text=True,
        )
        self.assertEqual(denied.returncode, 1)
        self.assertEqual((demo.root / "inspection.xlsx").read_bytes(), excel)
        target = demo.projects.detail("hydraulic-demo", demo.editor)["active_release"]
        restored = subprocess.run(
            command + ["project", "restore", "hydraulic-demo", target],
            check=True,
            capture_output=True,
            text=True,
        )
        rid = json.loads(restored.stdout)["release_id"]
        for role, action, extra in (
            ("reviewer", "review", []),
            ("publisher", "apply", ["--expected-active", target]),
        ):
            subprocess.run(
                command
                + ["--actor", role, "--roles", role, "project", action, "hydraulic-demo", rid]
                + extra,
                check=True,
                capture_output=True,
                text=True,
            )
        self.assertEqual(json.loads((demo.root / "requirement.json").read_text())["pressure_kpa"], 100)
        # Repeated demo steps consult the actual workbook, including externally restored contents.
        self.assertEqual(demo.step("repair")["verification"]["decision"], "PASS")
        self.assertEqual(json.loads((demo.root / "requirement.json").read_text())["pressure_kpa"], 120)


if __name__ == "__main__":
    unittest.main()
