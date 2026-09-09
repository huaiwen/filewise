"""Real follow/run CLI and HTTP check, using only disposable synthetic files.

Run: python examples/check_middleware.py (macOS; permission to bind loopback required).
"""

import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path


def main():
    if sys.platform != "darwin":
        raise SystemExit("The guarded subprocess check requires macOS sandbox-exec")
    with tempfile.TemporaryDirectory(prefix="filewise-middleware-check-") as directory:
        root = Path(directory).resolve() / "files"
        root.mkdir()
        (root / "a.md").write_text("100")
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        env = dict(os.environ)
        if env.get("PYTHONPATH"):
            env["PYTHONPATH"] = os.pathsep.join(
                str(Path(p).resolve()) for p in env["PYTHONPATH"].split(os.pathsep)
            )
        cli = [sys.executable, "-m", "filewise"]
        with Path(directory, "server.log").open("w+") as log:
            server = subprocess.Popen(
                [*cli, "follow", str(root), "--include", "*.md", "--port", str(port)],
                env=env,
                stdout=log,
                stderr=log,
            )
            try:
                url = f"http://127.0.0.1:{port}"
                for _ in range(100):
                    try:
                        with urllib.request.urlopen(url + "/health", timeout=0.5):
                            break
                    except (OSError, urllib.error.URLError):
                        time.sleep(0.05)
                else:
                    raise AssertionError("follow server did not start")
                tokens = json.loads((root / ".filewise/tokens.json").read_text())
                owner = next(k for k, a in tokens.items() if a["id"] == "local-owner")
                agent = next(k for k, a in tokens.items() if a["id"] == "local-agent")

                def request(path, method="GET", data=None, token=owner):
                    req = urllib.request.Request(
                        url + "/api/" + path,
                        data=json.dumps(data).encode() if data is not None else None,
                        method=method,
                        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
                    )
                    with urllib.request.urlopen(req, timeout=5) as response:
                        return json.load(response)

                project = "projects/workspace"
                (root / "a.md").write_text("110")
                for _ in range(80):
                    watch = request(project + "/watch")
                    if watch["last_event"]:
                        break
                    time.sleep(0.1)
                else:
                    raise AssertionError("No automatic after-save event")
                observed = watch["last_event"]["release_id"]
                assert watch["last_event"]["kind"] == "after_save"
                changes = request(project + "/snapshots/" + observed)["report"]["changes"]
                assert changes[0]["path"] == "a.md" and changes[0]["locations"]
                # Inherited OS boundary must deny BOTH original contents and private state.
                script = """
from pathlib import Path
for path in %r:
    try: Path(path).read_bytes()
    except PermissionError: pass
    else: raise AssertionError('Original read was allowed')
    try:
        with Path(path).open('ab'): pass
    except PermissionError: pass
    else: raise AssertionError('Original write was allowed')
assert Path('a.md').read_text() == '110'
Path('a.md').write_text('120')
""" % [str(root / "a.md"), str(root / ".filewise/filewise.db"), str(root / ".filewise/tokens.json")]
                result = subprocess.run(
                    [*cli, "run", str(root), "--", sys.executable, "-c", script],
                    env=env,
                    capture_output=True,
                    text=True,
                    timeout=30,
                )
                assert result.returncode == 0, result.stderr
                candidate = json.loads(result.stdout)
                assert candidate["status"] == "pending_review", candidate
                assert (root / "a.md").read_text() == "110"
                rid = candidate["release_id"]
                route = project + "/snapshots/" + rid
                try:
                    request(route + "/apply", "POST", {"expected_active": None})
                except urllib.error.HTTPError as error:
                    assert error.code == 409
                else:
                    raise AssertionError("Unreviewed writeback was allowed")
                request(route + "/approve", "POST")
                assert request(route + "/apply", "POST", {"expected_active": None})["status"] == "applied"
                assert (root / "a.md").read_text() == "120"
                session = request(project + "/sessions", "POST", token=agent)
                read = request("sessions/" + session["session_id"] + "/read?path=a.md", token=agent)
                assert read["text"] == "120" and read["receipt"]["decision"] == "PASS"
                print(
                    json.dumps(
                        {
                            "follow_cli": "PASS",
                            "automatic_diff": "PASS",
                            "guarded_run_cli": "PASS",
                            "raw_read_write": "DENIED",
                            "unreviewed_writeback": "DENIED",
                            "reviewed_writeback": "PASS",
                            "agent_receipt": "PASS",
                        }
                    )
                )
            finally:
                server.terminate()
                server.wait(timeout=10)


if __name__ == "__main__":
    main()
