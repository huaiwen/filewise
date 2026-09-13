import base64
import io
import json
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from unittest.mock import patch

from fastapi.testclient import TestClient
from openpyxl import load_workbook

from filewise.api import create_app, validate_tokens
from filewise.engine import FilewiseError
from filewise.middleware import Middleware
from filewise.models import Actor
from filewise.projects import FileMetadata
from filewise.showcase import Showcase
from filewise.workspace import Workspace, WriteQuery


class WorkspaceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.demo = Showcase(self.tmp.name)
        self.project = "hydraulic-demo"
        self.writer = Actor(
            id="working-agent",
            audience="agent",
            roles={"reader", "editor"},
            workspace_projects={self.project},
        )
        self.reader = Actor(
            id="knowledge-reader", audience="agent", roles={"reader"}, workspace_projects={self.project}
        )
        self.tokens = {**self.demo.tokens, "w" * 32: self.writer, "r" * 32: self.reader}
        self.client = TestClient(create_app(self.demo.engine, self.tokens))
        self.addCleanup(self.client.close)
        self.middleware = Middleware(self.demo.projects)
        self.workspace = Workspace(self.middleware)
        self.initial = self.demo.projects.detail(self.project, self.demo.editor)["active_release"]

    def call(self, operation, body=None, *, role="writer", status=200, project=None, method="POST"):
        token = {"writer": "w" * 32, "reader": "r" * 32}.get(role) or self.demo.identities[role]["token"]
        response = self.client.request(
            method,
            f"/api/workspaces/{project or self.project}/{operation}",
            json=body if body is not None else {},
            headers={"Authorization": "Bearer " + token},
        )
        self.assertEqual(response.status_code, status, response.text)
        return response.json()

    def write(self, changes, base=None, key="write-1", **extra):
        return self.call(
            "write",
            {
                "base_version": base or self.initial,
                "request_id": key,
                "message": "Update synthetic knowledge",
                "changes": changes,
                **extra,
            },
        )

    def pressure_changes(self, value):
        book = load_workbook(self.demo.root / "inspection.xlsx")
        book.active["B2"] = value
        stream = io.BytesIO()
        book.save(stream)
        book.close()
        return {
            "requirement.json": {"text": json.dumps({"pressure_kpa": value})},
            "inspection.xlsx": {"base64": base64.b64encode(stream.getvalue()).decode()},
        }

    def test_agent_performs_writes_metadata_history_diff_and_idempotency(self):
        query = {
            "base_version": self.initial,
            "request_id": "metadata-write",
            "message": "Pressure proposal",
            "model": "synthetic-model",
            "task": "task-42",
            "changes": {
                "requirement.json": {
                    "text": '{"pressure_kpa":120}',
                    "meta": {
                        "summary": "Pressure requirement",
                        "facts": {"pressure": 120},
                        "owner": "quality",
                        "tags": ["pressure"],
                    },
                }
            },
        }
        result = self.call("write", query)
        rid = result["version"]
        self.assertTrue(result["saved"])
        self.assertFalse(result["published"])
        self.assertEqual(result["verification"]["decision"], "BLOCKED")
        self.assertEqual(json.loads((self.demo.root / "requirement.json").read_text())["pressure_kpa"], 120)
        read = self.call("read", {"path": "requirement.json"})
        self.assertEqual(read["version"], rid)
        self.assertNotIn("base64", read)
        self.assertEqual(read["metadata"]["facts"]["pressure"], 120)
        self.assertEqual(read["metadata"]["declared_by"], self.writer.id)
        previous = self.call("read", {"path": "requirement.json", "version": self.initial}, role="reader")
        self.assertIn("100", previous["text"])
        self.assertEqual(
            self.call("read", {"path": "requirement.json", "version": "published"})["version"], self.initial
        )
        comparison = self.call("diff", {"before": self.initial})
        self.assertEqual(comparison["after"], rid)
        self.assertEqual(comparison["changes"][0]["locations"][0]["after"], "120")
        self.assertEqual(comparison["summary"]["modified"], 1)
        self.assertFalse(any("base64" in c for c in comparison["changes"]))
        trace = self.call("trace", {"version": rid})
        self.assertEqual(trace["write"]["model"], "synthetic-model")
        self.assertTrue(any(e["action"] == "workspace.saved" for e in trace["events"]))
        count = len(self.call("versions", method="GET")["versions"])
        self.assertEqual(self.call("write", query)["version"], rid)
        self.assertEqual(len(self.call("versions", method="GET")["versions"]), count)
        self.call("write", {**query, "message": "different retry"}, status=409)
        self.assertEqual(
            self.demo.projects.detail(self.project, self.demo.editor)["active_release"], self.initial
        )
        # Old released-only gateway still rejects original drift rather than using working knowledge.
        response = self.client.post(
            f"/api/projects/{self.project}/sessions",
            headers={"Authorization": "Bearer " + self.demo.identities["agent"]["token"]},
        )
        self.assertEqual(response.status_code, 409)

    def test_batch_binary_delete_dry_run_and_require_pass(self):
        rejected = self.write({"requirement.json": {"text": '{"pressure_kpa":120}'}}, require_pass=True)
        self.assertFalse(rejected["saved"])
        self.assertEqual(rejected["status"], "blocked")
        self.assertEqual(self.call("ls")["version"], self.initial)
        proposed = self.write(self.pressure_changes(120), key="dry", dry_run=True)
        self.assertFalse(proposed["saved"])
        self.assertEqual(proposed["verification"]["decision"], "PASS")
        self.assertEqual(
            self.call("read", {"version": proposed["version"], "path": "requirement.json"})["text"],
            '{"pressure_kpa": 120}',
        )
        changes = {
            **self.pressure_changes(120),
            "binary.bin": {"base64": "AP9maWxl"},
            "README.md": {"delete": True},
        }
        saved = self.write(changes, key="apply", require_pass=True)
        self.assertTrue(saved["saved"])
        self.assertFalse((self.demo.root / "README.md").exists())
        self.assertEqual((self.demo.root / "binary.bin").read_bytes(), b"\x00\xfffile")
        read = self.call("read", {"path": "binary.bin", "include_bytes": True})
        self.assertEqual(base64.b64decode(read["base64"]), b"\x00\xfffile")
        self.assertEqual(self.call("ls")["version"], saved["version"])
        self.assertEqual(self.call("verify", {"version": saved["version"]})["decision"], "PASS")

    def test_metadata_only_typed_knowledge_dependency_and_stale_fact_handling(self):
        initial_meta = {
            "summary": "Derived limit",
            "depends_on": ["requirement.json"],
            "facts": {"limit": 100},
        }
        added = self.write({"derived.md": {"text": "limit follows requirement", "meta": initial_meta}})
        rid = added["version"]
        commit = self.demo.projects.commit(
            self.project,
            {"release_id": rid, "message": "with metadata", "expected_parent": None},
            self.demo.editor,
        )
        changed = self.write(
            {"derived.md": {"meta": {**initial_meta, "facts": {"limit": 120}}}}, base=rid, key="meta-only"
        )
        self.assertTrue(changed["saved"])
        comparison = self.call("diff", {"before": rid})
        self.assertTrue(
            any(
                c["field"] == "fact:limit" and c["kind"] == "threshold_increased"
                for c in comparison["semantic_changes"]
            )
        )
        self.demo.projects.commit(
            self.project,
            {
                "release_id": changed["version"],
                "message": "changed metadata",
                "expected_parent": commit["id"],
            },
            self.demo.editor,
        )
        self.assertEqual(
            self.call("read", {"path": "derived.md", "version": "HEAD"})["version"], changed["version"]
        )
        forward = self.call("impact", {"paths": ["requirement.json"]})
        self.assertIn("derived.md", forward["affected"])
        reverse = self.call("impact", {"paths": ["derived.md"], "direction": "reverse"})
        self.assertEqual(reverse["paths"]["requirement.json"], ["derived.md", "requirement.json"])
        (self.demo.root / "derived.md").write_text("new text without refreshed fact metadata")
        synced = self.call("sync")
        state = self.call("resolve", {"paths": ["derived.md"], "version": synced["version"]})["state"][
            "derived.md"
        ]
        self.assertFalse(state["fields"]["metadata_current"])
        self.assertNotIn("fact:limit", state["fields"])
        self.call("resolve", {"valid_time": "nonsense"}, status=422)

    def test_compile_minimal_context_and_real_three_phase_verification(self):
        fixed = self.write(self.pressure_changes(120))
        rid = fixed["version"]
        task = self.call(
            "compile",
            {
                "goal": "Check delivery pressure",
                "paths": ["delivery.md"],
                "direction": "reverse",
                "output_checks": [
                    {"id": "output-pressure", "object_id": "result", "field": "pressure", "expected": 120}
                ],
            },
        )
        self.assertEqual(task["version"], rid)
        self.assertEqual(set(task["paths"]), {"delivery.md", "inspection.xlsx", "requirement.json"})
        self.assertNotIn("README.md", task["paths"])
        self.assertTrue(task["context"])
        self.assertIn("write", task["tools"])
        self.assertEqual(
            self.call("verify", {"phase": "preflight", "task_id": task["task_id"]})["decision"], "PASS"
        )
        invalid = self.call(
            "verify", {"phase": "postflight", "task_id": task["task_id"], "result": {"pressure": 100}}
        )
        self.assertEqual(invalid["decision"], "BLOCKED")
        valid = self.call(
            "verify", {"phase": "postflight", "task_id": task["task_id"], "result": {"pressure": 120}}
        )
        self.assertEqual(valid["decision"], "PASS")
        self.assertFalse(valid["production_authorized"])
        self.assertEqual(self.call("verify", {"phase": "postflight"})["decision"], "NEEDS_REVIEW")
        file = self.call("read", {"path": "requirement.json"})
        evidence = self.call("evidence/" + file["source_id"], method="GET")
        self.assertTrue(any("120" in f["text"] for f in evidence["fragments"]))
        self.assertEqual(
            self.call(
                "verify",
                {
                    "phase": "postflight",
                    "task_id": task["task_id"],
                    "result": {"pressure": 120},
                    "citations": [
                        {"source_id": file["source_id"], "locator": "line:1", "quote": "invented quote"}
                    ],
                },
            )["decision"],
            "BLOCKED",
        )
        self.assertEqual(
            self.call(
                "verify",
                {
                    "phase": "postflight",
                    "operation": "write",
                    "output_version": rid,
                    "outputs": {"requirement.json": "wrong"},
                },
            )["decision"],
            "BLOCKED",
        )
        bounded = self.call(
            "compile", {"goal": "Bounded context", "paths": ["delivery.md"], "max_chars": 100}
        )
        self.assertLessEqual(sum(len(f["text"]) for c in bounded["context"] for f in c["fragments"]), 100)
        self.assertTrue(bounded["context_truncated"])
        self.write({"delivery.md": {"text": "Changed after task compilation"}}, base=rid, key="next")
        self.assertEqual(
            self.call("verify", {"phase": "preflight", "operation": "write", "task_id": task["task_id"]})[
                "decision"
            ],
            "BLOCKED",
        )

    def test_permissions_project_scope_revocation_and_contract_integrity(self):
        for role in ("agent",):
            for operation in ("read", "write", "compile", "verify", "trace"):
                self.call(operation, role=role, status=403)
        self.call(
            "write",
            {
                "base_version": self.initial,
                "request_id": "denied",
                "message": "No",
                "changes": {"new.md": {"text": "denied"}},
            },
            role="reader",
            status=403,
        )
        self.demo.projects.create({"id": "other", "name": "Other"}, self.demo.editor)
        other = self.demo.projects.snapshot("other", self.demo.editor, {"secret.md": b"private"})
        self.call("ls", project="other", status=403)
        self.call("read", {"version": other["release_id"], "path": "secret.md"}, status=404)
        task = self.call("compile", {"goal": "Read-only contract"}, role="reader")
        self.assertNotIn("write", task["tools"])
        self.assertEqual(
            self.call(
                "verify",
                {"phase": "preflight", "operation": "write", "task_id": task["task_id"]},
                role="reader",
            )["decision"],
            "BLOCKED",
        )
        self.call("verify", {"phase": "preflight", "task_id": task["task_id"]}, status=403)
        self.assertEqual(
            self.call("verify", {"phase": "preflight", "operation": "publish"})["decision"], "BLOCKED"
        )
        with self.assertRaises(ValueError):
            validate_tokens(
                {
                    "x" * 32: self.writer,
                    "y" * 32: self.writer.model_copy(update={"workspace_projects": {"other"}}),
                }
            )
        read = self.call("read", {"path": "requirement.json"})
        self.demo.engine.source_policy(
            read["source_id"], {"reviewer"}, True, self.demo.tokens[self.demo.identities["reviewer"]["token"]]
        )
        self.call("read", {"path": "requirement.json"}, status=403)
        self.call("verify", {"task_id": task["task_id"]}, role="reader", status=403)

    def test_stale_concurrent_writes_and_path_validation(self):
        with self.middleware.lock(self.project):
            response = self.client.post(
                f"/api/projects/{self.project}/sync",
                headers={"Authorization": "Bearer " + self.demo.identities["editor"]["token"]},
            )
            self.assertEqual(response.status_code, 409)
        for changes, status in (
            ({"../outside.md": {"text": "x"}}, 400),
            ({".env": {"text": "x"}}, 403),
            ({"x.bin": {"base64": "not base64!"}}, 400),
            ({"a.md": {"text": "a", "delete": True}}, 422),
            ({"a.md": {"text": "a", "actor": "publisher"}}, 422),
            ({"a.md": {"text": "a", "meta": {"declared_by": "publisher"}}}, 422),
        ):
            self.call(
                "write",
                {
                    "base_version": self.initial,
                    "request_id": "invalid",
                    "message": "Invalid request",
                    "changes": changes,
                },
                status=status,
            )

        def write(index):
            try:
                return self.workspace.write(
                    self.project,
                    WriteQuery(
                        base_version=self.initial,
                        request_id="race-" + str(index),
                        message="Competing update",
                        changes={"new.md": {"text": str(index)}},
                    ),
                    self.writer,
                )
            except FilewiseError as error:
                return error.status

        with ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(write, (1, 2)))
        self.assertEqual(sum(isinstance(r, dict) for r in results), 1)
        self.assertIn(409, results)
        rid = self.call("ls")["version"]
        (self.demo.root / "new.md").write_text("external edit")
        self.call(
            "write",
            {
                "base_version": rid,
                "request_id": "stale",
                "message": "Must not overwrite",
                "changes": {"new.md": {"text": "wrong"}},
            },
            status=409,
        )
        self.assertEqual((self.demo.root / "new.md").read_text(), "external edit")

    def test_failed_save_compensation_retry_and_agent_owned_recovery(self):
        query = WriteQuery(
            base_version=self.initial,
            request_id="retry",
            message="Fault injection",
            changes={"new.md": {"text": "new"}, "README.md": {"delete": True}},
        )
        before = (self.demo.root / "README.md").read_bytes()
        audit = self.demo.engine._audit

        def fail(db, scope, actor, action, payload):
            if action == "workspace.saved":
                raise OSError("injected final-record failure")
            return audit(db, scope, actor, action, payload)

        with patch.object(self.demo.engine, "_audit", side_effect=fail):
            with self.assertRaises(OSError):
                self.workspace.write(self.project, query, self.writer)
        self.assertEqual((self.demo.root / "README.md").read_bytes(), before)
        self.assertFalse((self.demo.root / "new.md").exists())
        rid = self.demo.projects.detail(self.project, self.demo.editor)["snapshots"][0]["release_id"]
        self.middleware._status(rid, "applying")
        self.call("recover", {"version": rid}, role="reader", status=403)
        self.assertEqual(self.call("recover", {"version": rid})["status"], "recovered")
        saved = self.workspace.write(self.project, query, self.writer)
        self.assertEqual(saved["version"], rid)
        self.assertTrue(saved["saved"])
        self.assertTrue(
            self.demo.engine.audit(
                "project." + self.project, self.demo.tokens[self.demo.identities["reviewer"]["token"]]
            )["chain_valid"]
        )

    def test_candidates_never_become_latest_or_inherit_unsaved_metadata(self):
        self.write({"requirement.json": {"meta": {"facts": {"must_not_leak": 999}}}}, dry_run=True)
        synced = self.call("sync")
        self.assertNotIn("metadata", self.call("read", {"path": "requirement.json"}))
        audit = self.demo.engine._audit

        def fail(db, scope, actor, action, payload):
            if action == "guard.proposed":
                raise OSError("injected snapshot-to-intent gap")
            return audit(db, scope, actor, action, payload)

        query = WriteQuery(
            base_version=synced["version"],
            request_id="gap",
            message="Interrupted proposal",
            changes={"new.md": {"text": "candidate"}},
        )
        with patch.object(self.demo.engine, "_audit", side_effect=fail):
            with self.assertRaises(OSError):
                self.workspace.write(self.project, query, self.writer)
        self.assertEqual(self.call("ls")["version"], synced["version"])
        self.assertFalse((self.demo.root / "new.md").exists())
        self.assertTrue(self.workspace.write(self.project, query, self.writer)["saved"])

    def test_postflight_requires_completed_write_and_restores_metadata(self):
        task = self.call(
            "compile", {"goal": "Adjust delivery", "paths": ["delivery.md"], "direction": "reverse"}
        )
        saved = self.write({"delivery.md": {"text": "Revised delivery", "meta": {"facts": {"count": 1}}}})
        checked = self.call(
            "verify",
            {
                "phase": "postflight",
                "operation": "write",
                "task_id": task["task_id"],
                "output_version": saved["version"],
            },
        )
        self.assertEqual(checked["decision"], "PASS")
        self.assertEqual(
            self.call(
                "verify",
                {
                    "phase": "postflight",
                    "operation": "write",
                    "version": saved["version"],
                    "output_version": self.initial,
                },
            )["decision"],
            "BLOCKED",
        )
        restored = self.middleware.restore(self.project, self.initial, self.demo.editor)
        self.assertNotIn("metadata", next(f for f in restored["files"] if f["path"] == "delivery.md"))
        self.assertEqual(self.call("ls")["version"], restored["report"]["base_release"])
        # Metadata-only history is restorable too, without inventing a byte change.
        baseline = self.call("ls")["version"]
        original_meta = self.write(
            {"delivery.md": {"meta": {"facts": {"count": 2}}}}, base=baseline, key="meta-restore"
        )
        meta_restored = self.middleware.restore(self.project, saved["version"], self.demo.editor)
        file = next(f for f in meta_restored["files"] if f["path"] == "delivery.md")
        self.assertEqual(file["metadata"]["facts"], {"count": 1})
        self.assertNotEqual(meta_restored["release_id"], original_meta["version"])

    def test_future_validity_and_revoked_write_replay_are_not_authorized(self):
        query = {
            "base_version": self.initial,
            "request_id": "future",
            "message": "Future declaration",
            "changes": {
                "delivery.md": {"meta": {"valid_from": "2099-01-01T00:00:00Z", "facts": {"limit": 120}}}
            },
        }
        saved = self.call("write", query)
        task = self.call("compile", {"goal": "Use delivery", "paths": ["delivery.md"]})
        checked = self.call("verify", {"phase": "preflight", "task_id": task["task_id"]})
        self.assertEqual(checked["decision"], "BLOCKED")
        self.assertIn("outside_validity", [item["reason"] for item in checked["issues"]])
        publisher = self.demo.tokens[self.demo.identities["publisher"]["token"]]
        self.demo.engine.revoke(saved["version"], publisher)
        self.call("write", query, status=409)
        self.call("read", {"path": "delivery.md", "version": saved["version"]}, status=409)

    def test_postflight_checks_the_written_version_regression(self):
        task = self.call("compile", {"goal": "Adjust pressure", "paths": ["requirement.json"]})
        saved = self.write({"requirement.json": {"text": '{"pressure_kpa":120}'}})
        self.assertTrue(saved["saved"])
        checked = self.call(
            "verify",
            {
                "phase": "postflight",
                "operation": "write",
                "task_id": task["task_id"],
                "output_version": saved["version"],
            },
        )
        self.assertEqual(checked["decision"], "BLOCKED")
        self.assertIn("output_regression_failed", [item["reason"] for item in checked["issues"]])

    def test_upload_workspace_and_metadata_boundary(self):
        self.demo.projects.create({"id": "uploads", "name": "Uploads"}, self.demo.editor)
        base = self.demo.projects.snapshot("uploads", self.demo.editor, {"a.md": b"old"})["release_id"]
        actor = self.writer.model_copy(update={"workspace_projects": {"uploads"}})
        saved = self.workspace.write(
            "uploads",
            WriteQuery(
                base_version=base,
                request_id="upload-write",
                message="Update uploaded knowledge",
                changes={"a.md": {"text": "new"}},
            ),
            actor,
        )
        self.assertTrue(saved["saved"])
        self.assertFalse(saved["original_files_changed"])
        with self.assertRaises(ValueError):
            FileMetadata(tags=["x" * 65000])
        with self.assertRaises(FilewiseError):
            FileMetadata(depends_on=["../outside"])


if __name__ == "__main__":
    unittest.main()
