"""Real Agent CLI + HTTP lifecycle using disposable synthetic files, no model calls.

python examples/check_workspace.py
"""

import json
import os
import socket
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path

import uvicorn

from filewise.api import create_app, load_tokens
from filewise.showcase import Showcase


def main():
    with tempfile.TemporaryDirectory(prefix="filewise-workspace-check-") as directory:
        directory = str(Path(directory).resolve())
        demo = Showcase(directory)
        token_file = Path(directory, "tokens.json")
        token_file.write_text(json.dumps({k: v.model_dump(mode="json") for k, v in demo.tokens.items()}))
        token_file.chmod(0o600)
        scratch = Path(directory, "client")
        scratch.mkdir()
        env = dict(os.environ, FILEWISE_DB="/denied/no-agent-database.db")
        if env.get("PYTHONPATH"):
            env["PYTHONPATH"] = os.pathsep.join(
                str(Path(p).resolve()) for p in env["PYTHONPATH"].split(os.pathsep)
            )

        def command(*args, code=0):
            completed = subprocess.run(
                [sys.executable, "-m", "filewise", *args],
                cwd=scratch,
                env=env,
                capture_output=True,
                text=True,
            )
            assert completed.returncode == code, (args, completed.returncode, completed.stderr)
            return json.loads(completed.stdout if code != 1 else completed.stderr)

        credential = command(
            "--db", str(demo.engine.path), "auth-agent", "hydraulic-demo", "--tokens", str(token_file)
        )
        assert credential["actor"]["roles"] == ["editor", "reader"]
        assert len(load_tokens(token_file)) == len(demo.tokens) + 1
        assert token_file.stat().st_mode & 0o777 == 0o600
        env["FILEWISE_TOKEN"] = credential["FILEWISE_TOKEN"]
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            env["FILEWISE_URL"] = "http://127.0.0.1:" + str(sock.getsockname()[1])
            server = uvicorn.Server(
                uvicorn.Config(create_app(demo.engine, load_tokens(token_file)), log_level="error")
            )
            thread = threading.Thread(target=server.run, kwargs={"sockets": [sock]}, daemon=True)
            thread.start()

            def cli(*args, code=0):
                return command("agent", *args, code=code)

            project = "hydraulic-demo"
            try:
                for _ in range(100):
                    if server.started:
                        break
                    time.sleep(0.02)
                assert server.started
                assert cli("projects")[0]["id"] == project
                base = cli("ls", project)["version"]
                assert "100" in cli("read", project, "requirement.json")["text"]
                # Filewise writes originals; there is no raw edit/notify/watch step.
                argv = (
                    "write",
                    project,
                    "requirement.json",
                    "--base",
                    base,
                    "--content",
                    '{"pressure_kpa":120}',
                    "--meta",
                    '{"summary":"Synthetic pressure change","facts":{"pressure":120}}',
                    "--request-id",
                    "http-write",
                    "--model",
                    "synthetic",
                    "--task",
                    "pressure-update",
                    "-m",
                    "Raise pressure",
                )
                saved = cli(*argv)
                rid = saved["version"]
                assert saved["saved"] and saved["verification"]["decision"] == "BLOCKED"
                assert cli(*argv)["version"] == rid
                assert json.loads((demo.root / "requirement.json").read_text())["pressure_kpa"] == 120
                assert cli("read", project, "requirement.json")["metadata"]["facts"]["pressure"] == 120
                assert "100" in cli("read", project, "requirement.json", "--version", base)["text"]
                assert cli("search", project, "120")["hits"]
                assert (
                    cli("resolve", project, "--path", "requirement.json")["state"]["requirement.json"][
                        "fields"
                    ]["value:/pressure_kpa"]
                    == 120
                )
                assert cli("diff", project, base)["summary"]["modified"] == 1
                assert "delivery.md" in cli("impact", project, "--path", "requirement.json")["affected"]
                assert cli("verify", project, code=2)["decision"] == "BLOCKED"
                assert (
                    "STALE"
                    in cli(
                        "write",
                        project,
                        "requirement.json",
                        "--base",
                        base,
                        "--content",
                        "bad",
                        "-m",
                        "Refused stale write",
                        code=1,
                    )["error"]
                )
                # Modify a downloaded Excel copy, then let Filewise save its exact bytes.
                copy = scratch / "inspection.xlsx"
                cli("read", project, "inspection.xlsx", "--output", str(copy))
                from openpyxl import load_workbook

                book = load_workbook(copy)
                book.active["B2"] = 120
                book.save(copy)
                book.close()
                repaired = cli(
                    "write",
                    project,
                    "inspection.xlsx",
                    "--base",
                    rid,
                    "--file",
                    str(copy),
                    "--require-pass",
                    "-m",
                    "Align inspection",
                )
                assert repaired["saved"] and repaired["verification"]["decision"] == "PASS"
                assert (demo.root / "inspection.xlsx").read_bytes() == copy.read_bytes()
                checks = scratch / "checks.json"
                checks.write_text(
                    '[{"id":"pressure","object_id":"result","field":"pressure","expected":120}]'
                )
                task = cli(
                    "compile",
                    project,
                    "--path",
                    "delivery.md",
                    "--direction",
                    "reverse",
                    "--goal",
                    "Check delivery",
                    "--checks",
                    str(checks),
                )
                assert "README.md" not in task["paths"]
                assert (
                    cli("verify", project, "--phase", "preflight", "--task-id", task["task_id"])["decision"]
                    == "PASS"
                )
                result = scratch / "result.json"
                result.write_text('{"pressure":100}')
                assert (
                    cli(
                        "verify",
                        project,
                        "--phase",
                        "postflight",
                        "--task-id",
                        task["task_id"],
                        "--result",
                        str(result),
                        code=2,
                    )["decision"]
                    == "BLOCKED"
                )
                result.write_text('{"pressure":120}')
                assert (
                    cli(
                        "verify",
                        project,
                        "--phase",
                        "postflight",
                        "--task-id",
                        task["task_id"],
                        "--result",
                        str(result),
                    )["decision"]
                    == "PASS"
                )
                source = cli("read", project, "requirement.json")["source_id"]
                assert cli("evidence", project, source)["fragments"]
                assert cli("trace", project, "--version", rid)["write"]["task"] == "pressure-update"
                assert len(cli("versions", project)["versions"]) == 3
                deleted = cli(
                    "delete",
                    project,
                    "README.md",
                    "--base",
                    repaired["version"],
                    "-m",
                    "Remove synthetic note",
                )
                assert deleted["saved"] and not (demo.root / "README.md").exists()
                assert demo.projects.detail(project, demo.editor)["active_release"] == base
                print(
                    json.dumps(
                        {
                            "agent_cli_http": "PASS",
                            "writes_with_metadata": "PASS",
                            "history_diff_impact": "PASS",
                            "compile_and_three_phase_verify": "PASS",
                            "production_pointer": "UNCHANGED",
                            "data": "synthetic_only",
                        }
                    )
                )
            finally:
                server.should_exit = True
                thread.join(timeout=5)
                assert not thread.is_alive()


if __name__ == "__main__":
    main()
