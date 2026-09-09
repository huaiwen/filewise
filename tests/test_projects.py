import io
import tempfile
import unittest
from pathlib import Path

from filewise.engine import Engine, FilewiseError
from filewise.models import Actor
from filewise.projects import Projects, relative_path, scan


class ProjectTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.engine = Engine(Path(self.tmp.name) / "db.sqlite")
        self.projects = Projects(self.engine)
        self.editor = Actor(id="editor", roles={"editor"})
        self.reviewer = Actor(id="reviewer", roles={"reviewer"})
        self.publisher = Actor(id="publisher", roles={"publisher"})
        self.reader = Actor(id="agent", roles={"reader"}, audience="agent")

    def publish(self, snapshot, expected=None):
        rid = snapshot["release_id"]
        self.projects.approve("test", rid, self.reviewer)
        self.projects.activate("test", rid, expected, self.publisher)
        return rid

    def test_file_lifecycle_and_actor_bound_receipts(self):
        self.projects.create({"id": "test", "name": "Files"}, self.editor)
        files = {"src/main.py": b"print(100)\n", "empty.txt": b"", "opaque.bin": b"\xff\x00"}
        snapshot = self.projects.snapshot("test", self.editor, files)
        self.assertEqual(snapshot["verification"]["decision"], "PASS")
        self.assertEqual(snapshot["report"]["assurance"], "file_integrity_only")
        with self.assertRaises(FilewiseError):
            self.projects.session("test", self.reader, snapshot["release_id"])
        with self.assertRaises(FilewiseError):
            self.projects.read("test", snapshot["release_id"], "src/main.py", self.reader, preview=True)
        rid = self.publish(snapshot)
        session = self.projects.session("test", self.reader)
        read = self.projects.read("test", rid, "src/main.py", self.reader, session_id=session["session_id"])
        self.assertEqual(read["text"], "print(100)\n")
        self.assertEqual(read["receipt"]["decision"], "PASS")
        self.assertEqual(self.projects.read("test", rid, "opaque.bin", self.reader)["base64"], "/wA=")
        with self.assertRaises(FilewiseError):
            self.projects.session_info(session["session_id"], self.editor)
        self.assertEqual(
            self.projects.search(session["session_id"], "100", self.reader)["hits"][0]["locator"], "line:1"
        )
        unchanged = self.projects.snapshot("test", self.editor, files)
        self.assertEqual(unchanged["report"]["changes"], [])
        self.assertEqual(unchanged["files"][0]["revision_id"], snapshot["files"][0]["revision_id"])
        self.engine.revoke(rid, self.publisher)
        with self.assertRaises(FilewiseError):
            self.projects.session_info(session["session_id"], self.reader)
        self.assertTrue(self.engine.audit("project.test", self.reviewer)["chain_valid"])

    def test_excel_change_and_cross_file_impact_gate(self):
        from openpyxl import Workbook

        def workbook(value):
            book = Workbook()
            book.active["A1"], book.active["B1"] = "Inspection", "Pressure kPa"
            book.active["A2"], book.active["B2"] = "Hydraulic test", value
            out = io.BytesIO()
            book.save(out)
            return out.getvalue()

        self.projects.create(
            {
                "id": "test",
                "name": "Pressure",
                "dependencies": {"inspection.xlsx": ["requirement.json"], "delivery.md": ["inspection.xlsx"]},
                "checks": [
                    {
                        "id": "pressure-match",
                        "file": "inspection.xlsx",
                        "pointer": "sheet:1/cell:B2",
                        "reference_file": "requirement.json",
                        "reference_pointer": "/pressure_kpa",
                    }
                ],
            },
            self.editor,
        )
        files = {
            "requirement.json": b'{"pressure_kpa":100}',
            "inspection.xlsx": workbook(100),
            "delivery.md": b"Use inspection plan",
        }
        first = self.projects.snapshot("test", self.editor, files)
        rid = self.publish(first)
        files["requirement.json"] = b'{"pressure_kpa":120}'
        blocked = self.projects.snapshot("test", self.editor, files)
        self.assertEqual(blocked["verification"]["decision"], "BLOCKED")
        self.assertEqual(
            blocked["report"]["impact"]["delivery.md"], ["requirement.json", "inspection.xlsx", "delivery.md"]
        )
        with self.assertRaises(FilewiseError):
            self.projects.approve("test", blocked["release_id"], self.reviewer)
        files["inspection.xlsx"] = workbook(120)
        fixed = self.projects.snapshot("test", self.editor, files)
        self.assertEqual(fixed["verification"]["decision"], "PASS")
        excel = next(c for c in fixed["report"]["changes"] if c["path"] == "inspection.xlsx")
        self.assertTrue(
            any(
                c["after_locator"] == "sheet:1/cell:B2" and c["kind"] == "numeric_changed"
                for c in excel["locations"]
            )
        )
        new = self.publish(fixed, rid)
        self.assertEqual(self.projects.session("test", self.reader)["release_id"], new)
        # Uploaded releases are immutable; changing active does not invalidate a valid pinned release.
        self.projects.activate("test", rid, new, self.publisher, rollback=True)
        self.assertEqual(self.projects.session("test", self.reader, new)["release_id"], new)

    def test_local_drift_and_directory_boundary(self):
        root = Path(self.tmp.name) / "files"
        root.mkdir()
        (root / "a.md").write_text("100")
        (root / ".env").write_text("secret")
        (root / "outside").symlink_to(Path(self.tmp.name) / "db.sqlite")
        files, skipped = scan(root.resolve(), [".env"])
        self.assertEqual(set(files), {"a.md"})
        self.assertEqual(len(skipped), 2)
        for bad in ("../a", "/tmp/a", "a/../b", "a\\b", "./a", "a//b"):
            with self.assertRaises(FilewiseError):
                relative_path(bad)
        self.projects.create({"id": "test", "name": "Local"}, self.editor, root)
        rid = self.publish(self.projects.snapshot("test", self.editor))
        session = self.projects.session("test", self.reader)
        (root / "a.md").write_text("120")
        with self.assertRaisesRegex(FilewiseError, "STALE"):
            self.projects.read("test", rid, "a.md", self.reader, session_id=session["session_id"])
        self.assertEqual(self.projects.snapshot("test", self.editor)["report"]["changes"][0]["path"], "a.md")

    def test_missing_dependency_and_manifest_tamper(self):
        self.projects.create(
            {"id": "test", "name": "Files", "dependencies": {"a.txt": ["missing.txt"]}}, self.editor
        )
        snapshot = self.projects.snapshot("test", self.editor, {"a.txt": b"A"})
        self.assertEqual(snapshot["verification"]["decision"], "BLOCKED")
        with self.engine.connect(True) as db:
            db.execute("UPDATE project_snapshots SET manifest=?", ("{}",))
        with self.assertRaisesRegex(FilewiseError, "manifest incomplete"):
            self.projects.inspect("test", snapshot["release_id"], self.editor)
        with self.engine.connect(True) as db:
            db.execute("UPDATE project_snapshots SET report=?", ('{"verification":{"decision":"PASS"}}',))
        with self.assertRaisesRegex(FilewiseError, "report integrity"):
            self.projects.inspect("test", snapshot["release_id"], self.editor)


class ChangeLocationTests(unittest.TestCase):
    def test_json_typed_pointers_and_prose_alignment(self):
        from filewise.changes import fragment_diff

        def fragments(lines):
            return [{"locator": f"line:{i}", "text": line} for i, line in enumerate(lines, 1)]

        changes = fragment_diff(
            fragments(['{"pressure":100,"enabled":true}']),
            fragments(['{"enabled":1,"pressure":120}']),
            format="json",
        )
        self.assertEqual({c["after_locator"] for c in changes}, {"json:/pressure", "json:/enabled"})
        self.assertEqual(
            fragment_diff(fragments(["first", "second"]), fragments(["intro", "first", "second"]))[0][
                "after_locator"
            ],
            "line:1",
        )
        self.assertEqual(
            len(fragment_diff(fragments(["first", "second"]), fragments(["intro", "first", "second"]))), 1
        )


if __name__ == "__main__":
    unittest.main()
