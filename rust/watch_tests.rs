use filewise::{daemon, model::*, store::Store, watch};
use serde_json::{Value, json};
use std::{fs, path::PathBuf, process::Command, time::Duration};
struct Folder {
    _temp: tempfile::TempDir,
    db: PathBuf,
    root: PathBuf,
    project: String,
}
impl Folder {
    fn new(rules: Value) -> Self {
        filewise::worker::executable(PathBuf::from(env!("CARGO_BIN_EXE_filewise"))).unwrap();
        let temp = tempfile::tempdir().unwrap();
        let parent = temp.path().canonicalize().unwrap();
        let root = parent.join("files");
        fs::create_dir(&root).unwrap();
        let db = parent.join("state/filewise.db");
        let result = watch::register(
            &mut Store::open(&db).unwrap(),
            watch::Registration {
                root: root.clone(),
                name: "Synthetic folder".into(),
                includes: vec!["**".into()],
                excludes: vec![],
                rules: serde_json::from_value(rules).unwrap(),
            },
            &watch::actor(),
        )
        .unwrap();
        Self {
            _temp: temp,
            root,
            db,
            project: result["project"].as_str().unwrap().into(),
        }
    }
    fn drive(&self) {
        let mut s = Store::open(&self.db).unwrap();
        let now = chrono::Utc::now().timestamp();
        watch::scan(&mut s, &self.project, now - 20).unwrap();
        watch::scan(&mut s, &self.project, now - 10).unwrap();
        drop(s);
        for _ in 0..4 {
            watch::tick(&self.db).unwrap();
        }
    }
    fn view(&self) -> Value {
        watch::overview(&Store::open(&self.db).unwrap(), &watch::actor()).unwrap()
    }
}
#[test]
fn watch_stable_download_metadata_idempotence_edits_and_deletes() {
    let f = Folder::new(json!({"tags":["research"]}));
    fs::write(f.root.join("report.md.crdownload"), b"partial").unwrap();
    f.drive();
    assert_eq!(f.view()["jobs"], json!([]));
    fs::write(f.root.join("report.md"), b"# Final title\nA stable report.").unwrap();
    {
        let mut s = Store::open(&f.db).unwrap();
        let now = chrono::Utc::now().timestamp();
        watch::scan(&mut s, &f.project, now).unwrap();
        assert!(
            watch::overview(&s, &watch::actor()).unwrap()["jobs"]
                .as_array()
                .unwrap()
                .is_empty()
        );
    }
    f.drive();
    let v = f.view();
    assert_eq!(v["jobs"][0]["status"], "done");
    assert_eq!(v["jobs"][0]["analysis"]["title"], "Final title");
    let count = Store::open(&f.db)
        .unwrap()
        .versions(&f.project, &watch::actor())
        .unwrap();
    f.drive();
    assert_eq!(
        count,
        Store::open(&f.db)
            .unwrap()
            .versions(&f.project, &watch::actor())
            .unwrap()
    );
    fs::write(f.root.join("report.md"), b"# Changed title\nUpdated.").unwrap();
    f.drive();
    assert_eq!(f.view()["jobs"][0]["analysis"]["title"], "Changed title");
    fs::remove_file(f.root.join("report.md")).unwrap();
    f.drive();
    assert!(
        Store::open(&f.db)
            .unwrap()
            .version(&f.project, "latest", None, &watch::actor())
            .unwrap()
            .snapshot
            .files
            .is_empty()
    );
}
#[test]
fn watch_auto_rename_collision_and_undo_do_not_loop_or_publish() {
    let f = Folder::new(json!({"naming":"auto","template":"{title}"}));
    fs::write(f.root.join("Title.md"), b"Existing target stays intact.").unwrap();
    fs::write(f.root.join("download.md"), b"# Title\nSource content.").unwrap();
    use std::os::unix::fs::{MetadataExt, PermissionsExt};
    fs::set_permissions(
        f.root.join("download.md"),
        fs::Permissions::from_mode(0o640),
    )
    .unwrap();
    let inode = fs::metadata(f.root.join("download.md")).unwrap().ino();
    #[cfg(target_os = "macos")]
    assert!(
        Command::new("/usr/bin/xattr")
            .args([
                "-w",
                "com.apple.quarantine",
                "0081;00000000;FilewiseSyntheticTest;"
            ])
            .arg(f.root.join("download.md"))
            .status()
            .unwrap()
            .success()
    );
    f.drive();
    let view = f.view();
    let j = view["jobs"]
        .as_array()
        .unwrap()
        .iter()
        .find(|j| j["path"] == "download.md")
        .unwrap();
    assert_eq!(j["status"], "done", "{j}");
    let target = j["output_path"].as_str().unwrap();
    assert_ne!(target, "Title.md");
    assert_eq!(fs::metadata(f.root.join(target)).unwrap().ino(), inode);
    assert_eq!(
        fs::metadata(f.root.join(target))
            .unwrap()
            .permissions()
            .mode()
            & 0o777,
        0o640
    );
    #[cfg(target_os = "macos")]
    assert!(
        String::from_utf8(
            Command::new("/usr/bin/xattr")
                .args(["-p", "com.apple.quarantine"])
                .arg(f.root.join(target))
                .output()
                .unwrap()
                .stdout
        )
        .unwrap()
        .contains("FilewiseSyntheticTest")
    );
    assert_eq!(
        fs::read(f.root.join("Title.md")).unwrap(),
        b"Existing target stays intact."
    );
    assert!(!f.root.join("download.md").exists());
    assert!(view["folders"][0]["active_release"].is_null());
    let before = view["jobs"].as_array().unwrap().len();
    f.drive();
    assert_eq!(f.view()["jobs"].as_array().unwrap().len(), before);
    watch::action(
        &mut Store::open(&f.db).unwrap(),
        j["id"].as_str().unwrap(),
        "undo",
        &watch::actor(),
    )
    .unwrap();
    assert_eq!(
        fs::read(f.root.join("download.md")).unwrap(),
        b"# Title\nSource content."
    );
    assert!(!f.root.join(target).exists());
    assert_eq!(
        fs::metadata(f.root.join("download.md")).unwrap().ino(),
        inode
    );
    f.drive();
    assert!(f.root.join("download.md").exists());
}
#[test]
fn watch_suggestion_saves_metadata_before_confirmation_and_keeps_acl() {
    let f = Folder::new(json!({"naming":"suggest","template":"{title}"}));
    fs::write(f.root.join("download.md"), b"# Useful title\nBody.").unwrap();
    f.drive();
    let j = f.view()["jobs"][0].clone();
    assert_eq!(j["status"], "review");
    let mut s = Store::open(&f.db).unwrap();
    let v = s
        .version(&f.project, "latest", None, &watch::actor())
        .unwrap();
    assert_eq!(
        v.snapshot.files["download.md"]
            .metadata
            .as_ref()
            .unwrap()
            .summary,
        "# Useful title\nBody."
    );
    watch::action(
        &mut s,
        j["id"].as_str().unwrap(),
        "approve",
        &watch::actor(),
    )
    .unwrap();
    drop(s);
    watch::tick(&f.db).unwrap();
    let j = f.view()["jobs"][0].clone();
    assert_eq!(j["status"], "done", "{j}");
    assert!(f.root.join("Useful title.md").exists());
    let mut s = Store::open(&f.db).unwrap();
    let source = s
        .version(
            &f.project,
            j["version"].as_str().unwrap(),
            None,
            &watch::actor(),
        )
        .unwrap()
        .snapshot
        .files["download.md"]
        .source_id
        .clone();
    let mut reviewer = watch::actor();
    reviewer.id = "reviewer".into();
    reviewer.roles.insert(Role::Reviewer);
    s.policy(&f.project, &source, [Role::Editor].into(), true, &reviewer)
        .unwrap();
    let view = watch::overview(&s, &watch::actor()).unwrap();
    assert_eq!(view["jobs"][0]["status"], "unavailable");
    assert!(view["jobs"][0]["analysis"].is_null());
}
#[test]
fn watch_rules_pause_existing_files_and_manual_metadata_are_preserved() {
    let f = Folder::new(json!({"enabled":false}));
    fs::write(f.root.join("file.json"), br#"{"title":"A","amount":12.5}"#).unwrap();
    f.drive();
    assert!(f.view()["jobs"].as_array().unwrap().is_empty());
    let v = f.view();
    let mut rules: watch::Rules = serde_json::from_value(v["folders"][0]["rules"].clone()).unwrap();
    rules.enabled = true;
    rules.fields.insert("amount".into(), "/amount".into());
    watch::configure(
        &mut Store::open(&f.db).unwrap(),
        &f.project,
        rules,
        Some(1),
        &watch::actor(),
    )
    .unwrap();
    f.drive();
    assert_eq!(
        f.view()["jobs"][0]["analysis"]["fields"]["amount"],
        json!(12.5)
    );
    let mut s = Store::open(&f.db).unwrap();
    let v = s
        .version(&f.project, "latest", None, &watch::actor())
        .unwrap();
    let mut owner = watch::actor();
    owner.id = "human-editor".into();
    let q:WriteQuery=serde_json::from_value(json!({"base_version":v.id,"request_id":"human","message":"manual declaration","changes":{"file.json":{"meta":{"facts":{"approved_value":123}}}}})).unwrap();
    s.write(&f.project, &q, &owner).unwrap();
    drop(s);
    fs::write(f.root.join("file.json"), br#"{"title":"B","amount":19}"#).unwrap();
    f.drive();
    assert_eq!(f.view()["jobs"][0]["error_code"], "manual_metadata");
    let s = Store::open(&f.db).unwrap();
    let v = s
        .version(&f.project, "latest", None, &watch::actor())
        .unwrap();
    assert!(!v.snapshot.files["file.json"].metadata_current);
    assert_eq!(
        v.snapshot.files["file.json"]
            .metadata
            .as_ref()
            .unwrap()
            .facts["approved_value"],
        123
    );
}
#[test]
fn watch_completed_write_and_undo_receipts_resume_after_crash() {
    let f = Folder::new(json!({"naming":"auto","template":"{title}"}));
    fs::write(f.root.join("incoming.md"), b"# Restored name").unwrap();
    f.drive();
    let j = f.view()["jobs"][0].clone();
    let id = j["id"].as_str().unwrap();
    let replay = |status: &str| {
        let s = Store::open(&f.db).unwrap();
        let body: String =
            s.db.query_row("SELECT bundle FROM watch_jobs WHERE id=?", [id], |r| {
                r.get(0)
            })
            .unwrap();
        let mut body: Value = serde_json::from_str(&body).unwrap();
        body["status"] = json!(status);
        s.db.execute(
            "UPDATE watch_jobs SET status=?,bundle=? WHERE id=?",
            rusqlite::params![status, body.to_string(), id],
        )
        .unwrap();
        watch::resume(&s).unwrap();
        drop(s);
        watch::tick(&f.db).unwrap();
    };
    let before = Store::open(&f.db)
        .unwrap()
        .versions(&f.project, &watch::actor())
        .unwrap();
    replay("processing");
    assert_eq!(f.view()["jobs"][0]["status"], "done");
    assert_eq!(
        before,
        Store::open(&f.db)
            .unwrap()
            .versions(&f.project, &watch::actor())
            .unwrap()
    );
    watch::action(
        &mut Store::open(&f.db).unwrap(),
        id,
        "undo",
        &watch::actor(),
    )
    .unwrap();
    replay("undoing");
    assert_eq!(f.view()["jobs"][0]["status"], "undone");
    assert!(f.root.join("incoming.md").exists());
    // Simulate a crash after the exclusive hard link but before removing the old name.
    let mut s = Store::open(&f.db).unwrap();
    let current = s
        .version(&f.project, "latest", None, &watch::actor())
        .unwrap();
    let q:WriteQuery=serde_json::from_value(json!({"base_version":current.id,"request_id":"partial-link","message":"Synthetic partial move","dry_run":true,"changes":{"incoming.md":{"delete":true},"partial.md":{"text":"# Restored name"}}})).unwrap();
    let pending = s.write(&f.project, &q, &watch::actor()).unwrap();
    let version = pending["version"].as_str().unwrap();
    s.db.execute(
        "UPDATE versions SET status='applying' WHERE id=?",
        [version],
    )
    .unwrap();
    fs::hard_link(f.root.join("incoming.md"), f.root.join("partial.md")).unwrap();
    s.recover(&f.project, version, &watch::actor()).unwrap();
    assert!(f.root.join("incoming.md").exists());
    assert!(!f.root.join("partial.md").exists());
}
#[test]
fn watch_existing_files_opt_out_and_local_model_transport() {
    use std::io::{Read, Write};
    let temp = tempfile::tempdir().unwrap();
    let p = temp.path().canonicalize().unwrap();
    let root = p.join("files");
    fs::create_dir(&root).unwrap();
    fs::write(root.join("existing.md"), b"# Existing").unwrap();
    let db = p.join("state/db");
    let mut s = Store::open(&db).unwrap();
    let v = watch::register(
        &mut s,
        watch::Registration {
            root: root.clone(),
            name: "Skip existing".into(),
            includes: vec!["**".into()],
            excludes: vec![],
            rules: watch::Rules {
                process_existing: false,
                ..Default::default()
            },
        },
        &watch::actor(),
    )
    .unwrap();
    let project = v["project"].as_str().unwrap();
    let now = chrono::Utc::now().timestamp();
    watch::scan(&mut s, project, now - 20).unwrap();
    watch::scan(&mut s, project, now - 10).unwrap();
    assert!(
        watch::overview(&s, &watch::actor()).unwrap()["jobs"]
            .as_array()
            .unwrap()
            .is_empty()
    );
    drop(s);
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let endpoint = format!("http://{}", listener.local_addr().unwrap());
    let server = std::thread::spawn(move || {
        let (mut stream, _) = listener.accept().unwrap();
        stream
            .set_read_timeout(Some(Duration::from_secs(5)))
            .unwrap();
        let mut request = vec![];
        let mut byte = [0];
        while !request.ends_with(b"\r\n\r\n") {
            stream.read_exact(&mut byte).unwrap();
            request.push(byte[0]);
            assert!(request.len() < 16000);
        }
        let header = String::from_utf8(request).unwrap();
        assert!(header.starts_with("POST /api/chat HTTP/1.1"));
        let length = header
            .lines()
            .find_map(|l| {
                l.to_ascii_lowercase()
                    .strip_prefix("content-length: ")
                    .map(|s| s.parse::<usize>().unwrap())
            })
            .unwrap();
        let mut body = vec![0; length];
        stream.read_exact(&mut body).unwrap();
        let request: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(request["stream"], false);
        assert_eq!(request["messages"][1]["content"], "# Model input");
        let response=json!({"message":{"content":json!({"title":"Model title","summary":"Controlled response","tags":["local"],"fields":{}}).to_string()}}).to_string();
        write!(stream,"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",response.len(),response).unwrap();
    });
    let rules = watch::Rules {
        engine: "ollama".into(),
        model: "synthetic-model".into(),
        endpoint,
        ..Default::default()
    };
    let result = watch::analyze("model.md", b"# Model input", &rules).unwrap();
    server.join().unwrap();
    assert_eq!(result.title, "Model title");
    assert_eq!(result.coverage, "model_text");
}
#[test]
fn watch_analysis_validation_i18n_and_no_implicit_cloud() {
    let mut rules = watch::Rules {
        engine: "ollama".into(),
        model: "test".into(),
        endpoint: "https://example.com".into(),
        ..Default::default()
    };
    assert!(rules.validate().is_err());
    rules.endpoint = "http://127.0.0.1:1".into();
    assert!(rules.validate().is_ok());
    assert!(
        watch::analyze("a.md", b"# A", &rules)
            .unwrap_err()
            .message
            .contains("Local model unavailable")
    );
    let a = watch::analyze(
        "a.pdf",
        b"%PDF-1.4\n1 0 obj\n(This is container data, not extracted page text)",
        &watch::Rules::default(),
    )
    .unwrap_err();
    assert_eq!(filewise::error_code(&a.message), Some("document_extract"));
    let messages = filewise::strict_json(include_bytes!("ui/strings.json")).unwrap();
    assert_eq!(
        messages["en"]
            .as_object()
            .unwrap()
            .keys()
            .collect::<Vec<_>>(),
        messages["zh-CN"]
            .as_object()
            .unwrap()
            .keys()
            .collect::<Vec<_>>()
    );
    assert_eq!(
        filewise::error_code("Folder rules changed; reload before saving"),
        Some("rules_changed")
    );
}
#[test]
fn watch_documents_parse_rename_undo_and_isolate_failures() {
    let f = Folder::new(json!({"naming":"auto","template":"{title}"}));
    let original = super::document_tests::word("Watched document");
    fs::write(f.root.join("download.docx"), &original).unwrap();
    fs::write(
        f.root.join("scan.pdf"),
        super::document_tests::pdf(true, false),
    )
    .unwrap();
    fs::write(f.root.join("broken.pdf"), b"not a PDF").unwrap();
    fs::write(
        f.root.join("data.xlsx"),
        super::document_tests::sheet("9007199254740993", false),
    )
    .unwrap();
    f.drive();
    let view = f.view();
    let jobs = view["jobs"].as_array().unwrap();
    let doc = jobs.iter().find(|j| j["path"] == "download.docx").unwrap();
    assert_eq!(doc["status"], "done", "{view}");
    assert_eq!(doc["analysis"]["extraction"]["status"], "text");
    assert_eq!(doc["output_path"], "Watched document & 机器故障.docx");
    assert_eq!(
        fs::read(f.root.join(doc["output_path"].as_str().unwrap())).unwrap(),
        original
    );
    assert_eq!(
        jobs.iter().find(|j| j["path"] == "scan.pdf").unwrap()["error_code"],
        "document_no_text"
    );
    assert_eq!(
        jobs.iter().find(|j| j["path"] == "broken.pdf").unwrap()["error_code"],
        "document_extract"
    );
    assert_eq!(
        jobs.iter().find(|j| j["path"] == "data.xlsx").unwrap()["status"],
        "done"
    );
    let mut store = Store::open(&f.db).unwrap();
    watch::action(
        &mut store,
        doc["id"].as_str().unwrap(),
        "undo",
        &watch::actor(),
    )
    .unwrap();
    assert_eq!(fs::read(f.root.join("download.docx")).unwrap(), original);
    assert!(store.versions(&f.project, &watch::actor()).unwrap()["active_release"].is_null());
}
#[test]
fn watch_background_cli_http_restart_and_local_boundary() {
    let temp = tempfile::tempdir().unwrap();
    let p = temp.path().canonicalize().unwrap();
    let db = p.join("state/rust.db");
    let root = p.join("files");
    fs::create_dir(&root).unwrap();
    let cli = |args: &[&str]| {
        let output = Command::new(env!("CARGO_BIN_EXE_filewise"))
            .env_clear()
            .arg("--db")
            .arg(&db)
            .args(args)
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        filewise::strict_json(&output.stdout).unwrap()
    };
    let start = cli(&["start", "--no-open", "--port", "0"]);
    struct Cleanup(PathBuf);
    impl Drop for Cleanup {
        fn drop(&mut self) {
            let _ = daemon::stop(&self.0);
        }
    }
    let _cleanup = Cleanup(db.clone());
    let link = reqwest::Url::parse(start["url"].as_str().unwrap()).unwrap();
    let token = link.fragment().unwrap().strip_prefix("token=").unwrap();
    let origin = link.origin().ascii_serialization();
    let client = reqwest::blocking::Client::builder()
        .no_proxy()
        .timeout(Duration::from_secs(3))
        .build()
        .unwrap();
    assert_eq!(
        client
            .get(format!("{origin}/api/local/overview"))
            .send()
            .unwrap()
            .status()
            .as_u16(),
        401
    );
    assert_eq!(
        client
            .post(format!("{origin}/api/local/register"))
            .bearer_auth(token)
            .header("Origin", "https://evil.invalid")
            .json(&json!({"root":root,"name":"denied"}))
            .send()
            .unwrap()
            .status()
            .as_u16(),
        403
    );
    assert_eq!(
        client
            .get(format!("{origin}/"))
            .header("Host", "evil.invalid")
            .send()
            .unwrap()
            .status()
            .as_u16(),
        403
    );
    let registered:Value=client.post(format!("{origin}/api/local/register")).bearer_auth(token).json(&json!({"root":root,"name":"Background test","rules":{"poll_seconds":1,"settle_seconds":2}})).send().unwrap().json().unwrap();
    assert!(registered["project"].is_string(), "{registered}");
    fs::write(root.join("background.md"), b"# Without a browser").unwrap();
    let wait = |url: &str, count: usize| {
        for _ in 0..150 {
            let v: Value = client
                .get(format!("{url}/api/local/overview"))
                .bearer_auth(token)
                .send()
                .unwrap()
                .json()
                .unwrap();
            if v["jobs"]
                .as_array()
                .unwrap()
                .iter()
                .filter(|j| j["status"] == "done")
                .count()
                >= count
            {
                return;
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        panic!("Background did not process files");
    };
    wait(&origin, 1);
    // A lexically earlier Agent token must never become the UI/control credential.
    let token_file = db.with_extension("ui-tokens.json");
    let mut tokens = filewise::store::read_tokens(&token_file).unwrap();
    let agent_token = "0".repeat(64);
    tokens.insert(
        agent_token.clone(),
        Actor {
            id: "restricted-test-agent".into(),
            roles: [Role::Reader, Role::Editor].into(),
            audience: Audience::Agent,
            workspace_projects: [registered["project"].as_str().unwrap().to_owned()].into(),
        },
    );
    daemon::private_write(&token_file, &serde_json::to_vec(&tokens).unwrap()).unwrap();
    cli(&["status"]);
    cli(&["stop"]);
    fs::write(root.join("offline.md"), b"# Added while stopped").unwrap();
    let restarted = cli(&["start", "--no-open", "--port", "0"]);
    let url = reqwest::Url::parse(restarted["url"].as_str().unwrap()).unwrap();
    let origin = url.origin().ascii_serialization();
    wait(&origin, 2);
    assert_eq!(
        client
            .post(format!("{origin}/api/local/register"))
            .bearer_auth(&agent_token)
            .json(&json!({"root":root,"name":"denied"}))
            .send()
            .unwrap()
            .status()
            .as_u16(),
        403
    );
    assert_eq!(
        client
            .post(format!("{origin}/api/local/stop"))
            .bearer_auth(&agent_token)
            .json(&json!({}))
            .send()
            .unwrap()
            .status()
            .as_u16(),
        403
    );
    cli(&["stop"]);
    let unrelated = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    unrelated.set_nonblocking(true).unwrap();
    let runtime_file = db.with_extension("runtime.json");
    let mut stale: Value = serde_json::from_slice(&fs::read(&runtime_file).unwrap()).unwrap();
    stale["url"] = json!(format!("http://{}", unrelated.local_addr().unwrap()));
    daemon::private_write(&runtime_file, &serde_json::to_vec(&stale).unwrap()).unwrap();
    assert!(daemon::status(&db).is_err());
    assert_eq!(
        unrelated.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
}
