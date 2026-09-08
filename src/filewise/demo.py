"""Synthetic fixture; no workspace discovery and no enterprise source material."""

from .engine import FilewiseError
from .ingest import ingest
from .models import Actor, BuildRequest, Check, Evidence, Revision, Scope

EDITOR = Actor(id="demo-editor", roles={"editor"})
REVIEWER = Actor(id="demo-reviewer", roles={"reviewer"})
PUBLISHER = Actor(id="demo-publisher", roles={"publisher"})
READER = Actor(id="demo-reader", roles={"reader"})
ROLES = {"reader", "editor", "reviewer", "publisher"}
VALID_TIME = "2024-06-01T00:00:00Z"


def run_demo(engine):
    if any(s["id"] == "demo" for s in engine.overview(EDITOR)["scopes"]):
        raise FilewiseError("Demo scope already exists; use a fresh database", 409)
    scope = Scope(
        id="demo",
        title="合成规程：检验压力变更",
        required_objects=["rule", "procedure"],
        checks=[
            Check(
                id="pressure-consistency",
                object_id="procedure",
                field="pressure_kpa",
                op="eq",
                reference_object="rule",
                reference_field="pressure_kpa",
            ),
            Check(id="positive-pressure", object_id="rule", field="pressure_kpa", op="gte", expected=1),
        ],
    )
    engine.add_scope(scope, EDITOR)
    source = ingest(
        engine,
        "demo",
        "synthetic-pressure.txt",
        "合成演示；非真实设备规程。\n初版：规则和操作程序使用 100 kPa。\n修订：规则和操作程序使用 120 kPa。\n".encode(),
        EDITOR,
        ROLES,
    )

    def propose(oid, number, pressure, valid_from, line):
        fragment = source["fragments"][line - 1]
        rev = Revision(
            id=f"{oid}-v{number}",
            scope_id="demo",
            object_id=oid,
            kind="rule" if oid == "rule" else "procedure",
            title=oid,
            fields={"pressure_kpa": pressure},
            depends_on=[] if oid == "rule" else ["rule"],
            evidence=[Evidence(source_id=source["id"], locator=fragment["locator"], quote=fragment["text"])],
            valid_from=valid_from,
        )
        engine.add_revision(rev, EDITOR)
        engine.decide_revision(rev.id, "approved", REVIEWER)

    def build():
        return engine.build(BuildRequest(scope_id="demo", valid_time=VALID_TIME), EDITOR)

    propose("rule", 1, 100, "2024-01-01T00:00:00Z", 2)
    propose("procedure", 1, 100, "2024-01-01T00:00:00Z", 2)
    baseline = build()
    engine.approve(baseline["id"], REVIEWER)
    engine.activate(baseline["id"], None, PUBLISHER)
    propose("rule", 2, 120, "2024-02-01T00:00:00Z", 3)
    blocked = build()
    assert blocked["bundle"]["verification"]["decision"] == "BLOCKED"
    propose("procedure", 2, 120, "2024-02-01T00:00:00Z", 3)
    repaired = build()
    engine.approve(repaired["id"], REVIEWER)
    engine.activate(repaired["id"], baseline["id"], PUBLISHER)
    pinned = engine.context(repaired["id"], ["procedure"], READER)
    engine.activate(baseline["id"], repaired["id"], PUBLISHER, rollback=True)
    assert engine.context(repaired["id"], ["procedure"], READER) == pinned
    return {
        "scope_id": "demo",
        "baseline": baseline["id"],
        "blocked": blocked["id"],
        "repaired": repaired["id"],
        "active_after_rollback": baseline["id"],
        "pinned_pressure_kpa": pinned["objects"]["procedure"]["fields"]["pressure_kpa"],
        "audit_chain_valid": engine.audit("demo", REVIEWER)["chain_valid"],
    }
