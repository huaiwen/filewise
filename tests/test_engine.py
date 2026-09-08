import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from unittest.mock import patch

from filewise import Actor, BuildRequest, Engine, FilewiseError, Revision, Scope
from filewise.demo import EDITOR, PUBLISHER, READER, REVIEWER, ROLES, run_demo
from filewise.engine import canonical, diff, impact
from filewise.ingest import ingest
from filewise.models import Check, Evidence, now, timestamp


class EngineTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.engine = Engine(Path(self.temp.name) / "state.db")
        self.scope = Scope(
            id="s",
            title="Synthetic",
            required_objects=["rule"],
            checks=[
                Check(id="positive", object_id="rule", field="value", op="gte", expected=1, critical=False)
            ],
        )
        self.engine.add_scope(self.scope, EDITOR)
        self.source = ingest(
            self.engine, "s", "synthetic.txt", b"Evidence for synthetic rule.\n", EDITOR, ROLES
        )

    def revision(self, rid="r1", **changes):
        values = dict(
            id=rid,
            scope_id="s",
            object_id="rule",
            kind="rule",
            title="Synthetic rule",
            fields={"value": 1},
            valid_from="2020-01-01T00:00:00Z",
            evidence=[Evidence(source_id=self.source["id"], locator="line:1", quote="synthetic rule")],
        )
        values.update(changes)
        rev = Revision(**values)
        self.engine.add_revision(rev, EDITOR)
        return rev

    def approved(self, rid="r1", **changes):
        rev = self.revision(rid, **changes)
        self.engine.decide_revision(rid, "approved", REVIEWER)
        return rev

    def build(self, **changes):
        return self.engine.build(BuildRequest(scope_id="s", valid_time=now(), **changes), EDITOR)

    def published(self):
        self.approved()
        release = self.build()
        self.engine.approve(release["id"], REVIEWER)
        self.engine.activate(release["id"], None, PUBLISHER)
        return release

    def test_synthetic_lifecycle_and_pinning(self):
        result = run_demo(self.engine)
        self.assertTrue(result["audit_chain_valid"])
        self.assertEqual(result["pinned_pressure_kpa"], 120)
        self.assertEqual(result["baseline"], result["active_after_rollback"])
        blocked = self.engine.release(result["blocked"], READER)
        self.assertEqual(blocked["bundle"]["verification"]["decision"], "BLOCKED")
        self.assertEqual(blocked["bundle"]["impact"]["paths"]["procedure"], ["rule", "procedure"])
        with self.assertRaises(FilewiseError):
            self.engine.approve(result["blocked"], REVIEWER)

    def test_timestamp_ordering_and_timezone(self):
        start = timestamp("2024-01-01T00:00:00Z")
        self.assertLess(start, timestamp("2024-01-01T00:00:00.1Z"))
        self.assertEqual(start, timestamp("2024-01-01T08:00:00+08:00"))
        with self.assertRaises(ValueError):
            timestamp("2024-01-01")

    def test_review_is_required_and_transaction_history_is_preserved(self):
        with patch("filewise.engine.now", return_value=timestamp("2021-01-01T00:00:00Z")):
            self.revision()
        with patch("filewise.engine.now", return_value=timestamp("2022-01-01T00:00:00Z")):
            self.engine.decide_revision("r1", "approved", REVIEWER)
        old = self.engine.resolve("s", READER, now(), "2021-06-01T00:00:00Z")
        self.assertFalse(old["objects"])
        self.assertEqual(old["issues"][0]["reason"], "UNKNOWN")
        current = self.engine.resolve("s", READER, now(), "2022-01-01T00:00:00Z")
        self.assertEqual(current["objects"]["rule"]["id"], "r1")
        with self.assertRaises(FilewiseError):
            self.engine.resolve("s", READER, now(), "2999-01-01T00:00:00Z")

    def test_conflicts_and_revocation_never_silently_downgrade(self):
        self.approved()
        self.approved("r2")
        self.assertEqual(self.build()["bundle"]["state"]["issues"][0]["reason"], "CONFLICTED")
        self.approved("r3", authority=200)
        self.engine.decide_revision("r3", "revoked", REVIEWER)
        self.assertEqual(self.build()["bundle"]["state"]["issues"][0]["reason"], "REVOKED")

    def test_validity_end_is_exclusive(self):
        self.approved(valid_until="2024-01-01T00:00:00Z")
        state = self.engine.resolve("s", READER, "2024-01-01T00:00:00Z")
        self.assertFalse(state["objects"])

    def test_scope_and_revision_are_immutable(self):
        rev = self.revision()
        self.engine.add_revision(rev, EDITOR)
        with self.assertRaises(FilewiseError):
            self.revision(fields={"value": 2})
        with self.assertRaises(FilewiseError):
            self.engine.add_scope(self.scope.model_copy(update={"title": "changed"}), EDITOR)

    def test_typed_diff_and_removal_impact(self):
        rev = self.revision().model_dump(mode="json")
        old = {"rule": rev}
        new = {"rule": {**rev, "fields": {"value": True}}}
        self.assertEqual(diff(old, new)[0]["kind"], "modified")
        new["rule"]["fields"]["value"] = 1.0
        self.assertEqual(diff(old, new)[0]["kind"], "modified")
        downstream = {"a": {"depends_on": ["b"]}, "b": {"depends_on": ["a"]}}
        self.assertEqual(impact(downstream, ["a"])["affected"], ["a", "b"])
        self.assertEqual(impact(downstream, ["b"], "reverse")["affected"], ["a", "b"])
        removed = impact({"a": {"depends_on": []}}, ["b"], before=downstream)
        self.assertEqual(removed["affected"], ["a", "b"])
        self.assertTrue(removed["frontier"])

    def test_unchanged_bad_baseline_cannot_skip_regression(self):
        self.approved(fields={"value": -1})
        blocked = self.build()
        second = self.build(base_release=blocked["id"])
        self.assertFalse(second["bundle"]["changes"])
        self.assertEqual(second["bundle"]["verification"]["decision"], "BLOCKED")
        self.assertFalse(second["bundle"]["verification"]["tests"][0]["passed"])

    def test_missing_dependency_blocks(self):
        self.approved(depends_on=["missing"])
        bundle = self.build()["bundle"]
        self.assertEqual(bundle["verification"]["decision"], "BLOCKED")
        self.assertIn("missing", [f["object_id"] for f in bundle["impact"]["frontier"]])

    def test_no_oracle_needs_review(self):
        scope = Scope(id="empty-oracle", title="No oracle", required_objects=["rule"])
        self.engine.add_scope(scope, EDITOR)
        source = ingest(self.engine, scope.id, "evidence.txt", b"valid", EDITOR, ROLES)
        self.approved(
            "no-oracle",
            scope_id=scope.id,
            evidence=[Evidence(source_id=source["id"], locator="line:1", quote="valid")],
        )
        release = self.engine.build(BuildRequest(scope_id=scope.id, valid_time=now()), EDITOR)
        self.assertEqual(release["bundle"]["verification"]["decision"], "NEEDS_REVIEW")
        with self.assertRaises(FilewiseError):
            self.engine.approve(release["id"], REVIEWER)

    def test_source_quote_scope_and_integrity(self):
        with self.assertRaises(FilewiseError):
            self.revision(
                evidence=[Evidence(source_id=self.source["id"], locator="line:1", quote="invented")]
            )
        with self.assertRaises(FilewiseError):
            self.revision(
                evidence=[Evidence(source_id=self.source["id"], locator="line:2", quote="synthetic")]
            )
        self.engine.add_scope(Scope(id="other", title="Other", required_objects=["rule"]), EDITOR)
        with self.assertRaises(FilewiseError):
            self.revision(scope_id="other")
        release = self.published()
        with self.engine.connect(True) as db:
            db.execute(
                "UPDATE sources SET fragments=?",
                (canonical([{"locator": "line:1", "text": "synthetic rule and forged text"}]),),
            )
        with self.assertRaises(FilewiseError):
            self.engine.context(release["id"], ["rule"], READER)

    def test_independent_approval_and_roles(self):
        self.approved()
        release = self.build()
        same_person = Actor(id=EDITOR.id, roles={"reviewer"})
        with self.assertRaises(FilewiseError):
            self.engine.approve(release["id"], same_person)
        with self.assertRaises(FilewiseError):
            self.engine.activate(release["id"], None, PUBLISHER)
        with self.assertRaises(FilewiseError):
            self.engine.add_scope(self.scope, READER)
        with self.assertRaises(FilewiseError):
            self.engine.context(release["id"], ["rule"], READER)

    def test_atomic_activation_conflict(self):
        self.approved()
        release = self.build()
        self.engine.approve(release["id"], REVIEWER)

        def activate(_):
            try:
                self.engine.activate(release["id"], None, PUBLISHER)
                return 200
            except FilewiseError as exc:
                return exc.status

        with ThreadPoolExecutor(max_workers=2) as pool:
            self.assertEqual(sorted(pool.map(activate, range(2))), [200, 409])
        self.assertTrue(self.engine.audit("s", REVIEWER)["chain_valid"])

    def test_live_source_acl_and_revocation(self):
        release = self.published()
        self.engine.source_policy(self.source["id"], ROLES - {"reader"}, False, REVIEWER)
        for read in (
            lambda: self.engine.context(release["id"], ["rule"], READER),
            lambda: self.engine.release(release["id"], READER),
            lambda: self.engine.source(self.source["id"], READER),
        ):
            with self.assertRaises(FilewiseError):
                read()
        overview = self.engine.overview(READER)
        self.assertFalse(overview["releases"])
        self.assertFalse(overview["active"])
        self.engine.source_policy(self.source["id"], ROLES, True, REVIEWER)
        with self.assertRaises(FilewiseError):
            self.engine.context(release["id"], ["rule"], EDITOR)

    def test_historical_diff_evidence_is_protected(self):
        baseline = self.published()
        second_source = ingest(self.engine, "s", "v2.txt", b"new evidence", EDITOR, ROLES)
        self.approved(
            "r2",
            valid_from="2021-01-01T00:00:00Z",
            fields={"value": 2},
            evidence=[Evidence(source_id=second_source["id"], locator="line:1", quote="new evidence")],
        )
        newer = self.build()
        self.engine.source_policy(self.source["id"], ROLES - {"reader"}, False, REVIEWER)
        self.assertEqual(self.engine.resolve("s", READER, now())["objects"]["rule"]["id"], "r2")
        with self.assertRaises(FilewiseError):
            self.engine.release(newer["id"], READER)
        with self.assertRaises(FilewiseError):
            self.engine.diff(baseline["id"], newer["id"], READER)

    def test_revoked_revision_stops_pinned_context(self):
        release = self.published()
        self.engine.decide_revision("r1", "revoked", REVIEWER)
        with self.assertRaises(FilewiseError):
            self.engine.context(release["id"], ["rule"], READER)
        self.assertEqual(self.engine.verify(release["id"], READER)["runtime"]["decision"], "BLOCKED")

    def test_release_revocation_and_missing_context(self):
        release = self.published()
        for ids in ([], ["absent"]):
            with self.assertRaises(FilewiseError):
                self.engine.context(release["id"], ids, READER)
        self.engine.revoke(release["id"], PUBLISHER)
        self.assertFalse(self.engine.overview(READER)["active"])
        with self.assertRaises(FilewiseError):
            self.engine.context(release["id"], ["rule"], READER)
        with self.assertRaises(FilewiseError):
            self.engine.activate(release["id"], None, PUBLISHER, rollback=True)

    def test_tampered_bundle_and_audit_detected(self):
        release = self.published()
        with self.engine.connect(True) as db:
            data = release["bundle"]
            data["state"]["objects"]["rule"]["fields"]["value"] = 9
            db.execute("UPDATE releases SET bundle=? WHERE id=?", (canonical(data), release["id"]))
            db.execute("UPDATE audit SET action='tampered' WHERE seq=1")
        with self.assertRaises(FilewiseError):
            self.engine.release(release["id"], READER)
        self.assertFalse(self.engine.audit("s", REVIEWER)["chain_valid"])


if __name__ == "__main__":
    unittest.main()
