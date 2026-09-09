"""Authenticated local REST boundary. Tokens map to server-owned identities."""

import hmac
import json
import re
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import Depends, FastAPI, Request, UploadFile
from fastapi.responses import FileResponse, JSONResponse
from fastapi.security import HTTPBearer
from pydantic import Field, ValidationError

from .connections import Connections
from .engine import Engine, FilewiseError, require
from .ingest import MAX_BYTES, ingest
from .middleware import Middleware, WatchConfig
from .models import (
    ActivateRequest,
    Actor,
    BuildRequest,
    ContextRequest,
    DiffRequest,
    ImpactRequest,
    Model,
    ResolveRequest,
    Revision,
    Scope,
    SourcePolicy,
)
from .projects import CommitRequest, Projects, ProjectSpec, relative_path
from .workspace import (
    CompileQuery,
    DiffQuery,
    ImpactQuery,
    ReadQuery,
    ResolveQuery,
    SearchQuery,
    VerifyQuery,
    VersionQuery,
    Workspace,
    WriteQuery,
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
        if actor.id in identities and identities[actor.id] != actor:
            raise ValueError("Each actor ID must have one consistent role set")
        result[token] = actor
        identities[actor.id] = actor
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
        # Workspace authoring is an explicit project grant; legacy tokens stay released-only.
        if actor.audience == "agent" and not (
            (bool(actor.workspace_projects) and scope["path"].startswith("/api/workspaces/"))
            or (scope["method"] == "GET" and scope["path"] in ("/api/me", "/api/projects"))
            or (scope["method"] == "POST" and re.fullmatch(r"/api/projects/[^/]+/sessions", scope["path"]))
            or (
                scope["method"] == "GET"
                and re.fullmatch(r"/api/sessions/[^/]+(?:/read|/search)?", scope["path"])
            )
        ):
            return await JSONResponse(
                {"detail": "Agent credentials may only use the released file gateway"}, 403
            )(scope, receive, secured_send)
        limit = (
            70 * 1024 * 1024
            if scope["path"].startswith("/api/workspaces/") and scope["path"].endswith("/write")
            else MAX_BYTES + 1024 * 1024
        )
        body = bytearray()
        while True:
            message = await receive()
            if message["type"] == "http.disconnect":
                return
            chunk = message.get("body", b"")
            if len(body) + len(chunk) > limit:
                return await JSONResponse({"detail": "Request exceeds size limit"}, 413)(
                    scope, receive, secured_send
                )
            body.extend(chunk)
            if not message.get("more_body", False):
                break
        iterator = iter([{"type": "http.request", "body": bytes(body), "more_body": False}])

        async def bounded_receive():
            return next(iterator, {"type": "http.request", "body": b"", "more_body": False})

        await self.app(scope, bounded_receive, secured_send)


class LocalProject(Model):
    root: str = Field(min_length=1, max_length=2048)
    name: str = Field(default="", max_length=120)
    includes: list[str] = Field(default_factory=lambda: ["**"], min_length=1, max_length=50)


def create_app(engine: Engine, tokens: dict, *, showcase=None, local_setup=False) -> FastAPI:
    tokens = validate_tokens(tokens)
    projects = Projects(engine)
    middleware = Middleware(projects)
    connections = Connections(middleware)
    workspace = Workspace(middleware)

    @asynccontextmanager
    async def lifespan(app):
        middleware.start()
        try:
            yield
        finally:
            middleware.close()

    app = FastAPI(
        lifespan=lifespan,
        title="Filewise",
        version="0.1.0",
        docs_url=None,
        redoc_url=None,
        openapi_url="/api/openapi.json",
    )
    app.add_middleware(Boundary, tokens=tokens)
    security = HTTPBearer()
    app.state.middleware = middleware

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
        return FileResponse(Path(__file__).with_name("static") / "workspace.html")

    @app.get("/admin")
    def admin_console():
        return FileResponse(Path(__file__).with_name("static") / "index.html")

    @app.get("/workspace.js")
    def workspace_script():
        return FileResponse(Path(__file__).with_name("static") / "workspace.js")

    @app.get("/workspace.css")
    def workspace_style():
        return FileResponse(Path(__file__).with_name("static") / "workspace.css")

    @app.get("/demo")
    def demo_info():
        if showcase is None:
            raise FilewiseError("Demo is not enabled on this server", 404)
        return showcase.bootstrap()

    @app.post("/api/demo/{step}")
    def demo_step(step: str, who=Depends(actor)):
        if showcase is None:
            raise FilewiseError("Demo is not enabled on this server", 404)
        require(who, "editor")
        return showcase.step(step)

    def local_only(who):
        require(who, "editor")
        if not local_setup:
            raise FilewiseError("请使用 filewise start 启动本机连接向导。", 403)

    @app.get("/api/setup")
    def setup_info(who=Depends(actor)):
        return {"local_setup": local_setup, "native_connections": __import__("sys").platform == "darwin"}

    @app.post("/api/setup/project", status_code=201)
    def setup_project(data: LocalProject, who=Depends(actor)):
        import hashlib

        local_only(who)
        try:
            root = Path(data.root).expanduser().resolve(strict=True)
        except OSError as exc:
            raise FilewiseError("找不到这个文件夹，请检查完整路径和访问权限。") from exc
        project_id = "folder-" + hashlib.sha256(str(root).encode()).hexdigest()[:16]
        with engine.connect() as db:
            existing = db.execute("SELECT id FROM projects WHERE root=?", (str(root),)).fetchone()
        if existing:
            return projects.detail(existing[0], who)
        project = projects.create(
            {"id": project_id, "name": data.name or root.name, "includes": data.includes}, who, root
        )
        middleware.configure(project_id, WatchConfig(), who)
        return projects.detail(project["id"], who)

    @app.post("/api/workspaces/{project_id}/sync")
    def workspace_sync(project_id: str, who=Depends(actor)):
        return workspace.sync(project_id, who)

    @app.get("/api/workspaces/{project_id}/versions")
    def workspace_versions(project_id: str, who=Depends(actor)):
        return workspace.versions(project_id, who)

    @app.post("/api/workspaces/{project_id}/ls")
    def workspace_ls(project_id: str, data: VersionQuery, who=Depends(actor)):
        return workspace.ls(project_id, data, who)

    @app.post("/api/workspaces/{project_id}/read")
    def workspace_read(project_id: str, data: ReadQuery, who=Depends(actor)):
        return workspace.read(project_id, data, who)

    @app.post("/api/workspaces/{project_id}/search")
    def workspace_search(project_id: str, data: SearchQuery, who=Depends(actor)):
        return workspace.search(project_id, data, who)

    @app.post("/api/workspaces/{project_id}/write")
    def workspace_write(project_id: str, data: WriteQuery, who=Depends(actor)):
        return workspace.write(project_id, data, who)

    @app.post("/api/workspaces/{project_id}/resolve")
    def workspace_resolve(project_id: str, data: ResolveQuery, who=Depends(actor)):
        return workspace.resolve(project_id, data, who)

    @app.post("/api/workspaces/{project_id}/diff")
    def workspace_diff(project_id: str, data: DiffQuery, who=Depends(actor)):
        return workspace.diff(project_id, data, who)

    @app.post("/api/workspaces/{project_id}/impact")
    def workspace_impact(project_id: str, data: ImpactQuery, who=Depends(actor)):
        return workspace.impact(project_id, data, who)

    @app.post("/api/workspaces/{project_id}/compile")
    def workspace_compile(project_id: str, data: CompileQuery, who=Depends(actor)):
        return workspace.compile(project_id, data, who)

    @app.post("/api/workspaces/{project_id}/verify")
    def workspace_verify(project_id: str, data: VerifyQuery, who=Depends(actor)):
        return workspace.verify(project_id, data, who)

    @app.post("/api/workspaces/{project_id}/recover")
    def workspace_recover(project_id: str, data: VersionQuery, who=Depends(actor)):
        return workspace.recover(project_id, data.version, who)

    @app.post("/api/workspaces/{project_id}/trace")
    def workspace_trace(project_id: str, data: VersionQuery, who=Depends(actor)):
        return workspace.trace(project_id, data.version, who)

    @app.get("/api/workspaces/{project_id}/evidence/{source_id}")
    def workspace_evidence(project_id: str, source_id: str, locator: str | None = None, who=Depends(actor)):
        return workspace.evidence(project_id, source_id, who, locator)

    @app.get("/api/projects/{project_id}/connections")
    def connection_status(project_id: str, who=Depends(actor)):
        return connections.status(project_id, who)

    @app.get("/api/projects/{project_id}/connections/{agent_name}/preview")
    def connection_preview(project_id: str, agent_name: str, who=Depends(actor)):
        local_only(who)
        return connections.preview(project_id, agent_name, who)

    @app.post("/api/projects/{project_id}/connections/{agent_name}")
    def connect_agent(project_id: str, agent_name: str, who=Depends(actor)):
        local_only(who)
        return connections.install(project_id, agent_name, who)

    @app.delete("/api/projects/{project_id}/connections/{agent_name}")
    def disconnect_agent(project_id: str, agent_name: str, who=Depends(actor)):
        local_only(who)
        return connections.disconnect(project_id, agent_name, who)

    @app.post("/api/projects/{project_id}/connections/{agent_name}/check")
    def check_agent(project_id: str, agent_name: str, who=Depends(actor)):
        local_only(who)
        return connections.self_test(project_id, agent_name, who)

    @app.get("/api/projects")
    def project_list(who=Depends(actor)):
        items = projects.list(who)
        if who.audience == "agent":
            return [{k: p[k] for k in ("id", "name", "active_release")} for p in items]
        return items

    @app.post("/api/projects", status_code=201)
    def create_project(data: ProjectSpec, who=Depends(actor)):
        return projects.create(data, who)

    @app.get("/api/projects/{project_id}")
    def project_detail(project_id: str, who=Depends(actor)):
        return projects.detail(project_id, who)

    @app.get("/api/projects/{project_id}/watch")
    def watch_config(project_id: str, who=Depends(actor)):
        return middleware.config(project_id, who)

    @app.post("/api/projects/{project_id}/commits", status_code=201)
    def commit_project(project_id: str, data: CommitRequest, who=Depends(actor)):
        return projects.commit(project_id, data, who)

    @app.put("/api/projects/{project_id}/watch")
    def configure_watch(project_id: str, data: WatchConfig, who=Depends(actor)):
        return middleware.configure(project_id, data, who)

    @app.post("/api/projects/{project_id}/sync", status_code=201)
    def sync_project(project_id: str, who=Depends(actor)):
        return workspace.sync(project_id, who)

    @app.post("/api/projects/{project_id}/upload", status_code=201)
    def upload_project(project_id: str, files: list[UploadFile], who=Depends(actor)):
        require(who, "editor")
        data = {}
        for file in files:
            name = relative_path(file.filename or "")
            if name in data:
                raise FilewiseError("Duplicate uploaded path")
            data[name] = file.file.read(MAX_BYTES + 1)
        with middleware.lock(project_id):
            middleware.require_recovered(project_id)
            return projects.snapshot(project_id, who, data)

    @app.get("/api/projects/{project_id}/snapshots/{release_id}")
    def project_snapshot(project_id: str, release_id: str, who=Depends(actor)):
        snapshot = projects.inspect(project_id, release_id, who)
        return {**snapshot, "writeback": middleware.proposal(release_id)}

    @app.get("/api/projects/{project_id}/snapshots/{release_id}/preview")
    def preview_file(project_id: str, release_id: str, path: str, who=Depends(actor)):
        return projects.read(project_id, release_id, path, who, preview=True)

    @app.get("/api/projects/{project_id}/snapshots/{release_id}/compare")
    def compare_snapshot(
        project_id: str, release_id: str, base_release: str | None = None, who=Depends(actor)
    ):
        return projects.compare(project_id, release_id, who, base_release)

    @app.post("/api/projects/{project_id}/snapshots/{release_id}/restore", status_code=201)
    def restore_snapshot(project_id: str, release_id: str, who=Depends(actor)):
        return middleware.restore(project_id, release_id, who)

    @app.post("/api/projects/{project_id}/snapshots/{release_id}/approve")
    def approve_snapshot(project_id: str, release_id: str, who=Depends(actor)):
        return projects.approve(project_id, release_id, who)

    @app.post("/api/projects/{project_id}/snapshots/{release_id}/activate")
    def activate_snapshot(project_id: str, release_id: str, data: ActivateRequest, who=Depends(actor)):
        return projects.activate(project_id, release_id, data.expected_active, who)

    @app.post("/api/projects/{project_id}/snapshots/{release_id}/apply")
    def apply_snapshot(project_id: str, release_id: str, data: ActivateRequest, who=Depends(actor)):
        return middleware.apply(project_id, release_id, data.expected_active, who)

    @app.post("/api/projects/{project_id}/snapshots/{release_id}/recover")
    def recover_snapshot(project_id: str, release_id: str, who=Depends(actor)):
        return middleware.recover(project_id, release_id, who)

    @app.post("/api/projects/{project_id}/sessions", status_code=201)
    def open_session(project_id: str, release_id: str | None = None, who=Depends(actor)):
        return projects.session(project_id, who, release_id)

    @app.get("/api/sessions/{session_id}")
    def session_info(session_id: str, who=Depends(actor)):
        return projects.session_info(session_id, who)

    @app.get("/api/sessions/{session_id}/read")
    def read_file(session_id: str, path: str, who=Depends(actor)):
        session = projects.session_info(session_id, who)
        return projects.read(session["project_id"], session["release_id"], path, who, session_id=session_id)

    @app.get("/api/sessions/{session_id}/search")
    def search_files(session_id: str, q: str, who=Depends(actor)):
        return projects.search(session_id, q, who)

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
