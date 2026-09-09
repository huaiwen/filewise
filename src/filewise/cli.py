"""CLI for trusted local operators. JSON goes to stdout; errors to stderr."""

import argparse
import json
import os
import secrets
import sys
from pathlib import Path

from pydantic import ValidationError

from .engine import Engine, FilewiseError, canonical
from .ingest import MAX_BYTES, ingest
from .models import (
    Actor,
    BuildRequest,
    DiffRequest,
    ImpactRequest,
    ResolveRequest,
    Revision,
    Scope,
    SourcePolicy,
)


def local_tokens(state):
    from .api import load_tokens
    from .projects import ALL_ROLES

    owner = Actor(id="local-owner", roles=ALL_ROLES)
    token_file = state / "tokens.json"
    if token_file.resolve() != token_file.absolute():
        raise FilewiseError("Filewise credentials must not be a symlink")
    if not token_file.exists():
        tokens = {
            secrets.token_urlsafe(32): owner.model_dump(mode="json"),
            secrets.token_urlsafe(32): {"id": "local-agent", "roles": ["reader"], "audience": "agent"},
        }
        fd = os.open(token_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(json.dumps(tokens, indent=2) + "\n")
    tokens = load_tokens(token_file)
    token = next(
        (
            key
            for key, actor in tokens.items()
            if actor.id == "local-owner" and actor.roles == ALL_ROLES and actor.audience == "operator"
        ),
        None,
    )
    if token is None:
        raise FilewiseError("Local owner credential is missing from " + str(token_file))
    return tokens, token


def start(args):
    import threading
    import time
    import webbrowser

    import uvicorn

    from .api import create_app

    database = Path(args.db).absolute()
    if database.resolve() != database:
        raise FilewiseError("Filewise state must not be a symlink")
    database.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    engine = Engine(database)
    tokens, token = local_tokens(engine.path.parent)
    address = f"http://127.0.0.1:{args.port}"
    url = address + "/#connect=" + token
    print(
        "Filewise 工作台：" + address + "\n选择关注文件夹，再连接常用 Agent。Ctrl+C 停止服务。",
        file=sys.stderr,
        flush=True,
    )
    server = uvicorn.Server(
        uvicorn.Config(
            create_app(engine, tokens, local_setup=True), host="127.0.0.1", port=args.port, access_log=False
        )
    )
    if args.no_open:
        print("本机连接链接：" + url, file=sys.stderr, flush=True)
    else:

        def open_ready():
            for _ in range(50):
                if server.started:
                    webbrowser.open(url)
                    return
                if server.should_exit:
                    return
                time.sleep(0.1)

        threading.Thread(target=open_ready, daemon=True).start()
    server.run()


def followed_project(root):
    """Open only the explicitly selected folder's local Filewise state."""
    from .middleware import Middleware
    from .projects import Projects

    root = root.resolve(strict=True)
    state = root / ".filewise"
    if state.is_symlink() or (state / "filewise.db").is_symlink():
        raise FilewiseError("Filewise state must not be a symlink")
    if not (state / "filewise.db").is_file():
        raise FilewiseError("Run filewise follow for this folder first")
    projects = Projects(Engine(state / "filewise.db"))
    actor = Actor(id="local-owner", roles={"reader", "editor", "reviewer", "publisher"})
    with projects.engine.connect() as db:
        project = projects._project(db, "workspace", actor)
    if project["root"] != str(root):
        raise FilewiseError("Registered directory does not match this folder")
    return Middleware(projects), actor


def follow(args):
    import uvicorn

    from .api import create_app
    from .middleware import Middleware, WatchConfig
    from .projects import ALL_ROLES, Projects

    root = args.root.resolve(strict=True)
    if not root.is_dir():
        raise FilewiseError("Select a directory to follow")
    state = root / ".filewise"
    if state.is_symlink():
        raise FilewiseError("Filewise state must not be a symlink")
    state.mkdir(mode=0o700, exist_ok=True)
    for name in ("filewise.db", "tokens.json", ".gitignore"):
        if (state / name).is_symlink():
            raise FilewiseError("Filewise state must not contain symlinks")
    (state / ".gitignore").write_text("*\n", encoding="utf-8")
    engine = Engine(state / "filewise.db")
    projects = Projects(engine)
    owner = Actor(id="local-owner", roles=ALL_ROLES)
    with engine.connect() as db:
        exists = db.execute("SELECT 1 FROM projects WHERE id='workspace'").fetchone()
    if not exists:
        spec = json.loads(args.spec.read_text()) if args.spec else {}
        spec = {**spec, "id": "workspace", "name": root.name}
        if args.include:
            spec["includes"] = args.include
        projects.create(spec, owner, root)
    else:
        followed_project(root)
        if args.spec or (
            args.include
            and projects.detail("workspace", owner)["spec"].get("includes", ["**"]) != args.include
        ):
            raise FilewiseError("This folder already has a saved scope; restart without --include/--spec")
    middleware = Middleware(projects)
    settings = middleware.config("workspace", owner)
    if settings["baseline"] is None:
        middleware.configure("workspace", WatchConfig(), owner)
    tokens, token = local_tokens(state)
    print(
        f"Filewise: http://127.0.0.1:{args.port}\n访问凭据（粘贴到工作台）: {token}\n关注目录: {root}\nCtrl+C 停止自动关注；再次运行同一命令可继续。",
        file=sys.stderr,
        flush=True,
    )
    uvicorn.run(
        create_app(engine, tokens, local_setup=True), host="127.0.0.1", port=args.port, access_log=False
    )


def main(argv=None):
    from . import agent

    parser = argparse.ArgumentParser(
        prog="filewise", description="Evidence-bound knowledge and release control"
    )
    parser.add_argument("--db", default=os.environ.get("FILEWISE_DB", ".filewise/filewise.db"))
    parser.add_argument(
        "--actor", default="local-editor", help="Trusted local identity; not an HTTP credential"
    )
    parser.add_argument("--roles", default="editor", help="Comma-separated local roles")
    commands = parser.add_subparsers(dest="command", required=True)
    agent.arguments(commands)
    starter = commands.add_parser(
        "start", help="Open the local Filewise setup and Agent connection workbench"
    )
    starter.add_argument("--port", type=int, default=8000)
    starter.add_argument(
        "--no-open", action="store_true", help="Print the local sign-in link without opening a browser"
    )
    follow_parser = commands.add_parser(
        "follow", help="Configure and watch a folder; serve its local workbench"
    )
    follow_parser.add_argument("root", type=Path)
    follow_parser.add_argument(
        "--include", action="append", help="Managed relative glob; repeat for more formats (first setup)"
    )
    follow_parser.add_argument("--spec", type=Path, help="Dependency/check contract JSON (first setup)")
    follow_parser.add_argument("--port", type=int, default=8000)
    guarded = commands.add_parser("run", help="Run a new command in a guarded working copy (macOS)")
    guarded.add_argument("root", type=Path)
    guarded.add_argument("executable", nargs=argparse.REMAINDER)
    recovery = commands.add_parser("recover", help="Recover an interrupted writeback for a followed folder")
    recovery.add_argument("root", type=Path)
    recovery.add_argument("release_id")
    hook = commands.add_parser("agent-hook", help=argparse.SUPPRESS)
    hook.add_argument("project_id")
    hook.add_argument("agent", choices=("codex", "claude", "pi"))
    native_shell = commands.add_parser("agent-shell", help=argparse.SUPPRESS)
    native_shell.add_argument("job")
    native_shell.add_argument("worker_pid", type=int)
    worker = commands.add_parser("agent-worker", help=argparse.SUPPRESS)
    for argument in ("root", "worktree", "private_state", "cwd", "job"):
        worker.add_argument(argument)
    worker.add_argument("done_fd", type=int)
    worker.add_argument("shell_command")
    connection = commands.add_parser("connect", help="Install or inspect a project-scoped Agent connection")
    connection.add_argument("project_id")
    connection.add_argument("agent", choices=("codex", "claude", "pi"))
    connection.add_argument("--remove", action="store_true")
    connection.add_argument("--preview", action="store_true")
    showcase = commands.add_parser("showcase", help="Serve a disposable synthetic workspace demo")
    showcase.add_argument("--port", type=int, default=8765)
    project = commands.add_parser("project", help="Trusted local project administration")
    actions = project.add_subparsers(dest="action", required=True)
    add = actions.add_parser("add")
    add.add_argument("root", type=Path)
    add.add_argument("--id", required=True)
    add.add_argument("--name", required=True)
    add.add_argument("--include", action="append", help="Managed relative glob; repeat for more formats")
    add.add_argument("--spec", type=Path, help="Optional dependency and check contract JSON")
    launch = actions.add_parser("launch", help="Start a new Agent behind the macOS file boundary")
    launch.add_argument("project_id")
    launch.add_argument("--tokens", type=Path, required=True)
    launch.add_argument("--url", default=os.environ.get("FILEWISE_URL", "http://127.0.0.1:8000"))
    launch.add_argument("executable", nargs=argparse.REMAINDER)
    for name in ("status", "sync", "review", "publish"):
        action = actions.add_parser(name)
        action.add_argument("project_id")
        if name in ("review", "publish"):
            action.add_argument("release_id")
        if name == "publish":
            action.add_argument("--expected-active", default=None)
    commands.add_parser("demo", help="Run synthetic lifecycle in a fresh database")
    commands.add_parser("overview")
    for command in ("scope", "propose", "resolve", "impact", "diff", "build"):
        sub = commands.add_parser(command, help=f"{command} using a JSON request file")
        sub.add_argument("file", type=Path)
    upload = commands.add_parser("ingest")
    upload.add_argument("scope_id")
    upload.add_argument("file", type=Path)
    upload.add_argument("--acl", help="Comma-separated source roles; defaults to operator's roles")
    policy = commands.add_parser("source-policy")
    policy.add_argument("source_id")
    policy.add_argument("file", type=Path)
    decision = commands.add_parser("decide")
    decision.add_argument("revision_id")
    decision.add_argument("decision", choices=("approved", "revoked"))
    source = commands.add_parser("source")
    source.add_argument("source_id")
    audit = commands.add_parser("audit")
    audit.add_argument("scope_id")
    for command in ("release", "verify", "approve", "activate", "rollback", "revoke", "context"):
        sub = commands.add_parser(command)
        sub.add_argument("release_id")
        if command in ("activate", "rollback"):
            sub.add_argument("--expected-active", default=None)
        if command == "context":
            sub.add_argument("object_ids", nargs="+")
    auth = commands.add_parser(
        "auth-init", help="Generate operator and restricted Agent tokens; refuses overwrite"
    )
    auth.add_argument("--out", type=Path, default=Path(".filewise/tokens.json"))
    serve = commands.add_parser("serve")
    serve.add_argument("--tokens", type=Path, default=os.environ.get("FILEWISE_TOKENS_FILE"))
    serve.add_argument("--host", default="127.0.0.1")
    serve.add_argument("--port", type=int, default=8000)
    args = parser.parse_args(argv)
    if args.command == "agent-hook":
        # Hook failures must block before-use calls; ordinary exit 1 is fail-open in some hosts.
        try:
            from .connections import Connections
            from .middleware import Middleware
            from .projects import Projects

            raw = sys.stdin.buffer.read(11 * 1024 * 1024 + 1)
            if len(raw) > 11 * 1024 * 1024:
                raise ValueError("Hook request too large")
            event = json.loads(raw)
            result = Connections(Middleware(Projects(Engine(args.db)))).handle(
                args.project_id, args.agent, event
            )
            print(canonical(result))
            return 0
        except Exception as exc:
            print("Filewise 连接异常，已阻止操作：" + str(exc), file=sys.stderr)
            return 2
    try:
        if args.command == "agent-shell":
            from .connections import run_shell

            return run_shell(args.job, args.worker_pid)
        if args.command == "agent-worker":
            from .connections import shell_worker

            return shell_worker(
                args.root,
                args.worktree,
                args.private_state,
                args.cwd,
                args.job,
                args.done_fd,
                args.shell_command,
            )
        if args.command == "connect":
            from .connections import Connections
            from .middleware import Middleware
            from .projects import Projects

            connections = Connections(Middleware(Projects(Engine(args.db))))
            actor = Actor(id=args.actor, roles=set(args.roles.split(",")))
            action = (
                connections.disconnect
                if args.remove
                else connections.preview
                if args.preview
                else connections.install
            )
            result = action(args.project_id, args.agent, actor)
        elif args.command == "start":
            start(args)
            return 0
        elif args.command == "follow":
            follow(args)
            return 0
        elif args.command in ("run", "recover"):
            middleware, actor = followed_project(args.root)
            if args.command == "run":
                command = args.executable[1:] if args.executable[:1] == ["--"] else args.executable
                result = middleware.guard("workspace", command, actor)
                if result["release_id"]:
                    print(
                        "候选修改已保存，原文件未改变。回到 Filewise 工作台查看、审核并写回。",
                        file=sys.stderr,
                    )
            else:
                result = middleware.recover("workspace", args.release_id, actor)
        elif args.command == "agent":
            result = agent.run(args)
        elif args.command == "showcase":
            import tempfile

            import uvicorn

            from .api import create_app
            from .showcase import Showcase

            with tempfile.TemporaryDirectory(prefix="filewise-demo-") as directory:
                demo = Showcase(directory)
                print(f"Synthetic Filewise demo: http://127.0.0.1:{args.port}", file=sys.stderr)
                uvicorn.run(
                    create_app(demo.engine, demo.tokens, showcase=demo),
                    host="127.0.0.1",
                    port=args.port,
                    access_log=False,
                )
            return 0
        elif args.command == "auth-init":
            tokens = {
                secrets.token_urlsafe(32): {"id": f"local-{role}", "roles": [role]}
                for role in ("reader", "editor", "reviewer", "publisher")
            }
            tokens[secrets.token_urlsafe(32)] = {
                "id": "local-agent",
                "roles": ["reader"],
                "audience": "agent",
            }
            args.out.parent.mkdir(parents=True, exist_ok=True)
            fd = os.open(args.out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "w", encoding="utf-8") as handle:
                handle.write(json.dumps(tokens, indent=2) + "\n")
            result = {"tokens_file": str(args.out), "identities": len(tokens)}
        elif args.command == "serve":
            import uvicorn

            from .api import create_app, load_tokens

            if not args.tokens:
                raise FilewiseError(
                    "Use auth-init, then serve --tokens PATH; there are no default credentials"
                )
            tokens = load_tokens(args.tokens)
            uvicorn.run(create_app(Engine(args.db), tokens), host=args.host, port=args.port, access_log=False)
            return 0
        else:
            actor = Actor(id=args.actor, roles=set(args.roles.split(",")))
            engine = Engine(args.db)
            data = (
                json.loads(args.file.read_text(encoding="utf-8"))
                if hasattr(args, "file") and args.command != "ingest"
                else None
            )
            if args.command == "project":
                from .projects import Projects

                projects = Projects(engine)
                if args.action == "launch":
                    command = args.executable[1:] if args.executable[:1] == ["--"] else args.executable
                    result = agent.launch(projects, args.project_id, actor, args.tokens, args.url, command)
                elif args.action == "add":
                    spec = json.loads(args.spec.read_text()) if args.spec else {}
                    if args.include:
                        spec["includes"] = args.include
                    result = projects.create({**spec, "id": args.id, "name": args.name}, actor, args.root)
                elif args.action == "status":
                    result = projects.detail(args.project_id, actor)
                elif args.action == "sync":
                    result = projects.snapshot(args.project_id, actor)
                elif args.action == "review":
                    result = projects.approve(args.project_id, args.release_id, actor)
                else:
                    result = projects.activate(args.project_id, args.release_id, args.expected_active, actor)
            elif args.command == "demo":
                from .demo import run_demo

                result = run_demo(engine)
            elif args.command == "scope":
                result = engine.add_scope(Scope.model_validate(data), actor)
            elif args.command == "propose":
                result = engine.add_revision(Revision.model_validate(data), actor)
            elif args.command == "build":
                result = engine.build(BuildRequest.model_validate(data), actor)
            elif args.command in ("resolve", "impact", "diff"):
                model = {"resolve": ResolveRequest, "impact": ImpactRequest, "diff": DiffRequest}[
                    args.command
                ]
                result = getattr(engine, args.command)(actor=actor, **model.model_validate(data).model_dump())
            elif args.command == "ingest":
                with args.file.open("rb") as handle:
                    result = ingest(
                        engine,
                        args.scope_id,
                        args.file.name,
                        handle.read(MAX_BYTES + 1),
                        actor,
                        set(args.acl.split(",")) if args.acl is not None else None,
                    )
            elif args.command == "source-policy":
                result = engine.source_policy(
                    args.source_id, actor=actor, **SourcePolicy.model_validate(data).model_dump()
                )
            elif args.command == "decide":
                result = engine.decide_revision(args.revision_id, args.decision, actor)
            elif args.command in ("activate", "rollback"):
                result = engine.activate(
                    args.release_id, args.expected_active, actor, rollback=args.command == "rollback"
                )
            elif args.command == "context":
                result = engine.context(args.release_id, args.object_ids, actor)
            elif args.command == "source":
                result = engine.source(args.source_id, actor)
            elif args.command == "audit":
                result = engine.audit(args.scope_id, actor)
            elif args.command == "overview":
                result = engine.overview(actor)
            else:
                result = getattr(engine, args.command)(args.release_id, actor)
        print(canonical(result))
        if args.command == "run" or (args.command == "project" and args.action == "launch"):
            return result["exit_code"]
        return 0
    except (FilewiseError, ValidationError, OSError, ValueError) as exc:
        print(json.dumps({"error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
