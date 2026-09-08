"""Authenticated local REST boundary. Tokens map to server-owned identities."""

import hmac
import json
from pathlib import Path

from fastapi import Depends, FastAPI, Request, UploadFile
from fastapi.responses import FileResponse, JSONResponse
from fastapi.security import HTTPBearer
from pydantic import ValidationError

from .engine import Engine, FilewiseError, require
from .ingest import MAX_BYTES, ingest
from .models import (
    ActivateRequest,
    Actor,
    BuildRequest,
    ContextRequest,
    DiffRequest,
    ImpactRequest,
    ResolveRequest,
    Revision,
    Scope,
    SourcePolicy,
)


def load_tokens(path):
    return validate_tokens(json.loads(Path(path).read_text(encoding="utf-8")))


def validate_tokens(tokens):
    if not isinstance(tokens, dict) or not tokens:
        raise ValueError("A nonempty token-to-actor mapping is required")
    result, identities = {}, {}
    for token, actor in tokens.items():
        if (
            not isinstance(token, str)
            or len(token) < 32
            or not token.isascii()
            or any(c.isspace() for c in token)
        ):
            raise ValueError("Tokens must be at least 32 ASCII characters without whitespace")
        actor = Actor.model_validate(actor)
        if actor.id in identities and identities[actor.id] != actor.roles:
            raise ValueError("Each actor ID must have one consistent role set")
        result[token] = actor
        identities[actor.id] = actor.roles
    return result


class Boundary:
    """Authenticate before parsing, and bound bodies including chunked uploads."""

    def __init__(self, app, tokens):
        self.app, self.tokens = app, tokens

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            return await self.app(scope, receive, send)

        async def secured_send(message):
            if message["type"] == "http.response.start":
                message["headers"] = list(message.get("headers", [])) + [
                    (b"cache-control", b"no-store"),
                    (b"x-content-type-options", b"nosniff"),
                    (b"referrer-policy", b"no-referrer"),
                    (
                        b"content-security-policy",
                        b"default-src 'self'; script-src 'self'; style-src 'self'; "
                        b"frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
                    ),
                ]
            await send(message)

        if not scope["path"].startswith("/api/"):
            return await self.app(scope, receive, secured_send)
        headers = dict(scope["headers"])
        authorization = headers.get(b"authorization", b"").decode("latin-1")
        scheme, _, token = authorization.partition(" ")
        actor = (
            next(
                (
                    a
                    for secret, a in self.tokens.items()
                    if hmac.compare_digest(token.encode(), secret.encode())
                ),
                None,
            )
            if scheme.lower() == "bearer"
            else None
        )
        if actor is None:
            return await JSONResponse(
                {"detail": "Valid bearer token required"}, 401, headers={"WWW-Authenticate": "Bearer"}
            )(scope, receive, secured_send)
        scope.setdefault("state", {})["actor"] = actor
        body = bytearray()
        while True:
            message = await receive()
            if message["type"] == "http.disconnect":
                return
            chunk = message.get("body", b"")
            if len(body) + len(chunk) > MAX_BYTES + 1024 * 1024:
                return await JSONResponse({"detail": "Request exceeds 11 MiB"}, 413)(
                    scope, receive, secured_send
                )
            body.extend(chunk)
            if not message.get("more_body", False):
                break
        iterator = iter([{"type": "http.request", "body": bytes(body), "more_body": False}])

        async def bounded_receive():
            return next(iterator, {"type": "http.request", "body": b"", "more_body": False})

        await self.app(scope, bounded_receive, secured_send)


def create_app(engine: Engine, tokens: dict) -> FastAPI:
    tokens = validate_tokens(tokens)
    app = FastAPI(
        title="Filewise", version="0.1.0", docs_url=None, redoc_url=None, openapi_url="/api/openapi.json"
    )
    app.add_middleware(Boundary, tokens=tokens)
    security = HTTPBearer()

    def actor(request: Request, credentials=Depends(security)):
        return request.state.actor

    @app.exception_handler(FilewiseError)
    async def filewise_error(request, exc):
        return JSONResponse({"detail": str(exc)}, status_code=exc.status)

    @app.exception_handler(ValidationError)
    async def validation_error(request, exc):
        return JSONResponse({"detail": json.loads(exc.json(include_input=False, include_url=False))}, 422)

    @app.get("/health")
    def health():
        return {"status": "ok", "version": "0.1.0"}

    @app.get("/")
    def console():
        return FileResponse(Path(__file__).with_name("static") / "index.html")

    @app.get("/console.js")
    def script():
        return FileResponse(Path(__file__).with_name("static") / "console.js")

    @app.get("/console.css")
    def style():
        return FileResponse(Path(__file__).with_name("static") / "console.css")

    @app.get("/api/me")
    def me(who=Depends(actor)):
        return who

    @app.get("/api/overview")
    def overview(who=Depends(actor)):
        return engine.overview(who)

    @app.post("/api/scopes", status_code=201)
    def scope(data: Scope, who=Depends(actor)):
        return engine.add_scope(data, who)

    @app.post("/api/scopes/{scope_id}/sources", status_code=201)
    def upload(scope_id: str, file: UploadFile, acl: str | None = None, who=Depends(actor)):
        require(who, "editor")
        return ingest(
            engine,
            scope_id,
            file.filename or "",
            file.file.read(MAX_BYTES + 1),
            who,
            SourcePolicy(acl=set(acl.split(","))).acl if acl is not None else None,
        )

    @app.get("/api/sources/{source_id}")
    def source(source_id: str, who=Depends(actor)):
        return engine.source(source_id, who)

    @app.put("/api/sources/{source_id}/policy")
    def policy(source_id: str, data: SourcePolicy, who=Depends(actor)):
        return engine.source_policy(source_id, data.acl, data.revoked, who)

    @app.post("/api/revisions", status_code=201)
    def revision(data: Revision, who=Depends(actor)):
        return engine.add_revision(data, who)

    @app.post("/api/revisions/{revision_id}/{decision}")
    def decide(revision_id: str, decision: str, who=Depends(actor)):
        return engine.decide_revision(revision_id, decision, who)

    @app.post("/api/resolve")
    def resolve(data: ResolveRequest, who=Depends(actor)):
        return engine.resolve(actor=who, **data.model_dump())

    @app.post("/api/impact")
    def impact(data: ImpactRequest, who=Depends(actor)):
        return engine.impact(actor=who, **data.model_dump())

    @app.post("/api/diff")
    def diff(data: DiffRequest, who=Depends(actor)):
        return engine.diff(actor=who, **data.model_dump())

    @app.post("/api/build", status_code=201)
    def build(data: BuildRequest, who=Depends(actor)):
        return engine.build(data, who)

    @app.get("/api/releases/{release_id}")
    def release(release_id: str, who=Depends(actor)):
        return engine.release(release_id, who)

    @app.get("/api/releases/{release_id}/verify")
    def verify(release_id: str, who=Depends(actor)):
        return engine.verify(release_id, who)

    @app.post("/api/releases/{release_id}/approve")
    def approve(release_id: str, who=Depends(actor)):
        return engine.approve(release_id, who)

    @app.post("/api/releases/{release_id}/activate")
    def activate(release_id: str, data: ActivateRequest, who=Depends(actor)):
        return engine.activate(release_id, data.expected_active, who)

    @app.post("/api/releases/{release_id}/rollback")
    def rollback(release_id: str, data: ActivateRequest, who=Depends(actor)):
        return engine.activate(release_id, data.expected_active, who, rollback=True)

    @app.post("/api/releases/{release_id}/revoke")
    def revoke(release_id: str, who=Depends(actor)):
        return engine.revoke(release_id, who)

    @app.post("/api/releases/{release_id}/context")
    def context(release_id: str, data: ContextRequest, who=Depends(actor)):
        return engine.context(release_id, data.object_ids, who)

    @app.get("/api/scopes/{scope_id}/audit")
    def audit(scope_id: str, who=Depends(actor)):
        return engine.audit(scope_id, who)

    return app
