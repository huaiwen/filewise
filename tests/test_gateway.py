import tempfile
import unittest

from fastapi.testclient import TestClient

from filewise.api import create_app
from filewise.showcase import Showcase


class GatewayTests(unittest.TestCase):
    def test_actual_demo_and_agent_boundary(self):
        with tempfile.TemporaryDirectory() as directory:
            demo = Showcase(directory)
            client = TestClient(create_app(demo.engine, demo.tokens, showcase=demo))
            identities = client.get("/demo").json()["identities"]

            def call(method, path, role="agent", status=200, **kwargs):
                response = client.request(
                    method,
                    "/api/" + path,
                    headers={"Authorization": "Bearer " + identities[role]["token"]},
                    **kwargs,
                )
                self.assertEqual(response.status_code, status, response.text)
                return response.json()

            project = "projects/hydraulic-demo"
            baseline = call("POST", project + "/sessions", status=201)
            sid = baseline["session_id"]
            first = call("GET", f"sessions/{sid}/read?path=requirement.json")
            self.assertIn("100", first["text"])
            self.assertEqual(first["receipt"]["release_id"], baseline["release_id"])
            for path in (
                "overview",
                "openapi.json",
                "sources/" + first["source_id"],
                "releases/" + baseline["release_id"],
                project,
            ):
                call("GET", path, status=403)
            call("GET", f"sessions/{sid}", role="editor", status=403)
            blocked = call("POST", "demo/change", "editor")
            self.assertEqual(blocked["verification"]["decision"], "BLOCKED")
            call("GET", f"sessions/{sid}/read?path=requirement.json", status=409)
            release = project + "/snapshots/" + blocked["release_id"]
            call("GET", release + "/preview?path=requirement.json", status=403)
            call("POST", release + "/approve", "reviewer", status=409)
            repaired = call("POST", "demo/repair", "editor")
            self.assertEqual(repaired["verification"]["decision"], "PASS")
            release = project + "/snapshots/" + repaired["release_id"]
            call("POST", release + "/approve", "editor", status=403)
            call("POST", release + "/approve", "reviewer")
            call("POST", release + "/activate", "publisher", json={"expected_active": baseline["release_id"]})
            new = call("POST", project + "/sessions", status=201)
            self.assertIn(
                "120", call("GET", f"sessions/{new['session_id']}/read?path=requirement.json")["text"]
            )
            call("POST", "releases/" + repaired["release_id"] + "/revoke", "publisher")
            call("GET", f"sessions/{new['session_id']}", status=409)
            self.assertEqual(TestClient(create_app(demo.engine, demo.tokens)).get("/demo").status_code, 404)

    def test_upload_is_complete_set_and_never_server_path(self):
        with tempfile.TemporaryDirectory() as directory:
            demo = Showcase(directory)
            client = TestClient(create_app(demo.engine, demo.tokens))
            headers = {"Authorization": "Bearer " + demo.identities["editor"]["token"]}
            self.assertEqual(
                client.post(
                    "/api/projects", headers=headers, json={"id": "evil", "name": "Bad", "root": "/etc"}
                ).status_code,
                422,
            )
            self.assertEqual(
                client.post(
                    "/api/projects", headers=headers, json={"id": "upload", "name": "Uploads"}
                ).status_code,
                201,
            )

            def upload(files):
                return client.post("/api/projects/upload/upload", headers=headers, files=files)

            self.assertEqual(upload([("files", ("../private.txt", b"X"))]).status_code, 400)
            self.assertEqual(
                upload([("files", ("a.txt", b"X")), ("files", ("a.txt", b"Y"))]).status_code, 400
            )
            result = upload([("files", ("a.txt", b"X")), ("files", ("b.txt", b"Y"))])
            self.assertEqual(result.status_code, 201, result.text)
            self.assertEqual(len(result.json()["files"]), 2)


if __name__ == "__main__":
    unittest.main()
