use base64::{Engine as _, engine::general_purpose::STANDARD};
use clap::{Args, Parser, Subcommand};
use filewise::{
    Result, daemon, fail, http,
    model::*,
    random_id,
    store::{Store, auth_agent, auth_init, read_tokens},
    strict_json,
};
use serde_json::{Value, json};
use std::{
    collections::BTreeSet,
    fs::OpenOptions,
    io::{Read, Write},
    os::unix::fs::OpenOptionsExt,
    path::{Path, PathBuf},
    time::Duration,
};

#[derive(Parser)]
#[command(
    version,
    about = "Filewise — Rust-native versioned file and knowledge operations"
)]
struct Cli {
    #[arg(
        long,
        env = "FILEWISE_DB",
        default_value = ".filewise-rust/filewise.db"
    )]
    db: PathBuf,
    #[arg(long, default_value = "local-editor")]
    actor: String,
    #[arg(long, value_enum, value_delimiter = ',', default_value = "editor")]
    roles: Vec<Role>,
    #[command(subcommand)]
    command: Command,
}
// Parsed once: keep this small CLI value on the stack rather than boxing it.
#[allow(clippy::large_enum_variant)]
#[derive(Subcommand)]
enum Command {
    AuthInit {
        #[arg(long, default_value = ".filewise-rust/tokens.json")]
        out: PathBuf,
    },
    AuthAgent {
        project: String,
        #[arg(long, default_value = ".filewise-rust/tokens.json")]
        tokens: PathBuf,
        #[arg(long)]
        read_only: bool,
    },
    Project {
        #[command(subcommand)]
        command: ProjectCommand,
    },
    /// Start the local UI and monitoring service in the background.
    Start {
        #[arg(long, default_value_t = 8733)]
        port: u16,
        #[arg(long)]
        no_open: bool,
    },
    /// Inspect the background service.
    Status,
    /// Stop the background service (does not erase folders or history).
    Stop,
    /// Configure optional next-login startup on macOS.
    Autostart {
        #[arg(long, conflicts_with = "disable")]
        enable: bool,
        #[arg(long)]
        disable: bool,
        #[arg(long, default_value_t = 8733)]
        port: u16,
    },
    Serve {
        #[arg(long)]
        local_ui: bool,
        #[arg(long, default_value = ".filewise-rust/tokens.json")]
        tokens: PathBuf,
        #[arg(long, default_value = "127.0.0.1")]
        host: String,
        #[arg(long, default_value_t = 8000)]
        port: u16,
    },
    Agent(AgentArgs),
}
#[derive(Subcommand)]
enum ProjectCommand {
    Add {
        root: PathBuf,
        #[arg(long)]
        id: String,
        #[arg(long)]
        name: String,
        #[arg(long)]
        spec: Option<PathBuf>,
        #[arg(long = "include")]
        includes: Vec<String>,
    },
    Sync {
        project: String,
    },
    Status {
        project: String,
    },
    Review {
        project: String,
        version: String,
    },
    Publish {
        project: String,
        version: String,
        #[arg(long)]
        expected_active: Option<String>,
    },
    Revoke {
        project: String,
        version: String,
    },
    Recover {
        project: String,
        version: String,
    },
}
#[derive(Args)]
struct AgentArgs {
    #[arg(long, env = "FILEWISE_URL", default_value = "http://127.0.0.1:8000")]
    url: String,
    #[command(subcommand)]
    command: AgentCommand,
}
#[derive(Args, Default)]
struct Common {
    #[arg(long, default_value = "latest")]
    version: String,
    #[arg(long)]
    as_of: Option<String>,
    #[arg(long = "path")]
    paths: Vec<String>,
}
impl Common {
    fn query(self) -> Query {
        Query {
            version: self.version,
            as_of: self.as_of,
            paths: self.paths,
            ..Default::default()
        }
    }
}
#[derive(Subcommand)]
enum AgentCommand {
    Projects,
    Versions {
        project: String,
    },
    Ls {
        project: String,
        #[command(flatten)]
        common: Common,
    },
    Read {
        project: String,
        path: String,
        #[command(flatten)]
        common: Common,
        #[arg(long)]
        output: Option<PathBuf>,
    },
    Write(WriteArgs),
    Delete {
        project: String,
        path: String,
        #[arg(long)]
        base: String,
        #[arg(short = 'm', long)]
        message: String,
        #[arg(long)]
        request_id: Option<String>,
    },
    Sync {
        project: String,
    },
    Search {
        project: String,
        query: String,
        #[command(flatten)]
        common: Common,
        #[arg(long, default_value = "hybrid")]
        mode: String,
        #[arg(long, default_value_t = 30)]
        limit: usize,
        #[arg(long = "tag")]
        tags: Vec<String>,
        #[arg(long, default_value_t = 3)]
        max_per_file: usize,
        #[arg(long)]
        requirements: Option<PathBuf>,
    },
    Diff {
        project: String,
        before: String,
        #[arg(default_value = "latest")]
        after: String,
        #[arg(long = "path")]
        paths: Vec<String>,
    },
    Impact {
        project: String,
        #[command(flatten)]
        common: Common,
        #[arg(long)]
        base_version: Option<String>,
        #[arg(long, default_value = "forward")]
        direction: String,
    },
    Resolve {
        project: String,
        #[command(flatten)]
        common: Common,
    },
    Quality {
        project: String,
        #[command(flatten)]
        common: Common,
        #[arg(long)]
        requirements: Option<PathBuf>,
    },
    Compile {
        project: String,
        #[command(flatten)]
        common: Common,
        #[arg(long)]
        goal: String,
        #[arg(long)]
        query: Option<String>,
        #[arg(long)]
        base_version: Option<String>,
        #[arg(long, default_value = "forward")]
        direction: String,
        #[arg(long, default_value = "hybrid")]
        retrieval_mode: String,
        #[arg(long, default_value_t = 5)]
        retrieval_limit: usize,
        #[arg(long, default_value_t = 12000)]
        max_chars: usize,
        #[arg(long)]
        checks: Option<PathBuf>,
        #[arg(long)]
        requirements: Option<PathBuf>,
        #[arg(long, default_value = "none")]
        model_use: String,
    },
    Verify {
        project: String,
        #[command(flatten)]
        common: Common,
        #[arg(long, default_value = "build")]
        phase: String,
        #[arg(long)]
        task_id: Option<String>,
        #[arg(long, default_value = "read")]
        operation: String,
        #[arg(long)]
        result: Option<PathBuf>,
        #[arg(long)]
        outputs: Option<PathBuf>,
        #[arg(long)]
        citations: Option<PathBuf>,
        #[arg(long)]
        output_version: Option<String>,
    },
    Recover {
        project: String,
        #[arg(long)]
        version: String,
    },
    Trace {
        project: String,
        #[command(flatten)]
        common: Common,
    },
}
#[derive(Args)]
struct WriteArgs {
    project: String,
    path: Option<String>,
    #[arg(long)]
    base: Option<String>,
    #[arg(long)]
    request_id: Option<String>,
    #[arg(short = 'm', long)]
    message: Option<String>,
    #[arg(long, conflicts_with = "file")]
    content: Option<String>,
    #[arg(long)]
    file: Option<PathBuf>,
    #[arg(long)]
    meta: Option<String>,
    #[arg(long, conflicts_with_all=["path","base","request_id","message","content","file","meta","dry_run","require_pass","task","model"])]
    request: Option<PathBuf>,
    #[arg(long)]
    dry_run: bool,
    #[arg(long)]
    require_pass: bool,
    #[arg(long, default_value = "")]
    task: String,
    #[arg(long, default_value = "")]
    model: String,
}
fn read_bytes(path: &Path, limit: usize) -> Result<Vec<u8>> {
    let mut bytes = vec![];
    if path == Path::new("-") {
        std::io::stdin()
            .take((limit + 1) as u64)
            .read_to_end(&mut bytes)?;
    } else {
        std::fs::File::open(path)?
            .take((limit + 1) as u64)
            .read_to_end(&mut bytes)?;
    }
    if bytes.len() > limit {
        return Err(fail(413, "Input file exceeds limit"));
    }
    Ok(bytes)
}
fn load<T: serde::de::DeserializeOwned + Default>(path: Option<PathBuf>) -> Result<T> {
    path.map(|p| {
        serde_json::from_value(strict_json(&read_bytes(&p, 70 * 1024 * 1024)?)?).map_err(Into::into)
    })
    .unwrap_or_else(|| Ok(T::default()))
}
fn request(url: &str, path: &str, method: &str, body: Option<Value>) -> Result<Value> {
    let origin = reqwest::Url::parse(url).map_err(|_| fail(422, "Invalid FILEWISE_URL"))?;
    if !["http", "https"].contains(&origin.scheme())
        || !origin.username().is_empty()
        || origin.password().is_some()
        || origin.query().is_some()
        || origin.fragment().is_some()
        || origin.path() != "/"
    {
        return Err(fail(
            422,
            "Use an HTTP(S) origin without credentials, path or query",
        ));
    }
    if origin.scheme() == "http"
        && ![
            Some("localhost"),
            Some("127.0.0.1"),
            Some("[::1]"),
            Some("::1"),
        ]
        .contains(&origin.host_str())
    {
        return Err(fail(422, "Remote gateways require HTTPS"));
    }
    let token = std::env::var("FILEWISE_TOKEN")
        .map_err(|_| fail(401, "Set FILEWISE_TOKEN to a project credential"))?;
    if token.len() < 32 || token.len() > 256 || !token.bytes().all(|c| c.is_ascii_graphic()) {
        return Err(fail(401, "Invalid credential"));
    }
    let client = reqwest::blocking::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(Duration::from_secs(300))
        .build()
        .map_err(|e| fail(503, e.to_string()))?;
    let mut call = client
        .request(
            reqwest::Method::from_bytes(method.as_bytes())
                .map_err(|_| fail(422, "Invalid HTTP method"))?,
            format!("{}/api/{path}", url.trim_end_matches('/')),
        )
        .bearer_auth(token);
    if let Some(body) = body {
        call = call.json(&body);
    }
    let response = call.send().map_err(|e| fail(503, e.to_string()))?;
    let status = response.status();
    let mut bytes = vec![];
    response
        .take(100 * 1024 * 1024 + 1)
        .read_to_end(&mut bytes)?;
    if bytes.len() > 100 * 1024 * 1024 {
        return Err(fail(413, "Response exceeds client limit"));
    }
    if !status.is_success() {
        return Err(fail(
            status.as_u16(),
            format!(
                "Gateway {status}: {}",
                String::from_utf8_lossy(&bytes)
                    .chars()
                    .take(2000)
                    .collect::<String>()
            ),
        ));
    }
    strict_json(&bytes)
}
fn agent(args: AgentArgs) -> Result<Value> {
    let url = args.url;
    let invoke = |project: &str, op: &str, q: Query| -> Result<Value> {
        filewise::id(project)?;
        q.validate()?;
        request(
            &url,
            &format!("workspaces/{project}/{op}"),
            "POST",
            Some(serde_json::to_value(q)?),
        )
    };
    let save = |project: &str, q: WriteQuery| -> Result<Value> {
        filewise::id(project)?;
        q.validate()?;
        request(
            &url,
            &format!("workspaces/{project}/write"),
            "POST",
            Some(serde_json::to_value(q)?),
        )
    };
    match args.command {
        AgentCommand::Projects => request(&url, "projects", "GET", None),
        AgentCommand::Versions { project } => {
            filewise::id(&project)?;
            request(&url, &format!("workspaces/{project}/versions"), "GET", None)
        }
        AgentCommand::Ls { project, common } => invoke(&project, "ls", common.query()),
        AgentCommand::Read {
            project,
            path,
            common,
            output,
        } => {
            let mut q = common.query();
            q.path = Some(path);
            q.include_bytes = output.is_some();
            let result = invoke(&project, "read", q)?;
            if let Some(path) = output {
                let bytes = STANDARD
                    .decode(
                        result["base64"]
                            .as_str()
                            .ok_or_else(|| fail(409, "Missing original bytes"))?,
                    )
                    .map_err(|_| fail(409, "Invalid response encoding"))?;
                if json!(filewise::hash(&bytes)) != result["sha256"] {
                    return Err(fail(409, "Gateway content digest mismatch"));
                }
                let mut file = OpenOptions::new()
                    .create_new(true)
                    .write(true)
                    .mode(0o600)
                    .open(&path)?;
                file.write_all(&bytes)?;
                file.sync_all()?;
                Ok(json!({"output":path,"receipt":result["receipt"]}))
            } else {
                Ok(result)
            }
        }
        AgentCommand::Write(args) => {
            let q = if let Some(path) = args.request {
                serde_json::from_value(strict_json(&read_bytes(&path, 70 * 1024 * 1024)?)?)?
            } else {
                let path = args.path.ok_or_else(|| fail(422, "Write requires path"))?;
                let change = FileChange {
                    text: args.content,
                    base64: args
                        .file
                        .map(|p| {
                            read_bytes(&p, filewise::files::MAX_FILE).map(|b| STANDARD.encode(b))
                        })
                        .transpose()?,
                    meta: args
                        .meta
                        .map(|s| {
                            serde_json::from_value(strict_json(s.as_bytes())?)
                                .map_err(filewise::Error::from)
                        })
                        .transpose()?,
                    delete: false,
                };
                WriteQuery {
                    base_version: args
                        .base
                        .ok_or_else(|| fail(422, "Write requires --base"))?,
                    request_id: args.request_id.unwrap_or_else(random_id),
                    changes: [(path, change)].into(),
                    message: args
                        .message
                        .ok_or_else(|| fail(422, "Write requires --message"))?,
                    dry_run: args.dry_run,
                    require_pass: args.require_pass,
                    task: args.task,
                    model: args.model,
                    tool: "filewise.agent.write.rust".into(),
                }
            };
            save(&args.project, q)
        }
        AgentCommand::Delete {
            project,
            path,
            base,
            message,
            request_id,
        } => save(
            &project,
            WriteQuery {
                base_version: base,
                request_id: request_id.unwrap_or_else(random_id),
                changes: [(
                    path,
                    FileChange {
                        delete: true,
                        ..Default::default()
                    },
                )]
                .into(),
                message,
                dry_run: false,
                require_pass: false,
                task: String::new(),
                model: String::new(),
                tool: "filewise.agent.delete.rust".into(),
            },
        ),
        AgentCommand::Sync { project } => {
            filewise::id(&project)?;
            request(
                &url,
                &format!("workspaces/{project}/sync"),
                "POST",
                Some(json!({})),
            )
        }
        AgentCommand::Search {
            project,
            query,
            common,
            mode,
            limit,
            tags,
            max_per_file,
            requirements,
        } => {
            let mut q = common.query();
            q.query = Some(query);
            q.mode = mode;
            q.limit = limit;
            q.tags = tags;
            q.max_per_file = max_per_file;
            q.requirements = load(requirements)?;
            invoke(&project, "search", q)
        }
        AgentCommand::Diff {
            project,
            before,
            after,
            paths,
        } => invoke(
            &project,
            "diff",
            Query {
                before: Some(before),
                after,
                paths,
                ..Default::default()
            },
        ),
        AgentCommand::Impact {
            project,
            common,
            base_version,
            direction,
        } => {
            let mut q = common.query();
            q.base_version = base_version;
            q.direction = direction;
            invoke(&project, "impact", q)
        }
        AgentCommand::Resolve { project, common } => invoke(&project, "resolve", common.query()),
        AgentCommand::Quality {
            project,
            common,
            requirements,
        } => {
            let mut q = common.query();
            q.requirements = load(requirements)?;
            invoke(&project, "quality", q)
        }
        AgentCommand::Compile {
            project,
            common,
            goal,
            query,
            base_version,
            direction,
            retrieval_mode,
            retrieval_limit,
            max_chars,
            checks,
            requirements,
            model_use,
        } => {
            let mut q = common.query();
            q.goal = Some(goal);
            q.query = query;
            q.base_version = base_version;
            q.direction = direction;
            q.retrieval_mode = retrieval_mode;
            q.retrieval_limit = retrieval_limit;
            q.max_chars = max_chars;
            q.output_checks = load(checks)?;
            q.requirements = load(requirements)?;
            q.model_use = model_use;
            invoke(&project, "compile", q)
        }
        AgentCommand::Verify {
            project,
            common,
            phase,
            task_id,
            operation,
            result,
            outputs,
            citations,
            output_version,
        } => {
            let mut q = common.query();
            q.phase = phase;
            q.task_id = task_id;
            q.operation = operation;
            q.result = load(result)?;
            q.outputs = load(outputs)?;
            q.citations = load(citations)?;
            q.output_version = output_version;
            invoke(&project, "verify", q)
        }
        AgentCommand::Recover { project, version } => invoke(
            &project,
            "recover",
            Query {
                version,
                ..Default::default()
            },
        ),
        AgentCommand::Trace { project, common } => invoke(&project, "trace", common.query()),
    }
}
fn run(cli: Cli) -> Result<Value> {
    if let Command::Agent(args) = cli.command {
        return agent(args);
    }
    if let Command::AuthInit { out } = cli.command {
        return auth_init(&out);
    }
    if matches!(
        cli.command,
        Command::Start { .. } | Command::Stop | Command::Status | Command::Autostart { .. }
    ) {
        let db = daemon::state_path(&cli.db)?;
        return match cli.command {
            Command::Start { port, no_open } => daemon::start(&db, port, !no_open),
            Command::Stop => daemon::stop(&db),
            Command::Status => daemon::status(&db),
            Command::Autostart {
                enable,
                disable,
                port,
            } => daemon::autostart(
                &db,
                if enable {
                    Some(true)
                } else if disable {
                    Some(false)
                } else {
                    None
                },
                port,
            ),
            _ => unreachable!(),
        };
    }
    if let Command::Serve {
        tokens,
        host,
        port,
        local_ui,
    } = cli.command
    {
        let tokens = read_tokens(&tokens)?;
        let runtime = tokio::runtime::Runtime::new()?;
        runtime.block_on(http::serve_ui(cli.db, tokens, &host, port, local_ui))?;
        return Ok(json!({"stopped":true}));
    }
    let actor = Actor {
        id: cli.actor,
        roles: cli.roles.into_iter().collect(),
        audience: Audience::Operator,
        workspace_projects: BTreeSet::new(),
    };
    actor.validate()?;
    let mut store = Store::open(&cli.db)?;
    match cli.command {
        Command::AuthAgent {
            project,
            tokens,
            read_only,
        } => auth_agent(&store, &project, &tokens, read_only, &actor),
        Command::Project { command } => match command {
            ProjectCommand::Add {
                root,
                id,
                name,
                spec,
                includes,
            } => {
                let mut raw: Value = load(spec)?;
                if raw.is_null() {
                    raw = json!({})
                }
                raw["id"] = json!(id);
                raw["name"] = json!(name);
                if !includes.is_empty() {
                    raw["includes"] = json!(includes)
                }
                store.create(&serde_json::from_value(raw)?, Some(&root), &actor)
            }
            ProjectCommand::Sync { project } => store.sync(&project, &actor),
            ProjectCommand::Status { project } => store.versions(&project, &actor),
            ProjectCommand::Review { project, version } => store.review(&project, &version, &actor),
            ProjectCommand::Publish {
                project,
                version,
                expected_active,
            } => store.publish(&project, &version, expected_active.as_deref(), &actor),
            ProjectCommand::Revoke { project, version } => store.revoke(&project, &version, &actor),
            ProjectCommand::Recover { project, version } => {
                store.recover(&project, &version, &actor)
            }
        },
        _ => Err(fail(422, "Unsupported command")),
    }
}
fn main() {
    match run(Cli::parse()) {
        Ok(value) => {
            println!("{}", value);
            if ["BLOCKED", "NEEDS_REVIEW"].contains(&value["decision"].as_str().unwrap_or_default())
                || value["status"] == "blocked"
            {
                std::process::exit(2)
            }
        }
        Err(e) => {
            eprintln!("{}", json!({"error":e.message,"status":e.status}));
            std::process::exit(1)
        }
    }
}
