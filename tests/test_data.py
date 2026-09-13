import base64
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from fastapi.testclient import TestClient
from openpyxl import Workbook

from filewise.api import create_app
from filewise.data import DataContract, quality
from filewise.engine import Engine, FilewiseError, now
from filewise.models import Actor
from filewise.projects import Projects

CONTRACT = {
    "meaning": {"entity_type": "company", "unit": "CNY", "period": "2025-01", "population": "card_panel"},
    "allowed_uses": ["panel_spending"],
    "checks": [
        {"id": "rows", "op": "row_count", "minimum": 1},
        {"id": "identity", "op": "unique", "column": "id"},
        {"id": "present", "op": "not_null", "column": "amount"},
        {"id": "amount", "op": "range", "column": "amount", "minimum": 0, "maximum": 1000},
    ],
}


class DataTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.engine = Engine(self.root / "filewise.db")
        self.projects = Projects(self.engine)
        self.editor = Actor(id="operator", roles={"editor"})
        self.reviewer = Actor(id="reviewer", roles={"reviewer"})
        self.publisher = Actor(id="publisher", roles={"publisher"})
        self.agent = Actor(id="agent", audience="agent", roles={"reader", "editor"}, workspace_projects={"p"})
        self.denied = Actor(id="other", audience="agent", roles={"reader"}, workspace_projects={"other"})
        self.client = TestClient(create_app(self.engine, {"a" * 32: self.agent, "b" * 32: self.denied}))
        self.addCleanup(self.client.close)
        self.sequence = 0

    def start(self, contract=None, files=None, local=False):
        spec = {"id": "p", "name": "Synthetic data"}
        if contract:
            spec["data_contracts"] = {"data.csv": contract}
        bodies = files or {"data.csv": b"id,amount\na,100\nb,100\n"}
        if local:
            folder = self.root / "files"
            folder.mkdir()
            for path, body in bodies.items():
                (folder / path).write_bytes(body)
        else:
            folder = None
        self.projects.create(spec, self.editor, folder)
        result = self.projects.snapshot("p", self.editor, None if local else bodies)
        self.base = result["release_id"]
        return result

    def call(self, op, body=None, status=200, denied=False):
        response = self.client.post(
            "/api/workspaces/p/" + op,
            json=body or {},
            headers={"Authorization": "Bearer " + ("b" if denied else "a") * 32},
        )
        self.assertEqual(response.status_code, status, response.text)
        return response.json()

    def write(self, changes, **extra):
        self.sequence += 1
        result = self.call(
            "write",
            {
                "base_version": self.base,
                "request_id": "save-" + str(self.sequence),
                "message": "Synthetic data revision",
                "changes": changes,
                **extra,
            },
        )
        if result["saved"]:
            self.base = result["version"]
        return result

    def test_server_availability_restatement_and_no_future_aliases(self):
        base = self.start()
        self.assertEqual(self.call("quality")["decision"], "NEEDS_REVIEW")
        cutoff, original = base["availability"]["available_at"], self.base
        changed = self.write(
            {"data.csv": {"text": "id,amount\na,80\nb,80\n", "meta": {"valid_from": "2000-01-01T00:00:00Z"}}}
        )
        self.assertIn("100", self.call("read", {"path": "data.csv", "as_of": cutoff})["text"])
        self.assertIn("80", self.call("read", {"path": "data.csv"})["text"])
        self.call("read", {"path": "data.csv", "version": changed["version"], "as_of": cutoff}, 409)
        self.assertEqual(self.call("ls", {"as_of": cutoff})["version"], original)
        self.assertEqual(
            self.call("search", {"query": "100", "mode": "exact", "as_of": cutoff})["version"], original
        )
        self.assertFalse(self.call("search", {"query": "80", "mode": "exact", "as_of": cutoff})["hits"])
        self.assertEqual(self.call("impact", {"paths": ["data.csv"], "as_of": cutoff})["version"], original)
        self.call("impact", {"paths": ["data.csv"], "as_of": cutoff, "base_version": changed["version"]}, 409)
        self.assertEqual(self.call("resolve", {"paths": ["data.csv"], "as_of": cutoff})["version"], original)
        self.call("read", {"path": "data.csv", "as_of": "2999-01-01T00:00:00Z"}, 400)
        self.call("read", {"path": "data.csv", "as_of": cutoff}, 403, denied=True)
        self.engine.revoke(original, self.publisher)
        self.call("ls", {"as_of": cutoff}, 409)  # No fallback to a different historical version.

    def test_required_contracts_guard_saves_and_independent_publication(self):
        initial = self.start(CONTRACT)
        self.assertEqual(initial["verification"]["decision"], "PASS")
        self.assertEqual(self.call("quality")["decision"], "PASS")
        bad = {"data.csv": {"text": "id,amount\na,-2\na,\n"}}
        refused = self.write(bad, require_pass=True)
        self.assertFalse(refused["saved"])
        self.assertIsNone(self.projects.inspect("p", refused["version"], self.editor)["availability"])
        saved = self.write(bad)
        self.assertTrue(saved["saved"])
        self.assertEqual(saved["verification"]["decision"], "BLOCKED")
        self.assertEqual(self.call("quality")["decision"], "BLOCKED")
        with self.assertRaises(FilewiseError):
            self.projects.approve("p", saved["version"], self.reviewer)
        replacement = self.write({"data.csv": {"text": "id,amount\na,1\n", "meta": {"data": {"checks": []}}}})
        self.assertEqual(replacement["verification"]["decision"], "BLOCKED")
        deleted = self.write({"data.csv": {"delete": True}})
        self.assertEqual(deleted["verification"]["decision"], "BLOCKED")
        self.assertEqual(self.call("quality")["decision"], "BLOCKED")
        with self.engine.connect() as db:
            self.assertIsNone(db.execute("SELECT release_id FROM active").fetchone())

    def test_meaning_requirements_compile_and_live_verify(self):
        self.start(CONTRACT)
        requirements = {"data.csv": {"expect": {"unit": "USD"}, "purpose": "total_market_sales"}}
        task = self.call(
            "compile", {"goal": "Check sales", "paths": ["data.csv"], "requirements": requirements}
        )
        reasons = {i["reason"] for i in task["data_guard"]["issues"]}
        self.assertEqual(reasons, {"meaning_mismatch", "purpose_not_declared"})
        verified = self.call("verify", {"phase": "preflight", "task_id": task["task_id"]})
        self.assertEqual(verified["decision"], "BLOCKED")
        self.assertEqual(
            self.call(
                "quality",
                {"requirements": {"data.csv": {"expect": {"unit": "CNY"}, "purpose": "panel_spending"}}},
            )["decision"],
            "PASS",
        )
        self.call("quality", {"requirements": {"../secret": {}}}, 400)
        self.call("quality", {"requirements": {"data.csv": {"expect": {"invented_field": "x"}}}}, 422)

    def test_json_csv_xlsx_checks_are_not_truncated_projections(self):
        self.start()
        rows = [{"id": str(i), "amount": 1} for i in range(160)]
        rows[150]["amount"] = -1
        workbook = Workbook()
        workbook.active.append(["id", "amount"])
        for row in rows:
            workbook.active.append(list(row.values()))
        stream = io.BytesIO()
        workbook.save(stream)
        workbook.close()
        self.write(
            {
                "data.csv": {
                    "text": "id,amount\n" + "\n".join(f"{r['id']},{r['amount']}" for r in rows),
                    "meta": {"data": CONTRACT},
                },
                "data.json": {
                    "text": json.dumps({"records": rows}),
                    "meta": {"data": {**CONTRACT, "rows_pointer": "/records"}},
                },
                "data.xlsx": {
                    "base64": base64.b64encode(stream.getvalue()).decode(),
                    "meta": {"data": CONTRACT},
                },
            }
        )
        for file in self.call("quality")["files"]:
            report = file["quality"]
            self.assertEqual(report["profile"]["row_count"], 160)
            self.assertEqual(next(t for t in report["tests"] if t["id"] == "amount")["record_indices"], [150])
        for body in (b"a,a\n1,2", b"a,b\n1", b"a,b\n1,2,3"):
            self.assertEqual(
                quality("bad.csv", body, DataContract.model_validate(CONTRACT), [])["decision"], "BLOCKED"
            )
        self.assertEqual(
            quality("bad.json", b'[{"id":1,"id":2}]', DataContract.model_validate(CONTRACT), [])["decision"],
            "BLOCKED",
        )
        self.assertEqual(
            quality("bad.json", b'[{"amount":1e999}]', DataContract.model_validate(CONTRACT), [])["decision"],
            "BLOCKED",
        )
        self.assertEqual(
            quality(
                "big.json", json.dumps([{}] * 20_001).encode(), DataContract.model_validate(CONTRACT), []
            )["decision"],
            "BLOCKED",
        )

    def test_cohort_growth_is_flagged_and_unchanged_resync_cannot_erase_it(self):
        self.start()
        contract = {
            **CONTRACT,
            "checks": [
                *CONTRACT["checks"],
                {"id": "panel", "op": "distinct_count_change", "column": "id", "max_change": 0.1},
            ],
        }
        self.write({"data.csv": {"meta": {"data": contract}}})
        self.assertEqual(self.call("quality")["decision"], "PASS")
        self.write({"data.csv": {"text": "id,amount\na,100\nb,100\nc,100\n", "meta": {"data": contract}}})
        report = self.call("quality")
        self.assertEqual(report["decision"], "BLOCKED")
        panel = next(t for t in report["files"][0]["quality"]["tests"] if t["id"] == "panel")
        self.assertEqual(panel["actual"]["relative_change"], 0.5)
        saved_base = panel["actual"]["baseline"]
        self.projects.snapshot("p", self.editor, {"data.csv": b"id,amount\na,100\nb,100\nc,100\n"})
        again = self.call("quality")
        self.assertEqual(again["decision"], "BLOCKED")
        self.assertEqual(again["files"][0]["quality"]["baseline"], saved_base)

    def test_pinned_processing_lineage_revalidates_revocations_on_all_readers(self):
        self.start(CONTRACT)
        source_version = self.base
        self.write(
            {
                "derived.json": {
                    "text": '{"total":200}',
                    "meta": {
                        "processing": "deterministic",
                        "lineage": [{"version": source_version, "path": "data.csv", "role": "data"}],
                    },
                }
            }
        )
        intermediate = self.base
        self.write(
            {
                "final.json": {
                    "text": '{"result":200}',
                    "meta": {
                        "processing": "deterministic",
                        "lineage": [{"version": intermediate, "path": "derived.json", "role": "mapping"}],
                    },
                }
            }
        )
        report = self.call("quality", {"paths": ["final.json"]})
        self.assertEqual(report["decision"], "NEEDS_REVIEW")  # Provenance alone is not a data-quality oracle.
        self.assertEqual(len(report["inputs"]), 2)
        self.assertTrue(all(item["sha256"] and item["available_at"] for item in report["inputs"]))
        task = self.call("compile", {"goal": "Read result", "paths": ["final.json"]})
        self.assertEqual(
            self.call("verify", {"phase": "preflight", "task_id": task["task_id"]})["decision"], "PASS"
        )
        self.engine.revoke(source_version, self.publisher)
        self.call("read", {"path": "final.json"}, 409)
        self.call("verify", {"phase": "preflight", "task_id": task["task_id"]}, 409)
        with self.assertRaises(FilewiseError):
            self.projects.read("p", self.base, "final.json", self.editor, preview=True)
        with self.assertRaises(FilewiseError):
            self.projects.approve("p", self.base, self.reviewer)

    def test_historical_tasks_are_read_only_and_model_memory_is_review_required(self):
        base = self.start(CONTRACT)
        cutoff = base["availability"]["available_at"]
        task = self.call(
            "compile",
            {
                "goal": "Review historical data",
                "paths": ["data.csv"],
                "as_of": cutoff,
                "model_use": "generative",
            },
        )
        self.assertNotIn("write", task["tools"])
        self.assertEqual(task["tool_contract"], {})
        self.assertEqual(task["data_guard"]["decision"], "NEEDS_REVIEW")
        self.assertEqual(
            self.call("verify", {"phase": "build", "task_id": task["task_id"]})["decision"], "NEEDS_REVIEW"
        )
        checked = self.call("verify", {"phase": "preflight", "task_id": task["task_id"]})
        self.assertEqual(checked["decision"], "NEEDS_REVIEW")
        self.assertFalse(checked["production_authorized"])
        self.assertEqual(
            self.call("verify", {"phase": "preflight", "task_id": task["task_id"], "operation": "write"})[
                "decision"
            ],
            "BLOCKED",
        )
        self.call(
            "verify", {"phase": "preflight", "task_id": task["task_id"], "as_of": "2000-01-01T00:00:00Z"}, 409
        )
        self.call("resolve", {"as_of": cutoff, "valid_time": cutoff}, 400)
        self.call("recover", {"version": self.base, "as_of": cutoff}, 400)

    def test_role_binding_and_clock_rollback_remain_fail_closed(self):
        self.start()
        source = self.base
        self.write(
            {
                "derived.txt": {
                    "text": "Synthetic output",
                    "meta": {
                        "processing": "deterministic",
                        "lineage": [
                            {"version": source, "path": "data.csv", "role": "model"},
                            {"version": source, "path": "data.csv", "role": "data"},
                        ],
                    },
                }
            }
        )
        report = self.call("quality", {"paths": ["derived.txt"], "as_of": now()})
        self.assertEqual(report["decision"], "NEEDS_REVIEW")
        self.assertEqual({item["role"] for item in report["inputs"]}, {"data", "model"})
        with patch("filewise.projects.now", return_value="2000-01-01T00:00:00.000000+00:00"):
            with self.assertRaises(FilewiseError):
                self.projects.snapshot("p", self.editor, {"data.csv": b"id,amount\na,1\n"})
        self.assertEqual(self.call("ls", {"as_of": now()})["version"], self.base)

    def test_dry_run_legacy_and_failed_write_have_no_backdated_availability(self):
        self.start(local=True)
        candidate = self.write({"data.csv": {"text": "id,amount\na,80\n"}}, dry_run=True)
        self.assertIsNone(self.projects.inspect("p", candidate["version"], self.editor)["availability"])
        self.call("read", {"path": "data.csv", "version": candidate["version"], "as_of": now()}, 409)
        with patch.object(Projects, "mark_available", side_effect=RuntimeError("receipt failed")):
            with self.assertRaises(RuntimeError):
                self.write({"data.csv": {"text": "id,amount\na,80\n"}})
        self.assertIn("100", (self.root / "files" / "data.csv").read_text())
        self.assertEqual(self.call("ls", {"as_of": now()})["version"], self.base)
        with self.engine.connect(True) as db:
            self.assertEqual(db.execute("SELECT COUNT(*) FROM snapshot_availability").fetchone()[0], 1)
            db.execute("DELETE FROM snapshot_availability")  # Simulate a pre-upgrade database.
        self.assertIsNone(self.call("ls")["temporal"]["availability"])
        self.call("ls", {"version": self.base, "as_of": now()}, 409)
        self.call(
            "write",
            {
                "base_version": self.base,
                "request_id": "spoof",
                "message": "No backdating",
                "changes": {"data.csv": {"meta": {"available_at": "2000-01-01T00:00:00Z"}}},
            },
            422,
        )


if __name__ == "__main__":
    unittest.main()
