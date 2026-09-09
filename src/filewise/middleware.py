"""After-save observation and guarded working-copy writeback for local files."""

import hashlib
import json
import os
import secrets
import stat
import subprocess
import tempfile
import threading
import time
from contextlib import contextmanager
from pathlib import Path
from typing import Literal

from pydantic import Field

from .agent import sandbox_command
from .engine import FilewiseError, canonical, require
from .models import Actor, Model, now
from .projects import DEFAULT_EXCLUDES, ProjectSpec, relative_path, scan

MONITOR = Actor(id="filewise-monitor", roles={"editor"})
GUARD = Actor(id="filewise-guard", roles={"editor"})


def hashes(files):
    return {p: hashlib.sha256(b).hexdigest() for p, b in files.items()}


class WatchConfig(Model):
    enabled: bool = True
    mode: Literal["observe", "guard", "both"] = "both"
    settle_seconds: float = Field(default=1, ge=0.2, le=10)


class Middleware:
    def __init__(self, projects):
        self.projects, self.engine = projects, projects.engine
        self.pending = {}
        self.stop = threading.Event()
        self.thread = None
        with self.engine.connect(True) as db:
            db.executescript("""
                CREATE TABLE IF NOT EXISTS watches(project_id TEXT PRIMARY KEY REFERENCES projects(id),
                    config TEXT NOT NULL, baseline TEXT, last_event TEXT, checked_at TEXT, error TEXT);
                CREATE TABLE IF NOT EXISTS writebacks(release_id TEXT PRIMARY KEY,
                    project_id TEXT NOT NULL, before_release TEXT NOT NULL,
                    status TEXT NOT NULL, error TEXT, created_at TEXT NOT NULL);
            """)

    @contextmanager
    def lock(self, project_id):
        try:
            import fcntl
        except ImportError as exc:
            raise FilewiseError("File middleware currently requires macOS or Linux") from exc

        path = self.engine.path.parent / (
            "filewise-lock-" + hashlib.sha256(project_id.encode()).hexdigest()[:16]
        )
        fd = os.open(path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        try:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise FilewiseError("Project is busy; retry shortly", 409) from exc
            yield
        finally:
            os.close(fd)

    def config(self, project_id, actor):
        with self.engine.connect() as db:
            self.projects._project(db, project_id, actor)
            row = db.execute("SELECT * FROM watches WHERE project_id=?", (project_id,)).fetchone()
        result = (
            dict(row)
            if row
            else {
                "config": canonical(WatchConfig(enabled=False)),
                "baseline": None,
                "last_event": None,
                "checked_at": None,
                "error": None,
            }
        )
        result["config"] = json.loads(result["config"])
        result["last_event"] = json.loads(result["last_event"]) if result["last_event"] else None
        result["worker_running"] = bool(self.thread and self.thread.is_alive())
        return result

    def configure(self, project_id, config, actor):
        require(actor, "editor")
        config = WatchConfig.model_validate(config)
        with self.lock(project_id):
            with self.engine.connect() as db:
                project = self.projects._project(db, project_id, actor)
                existing = db.execute(
                    "SELECT baseline FROM watches WHERE project_id=?", (project_id,)
                ).fetchone()
            if not project["root"]:
                raise FilewiseError("Automatic observation requires a registered local directory")
            baseline = existing[0] if existing else self.projects.snapshot(project_id, MONITOR)["release_id"]
            with self.engine.connect(True) as db:
                db.execute(
                    "INSERT INTO watches VALUES(?,?,?,NULL,NULL,NULL) ON CONFLICT(project_id) DO UPDATE SET config=excluded.config,error=NULL",
                    (project_id, canonical(config), baseline),
                )
                self.engine._audit(db, project["scope_id"], actor, "watch.configured", config.model_dump())
        self.pending.pop(project_id, None)
        return self.config(project_id, actor)

    def require_recovered(self, project_id):
        with self.engine.connect() as db:
            if db.execute(
                "SELECT 1 FROM writebacks WHERE project_id=? AND status IN ('applying','recovery_required')",
                (project_id,),
            ).fetchone():
                raise FilewiseError("Writeback recovery is required before further changes", 409)

    def tick(self, project_id, clock=None):
        clock = time.monotonic() if clock is None else clock
        with self.lock(project_id):
            with self.engine.connect() as db:
                project = self.projects._project(db, project_id, MONITOR)
                row = db.execute("SELECT * FROM watches WHERE project_id=?", (project_id,)).fetchone()
                if not row:
                    return None
                config = WatchConfig.model_validate_json(row["config"])
                if not config.enabled or config.mode == "guard":
                    return None
                self.require_recovered(project_id)
                _, _, _, _, old = self.projects._snapshot(db, project_id, row["baseline"], MONITOR)
            spec = ProjectSpec.model_validate_json(project["spec"])
            files, _ = scan(project["root"], DEFAULT_EXCLUDES + spec.excludes, spec.includes)
            current = hashes(files)
            before = {p: f["sha256"] for p, f in old.items()}
            changed = sorted(p for p in current.keys() | before.keys() if current.get(p) != before.get(p))
            event = None
            if changed:
                signature = canonical(current)
                pending = self.pending.get(project_id)
                if not pending or pending[0] != signature:
                    self.pending[project_id] = (signature, clock)
                elif clock - pending[1] >= config.settle_seconds:
                    snapshot = self.projects.snapshot(
                        project_id, MONITOR, files, base_release=row["baseline"], captured=True
                    )
                    event = {
                        "kind": "after_save",
                        "release_id": snapshot["release_id"],
                        "paths": changed,
                        "decision": snapshot["verification"]["decision"],
                        "recorded_at": now(),
                        "writer": "unknown",
                        "original_files_changed": True,
                    }
                    with self.engine.connect(True) as db:
                        db.execute(
                            "UPDATE watches SET baseline=?,last_event=? WHERE project_id=?",
                            (snapshot["release_id"], canonical(event), project_id),
                        )
                        self.engine._audit(db, project["scope_id"], MONITOR, "watch.changed", event)
                    self.pending.pop(project_id, None)
            else:
                self.pending.pop(project_id, None)
            with self.engine.connect(True) as db:
                db.execute(
                    "UPDATE watches SET checked_at=?,error=NULL WHERE project_id=?", (now(), project_id)
                )
            return event

    def start(self):
        if self.thread and self.thread.is_alive():
            return
        self.stop.clear()

        def loop():
            # ponytail: bounded full scans once per second; use filesystem events if scale exceeds 1,000 files.
            while not self.stop.is_set():
                with self.engine.connect() as db:
                    ids = [r[0] for r in db.execute("SELECT project_id FROM watches")]
                for project_id in ids:
                    try:
                        self.tick(project_id)
                    except Exception as exc:
                        with self.engine.connect(True) as db:
                            db.execute(
                                "UPDATE watches SET checked_at=?,error=? WHERE project_id=?",
                                (now(), str(exc)[:1000], project_id),
                            )
                self.stop.wait(1)

        self.thread = threading.Thread(target=loop, name="filewise-observer", daemon=True)
        self.thread.start()

    def close(self):
        self.stop.set()
        if self.thread:
            self.thread.join(timeout=5)

    def proposal(self, release_id):
        with self.engine.connect() as db:
            row = db.execute("SELECT * FROM writebacks WHERE release_id=?", (release_id,)).fetchone()
        return dict(row) if row else None

    def guard(self, project_id, command, actor):
        require(actor, "editor")
        with self.engine.connect() as db:
            project = self.projects._project(db, project_id, actor)
        if not project["root"]:
            raise FilewiseError("Guarded execution requires a registered local directory")
        config = WatchConfig.model_validate(self.config(project_id, actor)["config"])
        if not config.enabled or config.mode == "observe":
            raise FilewiseError("Enable guarded modification in the project watch settings first", 409)
        # Reject unsupported isolation before launching anything; the model receives only a working copy.
        invocation = sandbox_command([project["root"], self.engine.path.parent], command)
        spec = ProjectSpec.model_validate_json(project["spec"])
        with self.lock(project_id):
            self.require_recovered(project_id)
            originals, _ = scan(project["root"], DEFAULT_EXCLUDES + spec.excludes, spec.includes)
            baseline = self.projects.snapshot(project_id, GUARD, originals, captured=True)
        with tempfile.TemporaryDirectory(prefix="filewise-work-") as directory:
            work = Path(directory).resolve()
            for path, body in originals.items():
                target = work / path
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(body)
                target.chmod(
                    0o600 | (Path(project["root"], path).stat(follow_symlinks=False).st_mode & 0o111)
                )
            env = {k: v for k, v in os.environ.items() if not k.startswith("FILEWISE_")}
            env["FILEWISE_WORKTREE"] = str(work)
            # The installed Filewise runtime must be outside the protected original project.
            completed = subprocess.run(invocation, cwd=work, env=env)
            files, _ = scan(work, DEFAULT_EXCLUDES + spec.excludes, spec.includes)
            if hashes(files) == hashes(originals):
                return {"exit_code": completed.returncode, "status": "unchanged", "release_id": None}
            with self.lock(project_id):
                snapshot = self.capture(project_id, baseline["release_id"], files, GUARD, writer=actor.id)
            return {
                "exit_code": completed.returncode,
                "status": "pending_review",
                "release_id": snapshot["release_id"],
                "verification": snapshot["verification"],
                "original_files_changed": False,
            }

    def capture(self, project_id, baseline, files, actor, *, writer=None):
        """Caller holds the project lock; snapshots and approval use the existing file kernel."""
        snapshot = self.projects.snapshot(project_id, actor, files, base_release=baseline, captured=True)
        with self.engine.connect(True) as db:
            project = self.projects._project(db, project_id, actor)
            db.execute(
                "INSERT INTO writebacks VALUES(?,?,?,?,NULL,?)",
                (snapshot["release_id"], project_id, baseline, "pending", now()),
            )
            event = {
                "kind": "before_write",
                "release_id": snapshot["release_id"],
                "paths": [c["path"] for c in snapshot["report"]["changes"]],
                "decision": snapshot["verification"]["decision"],
                "recorded_at": now(),
                "writer": writer or actor.id,
                "original_files_changed": False,
            }
            db.execute("UPDATE watches SET last_event=? WHERE project_id=?", (canonical(event), project_id))
            self.engine._audit(db, project["scope_id"], actor, "guard.proposed", event)
        return snapshot

    def _versions(self, project_id, release_id, actor):
        with self.engine.connect() as db:
            project, _, release, bundle, after = self.projects._snapshot(db, project_id, release_id, actor)
            proposal = db.execute(
                "SELECT * FROM writebacks WHERE release_id=? AND project_id=?", (release_id, project_id)
            ).fetchone()
            if not proposal:
                raise FilewiseError("Guarded writeback not found", 404)
            if proposal["before_release"] != bundle["base_release"]:
                raise FilewiseError("Writeback baseline integrity failed", 409)
            _, _, _, _, before = self.projects._snapshot(db, project_id, proposal["before_release"], actor)
            bodies = {}
            for file in [*before.values(), *after.values()]:
                row = db.execute("SELECT body FROM sources WHERE id=?", (file["source_id"],)).fetchone()
                if not row or hashlib.sha256(row[0]).hexdigest() != file["sha256"]:
                    raise FilewiseError("Writeback source integrity failed", 409)
                bodies[file["sha256"]] = bytes(row[0])
            return project, release, bundle, dict(proposal), before, after, bodies

    def _status(self, release_id, status, error=None):
        with self.engine.connect(True) as db:
            db.execute(
                "UPDATE writebacks SET status=?,error=? WHERE release_id=?", (status, error, release_id)
            )

    def _applied(self, project, release_id, actor, action):
        with self.engine.connect(True) as db:
            db.execute("UPDATE writebacks SET status='applied',error=NULL WHERE release_id=?", (release_id,))
            db.execute("UPDATE watches SET baseline=? WHERE project_id=?", (release_id, project["id"]))
            self.engine._audit(db, project["scope_id"], actor, action, {"release_id": release_id})

    def apply(self, project_id, release_id, expected_active, actor):
        require(actor, "publisher")
        with self.lock(project_id):
            project, release, bundle, proposal, before, after, bodies = self._versions(
                project_id, release_id, actor
            )
            self.require_recovered(project_id)
            if proposal["status"] == "applied":
                return {"status": "applied", "release_id": release_id}
            if proposal["status"] != "pending":
                raise FilewiseError("Recover the interrupted writeback before retrying", 409)
            with self.engine.connect() as db:
                if not release["approver"] or self.engine._runtime_issues(db, release, bundle, actor):
                    raise FilewiseError("Writeback requires passing checks and independent approval", 409)
                active = db.execute(
                    "SELECT release_id FROM active WHERE scope_id=?", (project["scope_id"],)
                ).fetchone()
                if (active[0] if active else None) != expected_active:
                    raise FilewiseError("Active release changed; refresh before writeback", 409)
            self.projects._live(project, before)
            # Durable intent precedes filesystem writes. Original bytes already exist in immutable sources.
            self._status(release_id, "applying")
            try:
                for path in sorted(before.keys() | after.keys()):
                    old, new = before.get(path, {}).get("sha256"), after.get(path, {}).get("sha256")
                    if old != new:
                        replace_bytes(project["root"], path, bodies.get(new), old)
                self.projects._live(project, after)
                result = self.engine.activate(release_id, expected_active, actor)
                self._applied(project, release_id, actor, "guard.applied")
                return {**result, "status": "applied"}
            except Exception as exc:
                with self.engine.connect() as db:
                    activated = db.execute(
                        "SELECT 1 FROM activations WHERE release_id=?", (release_id,)
                    ).fetchone()
                if activated:
                    self._status(release_id, "recovery_required", str(exc)[:1000])
                    raise FilewiseError(
                        "Activation was recorded; recover to reconcile writeback status", 409
                    ) from exc
                try:
                    self._restore(project, before, after, bodies)
                    self._status(release_id, "pending", str(exc)[:1000])
                except Exception as restore_error:
                    self._status(release_id, "recovery_required", str(restore_error)[:1000])
                    raise FilewiseError(
                        "RECOVERY_REQUIRED: originals were not overwritten further; inspect writeback recovery",
                        409,
                    ) from exc
                raise

    def _restore(self, project, before, after, bodies):
        spec = ProjectSpec.model_validate_json(project["spec"])
        current = hashes(scan(project["root"], DEFAULT_EXCLUDES + spec.excludes, spec.includes)[0])
        changed = [
            p
            for p in before.keys() | after.keys()
            if before.get(p, {}).get("sha256") != after.get(p, {}).get("sha256")
        ]
        for path in changed:
            if current.get(path) not in (
                before.get(path, {}).get("sha256"),
                after.get(path, {}).get("sha256"),
            ):
                raise FilewiseError(
                    "Recovery conflict at " + path + "; keep the external edit and resolve manually", 409
                )
        for path in sorted(changed):
            old = before.get(path, {}).get("sha256")
            if current.get(path) != old:
                replace_bytes(project["root"], path, bodies.get(old), current.get(path))

    def recover(self, project_id, release_id, actor):
        require(actor, "publisher")
        with self.lock(project_id):
            project, _, _, proposal, before, after, bodies = self._versions(project_id, release_id, actor)
            if proposal["status"] not in ("applying", "recovery_required"):
                raise FilewiseError("This writeback does not need recovery", 409)
            with self.engine.connect() as db:
                active = db.execute(
                    "SELECT release_id FROM active WHERE scope_id=?", (project["scope_id"],)
                ).fetchone()
            if active and active[0] == release_id:
                self.projects._live(project, after)
                self._applied(project, release_id, actor, "guard.recovered_after_activation")
                return {"status": "applied"}
            self._restore(project, before, after, bodies)
            self._status(release_id, "pending")
            with self.engine.connect(True) as db:
                self.engine._audit(
                    db, project["scope_id"], actor, "guard.recovered_originals", {"release_id": release_id}
                )
            return {"status": "recovered", "release_id": release_id}


def replace_bytes(root, path, body, expected_hash):
    """One atomic replacement, no symlink traversal, with a last-moment content check."""
    root = Path(root)
    relative_path(path)
    fd = os.open(root.anchor, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    temporary = None
    try:
        for index, component in enumerate((*root.parts[1:], *Path(path).parts[:-1])):
            try:
                child = os.open(component, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            except FileNotFoundError:
                if body is None or index < len(root.parts) - 1:
                    raise
                os.mkdir(component, mode=0o700, dir_fd=fd)
                child = os.open(component, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = child
        name = Path(path).name
        mode = 0o600
        try:
            child = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
            with os.fdopen(child, "rb") as handle:
                info = os.fstat(handle.fileno())
                if not stat.S_ISREG(info.st_mode):
                    raise FilewiseError("Writeback target is not a regular file", 409)
                mode = stat.S_IMODE(info.st_mode)
                actual = hashlib.sha256(handle.read(10 * 1024 * 1024 + 1)).hexdigest()
        except FileNotFoundError:
            actual = None
        if actual != expected_hash:
            raise FilewiseError("Original changed before writeback: " + path, 409)
        if body is None:
            if actual is not None:
                os.unlink(name, dir_fd=fd)
        else:
            temporary = ".filewise-write-" + secrets.token_hex(12)
            child = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode, dir_fd=fd)
            with os.fdopen(child, "wb") as handle:
                handle.write(body)
                handle.flush()
                os.fsync(handle.fileno())
                os.fchmod(handle.fileno(), mode)
            os.replace(temporary, name, src_dir_fd=fd, dst_dir_fd=fd)
            temporary = None
        os.fsync(fd)
    finally:
        if temporary is not None:
            os.unlink(temporary, dir_fd=fd)
        os.close(fd)
