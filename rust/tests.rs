#[path = "document_tests.rs"]
mod document_tests;
#[path = "watch_tests.rs"]
mod watch_tests;

use base64::{Engine as _, engine::general_purpose::STANDARD};
use filewise::{
    compute, files, hash,
    model::*,
    store::{Store, auth_init, read_tokens},
    strict_json,
};
use serde_json::{Value, json};
use std::{
    collections::BTreeSet,
    fs,
    path::PathBuf,
    process::{Child, Command, Stdio},
    time::Duration,
};
use tempfile::TempDir;

fn actor(id: &str, roles: &[Role], agent: bool) -> Actor {
    Actor {
        id: id.into(),
        roles: roles.iter().copied().collect(),
        audience: if agent {
            Audience::Agent
        } else {
            Audience::Operator
        },
        workspace_projects: if agent {
            ["p".into()].into()
        } else {
            BTreeSet::new()
        },
    }
}
fn editor() -> Actor {
    actor("editor", &[Role::Editor], false)
}
fn agent() -> Actor {
    actor("agent", &[Role::Reader, Role::Editor], true)
}
fn reviewer() -> Actor {
    actor("reviewer", &[Role::Reviewer], false)
}
fn publisher() -> Actor {
    actor("publisher", &[Role::Publisher], false)
}
fn q(value: Value) -> Query {
    serde_json::from_value(value).unwrap()
}
fn write(base: &str, request: &str, changes: Value) -> WriteQuery {
    serde_json::from_value(json!({"base_version":base,"request_id":request,"message":"Synthetic change","changes":changes})).unwrap()
}
struct Fixture {
    _temp: TempDir,
    root: PathBuf,
    db: PathBuf,
}
impl Fixture {
    fn new(spec: Value) -> Self {
        let temp = tempfile::tempdir().unwrap();
        let parent = temp.path().canonicalize().unwrap();
        let root = parent.join("files");
        fs::create_dir(&root).unwrap();
        fs::write(root.join("requirement.json"), br#"{"pressure_kpa":100}"#).unwrap();
        fs::write(
            root.join("delivery.md"),
            "Check pressure before delivery.\n",
        )
        .unwrap();
        fs::write(
            root.join("machine.md"),
            "机器故障请先断电检查。\nRestart damaged machines safely.\n",
        )
        .unwrap();
        fs::write(root.join("spending.csv"), "id,amount\nA,100\nB,200\n").unwrap();
        let mut default = json!({"id":"p","name":"Synthetic","dependencies":{"delivery.md":["requirement.json"]},"checks":[{"id":"positive-pressure","file":"requirement.json","pointer":"/pressure_kpa","op":"gte","expected":0}]});
        for (k, v) in spec.as_object().unwrap() {
            default[k] = v.clone();
        }
        let f = Self {
            _temp: temp,
            root,
            db: parent.join("private/state.db"),
        };
        let mut store = f.open();
        store
            .create(
                &serde_json::from_value(default).unwrap(),
                Some(&f.root),
                &editor(),
            )
            .unwrap();
        store.sync("p", &editor()).unwrap();
        drop(store);
        f
    }
    fn open(&self) -> Store {
        Store::open(&self.db).unwrap()
    }
}
fn base(store: &Store) -> String {
    store.version("p", "latest", None, &agent()).unwrap().id
}
fn data_contract() -> Value {
    json!({"meaning":{"unit":"CNY","population":"card_panel"},"allowed_uses":["panel_spending"],"checks":[{"id":"rows","op":"row_count","minimum":1},{"id":"ids","op":"unique","column":"id"},{"id":"amount","op":"range","column":"amount","minimum":0}]})
}

#[test]
fn native_save_cas_history_and_independent_publication() {
    let f = Fixture::new(json!({}));
    let mut s = f.open();
    let old = base(&s);
    let cutoff = s
        .version("p", &old, None, &agent())
        .unwrap()
        .available_at
        .unwrap();
    let request = write(
        &old,
        "first",
        json!({"requirement.json":{"text":"{\"pressure_kpa\":120}"},"delivery.md":{"text":"Check pressure 120 before delivery."},"nested/new.txt":{"base64":STANDARD.encode([0,1,2,255])}}),
    );
    let saved = s.write("p", &request, &agent()).unwrap();
    let new = saved["version"].as_str().unwrap();
    assert_eq!(saved["saved"], true);
    assert_eq!(saved["published"], false);
    assert!(s.project("p", &agent()).unwrap().2.is_none());
    assert_eq!(
        fs::read(f.root.join("requirement.json")).unwrap(),
        br#"{"pressure_kpa":120}"#
    );
    assert_eq!(
        fs::read(f.root.join("nested/new.txt")).unwrap(),
        [0, 1, 2, 255]
    );
    assert_eq!(s.write("p", &request, &agent()).unwrap(), saved);
    assert_eq!(
        s.write(
            "p",
            &write(&old, "second", json!({"delivery.md":{"text":"stale"}})),
            &agent()
        )
        .unwrap_err()
        .status,
        409
    );
    let mut changed = request.clone();
    changed.message = "different".into();
    assert!(
        s.write("p", &changed, &agent())
            .unwrap_err()
            .message
            .contains("Idempotency")
    );
    assert_eq!(
        s.read(
            "p",
            &q(json!({"path":"requirement.json","as_of":cutoff})),
            &agent()
        )
        .unwrap()["text"],
        "{\"pressure_kpa\":100}"
    );
    assert!(s.version("p", new, Some(&cutoff), &agent()).is_err());
    assert!(
        s.version("p", "latest", Some("2999-01-01T00:00:00Z"), &agent())
            .is_err()
    );
    let diff = compute::diff(&s, "p", &q(json!({"before":old,"after":new})), &agent()).unwrap();
    assert!(diff["changes"].as_array().unwrap().iter().any(
        |c| c["path"] == "requirement.json" && c["locations"][0]["locator"] == "/pressure_kpa"
    ));
    let impact = compute::dispatch(
        &mut s,
        "p",
        "impact",
        &q(json!({"paths":["requirement.json"]})),
        &agent(),
    )
    .unwrap();
    assert!(
        impact["affected"]
            .as_array()
            .unwrap()
            .contains(&json!("delivery.md"))
    );
    assert!(s.review("p", new, &agent()).is_err());
    assert!(
        s.review("p", new, &actor("agent", &[Role::Reviewer], false))
            .is_err()
    );
    assert!(s.publish("p", new, None, &publisher()).is_err());
    s.review("p", new, &reviewer()).unwrap();
    s.publish("p", new, None, &publisher()).unwrap();
    assert_eq!(s.version("p", "published", None, &agent()).unwrap().id, new);
    assert!(s.publish("p", new, None, &publisher()).is_err());
    s.revoke("p", new, &publisher()).unwrap();
    assert!(
        s.version("p", "latest", None, &agent())
            .unwrap_err()
            .message
            .contains("revoked")
    );
    assert_eq!(
        s.version("p", "latest", Some(&cutoff), &agent())
            .unwrap()
            .id,
        old
    );
    assert_eq!(
        s.trace("p", None, &agent()).unwrap()["chain_verified"],
        true
    );
    let trace = s.trace("p", Some(&cutoff), &agent()).unwrap();
    assert!(
        trace["events"]
            .as_array()
            .unwrap()
            .iter()
            .all(|e| e["recorded_at"].as_str().unwrap() <= cutoff.as_str())
    );
}

#[test]
fn dry_run_require_pass_and_unsaved_metadata_do_not_become_latest() {
    let f = Fixture::new(json!({}));
    let mut s = f.open();
    let old = base(&s);
    let mut request = write(
        &old,
        "dry",
        json!({"requirement.json":{"text":"{\"pressure_kpa\":150}","meta":{"summary":"unsaved marker"}}}),
    );
    request.dry_run = true;
    let preview = s.write("p", &request, &agent()).unwrap();
    assert_eq!(preview["saved"], false);
    assert!(preview["availability"].is_null());
    assert_eq!(base(&s), old);
    let mut blocked = write(
        &old,
        "refuse",
        json!({"requirement.json":{"text":"{\"pressure_kpa\":-1}"}}),
    );
    blocked.require_pass = true;
    let report = s.write("p", &blocked, &agent()).unwrap();
    assert_eq!(report["status"], "blocked");
    assert_eq!(base(&s), old);
    assert_eq!(s.write("p", &blocked, &agent()).unwrap(), report);
    let synced = s.sync("p", &editor()).unwrap();
    assert!(
        synced["files"]
            .as_array()
            .unwrap()
            .iter()
            .all(|f| f["metadata"].is_null())
    );
    blocked.base_version = base(&s);
    blocked.request_id = "allow-repair".into();
    blocked.require_pass = false;
    assert_eq!(s.write("p", &blocked, &agent()).unwrap()["saved"], true);
    assert!(s.review("p", &base(&s), &reviewer()).is_err());
    assert_eq!(
        fs::read_to_string(f.root.join("requirement.json")).unwrap(),
        "{\"pressure_kpa\":-1}"
    );
}

#[test]
fn metadata_freshness_and_content_evidence_remain_live() {
    let f = Fixture::new(json!({}));
    let mut s = f.open();
    let old = base(&s);
    let r=s.write("p",&write(&old,"meta",json!({"requirement.json":{"meta":{"summary":"unique-metadata-only-token","facts":{"pressure":100}}}})),&agent()).unwrap();
    let a = r["version"].as_str().unwrap().to_owned();
    assert_eq!(
        s.read("p", &q(json!({"path":"requirement.json"})), &agent())
            .unwrap()["declared_by"],
        "agent"
    );
    assert!(
        !compute::search(
            &mut s,
            "p",
            &q(json!({"query":"unique-metadata-only-token","mode":"exact"})),
            &agent()
        )
        .unwrap()["hits"]
            .as_array()
            .unwrap()
            .is_empty()
    );
    let changed = s
        .write(
            "p",
            &write(
                &a,
                "stale-fact",
                json!({"requirement.json":{"text":"{\"pressure_kpa\":120}"}}),
            ),
            &agent(),
        )
        .unwrap();
    assert_eq!(changed["verification"]["decision"], "BLOCKED");
    assert_eq!(
        s.read("p", &q(json!({"path":"requirement.json"})), &agent())
            .unwrap()["metadata_current"],
        false
    );
    assert!(
        compute::search(
            &mut s,
            "p",
            &q(json!({"query":"unique-metadata-only-token","mode":"exact"})),
            &agent()
        )
        .unwrap()["hits"]
            .as_array()
            .unwrap()
            .is_empty()
    );
    let source = s.version("p", &a, None, &agent()).unwrap().snapshot.files["requirement.json"]
        .source_id
        .clone();
    let current = base(&s);
    s.write("p",&write(&current,"evidence",json!({"derived.md":{"text":"Derived statement","meta":{"evidence":[{"source_id":source,"locator":"line:1","quote":"100"}]}}})),&agent()).unwrap();
    s.policy(
        "p",
        &source,
        [Role::Reader, Role::Editor, Role::Reviewer].into(),
        true,
        &reviewer(),
    )
    .unwrap();
    assert!(s.version("p", "latest", None, &agent()).is_err());
}

#[test]
fn json_csv_quality_reads_the_full_table_and_rejects_malformed_values() {
    let contract: DataContract = serde_json::from_value(data_contract()).unwrap();
    contract.validate().unwrap();
    let mut rows: Vec<_> = (0..160)
        .map(|i| json!({"id":format!("p{i}"),"amount":100}))
        .collect();
    rows[150]["amount"] = json!(-1);
    let quality = compute::quality(
        "table.json",
        &serde_json::to_vec(&rows).unwrap(),
        &contract,
        None,
        None,
    );
    assert_eq!(quality["decision"], "BLOCKED");
    assert_eq!(quality["tests"][2]["record_indices"], json!([150]));
    let csv = format!(
        "id,amount\n{}",
        (0..160)
            .map(|i| format!("p{i},{}\n", if i == 150 { -1 } else { 100 }))
            .collect::<String>()
    );
    assert_eq!(
        compute::quality("table.csv", csv.as_bytes(), &contract, None, None)["tests"][2]["record_indices"],
        json!([150])
    );
    for (path, body) in [
        ("bad.json", "[{\"id\":\"A\",\"amount\":1,\"amount\":2}]"),
        ("bad.json", "[{\"id\":\"A\",\"amount\":1e999}]"),
        ("bad.json", "[{\"id\":\"A\",\"amount\":true}]"),
        ("bad.csv", "id,amount\nA,=SUM(A1)\n"),
        ("bad.csv", "id,amount\nA,1,2\n"),
        ("bad.csv", "id,id\nA,1\n"),
        ("bad.xlsx", "binary"),
    ] {
        assert_eq!(
            compute::quality(path, body.as_bytes(), &contract, None, None)["decision"],
            "BLOCKED",
            "{body}"
        );
    }
    let too_big = vec![json!({"id":"A","amount":1}); 20001];
    assert_eq!(
        compute::quality(
            "large.json",
            &serde_json::to_vec(&too_big).unwrap(),
            &contract,
            None,
            None
        )["decision"],
        "BLOCKED"
    );
    let decimal:DataContract=serde_json::from_value(json!({"checks":[{"id":"range","op":"range","column":"amount","minimum":0.1,"maximum":0.1}]})).unwrap();
    assert_eq!(
        compute::quality("t.csv", b"amount\n0.10\n", &decimal, None, None)["decision"],
        "PASS"
    );
    assert_eq!(
        compute::quality(
            "t.csv",
            b"amount\n0.100000000000000001\n",
            &decimal,
            None,
            None
        )["decision"],
        "BLOCKED"
    );
    assert_eq!(
        compute::quality(
            "empty.csv",
            b"id,amount\n",
            &DataContract::default(),
            None,
            None
        )["decision"],
        "NEEDS_REVIEW"
    );
    let precise:DataContract=serde_json::from_value(strict_json(br#"{"checks":[{"id":"exact","op":"range","column":"amount","minimum":0.100000000000000001,"maximum":0.100000000000000001}]}"#).unwrap()).unwrap();
    assert_eq!(
        compute::quality(
            "precise.json",
            br#"[{"amount":0.100000000000000001}]"#,
            &precise,
            None,
            None
        )["decision"],
        "PASS"
    );
    assert_eq!(
        compute::quality("precise.json", br#"[{"amount":0.1}]"#, &precise, None, None)["decision"],
        "BLOCKED"
    );
    for pointer in ["/records/~2", "/a/~", "not/a/pointer"] {
        let c = DataContract {
            rows_pointer: pointer.into(),
            ..Default::default()
        };
        assert!(c.validate().is_err());
    }
    assert!(strict_json(b"{\"nested\":{\"x\":1,\"x\":2}}").is_err());
}

#[test]
fn immutable_quality_policy_cannot_be_lowered_or_deleted_around() {
    let f = Fixture::new(json!({"data_contracts":{"spending.csv":data_contract()}}));
    let mut s = f.open();
    assert_eq!(
        compute::dispatch(
            &mut s,
            "p",
            "quality",
            &q(json!({"paths":["spending.csv"]})),
            &agent()
        )
        .unwrap()["decision"],
        "PASS"
    );
    let conflict = q(
        json!({"paths":["spending.csv"],"requirements":{"spending.csv":{"expect":{"unit":"USD"},"purpose":"total_market","require_quality":false}}}),
    );
    assert_eq!(
        compute::dispatch(&mut s, "p", "quality", &conflict, &agent()).unwrap()["decision"],
        "BLOCKED"
    );
    let current = base(&s);
    let mut downgrade = write(
        &current,
        "downgrade",
        json!({"spending.csv":{"meta":{"data":{"checks":[]}}}}),
    );
    downgrade.require_pass = true;
    assert_eq!(
        s.write("p", &downgrade, &agent()).unwrap()["status"],
        "blocked"
    );
    assert_eq!(base(&s), current);
    let mut deletion = write(
        &current,
        "delete-policy",
        json!({"spending.csv":{"delete":true}}),
    );
    deletion.require_pass = true;
    assert_eq!(
        s.write("p", &deletion, &agent()).unwrap()["status"],
        "blocked"
    );
    assert!(f.root.join("spending.csv").exists());
    assert_eq!(
        compute::dispatch(
            &mut s,
            "p",
            "quality",
            &q(json!({"paths":["delivery.md"]})),
            &agent()
        )
        .unwrap()["decision"],
        "NEEDS_REVIEW"
    );
}

#[test]
fn cohort_failed_resync_keeps_its_original_baseline() {
    let f = Fixture::new(json!({}));
    let mut s = f.open();
    let old = base(&s);
    let mut contract = data_contract();
    contract["checks"]
        .as_array_mut()
        .unwrap()
        .push(json!({"id":"cohort","op":"distinct_count_change","column":"id","max_change":0.1}));
    s.write(
        "p",
        &write(
            &old,
            "cohort-contract",
            json!({"spending.csv":{"meta":{"data":contract}}}),
        ),
        &agent(),
    )
    .unwrap();
    let baseline = base(&s);
    let result=s.write("p",&write(&baseline,"growth",json!({"spending.csv":{"text":"id,amount\nA,100\nB,200\nC,300\n","meta":{"data":contract}}})),&agent()).unwrap();
    assert_eq!(result["verification"]["decision"], "BLOCKED");
    let f1 = s
        .version("p", "latest", None, &agent())
        .unwrap()
        .snapshot
        .files["spending.csv"]
        .clone();
    let q1 = f1.data_quality.unwrap();
    assert_eq!(q1["baseline"], baseline);
    assert_eq!(q1["tests"][3]["actual"]["relative_change"], 0.5);
    s.sync("p", &editor()).unwrap();
    let current = s.version("p", "latest", None, &agent()).unwrap();
    assert_eq!(
        current.snapshot.files["spending.csv"]
            .data_quality
            .as_ref()
            .unwrap(),
        &q1
    );
    assert_eq!(
        current.snapshot.files["spending.csv"].lineage[0]["role"],
        "baseline"
    );
}

#[test]
fn pinned_lineage_preserves_roles_and_transitive_review() {
    let f = Fixture::new(json!({}));
    let mut s = f.open();
    let old = base(&s);
    s.write("p",&write(&old,"undeclared",json!({"derived.md":{"text":"Unverified derived data","meta":{"processing":"deterministic"}}})),&agent()).unwrap();
    let input = base(&s);
    let output=s.write("p",&write(&input,"dependent",json!({"result.md":{"text":"Depends on derived data","meta":{"processing":"deterministic","lineage":[{"version":input,"path":"derived.md","role":"data"}]}}})),&agent()).unwrap();
    assert_eq!(output["verification"]["decision"], "NEEDS_REVIEW");
    assert!(
        s.review("p", output["version"].as_str().unwrap(), &reviewer())
            .is_err()
    );
    let current = base(&s);
    s.write("p",&write(&current,"roles",json!({"model-result.md":{"text":"Historical interpretation","meta":{"processing":"deterministic","lineage":[{"version":old,"path":"requirement.json","role":"data"},{"version":old,"path":"requirement.json","role":"model"}]}}})),&agent()).unwrap();
    let v = s.version("p", "latest", None, &agent()).unwrap();
    let cutoff = v.available_at.clone().unwrap();
    let inputs = s
        .lineage(
            "p",
            vec![v.snapshot.files["model-result.md"].clone()],
            Some(&cutoff),
            &agent(),
        )
        .unwrap();
    assert_eq!(inputs.len(), 2);
    assert!(inputs.iter().any(|v| v["role"] == "model"));
    let guard = compute::guard(
        &s,
        "p",
        &v,
        &["model-result.md".into()],
        &q(json!({"as_of":cutoff})),
        &agent(),
        false,
    )
    .unwrap();
    assert_eq!(guard["decision"], "NEEDS_REVIEW");
    s.revoke("p", &old, &publisher()).unwrap();
    assert!(s.version("p", "latest", None, &agent()).is_err());
}

#[test]
fn retrieval_task_oracles_historical_models_and_no_evidence() {
    let f = Fixture::new(json!({}));
    let mut s = f.open();
    let hits = compute::search(
        &mut s,
        "p",
        &q(json!({"query":"机器故障","mode":"lexical"})),
        &agent(),
    )
    .unwrap();
    assert_eq!(hits["hits"][0]["path"], "machine.md");
    let english = compute::search(
        &mut s,
        "p",
        &q(json!({"query":"machine","mode":"lexical"})),
        &agent(),
    )
    .unwrap();
    assert_eq!(english["hits"][0]["path"], "machine.md");
    assert_eq!(
        compute::search(
            &mut s,
            "p",
            &q(json!({"query":"How to repair?","mode":"semantic"})),
            &agent()
        )
        .unwrap_err()
        .status,
        503
    );
    let task=compute::compile(&mut s,"p",&q(json!({"paths":["delivery.md"],"direction":"reverse","goal":"Check pressure","output_checks":[{"id":"pressure","object_id":"result","field":"pressure","expected":100}]})),&agent()).unwrap();
    assert!(
        task["paths"]
            .as_array()
            .unwrap()
            .contains(&json!("requirement.json"))
    );
    let id = task["task_id"].as_str().unwrap();
    assert_eq!(
        compute::verify(
            &mut s,
            "p",
            &q(json!({"phase":"preflight","task_id":id})),
            &agent()
        )
        .unwrap()["decision"],
        "PASS"
    );
    assert_eq!(
        compute::verify(
            &mut s,
            "p",
            &q(json!({"phase":"postflight","task_id":id,"result":{"pressure":100}})),
            &agent()
        )
        .unwrap()["decision"],
        "PASS"
    );
    assert_eq!(
        compute::verify(
            &mut s,
            "p",
            &q(json!({"phase":"postflight","task_id":id,"result":{"pressure":120}})),
            &agent()
        )
        .unwrap()["decision"],
        "BLOCKED"
    );
    assert_eq!(
        compute::verify(&mut s, "p", &q(json!({"phase":"postflight"})), &agent()).unwrap()["decision"],
        "NEEDS_REVIEW"
    );
    let empty = compute::compile(
        &mut s,
        "p",
        &q(json!({"goal":"zzzzzzzzzznotpresent","retrieval_mode":"lexical"})),
        &agent(),
    )
    .unwrap();
    assert_eq!(
        compute::verify(
            &mut s,
            "p",
            &q(json!({"phase":"preflight","task_id":empty["task_id"]})),
            &agent()
        )
        .unwrap()["decision"],
        "BLOCKED"
    );
    let cutoff = s
        .version("p", "latest", None, &agent())
        .unwrap()
        .available_at
        .unwrap();
    let history=compute::compile(&mut s,"p",&q(json!({"paths":["requirement.json"],"goal":"Historical result","as_of":cutoff,"model_use":"generative","output_checks":[{"id":"oracle","object_id":"result","field":"pressure","expected":100}]})),&agent()).unwrap();
    assert!(
        !history["tools"]
            .as_array()
            .unwrap()
            .contains(&json!("write"))
    );
    assert_eq!(
        compute::verify(
            &mut s,
            "p",
            &q(
                json!({"phase":"postflight","task_id":history["task_id"],"result":{"pressure":100}})
            ),
            &agent()
        )
        .unwrap()["decision"],
        "NEEDS_REVIEW"
    );
    assert_eq!(
        compute::verify(
            &mut s,
            "p",
            &q(json!({"phase":"preflight","task_id":history["task_id"],"operation":"write"})),
            &agent()
        )
        .unwrap()["decision"],
        "BLOCKED"
    );
    assert!(compute::verify(&mut s,"p",&q(json!({"phase":"preflight","task_id":history["task_id"],"as_of":"2020-01-01T00:00:00Z"})),&agent()).is_err());
    assert_eq!(
        compute::verify(
            &mut s,
            "p",
            &q(json!({"phase":"postflight","operation":"write"})),
            &agent()
        )
        .unwrap_err()
        .status,
        501
    );
}

#[test]
fn drift_compensation_and_owned_recovery_preserve_external_edits() {
    let f = Fixture::new(json!({}));
    let mut s = f.open();
    let old = base(&s);
    fs::write(f.root.join("requirement.json"), "external").unwrap();
    assert!(
        s.write(
            "p",
            &write(&old, "drift", json!({"delivery.md":{"text":"new"}})),
            &agent()
        )
        .unwrap_err()
        .message
        .contains("STALE")
    );
    fs::write(f.root.join("requirement.json"), br#"{"pressure_kpa":100}"#).unwrap();
    // Inject completion failure after real filesystem replacement; compensation must restore originals.
    s.db.execute_batch("CREATE TRIGGER refuse_completion BEFORE UPDATE OF available_at ON versions BEGIN SELECT RAISE(ABORT,'synthetic disk failure'); END;").unwrap();
    let request = write(
        &old,
        "recoverable",
        json!({"requirement.json":{"text":"{\"pressure_kpa\":120}"},"new.md":{"text":"new"}}),
    );
    assert!(s.write("p", &request, &agent()).is_err());
    assert_eq!(
        fs::read(f.root.join("requirement.json")).unwrap(),
        br#"{"pressure_kpa":100}"#
    );
    assert!(!f.root.join("new.md").exists());
    assert_eq!(base(&s), old);
    let candidate: String =
        s.db.query_row(
            "SELECT version FROM operations WHERE request='recoverable'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert!(
        s.version("p", &candidate, None, &agent())
            .unwrap()
            .available_at
            .is_none()
    );
    s.db.execute_batch("DROP TRIGGER refuse_completion;")
        .unwrap();
    // Simulate a process interruption after replacing one file, leaving durable 'applying' intent.
    s.db.execute(
        "UPDATE versions SET status='applying' WHERE id=?",
        [&candidate],
    )
    .unwrap();
    files::replace(
        &f.root,
        "requirement.json",
        Some(br#"{"pressure_kpa":120}"#),
        Some(&hash(br#"{"pressure_kpa":100}"#)),
    )
    .unwrap();
    assert!(
        s.sync("p", &editor())
            .unwrap_err()
            .message
            .contains("RECOVERY_REQUIRED")
    );
    assert!(s.recover("p", &candidate, &editor()).is_err());
    fs::write(f.root.join("requirement.json"), "external edit").unwrap();
    assert!(s.recover("p", &candidate, &agent()).is_err());
    assert_eq!(
        fs::read_to_string(f.root.join("requirement.json")).unwrap(),
        "external edit"
    );
    fs::write(f.root.join("requirement.json"), br#"{"pressure_kpa":120}"#).unwrap();
    s.recover("p", &candidate, &agent()).unwrap();
    assert_eq!(
        fs::read(f.root.join("requirement.json")).unwrap(),
        br#"{"pressure_kpa":100}"#
    );
    assert_eq!(s.write("p", &request, &agent()).unwrap()["saved"], true);
}

#[test]
fn permission_scope_symlinks_and_credentials_fail_closed() {
    use std::os::unix::fs::{PermissionsExt, symlink};
    let f = Fixture::new(json!({}));
    let mut s = f.open();
    let mut other = agent();
    other.workspace_projects = ["other".into()].into();
    assert!(s.version("p", "latest", None, &other).is_err());
    let mut bad = agent();
    bad.roles.insert(Role::Reviewer);
    assert!(bad.validate().is_err());
    let source = s
        .version("p", "latest", None, &agent())
        .unwrap()
        .snapshot
        .files["requirement.json"]
        .source_id
        .clone();
    s.policy("p", &source, [Role::Reviewer].into(), false, &reviewer())
        .unwrap();
    assert!(s.version("p", "latest", None, &agent()).is_err());
    s.policy(
        "p",
        &source,
        [Role::Reader, Role::Editor, Role::Reviewer, Role::Publisher].into(),
        false,
        &reviewer(),
    )
    .unwrap();
    if f.root.join("Requirement.json").exists() {
        let current = base(&s);
        assert!(
            s.write(
                "p",
                &write(
                    &current,
                    "case-alias",
                    json!({"Requirement.json":{"text":"{\"pressure_kpa\":100}"}})
                ),
                &agent()
            )
            .is_err()
        );
        assert_eq!(
            fs::read(f.root.join("requirement.json")).unwrap(),
            br#"{"pressure_kpa":100}"#
        );
        assert_eq!(base(&s), current);
    }
    let spec = s.project("p", &agent()).unwrap().0;
    for path in [
        "TOKENS.JSON",
        "nested/HISTORY.DB",
        ".SSH/id_rsa",
        ".DS_Store",
    ] {
        assert!(files::excluded(path, &spec), "{path}");
    }
    let secret = f._temp.path().join("outside.secret");
    fs::write(&secret, "never capture").unwrap();
    symlink(&secret, f.root.join("link.md")).unwrap();
    assert!(
        !files::scan(&f.root, &s.project("p", &agent()).unwrap().0)
            .unwrap()
            .contains_key("link.md")
    );
    let current = base(&s);
    assert!(
        s.write(
            "p",
            &write(&current, "symlink", json!({"link.md":{"text":"overwrite"}})),
            &agent()
        )
        .is_err()
    );
    assert_eq!(fs::read_to_string(&secret).unwrap(), "never capture");
    for path in ["../escape", "/absolute", "a//b", "a/./b", "a/../b", "a\\b"] {
        assert!(files::relative(path).is_err());
    }
    let tokens = f._temp.path().join("tokens.json");
    auth_init(&tokens).unwrap();
    assert_eq!(
        fs::metadata(&tokens).unwrap().permissions().mode() & 0o777,
        0o600
    );
    let alias = f._temp.path().join("alias.json");
    symlink(&tokens, &alias).unwrap();
    assert!(read_tokens(&alias).is_err());
    fs::set_permissions(&tokens, fs::Permissions::from_mode(0o644)).unwrap();
    assert!(read_tokens(&tokens).is_err());
    assert_eq!(
        fs::metadata(&f.db).unwrap().permissions().mode() & 0o777,
        0o600
    );
}

#[test]
fn legacy_database_and_receipt_tampering_are_not_accepted() {
    use std::os::unix::fs::PermissionsExt;
    let temp = tempfile::tempdir().unwrap();
    let old = temp.path().join("legacy.db");
    let db = rusqlite::Connection::open(&old).unwrap();
    db.execute_batch(
        "CREATE TABLE python_history(value TEXT);INSERT INTO python_history VALUES('keep');",
    )
    .unwrap();
    drop(db);
    fs::set_permissions(&old, fs::Permissions::from_mode(0o600)).unwrap();
    let bytes = fs::read(&old).unwrap();
    let error = match Store::open(&old) {
        Err(e) => e,
        Ok(_) => panic!("Legacy database accepted"),
    };
    assert!(error.message.contains("Legacy"));
    assert_eq!(fs::read(&old).unwrap(), bytes);
    let f = Fixture::new(json!({}));
    let s = f.open();
    let current = base(&s);
    s.db.execute(
        "UPDATE versions SET available_at='2020-01-01T00:00:00.000000+00:00' WHERE id=?",
        [&current],
    )
    .unwrap();
    assert!(
        s.version("p", &current, None, &agent())
            .unwrap_err()
            .message
            .contains("Availability")
    );
    s.db.execute("UPDATE audit SET event='{}' WHERE seq=1", [])
        .unwrap();
    assert!(s.trace("p", None, &agent()).is_err());
    // A future receipt on a separate candidate prevents a newly recorded completion, without backdating.
    let f2 = Fixture::new(json!({}));
    let mut s = f2.open();
    let old = base(&s);
    let mut dry = write(
        &old,
        "clock-marker",
        json!({"delivery.md":{"text":"marker"}}),
    );
    dry.dry_run = true;
    let marker = s.write("p", &dry, &agent()).unwrap();
    s.db.execute(
        "UPDATE versions SET available_at='2999-01-01T00:00:00.000000+00:00' WHERE id=?",
        [marker["version"].as_str().unwrap()],
    )
    .unwrap();
    assert!(
        s.sync("p", &editor())
            .unwrap_err()
            .message
            .contains("clock")
    );
    assert_eq!(base(&s), old);
}

#[test]
fn trace_rechecks_revoked_evidence_even_after_the_file_is_deleted() {
    let f = Fixture::new(json!({}));
    let mut s = f.open();
    let old = base(&s);
    let source = s.version("p", &old, None, &agent()).unwrap().snapshot.files["requirement.json"]
        .source_id
        .clone();
    let task=compute::compile(&mut s,"p",&q(json!({"paths":["requirement.json"],"goal":"Read pressure","output_checks":[{"id":"pressure","object_id":"result","field":"pressure","expected":100}]})),&agent()).unwrap();
    compute::verify(
        &mut s,
        "p",
        &q(json!({"task_id":task["task_id"],"phase":"postflight","result":{"pressure":100}})),
        &agent(),
    )
    .unwrap();
    assert!(
        s.trace("p", None, &agent()).unwrap()["events"]
            .as_array()
            .unwrap()
            .iter()
            .any(|e| e["action"] == "task.verified")
    );
    s.write(
        "p",
        &write(
            &old,
            "delete-old-source",
            json!({"requirement.json":{"delete":true}}),
        ),
        &agent(),
    )
    .unwrap();
    s.policy(
        "p",
        &source,
        [Role::Reader, Role::Editor, Role::Reviewer].into(),
        true,
        &reviewer(),
    )
    .unwrap();
    s.version("p", "latest", None, &agent()).unwrap();
    let trace = s.trace("p", None, &agent()).unwrap();
    assert!(
        trace["events"]
            .as_array()
            .unwrap()
            .iter()
            .all(|e| e["data"]["version"] != old)
    );
    assert!(!trace.to_string().contains("task.verified"));
}

#[test]
fn rust_guide_shell_and_json_examples_parse() {
    use std::io::Write as _;
    let mut shell_count = 0;
    for doc in [
        include_str!("../README.md"),
        include_str!("../README.en.md"),
        include_str!("../docs/rust.md"),
        include_str!("../docs/folders.md"),
    ] {
        let mut language = "";
        let mut body = String::new();
        for line in doc.lines() {
            if line.starts_with("```") {
                if language == "bash" {
                    let mut child = Command::new("/bin/bash")
                        .args(["-n"])
                        .stdin(Stdio::piped())
                        .stderr(Stdio::piped())
                        .spawn()
                        .unwrap();
                    child
                        .stdin
                        .take()
                        .unwrap()
                        .write_all(body.as_bytes())
                        .unwrap();
                    let output = child.wait_with_output().unwrap();
                    assert!(
                        output.status.success(),
                        "{}\n{}",
                        body,
                        String::from_utf8_lossy(&output.stderr)
                    );
                    shell_count += 1;
                } else if language == "json" {
                    strict_json(body.as_bytes()).unwrap();
                }
                language = line.strip_prefix("```").unwrap_or_default();
                body.clear();
            } else if !language.is_empty() {
                body.push_str(line);
                body.push('\n');
            }
        }
        assert!(language.is_empty(), "Unclosed Markdown fence");
    }
    assert!(shell_count >= 20);
}

struct Server(Child);
impl Drop for Server {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}
fn run_cli(db: &PathBuf, args: &[&str]) -> Value {
    let output = Command::new(env!("CARGO_BIN_EXE_filewise"))
        .env_clear()
        .arg("--db")
        .arg(db)
        .args(args)
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
    strict_json(&output.stdout).unwrap()
}
#[test]
fn actual_rust_cli_http_storage_roundtrip_without_python_environment() {
    let temp = tempfile::tempdir().unwrap();
    let parent = temp.path().canonicalize().unwrap();
    let root = parent.join("files");
    fs::create_dir(&root).unwrap();
    fs::write(root.join("requirement.json"), br#"{"pressure_kpa":100}"#).unwrap();
    let db = parent.join("state/rust.db");
    let tokens = parent.join("state/tokens.json");
    run_cli(&db, &["auth-init", "--out", tokens.to_str().unwrap()]);
    run_cli(
        &db,
        &[
            "project",
            "add",
            root.to_str().unwrap(),
            "--id",
            "p",
            "--name",
            "Rust CLI test",
        ],
    );
    fs::write(
        root.join("report.docx"),
        document_tests::word("HTTP document"),
    )
    .unwrap();
    fs::write(root.join("report.pdf"), document_tests::pdf(false, true)).unwrap();
    fs::write(root.join("slides.pptx"), document_tests::slides()).unwrap();
    fs::write(
        root.join("data.xlsx"),
        document_tests::sheet("9007199254740993", false),
    )
    .unwrap();
    let initial = run_cli(&db, &["project", "sync", "p"]);
    let old = initial["version"].as_str().unwrap();
    let auth = run_cli(
        &db,
        &["auth-agent", "p", "--tokens", tokens.to_str().unwrap()],
    );
    let token = auth["FILEWISE_TOKEN"].as_str().unwrap();
    let bind = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let port = bind.local_addr().unwrap().port();
    drop(bind);
    let url = format!("http://127.0.0.1:{port}");
    let log = fs::File::create(parent.join("server.log")).unwrap();
    let mut server = Server(
        Command::new(env!("CARGO_BIN_EXE_filewise"))
            .env_clear()
            .arg("--db")
            .arg(&db)
            .args(["serve", "--tokens"])
            .arg(&tokens)
            .args(["--port", &port.to_string()])
            .stdout(Stdio::null())
            .stderr(log)
            .spawn()
            .unwrap(),
    );
    let client = reqwest::blocking::Client::builder()
        .timeout(Duration::from_secs(3))
        .build()
        .unwrap();
    let mut ready = false;
    for _ in 0..100 {
        if client
            .get(format!("{url}/health"))
            .send()
            .is_ok_and(|r| r.status().is_success())
        {
            ready = true;
            break;
        }
        assert!(server.0.try_wait().unwrap().is_none(), "Server exited");
        std::thread::sleep(Duration::from_millis(30));
    }
    assert!(ready);
    let agent_call = |args: &[&str], exit: i32| -> Value {
        let output = Command::new(env!("CARGO_BIN_EXE_filewise"))
            .env_clear()
            .env("FILEWISE_TOKEN", token)
            .env("FILEWISE_URL", &url)
            .env("HTTP_PROXY", "http://127.0.0.1:1")
            .env("ALL_PROXY", "http://127.0.0.1:1")
            .env("NO_PROXY", "")
            .arg("agent")
            .args(args)
            .output()
            .unwrap();
        assert_eq!(
            output.status.code(),
            Some(exit),
            "stderr={} stdout={}",
            String::from_utf8_lossy(&output.stderr),
            String::from_utf8_lossy(&output.stdout)
        );
        strict_json(&output.stdout).unwrap()
    };
    assert_eq!(
        client
            .get(format!("{url}/api/projects"))
            .send()
            .unwrap()
            .status()
            .as_u16(),
        401
    );
    assert_eq!(agent_call(&["projects"], 0)[0]["id"], "p");
    for path in ["report.docx", "report.pdf", "slides.pptx", "data.xlsx"] {
        let read = agent_call(&["read", "p", path], 0);
        assert_eq!(read["extraction"]["status"], "text", "{path}: {read}");
        assert!(read["text"].as_str().unwrap().contains("机器故障"));
    }
    let exported = parent.join("exported.docx");
    agent_call(
        &[
            "read",
            "p",
            "report.docx",
            "--output",
            exported.to_str().unwrap(),
        ],
        0,
    );
    assert_eq!(
        fs::read(exported).unwrap(),
        fs::read(root.join("report.docx")).unwrap()
    );
    let saved = agent_call(
        &[
            "write",
            "p",
            "requirement.json",
            "--base",
            old,
            "--request-id",
            "http-save",
            "--content",
            "{\"pressure_kpa\":120}",
            "--meta",
            "{\"summary\":\"changed pressure\"}",
            "-m",
            "Rust native save",
        ],
        0,
    );
    assert_eq!(saved["saved"], true);
    assert_eq!(
        fs::read(root.join("requirement.json")).unwrap(),
        br#"{"pressure_kpa":120}"#
    );
    assert_eq!(
        agent_call(&["read", "p", "requirement.json", "--version", old], 0)["text"],
        "{\"pressure_kpa\":100}"
    );
    assert!(
        agent_call(&["search", "p", "pressure", "--mode", "lexical"], 0)["hits"]
            .as_array()
            .unwrap()
            .iter()
            .any(|h| h["path"] == "requirement.json")
    );
    assert_eq!(
        agent_call(&["quality", "p", "--path", "requirement.json"], 2)["decision"],
        "NEEDS_REVIEW"
    );
    assert_eq!(
        client
            .post(format!("{url}/api/projects/p/versions/{old}/approve"))
            .bearer_auth(token)
            .json(&json!({}))
            .send()
            .unwrap()
            .status()
            .as_u16(),
        403
    );
    assert_eq!(
        client
            .post(format!("{url}/api/workspaces/other/ls"))
            .bearer_auth(token)
            .json(&json!({}))
            .send()
            .unwrap()
            .status()
            .as_u16(),
        403
    );
    assert_eq!(
        client
            .post(format!("{url}/api/workspaces/p/read"))
            .bearer_auth(token)
            .json(&json!({"path":"../state/tokens.json"}))
            .send()
            .unwrap()
            .status()
            .as_u16(),
        422
    );
    assert_eq!(
        client
            .post(format!("{url}/api/workspaces/p/read"))
            .bearer_auth(token)
            .body("{\"path\":\"requirement.json\",\"path\":\"machine.md\"}")
            .send()
            .unwrap()
            .status()
            .as_u16(),
        422
    );
    let malformed=client.post(format!("{url}/api/workspaces/p/write")).bearer_auth(token).json(&json!({"base_version":saved["version"],"request_id":"bad-field","message":"no trust injection","changes":{"x.md":{"text":"x","meta":{"declared_by":"reviewer"}}}})).send().unwrap();
    assert_eq!(malformed.status().as_u16(), 422);
    #[cfg(target_os = "macos")]
    {
        let profile = format!(
            "(version 1)(allow default)(deny file-read* file-write* (subpath \"{}\") (subpath \"{}\"))",
            root.display(),
            db.parent().unwrap().display()
        );
        for path in [&db, &tokens, &root.join("requirement.json")] {
            let denied = Command::new("/usr/bin/sandbox-exec")
                .args(["-p", &profile, "/bin/cat"])
                .arg(path)
                .output()
                .unwrap();
            assert!(!denied.status.success());
        }
        let output = Command::new("/usr/bin/sandbox-exec")
            .env_clear()
            .env("FILEWISE_TOKEN", token)
            .env("FILEWISE_URL", &url)
            .args([
                "-p",
                &profile,
                env!("CARGO_BIN_EXE_filewise"),
                "agent",
                "read",
                "p",
                "requirement.json",
            ])
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        assert_eq!(
            strict_json(&output.stdout).unwrap()["text"],
            "{\"pressure_kpa\":120}"
        );
    }
    drop(server);
    assert!(
        Store::open(&db)
            .unwrap()
            .project("p", &editor())
            .unwrap()
            .2
            .is_none()
    );
}
