"""Synthetic real CLI/HTTP retrieval proof. --semantic explicitly prepares public local model weights.

python examples/check_retrieval.py
python examples/check_retrieval.py --semantic --model-cache /path/to/model-cache
"""

import argparse
import json
import os
import socket
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path
from unittest.mock import patch

import uvicorn

from filewise.api import create_app
from filewise.engine import Engine
from filewise.middleware import Middleware
from filewise.models import Actor
from filewise.projects import Projects
from filewise.workspace import SearchQuery, Workspace


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--semantic", action="store_true")
    parser.add_argument("--model-cache", type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="filewise-retrieval-check-") as directory:
        root = Path(directory).resolve()
        files = root / "files"
        files.mkdir()
        texts = {
            "machine.md": "发生非计划停机时，先切断设备电源，挂牌锁定，再由维修人员排查故障。",
            "fire.md": "发现火情后立即发出警报，组织人员疏散，拨打消防救援电话。",
            "leave.md": "员工休假须事先在系统中提交申请，由直属主管审批。",
            "shipping.md": "出库前核对包装标签与收货地址，填写物流交接记录。",
        }
        for path, text in texts.items():
            (files / path).write_text(text)
        engine = Engine(root / "filewise.db")
        projects = Projects(engine)
        editor = Actor(id="editor", roles={"editor"})
        actor = Actor(id="agent", audience="agent", roles={"reader", "editor"}, workspace_projects={"p"})
        projects.create({"id": "p", "name": "Synthetic search"}, editor, files)
        base = projects.snapshot("p", editor)["release_id"]
        env = dict(os.environ, FILEWISE_DB="/denied/not-a-database.db", FILEWISE_TOKEN="x" * 32)
        if env.get("PYTHONPATH"):
            env["PYTHONPATH"] = os.pathsep.join(
                str(Path(p).resolve()) for p in env["PYTHONPATH"].split(os.pathsep)
            )

        def command(*argv):
            result = subprocess.run(
                [sys.executable, "-m", "filewise", *argv],
                cwd=root,
                env=env,
                capture_output=True,
                text=True,
                timeout=600,
            )
            assert result.returncode == 0, (argv, result.stderr)
            return json.loads(result.stdout)

        if args.semantic:
            setup_args = ["--db", str(engine.path), "retrieval", "setup"]
            if args.model_cache:
                setup_args += ["--cache-dir", str(args.model_cache.resolve())]
            configured = command(*setup_args)
            assert configured["semantic"] == "configured"
            # Cold in-process model loading and inference are offline, not remote embedding calls.
            workspace = Workspace(Middleware(projects))
            with patch("socket.socket.connect", side_effect=AssertionError("Unexpected outbound connection")):
                proof = workspace.search(
                    "p", SearchQuery(query="How can an employee request time off?", mode="semantic"), actor
                )
            assert proof["hits"][0]["path"] == "leave.md", proof
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            env["FILEWISE_URL"] = "http://127.0.0.1:" + str(sock.getsockname()[1])
            server = uvicorn.Server(uvicorn.Config(create_app(engine, {"x" * 32: actor}), log_level="error"))
            thread = threading.Thread(target=server.run, kwargs={"sockets": [sock]}, daemon=True)
            thread.start()

            def cli(*argv):
                return command("agent", *argv)

            try:
                for _ in range(100):
                    if server.started:
                        break
                    time.sleep(0.02)
                assert server.started
                assert cli("search", "p", "切断 电源", "--mode", "lexical")["hits"][0]["path"] == "machine.md"
                question = "机器突然不运转该如何处理？"
                lexical = cli("search", "p", question, "--mode", "lexical")
                assert not lexical["hits"], lexical
                if args.semantic:
                    found = cli("search", "p", question, "--mode", "semantic")
                    assert found["hits"][0]["path"] == "machine.md", found
                    assert "semantic" in found["hits"][0]["matches"]
                    assert not found["retrieval"]["document_upload"]
                    repeated = cli("search", "p", question, "--mode", "semantic")
                    assert repeated["retrieval"]["new_embeddings"] == 0
                    task = cli(
                        "compile",
                        "p",
                        "--goal",
                        question,
                        "--retrieval-mode",
                        "semantic",
                        "--retrieval-limit",
                        "1",
                    )
                    assert task["paths"] == ["machine.md"], task
                    assert (
                        cli("verify", "p", "--task-id", task["task_id"], "--phase", "preflight")["decision"]
                        == "PASS"
                    )
                    # Derived-vector corruption is repaired from the pinned local model and sources.
                    with engine.connect(True) as db:
                        db.execute("UPDATE retrieval_vectors SET checksum='bad'")
                    assert (
                        cli("search", "p", question, "--mode", "semantic")["retrieval"]["new_embeddings"] > 0
                    )
                saved = cli(
                    "write",
                    "p",
                    "machine.md",
                    "--base",
                    base,
                    "--content",
                    "维护任务已完成，设备恢复运行。",
                    "-m",
                    "Update synthetic maintenance",
                )
                assert cli("search", "p", "恢复运行", "--mode", "exact")["version"] == saved["version"]
                assert not cli("search", "p", "切断电源", "--mode", "exact")["hits"]
                assert cli("search", "p", "切断设备电源", "--mode", "exact", "--version", base)["hits"]
                if args.semantic:
                    updated = cli("search", "p", question, "--mode", "semantic")
                    assert updated["retrieval"]["new_embeddings"] == 1, updated["retrieval"]
                print(
                    json.dumps(
                        {
                            "ranked_cli_http": "PASS",
                            "latest_and_history": "ISOLATED",
                            "real_local_multilingual_model": "PASS" if args.semantic else "not_requested",
                            "paraphrase_without_keyword_hit": "PASS" if args.semantic else "not_requested",
                            "document_upload": False,
                        }
                    )
                )
            finally:
                server.should_exit = True
                thread.join(timeout=5)
                assert not thread.is_alive()


if __name__ == "__main__":
    main()
