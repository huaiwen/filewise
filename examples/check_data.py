"""Synthetic CLI → HTTP proof of contracts, restatements and historical-use guards."""

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

from filewise.api import create_app
from filewise.engine import Engine, now
from filewise.models import Actor
from filewise.projects import Projects


def main():
    with tempfile.TemporaryDirectory(prefix="filewise-data-check-") as directory:
        root = Path(directory).resolve()
        files = root / "files"
        files.mkdir()
        (files / "data.csv").write_text("id,amount\na,100\nb,100\n")
        (files / "model.txt").write_text("Synthetic model artifact; no model is executed in this example.")
        engine = Engine(root / "filewise.db")
        projects = Projects(engine)
        operator = Actor(id="operator", roles={"editor"})
        agent = Actor(id="agent", audience="agent", roles={"reader", "editor"}, workspace_projects={"p"})
        projects.create({"id": "p", "name": "Synthetic restatements"}, operator, files)
        initial = projects.snapshot("p", operator)
        base, cutoff = initial["release_id"], initial["availability"]["available_at"]
        env = dict(os.environ, FILEWISE_DB="/denied/not-a-database.db", FILEWISE_TOKEN="a" * 32)
        if env.get("PYTHONPATH"):
            env["PYTHONPATH"] = os.pathsep.join(
                str(Path(p).resolve()) for p in env["PYTHONPATH"].split(os.pathsep)
            )

        def cli(*args, exit_code=0):
            result = subprocess.run(
                [sys.executable, "-m", "filewise", "agent", *args],
                cwd=root,
                env=env,
                capture_output=True,
                text=True,
                timeout=120,
            )
            assert result.returncode == exit_code, (args, result.stdout, result.stderr)
            return json.loads(result.stdout if result.stdout else result.stderr)

        contract = {
            "meaning": {"unit": "CNY", "population": "card_panel", "period": "2025-01"},
            "allowed_uses": ["panel_spending"],
            "checks": [
                {"id": "rows", "op": "row_count", "minimum": 1},
                {"id": "identity", "op": "unique", "column": "id"},
                {"id": "amount", "op": "range", "column": "amount", "minimum": 0},
                {"id": "panel", "op": "distinct_count_change", "column": "id", "max_change": 0.1},
            ],
        }
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            env["FILEWISE_URL"] = "http://127.0.0.1:" + str(sock.getsockname()[1])
            server = uvicorn.Server(uvicorn.Config(create_app(engine, {"a" * 32: agent}), log_level="error"))
            thread = threading.Thread(target=server.run, kwargs={"sockets": [sock]}, daemon=True)
            thread.start()
            try:
                for _ in range(100):
                    if server.started:
                        break
                    time.sleep(0.02)
                assert server.started
                revised = cli(
                    "write",
                    "p",
                    "data.csv",
                    "--base",
                    base,
                    "--content",
                    "id,amount\na,80\nb,80\n",
                    "--meta",
                    json.dumps({"data": contract, "valid_from": "2000-01-01T00:00:00Z"}),
                    "-m",
                    "Synthetic supplier correction",
                )
                assert "100" in cli("read", "p", "data.csv", "--as-of", cutoff)["text"]
                assert "80" in cli("read", "p", "data.csv")["text"]
                assert cli("search", "p", "100", "--mode", "exact", "--as-of", cutoff)["version"] == base
                cli("read", "p", "data.csv", "--version", revised["version"], "--as-of", cutoff, exit_code=1)
                assert cli("quality", "p", "--path", "data.csv")["decision"] == "PASS"
                requirements = root / "requirements.json"
                requirements.write_text(
                    json.dumps({"data.csv": {"expect": {"unit": "USD"}, "purpose": "total_market_sales"}})
                )
                assert (
                    cli("quality", "p", "--requirements", str(requirements), exit_code=2)["decision"]
                    == "BLOCKED"
                )
                task = cli(
                    "compile",
                    "p",
                    "--goal",
                    "Check sales",
                    "--path",
                    "data.csv",
                    "--requirements",
                    str(requirements),
                )
                assert (
                    cli("verify", "p", "--phase", "preflight", "--task-id", task["task_id"], exit_code=2)[
                        "decision"
                    ]
                    == "BLOCKED"
                )
                model_task = cli(
                    "compile",
                    "p",
                    "--goal",
                    "Historical review",
                    "--path",
                    "data.csv",
                    "--as-of",
                    cutoff,
                    "--model-use",
                    "generative",
                )
                assert (
                    cli(
                        "verify", "p", "--phase", "preflight", "--task-id", model_task["task_id"], exit_code=2
                    )["decision"]
                    == "NEEDS_REVIEW"
                )
                derived = cli(
                    "write",
                    "p",
                    "derived.json",
                    "--base",
                    revised["version"],
                    "--content",
                    '{"result":160}',
                    "--meta",
                    json.dumps(
                        {
                            "processing": "model",
                            "lineage": [
                                {"version": revised["version"], "path": "data.csv", "role": "data"},
                                {"version": base, "path": "model.txt", "role": "model"},
                            ],
                        }
                    ),
                    "-m",
                    "Synthetic declared processing output",
                )
                lineage = cli("quality", "p", "--path", "derived.json", "--as-of", now(), exit_code=2)
                assert lineage["decision"] == "NEEDS_REVIEW" and len(lineage["inputs"]) >= 2
                expanded = cli(
                    "write",
                    "p",
                    "data.csv",
                    "--base",
                    derived["version"],
                    "--content",
                    "id,amount\na,80\nb,80\nc,80\n",
                    "--meta",
                    json.dumps({"data": contract}),
                    "-m",
                    "Synthetic panel expansion",
                )
                assert expanded["saved"] and not expanded["published"]
                report = cli("quality", "p", "--path", "data.csv", exit_code=2)
                panel = next(t for t in report["files"][0]["quality"]["tests"] if t["id"] == "panel")
                assert panel["actual"]["relative_change"] == 0.5
                with engine.connect() as db:
                    assert not db.execute("SELECT * FROM active").fetchone()
                print(
                    json.dumps(
                        {
                            "cli_http": "PASS",
                            "restatement_time_isolation": "PASS",
                            "meaning_and_cohort_guards": "PASS",
                            "pinned_lineage": "PASS",
                            "model_hindsight": "NEEDS_REVIEW",
                            "production_activated": False,
                        }
                    )
                )
            finally:
                server.should_exit = True
                thread.join(timeout=5)
                assert not thread.is_alive()


if __name__ == "__main__":
    main()
