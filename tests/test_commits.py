import json
import subprocess
import sys
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor

from fastapi.testclient import TestClient

from filewise.api import create_app
from filewise.engine import FilewiseError
from filewise.middleware import Middleware
from filewise.showcase import Showcase


class CommitWorkflowTests(unittest.TestCase):
    def test_named_commits_group_saves_without_publishing(self):
        with tempfile.TemporaryDirectory() as directory:
            demo = Showcase(directory)
            project = "hydraulic-demo"
            client = TestClient(create_app(demo.engine, demo.tokens))
            self.addCleanup(client.close)

            def call(method, path="", role="editor", status=200, **kwargs):
                response = client.request(
                    method,
                    f"/api/projects/{project}{path}",
                    headers={"Authorization": "Bearer " + demo.identities[role]["token"]},
                    **kwargs,
                )
                self.assertEqual(response.status_code, status, response.text)
                return response.json()

            baseline = call("GET")["active_release"]
            first = call(
                "POST",
                "/commits",
                status=201,
                json={
                    "release_id": baseline,
                    "message": "初始检验资料",
                    "expected_parent": None,
                },
            )
            blocked = demo.step("change")
            fixed = demo.step("repair")
            rid = fixed["release_id"]
            report = call("GET", f"/snapshots/{rid}/compare?base_release={baseline}")
            self.assertEqual({c["path"] for c in report["changes"]}, {"requirement.json", "inspection.xlsx"})
            excel = next(c for c in report["changes"] if c["path"] == "inspection.xlsx")
            cell = next(c for c in excel["locations"] if c["after_locator"] == "sheet:1/cell:B2")
            self.assertEqual((cell["before"], cell["after"]), ("100", "120"))
            self.assertEqual(report["impact"]["delivery.md"], ["inspection.xlsx", "delivery.md"])
            second = call(
                "POST",
                "/commits",
                status=201,
                json={
                    "release_id": rid,
                    "message": "  将检验压力统一为 120 kPa  ",
                    "expected_parent": first["id"],
                },
            )
            detail = call("GET")
            self.assertEqual(detail["active_release"], baseline)
            self.assertEqual(len(detail["snapshots"]), 3)
            self.assertEqual([c["id"] for c in detail["commits"]], [second["id"], first["id"]])
            self.assertEqual(second["message"], "将检验压力统一为 120 kPa")
            self.assertIsNone(call("GET", f"/snapshots/{rid}")["approver"])
            call(
                "POST",
                f"/snapshots/{rid}/activate",
                role="publisher",
                status=409,
                json={"expected_active": baseline},
            )
            call("POST", "/sessions", role="agent", status=409, params={"release_id": rid})

            unchanged = demo.projects.snapshot(project, demo.editor)["release_id"]
            self.assertEqual(call("GET", f"/snapshots/{unchanged}/compare?base_release={rid}")["changes"], [])
            payload = {"release_id": unchanged, "message": "重复内容", "expected_parent": second["id"]}
            call("POST", "/commits", status=409, json=payload)
            call("POST", "/commits", role="reviewer", status=403, json=payload)
            call("POST", "/commits", role="agent", status=403, json=payload)
            call("GET", f"/snapshots/{rid}/compare", role="agent", status=403)
            call("POST", "/commits", status=422, json={**payload, "message": " \n "})
            call(
                "POST",
                "/commits",
                status=422,
                json={k: v for k, v in payload.items() if k != "expected_parent"},
            )

            # A guarded proposal can be committed while originals and its publish gate stay unchanged.
            files = {p.name: p.read_bytes() for p in demo.root.iterdir()}
            proposed = {**files, "requirement.json": b'{"pressure_kpa":130}', "added.md": b"New"}
            del proposed["README.md"]
            middleware = Middleware(demo.projects)
            candidate = middleware.capture(project, rid, proposed, demo.editor)
            self.assertEqual(candidate["verification"]["decision"], "BLOCKED")
            cid = candidate["release_id"]
            report = call("GET", f"/snapshots/{cid}/compare?base_release={rid}")
            self.assertEqual(
                {c["path"]: c["kind"] for c in report["changes"]},
                {
                    "README.md": "removed",
                    "requirement.json": "modified",
                    "added.md": "added",
                },
            )
            third = call(
                "POST",
                "/commits",
                status=201,
                json={
                    "release_id": cid,
                    "message": "压力 130 草稿，待修复检验表",
                    "expected_parent": second["id"],
                },
            )
            self.assertEqual({p.name: p.read_bytes() for p in demo.root.iterdir()}, files)
            self.assertEqual(middleware.proposal(cid)["status"], "pending")
            call(
                "POST",
                f"/snapshots/{cid}/apply",
                role="publisher",
                status=409,
                json={"expected_active": baseline},
            )

            # CLI uses the same stored history and file comparisons after reopening SQLite.
            command = [sys.executable, "-m", "filewise", "--db", demo.engine.path, "project"]
            log = subprocess.run(command + ["log", project], check=True, capture_output=True, text=True)
            self.assertEqual(json.loads(log.stdout)[0]["id"], third["id"])
            comparison = subprocess.run(
                command + ["diff", project, cid, "--base-release", rid],
                check=True,
                capture_output=True,
                text=True,
            )
            self.assertEqual(json.loads(comparison.stdout)["changes"], report["changes"])

            # Same parent raced by two writers: only one can extend the linear history.
            def competing_commit(release):
                try:
                    return demo.projects.commit(
                        project,
                        {
                            "release_id": release,
                            "message": "并发记录",
                            "expected_parent": third["id"],
                        },
                        demo.editor,
                    )
                except FilewiseError as error:
                    return error.status

            with ThreadPoolExecutor(max_workers=2) as pool:
                results = list(pool.map(competing_commit, [baseline, blocked["release_id"]]))
            self.assertEqual(sum(isinstance(result, dict) for result in results), 1)
            self.assertIn(409, results)
            history = call("GET")["commits"]
            self.assertEqual(len(history), 4)
            self.assertEqual(history[0]["parent_id"], third["id"])
            demo.projects.create({"id": "other", "name": "Other"}, demo.editor)
            other = demo.projects.snapshot("other", demo.editor, {"a.md": b"A"})["release_id"]
            call("GET", f"/snapshots/{cid}/compare?base_release={other}", status=404)
            call(
                "POST",
                "/commits",
                status=404,
                json={
                    "release_id": other,
                    "message": "Wrong project",
                    "expected_parent": history[0]["id"],
                },
            )
            recorded = subprocess.run(
                command
                + [
                    "commit",
                    project,
                    cid,
                    "-m",
                    "通过 CLI 记录选中版本",
                    "--expected-parent",
                    history[0]["id"],
                ],
                check=True,
                capture_output=True,
                text=True,
            )
            self.assertEqual(json.loads(recorded.stdout)["parent_id"], history[0]["id"])
            self.assertEqual(len(call("GET")["commits"]), 5)
            with demo.engine.connect(True) as db:
                db.execute("UPDATE project_commits SET message='tampered' WHERE id=?", (first["id"],))
            call("GET", status=409)


if __name__ == "__main__":
    unittest.main()
