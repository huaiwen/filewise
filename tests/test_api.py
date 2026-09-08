import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from fastapi.testclient import TestClient

from filewise import Engine
from filewise.api import create_app
from filewise.models import now

TOKENS = {role * 32: {"id": role, "roles": [role]} for role in ("editor", "reader", "reviewer", "publisher")}


class APITests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.db = Path(self.temp.name) / "api.db"
        self.engine = Engine(self.db)
        self.client = TestClient(create_app(self.engine, TOKENS))
        self.addCleanup(self.client.close)

    def call(self, method, path, role="editor", expected=200, **kwargs):
        response = self.client.request(
            method, "/api/" + path, headers={"Authorization": "Bearer " + role * 32}, **kwargs
        )
        self.assertEqual(response.status_code, expected, response.text)
        return response.json()

    def test_http_lifecycle_upload_and_auth_boundaries(self):
        scope = {
            "id": "http",
            "title": "HTTP",
            "required_objects": ["rule"],
            "checks": [{"id": "oracle", "object_id": "rule", "field": "value", "op": "eq", "expected": 120}],
        }
        self.call("POST", "scopes", "reader", expected=403, json=scope)
        self.call("POST", "scopes", expected=201, json=scope)
        source = self.call(
            "POST",
            "scopes/http/sources?acl=reader,editor,reviewer,publisher",
            expected=201,
            files={"file": ("../../rule.txt", b"Pressure is 120 kPa.", "text/plain")},
        )
        self.assertEqual(source["name"], "rule.txt")
        revision = {
            "id": "r1",
            "scope_id": "http",
            "object_id": "rule",
            "kind": "rule",
            "title": "Pressure",
            "fields": {"value": 120},
            "valid_from": "2020-01-01T00:00:00Z",
            "evidence": [{"source_id": source["id"], "locator": "line:1", "quote": "120 kPa"}],
        }
        self.call("POST", "revisions", expected=201, json=revision)
        draft = self.call("POST", "resolve", "reader", json={"scope_id": "http", "valid_time": now()})
        self.assertFalse(draft["objects"])
        self.call("POST", "revisions/r1/approved", "reviewer")
        build = self.call("POST", "build", expected=201, json={"scope_id": "http", "valid_time": now()})
        rid = build["id"]
        self.assertEqual(build["bundle"]["verification"]["decision"], "PASS")
        self.call("POST", f"releases/{rid}/context", "reader", expected=409, json={"object_ids": ["rule"]})
        self.call("POST", f"releases/{rid}/approve", "reviewer")
        self.call("POST", f"releases/{rid}/activate", "publisher", json={"expected_active": None})
        self.call(
            "POST", f"releases/{rid}/activate", "publisher", expected=409, json={"expected_active": None}
        )
        context = self.call("POST", f"releases/{rid}/context", "reader", json={"object_ids": ["rule"]})
        self.assertEqual(context["objects"]["rule"]["fields"]["value"], 120)
        self.assertEqual(self.call("GET", f"releases/{rid}/verify", "reader")["runtime"]["decision"], "PASS")
        self.assertEqual(
            self.call("POST", "diff", "reader", json={"before_release": rid, "after_release": rid})[
                "changes"
            ],
            [],
        )
        self.assertEqual(
            self.call(
                "POST", "impact", "reader", json={"scope_id": "http", "valid_time": now(), "seeds": ["rule"]}
            )["affected"],
            ["rule"],
        )
        self.assertTrue(self.call("GET", "scopes/http/audit", "reviewer")["chain_valid"])
        self.call(
            "PUT",
            f"sources/{source['id']}/policy",
            "reviewer",
            json={"acl": ["editor", "reviewer", "publisher"]},
        )
        self.call("GET", f"releases/{rid}", "reader", expected=403)
        self.call("POST", f"releases/{rid}/context", "reader", expected=403, json={"object_ids": ["rule"]})
        self.call("POST", f"releases/{rid}/revoke", "publisher")
        self.assertTrue(self.call("GET", f"releases/{rid}", "reviewer")["revoked"])

    def test_authentication_validation_and_size_limits(self):
        for path in ("/api/overview", "/api/openapi.json", "/api/me"):
            response = self.client.get(path, headers={"X-Actor": "admin", "X-Roles": "publisher"})
            self.assertEqual(response.status_code, 401)
        self.assertEqual(
            self.client.get("/api/overview", headers={"Authorization": "Bearer invalid"}).status_code, 401
        )
        self.call("POST", "resolve", expected=422, json={"scope_id": "s", "valid_time": "2020-01-01"})
        self.call(
            "POST", "scopes", expected=422, json={"id": "../path", "title": "No", "required_objects": ["r"]}
        )
        self.call(
            "POST",
            "scopes",
            expected=422,
            json={"id": "s", "title": "No", "required_objects": ["r"], "roles": ["publisher"]},
        )
        self.call("POST", "scopes/s/sources?acl=not-a-role", expected=422, files={"file": ("a.txt", b"a")})
        self.call("POST", "scopes", expected=413, content=b"x" * (11 * 1024 * 1024 + 1))
        self.assertEqual(self.client.post("/api/scopes", content=b"x" * 100).status_code, 401)
        with self.assertRaises(ValueError):
            create_app(self.engine, {})
        with self.assertRaises(ValueError):
            create_app(self.engine, {"weak": {"id": "reader", "roles": ["reader"]}})

    def test_console_and_openapi_are_packaged(self):
        page = self.client.get("/")
        self.assertEqual(page.status_code, 200)
        self.assertIn("知识发布控制台", page.text)
        self.assertIn("script-src 'self'", page.headers["Content-Security-Policy"])
        self.assertEqual(self.client.get("/console.js").status_code, 200)
        self.assertEqual(self.client.get("/console.css").status_code, 200)
        self.assertEqual(self.client.get("/health").json()["status"], "ok")
        schema = self.call("GET", "openapi.json", "reader")
        self.assertIn("/api/build", schema["paths"])

    def test_installed_cli_auth_and_demo(self):
        env = dict(os.environ)

        def run(*args):
            return subprocess.run(
                [sys.executable, "-m", "filewise", *args], capture_output=True, text=True, env=env
            )

        token_path = Path(self.temp.name) / "tokens.json"
        auth = run("auth-init", "--out", str(token_path))
        self.assertEqual(auth.returncode, 0, auth.stderr)
        self.assertEqual(len(json.loads(token_path.read_text())), 4)
        self.assertNotEqual(run("auth-init", "--out", str(token_path)).returncode, 0)
        if os.name != "nt":
            self.assertEqual(token_path.stat().st_mode & 0o777, 0o600)
        demo = run("--db", str(self.db), "demo")
        self.assertEqual(demo.returncode, 0, demo.stderr)
        result = json.loads(demo.stdout)
        self.assertTrue(result["audit_chain_valid"])
        again = run("--db", str(self.db), "demo")
        self.assertNotEqual(again.returncode, 0)
        self.assertIn("fresh database", again.stderr)
        self.assertNotEqual(run("serve").returncode, 0)


if __name__ == "__main__":
    unittest.main()
