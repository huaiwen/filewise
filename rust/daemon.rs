//! Owned background process and explicit macOS login startup. No shell commands or Python.
use crate::{
    Result, fail, files, hash,
    model::*,
    random_id,
    store::{Store, read_tokens},
    strict_json,
};
use fs2::FileExt;
use serde_json::{Value, json};
use std::{
    fs::{self, File, OpenOptions},
    io::Write,
    os::unix::{
        fs::{OpenOptionsExt, PermissionsExt},
        process::CommandExt,
    },
    path::{Path, PathBuf},
    process::{Command, Stdio},
    time::Duration,
};

pub fn state_path(path: &Path) -> Result<PathBuf> {
    let path = if path == Path::new(".filewise-rust/filewise.db") {
        let home = std::env::var_os("HOME")
            .ok_or_else(|| fail(422, "HOME required; alternatively supply --db"))?;
        PathBuf::from(home).join(if cfg!(target_os = "macos") {
            "Library/Application Support/Filewise/filewise.db"
        } else {
            ".local/share/filewise/filewise.db"
        })
    } else {
        path.to_path_buf()
    };
    let s = Store::open(&path)?;
    Ok(s.path.clone())
}
pub fn private_write(path: &Path, bytes: &[u8]) -> Result<()> {
    let parent = path
        .parent()
        .ok_or_else(|| fail(422, "Missing private parent"))?;
    let name = path
        .file_name()
        .and_then(|s| s.to_str())
        .ok_or_else(|| fail(422, "Invalid private filename"))?;
    if let Ok(m) = fs::symlink_metadata(path) {
        if !m.is_file() || m.permissions().mode() & 0o077 != 0 {
            return Err(fail(403, "Runtime state must be a private regular file"));
        }
    }
    let old = files::read(parent, name)?;
    files::replace(
        parent,
        name,
        Some(bytes),
        old.as_deref().map(hash).as_deref(),
    )
}
pub fn lock(db: &Path) -> Result<File> {
    let file = OpenOptions::new()
        .create(true)
        .truncate(false)
        .read(true)
        .write(true)
        .mode(0o600)
        .custom_flags(rustix::fs::OFlags::NOFOLLOW.bits() as i32)
        .open(db.with_extension("service.lock"))?;
    file.try_lock_exclusive()
        .map_err(|_| fail(409, "A Filewise service already owns this database"))?;
    Ok(file)
}
fn ui_tokens(db: &Path) -> Result<Tokens> {
    let path = db.with_extension("ui-tokens.json");
    if !path.try_exists()? {
        let tokens = Tokens::from([(
            random_id(),
            Actor {
                id: "filewise-local-ui".into(),
                roles: [Role::Reader, Role::Editor].into(),
                audience: Audience::Operator,
                workspace_projects: Default::default(),
            },
        )]);
        let mut f = OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .custom_flags(rustix::fs::OFlags::NOFOLLOW.bits() as i32)
            .open(&path)?;
        f.write_all(&serde_json::to_vec(&tokens)?)?;
        f.sync_all()?;
    }
    read_tokens(&path)
}
fn ui_token(tokens: &Tokens) -> Result<&str> {
    tokens
        .iter()
        .find(|(_, a)| {
            a.id == "filewise-local-ui"
                && a.audience == Audience::Operator
                && a.roles == [Role::Reader, Role::Editor].into()
        })
        .map(|(token, _)| token.as_str())
        .ok_or_else(|| fail(403, "Local UI operator credential is missing"))
}
fn client() -> Result<reqwest::blocking::Client> {
    reqwest::blocking::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(Duration::from_secs(2))
        .build()
        .map_err(|e| fail(503, e.to_string()))
}
fn runtime(db: &Path) -> Result<Value> {
    let p = db.with_extension("runtime.json");
    let m = fs::symlink_metadata(&p)?;
    if !m.is_file() || m.permissions().mode() & 0o077 != 0 {
        return Err(fail(403, "Invalid private runtime state"));
    }
    let bytes = files::read(
        p.parent().unwrap(),
        p.file_name().unwrap().to_str().unwrap(),
    )?
    .ok_or_else(|| fail(404, "Service is not started"))?;
    let v = strict_json(&bytes)?;
    let url = reqwest::Url::parse(
        v["url"]
            .as_str()
            .ok_or_else(|| fail(409, "Missing service URL"))?,
    )
    .map_err(|_| fail(409, "Invalid service URL"))?;
    if url.scheme() != "http"
        || url.host_str() != Some("127.0.0.1")
        || url.path() != "/"
        || url.query().is_some()
        || url.fragment().is_some()
        || !url.username().is_empty()
        || url.password().is_some()
    {
        return Err(fail(409, "Runtime URL must be a local origin"));
    }
    Ok(v)
}
pub fn status(db: &Path) -> Result<Value> {
    let v = runtime(db)?;
    // Do not send an operator token to a recycled port after our daemon exited.
    match lock(db) {
        Ok(_) => return Err(fail(503, "Filewise is not running; run filewise start")),
        Err(e) if e.message == "A Filewise service already owns this database" => {}
        Err(e) => return Err(e),
    }
    let response = client()?
        .get(format!("{}/api/local/status", v["url"].as_str().unwrap()))
        .bearer_auth(ui_token(&ui_tokens(db)?)?)
        .send()
        .map_err(|_| fail(503, "Filewise is not running; run filewise start"))?;
    let live: Value = response
        .json()
        .map_err(|_| fail(503, "Service identity check failed"))?;
    if live["instance"] != v["instance"] {
        return Err(fail(
            409,
            "Service identity changed; refusing to control another process",
        ));
    }
    Ok(live)
}
pub fn stop(db: &Path) -> Result<Value> {
    status(db)?;
    let v = runtime(db)?;
    let response = client()?
        .post(format!("{}/api/local/stop", v["url"].as_str().unwrap()))
        .bearer_auth(ui_token(&ui_tokens(db)?)?)
        .json(&json!({}))
        .send()
        .map_err(|e| fail(503, e.to_string()))?;
    if !response.status().is_success() {
        return Err(fail(409, "Service refused shutdown"));
    }
    for _ in 0..100 {
        if lock(db).is_ok() {
            return Ok(
                json!({"stopped":true,"login_startup":autostart_path(db).ok().flatten().map(|p|p.exists())}),
            );
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    Err(fail(
        409,
        "Shutdown is still waiting for current work; retry status",
    ))
}
pub fn start(db: &Path, port: u16, open: bool) -> Result<Value> {
    let tokens = ui_tokens(db)?;
    if status(db).is_err() {
        let guard = lock(db)?;
        let logpath = db.with_extension("service.log");
        let log = OpenOptions::new()
            .create(true)
            .append(true)
            .mode(0o600)
            .custom_flags(rustix::fs::OFlags::NOFOLLOW.bits() as i32)
            .open(&logpath)?;
        if log.metadata()?.permissions().mode() & 0o077 != 0 {
            return Err(fail(403, "Service log must be private"));
        }
        drop(guard);
        let mut child = Command::new(std::env::current_exe()?)
            .arg("--db")
            .arg(db)
            .args(["serve", "--local-ui", "--tokens"])
            .arg(db.with_extension("ui-tokens.json"))
            .args(["--port", &port.to_string()])
            .stdin(Stdio::null())
            .stdout(Stdio::from(log.try_clone()?))
            .stderr(Stdio::from(log))
            .process_group(0)
            .spawn()?;
        let mut ready = false;
        for _ in 0..100 {
            if status(db).is_ok() {
                ready = true;
                break;
            }
            if child.try_wait()?.is_some() {
                return Err(fail(
                    503,
                    format!("Background service exited; inspect {}", logpath.display()),
                ));
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        if !ready {
            let _ = child.kill();
            let _ = child.wait();
            return Err(fail(503, "Background service did not become ready"));
        }
    }
    let live = status(db)?;
    let url = format!(
        "{}/#token={}",
        live["url"]
            .as_str()
            .ok_or_else(|| fail(409, "Missing live URL"))?,
        ui_token(&tokens)?
    );
    if open {
        let command = if cfg!(target_os = "macos") {
            "/usr/bin/open"
        } else {
            "xdg-open"
        };
        let _ = Command::new(command)
            .arg(&url)
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn();
    }
    Ok(
        json!({"running":true,"url":url,"db":db,"log":db.with_extension("service.log"),"note":"Browser may be closed; the background process keeps monitoring. The URL contains an operator editing credential; do not share."}),
    )
}
fn autostart_path(db: &Path) -> Result<Option<PathBuf>> {
    if !cfg!(target_os = "macos") {
        return Ok(None);
    }
    let home =
        std::env::var_os("HOME").ok_or_else(|| fail(422, "HOME required for login startup"))?;
    Ok(Some(PathBuf::from(home).join("Library/LaunchAgents").join(
        format!(
            "local.filewise.{}.plist",
            &hash(db.to_string_lossy().as_bytes())[..16]
        ),
    )))
}
fn xml(s: &str) -> String {
    s.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
        .replace('\'', "&apos;")
}
pub fn autostart(db: &Path, enable: Option<bool>, port: u16) -> Result<Value> {
    let Some(path) = autostart_path(db)? else {
        if enable == Some(true) {
            return Err(fail(
                501,
                "Login startup is currently implemented for macOS only",
            ));
        }
        return Ok(json!({"supported":false,"enabled":false}));
    };
    if let Some(enable) = enable {
        if enable {
            ui_tokens(db)?;
            fs::create_dir_all(path.parent().unwrap())?;
            let parent = path.parent().unwrap().canonicalize()?;
            if parent.metadata()?.permissions().mode() & 0o022 != 0 {
                return Err(fail(
                    403,
                    "LaunchAgents directory must not be writable by others",
                ));
            }
            let path = parent.join(path.file_name().unwrap());
            let args = [
                std::env::current_exe()?.to_string_lossy().into_owned(),
                "--db".into(),
                db.to_string_lossy().into_owned(),
                "serve".into(),
                "--local-ui".into(),
                "--tokens".into(),
                db.with_extension("ui-tokens.json")
                    .to_string_lossy()
                    .into_owned(),
                "--port".into(),
                port.to_string(),
            ];
            let label = path.file_stem().unwrap().to_string_lossy();
            let args = args
                .iter()
                .map(|s| format!("<string>{}</string>", xml(s)))
                .collect::<String>();
            let log = xml(&db.with_extension("service.log").to_string_lossy());
            let plist = format!(
                "<?xml version=\"1.0\" encoding=\"UTF-8\"?><!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\"><plist version=\"1.0\"><dict><key>Label</key><string>{label}</string><key>ProgramArguments</key><array>{args}</array><key>RunAtLoad</key><true/><key>KeepAlive</key><false/><key>StandardOutPath</key><string>{log}</string><key>StandardErrorPath</key><string>{log}</string></dict></plist>"
            );
            private_write(&path, plist.as_bytes())?;
        } else if path.try_exists()? {
            let m = fs::symlink_metadata(&path)?;
            if !m.is_file() || m.permissions().mode() & 0o077 != 0 {
                return Err(fail(403, "Refusing to remove an unmanaged login item"));
            }
            fs::remove_file(&path)?;
        }
    }
    Ok(
        json!({"supported":true,"enabled":path.exists(),"path":path,"takes_effect":"next_login","note":"No system-wide service; keep the executable and state at their current paths. Not a crash supervisor."}),
    )
}
pub fn pick_folder(locale: &str) -> Result<Value> {
    let prompt = match locale {
        "en" => {
            "POSIX path of (choose folder with prompt \"Choose a folder for Filewise to monitor\")"
        }
        "zh-CN" => {
            "POSIX path of (choose folder with prompt \"选择要由 Filewise 持续监控的文件夹\")"
        }
        _ => return Err(fail(422, "Unsupported UI locale")),
    };
    if !cfg!(target_os = "macos") {
        return Err(fail(501, "Paste an absolute folder path on this platform"));
    }
    let result = Command::new("/usr/bin/osascript")
        .args(["-e", prompt])
        .stdin(Stdio::null())
        .output()?;
    if !result.status.success() {
        return Err(fail(
            409,
            "Folder selection cancelled or unavailable; paste the path instead",
        ));
    }
    let path =
        String::from_utf8(result.stdout).map_err(|_| fail(422, "Folder path must be UTF-8"))?;
    Ok(json!({"root":path.trim()}))
}
