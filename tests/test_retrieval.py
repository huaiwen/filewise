import json
import tempfile
import unittest
from unittest.mock import patch

from fastapi.testclient import TestClient

from filewise.api import create_app
from filewise.engine import Engine, FilewiseError
from filewise.middleware import Middleware
from filewise.models import Actor
from filewise.projects import Projects
from filewise.retrieval import Retrieval, chunks
from filewise.workspace import Workspace, WriteQuery


class RetrievalTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.engine = Engine(self.tmp.name + "/test.db")
        self.projects = Projects(self.engine)
        self.editor = Actor(id="editor", roles={"editor"})
        self.reviewer = Actor(id="reviewer", roles={"reviewer"})
        self.publisher = Actor(id="publisher", roles={"publisher"})
        self.agent = Actor(id="agent", audience="agent", roles={"reader", "editor"}, workspace_projects={"p"})
        self.legacy = Actor(id="legacy", audience="agent", roles={"reader"})
        self.projects.create(
            {"id": "p", "name": "Synthetic retrieval", "dependencies": {"delivery.md": ["pressure.md"]}},
            self.editor,
        )
        self.files = {
            "pressure.md": "# 压力检验\n设备交付前必须核对压力上限，记录试验结果。\npressure limit inspection procedure".encode(),
            "delivery.md": "交付清单引用检验要求。".encode(),
            "leave.md": "员工休假申请通过人力资源系统办理。".encode(),
            "setup.py": b"def startMachine():\n    return 'power isolated'\n",
        }
        self.base = self.projects.snapshot("p", self.editor, self.files)["release_id"]
        self.workspace = Workspace(Middleware(self.projects))
        self.client = TestClient(create_app(self.engine, {"a" * 32: self.agent, "l" * 32: self.legacy}))
        self.addCleanup(self.client.close)

    def post(self, operation, body, status=200):
        response = self.client.post(
            "/api/workspaces/p/" + operation, json=body, headers={"Authorization": "Bearer " + "a" * 32}
        )
        self.assertEqual(response.status_code, status, response.text)
        return response.json()

    def search(self, query, **extra):
        return self.post("search", {"query": query, **extra})

    def test_ranked_chinese_bm25_exact_identifiers_and_no_invented_hits(self):
        found = self.search("压力 检验")
        self.assertEqual(found["hits"][0]["path"], "pressure.md")
        self.assertIn("bm25", found["hits"][0]["matches"])
        self.assertEqual(found["retrieval"]["semantic"], "disabled")
        self.assertEqual(self.search("PRESSURE LIMIT", mode="exact")["hits"][0]["path"], "pressure.md")
        self.assertEqual(self.search("start_machine", mode="lexical")["hits"][0]["path"], "setup.py")
        self.assertFalse(self.search("florbnuxglitter")["hits"])
        self.assertFalse(self.search('" OR *')["hits"])
        self.post("search", {"query": "   "}, status=422)
        self.post("search", {"query": "x", "mode": "invented"}, status=422)
        self.post("search", {"query": "x", "mode": "semantic"}, status=503)
        self.post("search", {"query": "x", "paths": ["../outside"]}, status=400)
        self.post("search", {"query": "x", "paths": ["missing.md"]}, status=404)
        self.assertFalse(self.search("压力", paths=["leave.md"])["hits"])

    def test_metadata_filters_history_update_delete_and_stale_declarations(self):
        saved = self.workspace.write(
            "p",
            WriteQuery(
                base_version=self.base,
                request_id="meta",
                message="Add discovery metadata",
                changes={
                    "pressure.md": {
                        "meta": {
                            "summary": "液压安全校验",
                            "tags": ["quality"],
                            "facts": {"maximum_kpa": 120},
                        }
                    }
                },
            ),
            self.agent,
        )
        result = self.search("液压安全", tags=["quality"])
        hit = result["hits"][0]
        self.assertEqual(hit["kind"], "metadata")
        self.assertEqual(hit["evidence"][0]["locator"], "file:sha256")
        self.assertFalse(self.search("液压安全", version=self.base)["hits"])
        self.assertFalse(self.search("液压安全", tags=["nonexistent"])["hits"])
        changed = self.workspace.write(
            "p",
            WriteQuery(
                base_version=saved["version"],
                request_id="change",
                message="Replace text",
                changes={"pressure.md": {"text": "Changed source, refresh the facts"}},
            ),
            self.agent,
        )
        self.assertFalse(self.search("液压安全")["hits"])
        self.assertTrue(self.search("液压安全", version=saved["version"])["hits"])
        deleted = self.workspace.write(
            "p",
            WriteQuery(
                base_version=changed["version"],
                request_id="delete",
                message="Remove files",
                changes={"delivery.md": {"delete": True}, "pressure.md": {"delete": True}},
            ),
            self.agent,
        )
        self.assertFalse(self.search("Changed source")["hits"])
        self.assertEqual(
            self.search("Changed source", version=changed["version"])["hits"][0]["path"], "pressure.md"
        )
        self.assertEqual(self.search("vacation")["version"], deleted["version"])

    def test_bm25_corpus_does_not_include_other_projects_or_historical_versions(self):
        original = self.search("pressure limit")
        self.projects.create({"id": "private", "name": "Other"}, self.editor)
        self.projects.snapshot("private", self.editor, {"secret.md": b"pressure limit " * 1000})
        self.projects.snapshot("p", self.editor, {**self.files, "future.md": b"pressure limit " * 1000})
        again = self.search("pressure limit", version=self.base)
        self.assertEqual(original["hits"], again["hits"])
        self.assertEqual(again["retrieval"]["chunks"], original["retrieval"]["chunks"])
        response = self.client.post(
            "/api/workspaces/private/search",
            json={"query": "pressure"},
            headers={"Authorization": "Bearer " + "a" * 32},
        )
        self.assertEqual(response.status_code, 403)

    def test_chunk_evidence_and_offsets_match_original_long_fragments(self):
        long = "prefix " * 80 + "TARGET-SEARCH-STRING" + " suffix" * 120
        rid = self.projects.snapshot("p", self.editor, {"large.md": long.encode()})["release_id"]
        result = self.search("TARGET-SEARCH-STRING", version=rid, mode="exact")
        self.assertEqual(len(result["hits"]), 1)
        hit = result["hits"][0]
        for part, evidence in zip(hit["parts"], hit["evidence"]):
            self.assertEqual(long[part["start"] : part["start"] + len(part["text"])], evidence["quote"])
            self.assertEqual(evidence["locator"], "line:1")
        with self.engine.connect() as db:
            manifest = self.projects._snapshot(db, "p", rid, self.editor)[4]
        with patch("filewise.retrieval.MAX_CHUNKS", 1):
            with self.assertRaises(FilewiseError):
                chunks(self.engine, manifest)

    def test_discovery_compile_uses_relevant_evidence_and_blocks_empty_discovery(self):
        task = self.post(
            "compile", {"goal": "核对压力检验要求", "retrieval_mode": "lexical", "retrieval_limit": 1}
        )
        self.assertTrue(task["discovery"]["hits"])
        self.assertIn("pressure.md", task["paths"])
        self.assertNotIn("leave.md", task["paths"])
        self.assertEqual(
            next(c for c in task["context"] if c["path"] == "pressure.md")["selection"], "retrieved_evidence"
        )
        self.assertEqual(
            self.post("verify", {"phase": "preflight", "task_id": task["task_id"]})["decision"], "PASS"
        )
        empty = self.post("compile", {"goal": "florbnuxglitter"})
        self.assertFalse(empty["paths"])
        self.assertFalse(empty["context"])
        self.assertEqual(
            self.post("verify", {"phase": "preflight", "task_id": empty["task_id"]})["decision"], "BLOCKED"
        )
        explicit = self.post("compile", {"goal": "florbnuxglitter", "paths": ["leave.md"]})
        self.assertIsNone(explicit["discovery"])
        self.assertEqual(explicit["paths"], ["leave.md"])

    def test_legacy_session_and_revocation_rechecked_after_retrieval(self):
        self.projects.approve("p", self.base, self.reviewer)
        self.projects.activate("p", self.base, None, self.publisher)
        session = self.projects.session("p", self.legacy)
        response = self.client.get(
            "/api/sessions/" + session["session_id"] + "/search",
            params={"q": "压力 检验", "mode": "lexical"},
            headers={"Authorization": "Bearer " + "l" * 32},
        )
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["hits"][0]["path"], "pressure.md")
        self.assertEqual(
            self.client.post(
                "/api/workspaces/p/search",
                json={"query": "压力"},
                headers={"Authorization": "Bearer " + "l" * 32},
            ).status_code,
            403,
        )
        search = Retrieval.search

        def revoke_during_search(retriever, manifest, query):
            result = search(retriever, manifest, query)
            self.engine.source_policy(manifest["pressure.md"]["source_id"], {"reviewer"}, True, self.reviewer)
            return result

        with patch.object(Retrieval, "search", revoke_during_search):
            self.post("search", {"query": "压力"}, status=403)
        self.post("search", {"query": "压力", "version": self.base}, status=403)
        with self.engine.connect() as db:
            count = db.execute("SELECT COUNT(*) FROM audit WHERE action='file.search'").fetchone()[0]
        self.assertEqual(count, 1)  # Only the earlier allowed released-session search was disclosed.

    def test_semantic_configuration_failure_does_not_silently_fall_back(self):
        with self.engine.connect(True) as db:
            db.execute("CREATE TABLE retrieval_config(id INTEGER PRIMARY KEY, data TEXT NOT NULL)")
            db.execute(
                "INSERT INTO retrieval_config VALUES(1,?)",
                (
                    json.dumps(
                        {
                            "model": "missing",
                            "path": self.tmp.name + "/missing",
                            "files": {"model.onnx": "missing"},
                            "dimension": 384,
                        }
                    ),
                ),
            )
        self.post("search", {"query": "压力", "mode": "hybrid"}, status=503)
        self.assertTrue(self.search("压力", mode="lexical")["hits"])
        self.assertEqual(Retrieval(self.engine).disable()["semantic"], "disabled")


if __name__ == "__main__":
    unittest.main()
