"""Run against a disposable real HTTP gateway; no outside files or models are used.

    python examples/check_agent_boundary.py

Requires the documents extra and permission to bind 127.0.0.1.
"""

import json
import os
import socket
import subprocess
import sys
import tempfile
import threading
from pathlib import Path

import uvicorn

from filewise.agent import launch
from filewise.api import create_app
from filewise.showcase import Showcase


def main():
    with tempfile.TemporaryDirectory(prefix="filewise-boundary-check-") as directory:
        demo = Showcase(directory)
        tokens = Path(directory, "tokens.json")
        tokens.write_text(json.dumps({k: v.model_dump(mode="json") for k, v in demo.tokens.items()}))
        tokens.chmod(0o600)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            url = "http://127.0.0.1:" + str(sock.getsockname()[1])
            server = uvicorn.Server(uvicorn.Config(create_app(demo.engine, demo.tokens), log_level="error"))
            thread = threading.Thread(target=server.run, kwargs={"sockets": [sock]}, daemon=True)
            thread.start()
            env = dict(
                os.environ,
                FILEWISE_URL=url,
                FILEWISE_TOKEN=demo.identities["agent"]["token"],
                FILEWISE_DB="/denied/not-a-local-database.db",
            )
            # Resolve source path before changing cwd; normal installs do not need PYTHONPATH.
            if env.get("PYTHONPATH"):
                env["PYTHONPATH"] = os.pathsep.join(
                    str(Path(p).resolve()) for p in env["PYTHONPATH"].split(os.pathsep)
                )
            scratch = Path(directory, "client")
            scratch.mkdir()

            def cli(*args, code=0):
                result = subprocess.run(
                    [sys.executable, "-m", "filewise", "agent", *args],
                    cwd=scratch,
                    env=env,
                    capture_output=True,
                    text=True,
                )
                assert result.returncode == code, (result.returncode, result.stderr)
                return json.loads(result.stdout) if code == 0 else result.stderr

            try:
                # A bounded ready check, not an unbounded sleep or a fake HTTP client.
                import time

                for _ in range(100):
                    if server.started:
                        break
                    time.sleep(0.02)
                assert server.started, "HTTP server did not start"
                session = cli("open", "hydraulic-demo")
                sid = session["session_id"]
                assert len(cli("ls", sid)["files"]) == 4
                original = cli("read", sid, "requirement.json")
                assert "100" in original["text"]
                output = scratch / "exact.json"
                cli("read", sid, "requirement.json", "--output", str(output))
                assert output.read_bytes() == (demo.root / "requirement.json").read_bytes()
                assert "error" in cli("read", sid, "requirement.json", "--output", str(output), code=1)
                assert cli("search", sid, "100")["hits"]
                if sys.platform == "darwin":
                    probe = """
import json, os
from pathlib import Path
from filewise.agent import request
for name in %r:
    try: Path(name).read_bytes()
    except PermissionError: pass
    else: raise AssertionError('Direct private read was not denied')
    try:
        with Path(name).open('ab'): pass
    except PermissionError: pass
    else: raise AssertionError('Direct private write was not denied')
result=request(os.environ['FILEWISE_URL'],os.environ['FILEWISE_TOKEN'],'sessions/'+os.environ['FILEWISE_SESSION']+'/read?path=requirement.json')
assert '100' in result['text']
assert result['receipt']['decision']=='PASS'
print(json.dumps({'raw_file':'DENIED','database':'DENIED','operator_tokens':'DENIED','gateway':'PASS'}))
""" % [
                        str((demo.root / "requirement.json").resolve()),
                        str(demo.engine.path.resolve()),
                        str(tokens.resolve()),
                    ]
                    old = os.environ.get("PYTHONPATH")
                    if env.get("PYTHONPATH"):
                        os.environ["PYTHONPATH"] = env["PYTHONPATH"]
                    try:
                        result = launch(
                            demo.projects,
                            "hydraulic-demo",
                            demo.editor,
                            tokens,
                            url,
                            [sys.executable, "-c", probe],
                        )
                        assert result["exit_code"] == 0, result
                        # Exercise the public CLI argument route as well.
                        command = subprocess.run(
                            [
                                sys.executable,
                                "-m",
                                "filewise",
                                "--db",
                                str(demo.engine.path),
                                "project",
                                "launch",
                                "--tokens",
                                str(tokens),
                                "--url",
                                url,
                                "hydraulic-demo",
                                "--",
                                sys.executable,
                                "-c",
                                'print("isolated CLI started")',
                            ],
                            cwd=scratch,
                            env=env,
                            capture_output=True,
                            text=True,
                        )
                        assert command.returncode == 0, command.stderr
                    finally:
                        if old is None:
                            os.environ.pop("PYTHONPATH", None)
                        else:
                            os.environ["PYTHONPATH"] = old
                demo.write_requirement(120)
                assert "STALE" in cli("read", sid, "requirement.json", code=1)
                print(
                    json.dumps(
                        {
                            "remote_cli": "PASS",
                            "exact_bytes": "PASS",
                            "stale_read": "DENIED",
                            "sandbox": sys.platform == "darwin",
                        }
                    )
                )
            finally:
                server.should_exit = True
                thread.join(timeout=5)


if __name__ == "__main__":
    main()
