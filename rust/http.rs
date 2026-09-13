use crate::{Error, Result, compute, daemon, fail, model::*, store::Store, watch};
use axum::{
    Extension, Json, Router,
    body::to_bytes,
    extract::{Path, Request, State},
    http::{HeaderValue, StatusCode},
    middleware::{self, Next},
    response::{Html, IntoResponse, Response},
    routing::{get, post, put},
};
use serde::Deserialize;
use serde_json::{Value, json};
use std::{collections::BTreeSet, path::PathBuf, sync::Arc};
use subtle::ConstantTimeEq;

#[derive(Clone)]
struct App {
    db: PathBuf,
    tokens: Arc<Tokens>,
    local: Option<Local>,
}
#[derive(Clone)]
struct Local {
    url: String,
    instance: String,
    stop: tokio::sync::watch::Sender<bool>,
}
pub fn router(db: PathBuf, tokens: Tokens) -> Result<Router> {
    app_router(db, tokens, None)
}
fn app_router(db: PathBuf, tokens: Tokens, local: Option<Local>) -> Result<Router> {
    validate_tokens(&tokens)?;
    let state = App {
        db,
        tokens: Arc::new(tokens),
        local,
    };
    Ok(Router::new()
        .route("/",get(|State(s):State<App>|async move { if s.local.is_some() { Html(include_str!("ui/index.html")).into_response() } else { Json(json!({"product":"Filewise","runtime":"Rust","guide":"docs/rust.md","browser_workbench":"start_with_local_ui","python_runtime":false})).into_response() } }))
        .route("/ui/app.js",get(||async{([("content-type","text/javascript; charset=utf-8")],include_str!("ui/app.js"))}))
        .route("/ui/i18n.js",get(||async{([("content-type","text/javascript; charset=utf-8")],include_str!("ui/i18n.js"))}))
        .route("/ui/strings.json",get(||async{([("content-type","application/json; charset=utf-8")],include_str!("ui/strings.json"))}))
        .route("/ui/style.css",get(||async{([("content-type","text/css; charset=utf-8")],include_str!("ui/style.css"))}))
        .route("/api/local/{operation}",get(local_api).post(local_api))
        .route("/health",get(||async{Json(json!({"status":"ok","runtime":"rust"}))}))
        .route("/api/me",get(|Extension(actor):Extension<Actor>|async move{Json(actor)}))
        .route("/api/projects",get(projects))
        .route("/api/workspaces/{project}/{operation}",get(workspace).post(workspace))
        .route("/api/projects/{project}/versions/{version}/{operation}",post(release))
        .route("/api/projects/{project}/sources/{source}/policy",put(policy))
        .fallback(||async{(StatusCode::NOT_FOUND,Json(json!({"error":"Endpoint not available in the Rust migration"})))})
        .layer(middleware::from_fn_with_state(state.clone(),authorize)).with_state(state))
}
async fn authorize(State(state): State<App>, mut request: Request, next: Next) -> Response {
    if let Some(local) = &state.local {
        let host = request.headers().get("host").and_then(|h| h.to_str().ok());
        let origin = request
            .headers()
            .get("origin")
            .and_then(|h| h.to_str().ok());
        if host != local.url.strip_prefix("http://") || origin.is_some_and(|o| o != local.url) {
            return fail(403, "Local UI requires its exact loopback Host and Origin")
                .into_response();
        }
    }
    if request.uri().path().starts_with("/api/") {
        let token = request
            .headers()
            .get("authorization")
            .and_then(|h| h.to_str().ok())
            .and_then(|h| h.strip_prefix("Bearer "));
        let actor = token.and_then(|token| {
            state
                .tokens
                .iter()
                .find(|(key, _)| bool::from(key.as_bytes().ct_eq(token.as_bytes())))
                .map(|(_, a)| a.clone())
        });
        let Some(actor) = actor else {
            return fail(401, "Bearer credential required").into_response();
        };
        request.extensions_mut().insert(actor);
    }
    let mut response = next.run(request).await;
    for (name, value) in [
        ("cache-control", "no-store"),
        ("x-content-type-options", "nosniff"),
        ("referrer-policy", "no-referrer"),
        (
            "content-security-policy",
            "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
        ),
    ] {
        response
            .headers_mut()
            .insert(name, HeaderValue::from_static(value));
    }
    response
}
async fn run<T: Send + 'static>(
    db: PathBuf,
    f: impl FnOnce(&mut Store) -> Result<T> + Send + 'static,
) -> Result<T> {
    tokio::task::spawn_blocking(move || f(&mut Store::open(&db)?))
        .await
        .map_err(|_| fail(500, "Storage worker failed"))?
}
async fn projects(
    State(state): State<App>,
    Extension(actor): Extension<Actor>,
) -> Result<Json<Value>> {
    run(state.db, move |s| s.projects(&actor)).await.map(Json)
}
async fn workspace(
    State(state): State<App>,
    Extension(actor): Extension<Actor>,
    Path((project, operation)): Path<(String, String)>,
    request: Request,
) -> Result<Json<Value>> {
    actor.access(&project)?;
    let method = request.method().clone();
    if operation == "versions" && method == "GET" {
        return run(state.db, move |s| s.versions(&project, &actor))
            .await
            .map(Json);
    }
    if method != "POST" {
        return Err(fail(405, "Use POST JSON"));
    }
    if ["write", "sync", "recover"].contains(&operation.as_str()) {
        actor.require(Role::Editor)?;
    }
    let limit = if operation == "write" {
        70 * 1024 * 1024
    } else {
        11 * 1024 * 1024
    };
    let bytes = to_bytes(request.into_body(), limit)
        .await
        .map_err(|_| fail(413, "Request body limit exceeded"))?;
    let value = if bytes.is_empty() {
        json!({})
    } else {
        crate::strict_json(&bytes)?
    };
    run(state.db, move |store| match operation.as_str() {
        "write" => store.write(
            &project,
            &serde_json::from_value::<WriteQuery>(value)?,
            &actor,
        ),
        "sync" => {
            if value != json!({}) {
                return Err(fail(422, "sync accepts an empty object"));
            }
            store.sync(&project, &actor)
        }
        _ => compute::dispatch(
            store,
            &project,
            &operation,
            &serde_json::from_value::<Query>(value)?,
            &actor,
        ),
    })
    .await
    .map(Json)
}
#[derive(Deserialize, Default)]
#[serde(deny_unknown_fields)]
struct Publication {
    #[serde(default)]
    expected_active: Option<String>,
}
async fn release(
    State(state): State<App>,
    Extension(actor): Extension<Actor>,
    Path((project, version, operation)): Path<(String, String, String)>,
    request: Request,
) -> Result<Json<Value>> {
    actor.operator(if operation == "approve" {
        Role::Reviewer
    } else {
        Role::Publisher
    })?;
    let bytes = to_bytes(request.into_body(), 8192)
        .await
        .map_err(|_| fail(413, "Request too large"))?;
    let body: Publication = if bytes.is_empty() {
        Publication::default()
    } else {
        serde_json::from_value(crate::strict_json(&bytes)?)?
    };
    run(state.db, move |s| match operation.as_str() {
        "approve" => s.review(&project, &version, &actor),
        "publish" => s.publish(&project, &version, body.expected_active.as_deref(), &actor),
        "revoke" => s.revoke(&project, &version, &actor),
        _ => Err(fail(404, "Unknown release operation")),
    })
    .await
    .map(Json)
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Policy {
    acl: BTreeSet<Role>,
    revoked: bool,
}
async fn policy(
    State(state): State<App>,
    Extension(actor): Extension<Actor>,
    Path((project, source)): Path<(String, String)>,
    request: Request,
) -> Result<Json<Value>> {
    actor.operator(Role::Reviewer)?;
    let bytes = to_bytes(request.into_body(), 8192)
        .await
        .map_err(|_| fail(413, "Request too large"))?;
    let body: Policy = serde_json::from_value(crate::strict_json(&bytes)?)?;
    run(state.db, move |s| {
        s.policy(&project, &source, body.acl, body.revoked, &actor)
    })
    .await
    .map(Json)
}
#[derive(Deserialize, Default)]
#[serde(default, deny_unknown_fields)]
struct Control {
    project: Option<String>,
    rules: Option<watch::Rules>,
    revision: Option<u64>,
    job: Option<String>,
    action: Option<String>,
    enabled: Option<bool>,
    locale: Option<String>,
}
async fn local_api(
    State(state): State<App>,
    Extension(actor): Extension<Actor>,
    Path(operation): Path<String>,
    request: Request,
) -> Result<Json<Value>> {
    let local = state
        .local
        .clone()
        .ok_or_else(|| fail(404, "Local UI is disabled on this gateway"))?;
    actor.operator(Role::Editor)?;
    let read = matches!(operation.as_str(), "overview" | "status" | "autostart");
    if request.method() != "POST" && !(read && request.method() == "GET") {
        return Err(fail(405, "Use POST JSON"));
    }
    let bytes = to_bytes(request.into_body(), 65536)
        .await
        .map_err(|_| fail(413, "Request too large"))?;
    let value = if bytes.is_empty() {
        json!({})
    } else {
        crate::strict_json(&bytes)?
    };
    if operation == "register" {
        let q: watch::Registration = serde_json::from_value(value)?;
        return run(state.db, move |s| watch::register(s, q, &actor))
            .await
            .map(Json);
    }
    let q: Control = serde_json::from_value(value)?;
    if operation == "pick" {
        return tokio::task::spawn_blocking(move || {
            daemon::pick_folder(q.locale.as_deref().unwrap_or("en"))
        })
        .await
        .map_err(|_| fail(500, "Folder picker failed"))?
        .map(Json);
    }
    if operation == "status" {
        return Ok(Json(
            json!({"running":true,"instance":local.instance,"url":local.url,"pid":std::process::id()}),
        ));
    }
    if operation == "stop" {
        let _ = local.stop.send(true);
        return Ok(Json(json!({"stopping":true})));
    }
    run(state.db, move |s| match operation.as_str() {
        "overview" => watch::overview(s, &actor),
        "configure" => watch::configure(
            s,
            q.project
                .as_deref()
                .ok_or_else(|| fail(422, "Project required"))?,
            q.rules.ok_or_else(|| fail(422, "Rules required"))?,
            q.revision,
            &actor,
        ),
        "action" => watch::action(
            s,
            q.job.as_deref().ok_or_else(|| fail(422, "Job required"))?,
            q.action
                .as_deref()
                .ok_or_else(|| fail(422, "Action required"))?,
            &actor,
        ),
        "autostart" => daemon::autostart(
            &s.path,
            q.enabled,
            reqwest::Url::parse(&local.url)
                .ok()
                .and_then(|u| u.port())
                .unwrap_or(8733),
        ),
        _ => Err(fail(404, "Unknown local operation")),
    })
    .await
    .map(Json)
}
pub async fn serve(db: PathBuf, tokens: Tokens, host: &str, port: u16) -> Result<()> {
    serve_ui(db, tokens, host, port, false).await
}
pub async fn serve_ui(
    db: PathBuf,
    tokens: Tokens,
    host: &str,
    port: u16,
    local_ui: bool,
) -> Result<()> {
    // Validate storage before binding; legacy databases are never silently upgraded.
    let store = Store::open(&db)?;
    let db = store.path.clone();
    drop(store);
    let _owner = daemon::lock(&db)?;
    if local_ui && host != "127.0.0.1" {
        return Err(fail(422, "Local UI must bind to 127.0.0.1"));
    }
    let listener = tokio::net::TcpListener::bind((host, port)).await?;
    let (stop, mut shutdown) = tokio::sync::watch::channel(false);
    let url = format!("http://{}", listener.local_addr()?);
    let local = local_ui.then(|| Local {
        url: url.clone(),
        instance: crate::random_id(),
        stop: stop.clone(),
    });
    if let Some(local) = &local {
        daemon::private_write(
            &db.with_extension("runtime.json"),
            &serde_json::to_vec(
                &json!({"url":url,"instance":local.instance,"pid":std::process::id()}),
            )?,
        )?;
    }
    watch::resume(&Store::open(&db)?)?;
    let worker_db = db.clone();
    let mut worker_stop = stop.subscribe();
    let worker = tokio::spawn(async move {
        loop {
            if *worker_stop.borrow() {
                break;
            }
            let db = worker_db.clone();
            match tokio::task::spawn_blocking(move || watch::tick(&db)).await {
                Ok(Ok(())) => {}
                other => eprintln!("Watcher tick failed: {other:?}"),
            }
            tokio::select! { _=tokio::time::sleep(std::time::Duration::from_secs(1))=>{}, _=worker_stop.changed()=>break }
        }
    });
    eprintln!(
        "Filewise Rust listening on http://{}",
        listener.local_addr()?
    );
    let result=axum::serve(listener, app_router(db, tokens, local)?)
        .with_graceful_shutdown(async move {
            #[cfg(unix)]
            { let mut term=tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()).expect("SIGTERM handler"); tokio::select! { _=tokio::signal::ctrl_c()=>{}, _=term.recv()=>{}, _=shutdown.changed()=>{} } }
            #[cfg(not(unix))]
            { tokio::select! { _=tokio::signal::ctrl_c()=>{}, _=shutdown.changed()=>{} } }
        }).await.map_err(Error::from);
    let _ = stop.send(true);
    let _ = worker.await;
    result
}
