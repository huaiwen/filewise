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


def main(argv=None):
    parser = argparse.ArgumentParser(
        prog="filewise", description="Evidence-bound knowledge and release control"
    )
    parser.add_argument("--db", default=os.environ.get("FILEWISE_DB", ".filewise/filewise.db"))
    parser.add_argument(
        "--actor", default="local-editor", help="Trusted local identity; not an HTTP credential"
    )
    parser.add_argument("--roles", default="editor", help="Comma-separated local roles")
    commands = parser.add_subparsers(dest="command", required=True)
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
    auth = commands.add_parser("auth-init", help="Generate four separate role tokens; refuses to overwrite")
    auth.add_argument("--out", type=Path, default=Path(".filewise/tokens.json"))
    serve = commands.add_parser("serve")
    serve.add_argument("--tokens", type=Path, default=os.environ.get("FILEWISE_TOKENS_FILE"))
    serve.add_argument("--host", default="127.0.0.1")
    serve.add_argument("--port", type=int, default=8000)
    args = parser.parse_args(argv)
    try:
        if args.command == "auth-init":
            tokens = {
                secrets.token_urlsafe(32): {"id": f"local-{role}", "roles": [role]}
                for role in ("reader", "editor", "reviewer", "publisher")
            }
            args.out.parent.mkdir(parents=True, exist_ok=True)
            fd = os.open(args.out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "w", encoding="utf-8") as handle:
                handle.write(json.dumps(tokens, indent=2) + "\n")
            result = {"tokens_file": str(args.out), "identities": 4}
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
            if args.command == "demo":
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
        return 0
    except (FilewiseError, ValidationError, OSError, ValueError) as exc:
        print(json.dumps({"error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
