use crate::{Result, compute, digest, fail, files, hash, model::*, now, random_id, timestamp};
use base64::{Engine as _, engine::general_purpose::STANDARD};
use fs2::FileExt;
use rusqlite::{Connection, OptionalExtension, params};
use serde_json::{Value, json};
use std::{
    collections::{BTreeMap, BTreeSet},
    fs::{self, File, OpenOptions},
    io::Write,
    os::unix::fs::OpenOptionsExt,
    path::{Path, PathBuf},
    time::Duration,
};

pub struct Store {
    pub db: Connection,
    pub path: PathBuf,
    _lock: File,
}
impl Store {
    pub fn open(path: &Path) -> Result<Self> {
        let path = if path.is_absolute() {
            path.to_owned()
        } else {
            std::env::current_dir()?.join(path)
        };
        let parent = path
            .parent()
            .ok_or_else(|| fail(422, "Invalid database path"))?;
        if !parent.exists() {
            fs::create_dir_all(parent)?;
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(parent, fs::Permissions::from_mode(0o700))?;
        }
        let parent = parent.canonicalize()?;
        use std::os::unix::fs::PermissionsExt;
        if parent.metadata()?.permissions().mode() & 0o022 != 0 {
            return Err(fail(
                403,
                "Database directory must not be writable by group or others",
            ));
        }
        let path = parent.join(
            path.file_name()
                .ok_or_else(|| fail(422, "Invalid database filename"))?,
        );
        let lock = OpenOptions::new()
            .create(true)
            .truncate(false)
            .read(true)
            .write(true)
            .mode(0o600)
            .custom_flags(rustix::fs::OFlags::NOFOLLOW.bits() as i32)
            .open(path.with_extension("lock"))?;
        // ponytail: one cross-process lock per database; use project locks if measured throughput requires it.
        lock.lock_exclusive()?;
        let file = OpenOptions::new()
            .create(true)
            .truncate(false)
            .read(true)
            .write(true)
            .mode(0o600)
            .custom_flags(rustix::fs::OFlags::NOFOLLOW.bits() as i32)
            .open(&path)?;
        let meta = file.metadata()?;
        if !meta.is_file() || meta.permissions().mode() & 0o077 != 0 {
            return Err(fail(403, "Database must be a private regular file (0600)"));
        }
        let db = Connection::open(&path)?;
        db.busy_timeout(Duration::from_secs(10))?;
        let tables: i64 = db.query_row(
            "SELECT count(*) FROM sqlite_master WHERE type='table'",
            [],
            |r| r.get(0),
        )?;
        if tables > 0 {
            let schema:Option<String>=db.query_row("SELECT value FROM native_meta WHERE key='schema'",[],|r|r.get(0)).optional().map_err(|_|fail(409,"Legacy/non-native database: use a separate Rust database; migration is not implicit"))?;
            if schema.as_deref() != Some("filewise-rust-v1") {
                return Err(fail(409, "Unsupported native database schema"));
            }
        }
        db.execute_batch("PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS native_meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
INSERT OR IGNORE INTO native_meta VALUES('schema','filewise-rust-v1');
CREATE TABLE IF NOT EXISTS projects(id TEXT PRIMARY KEY,spec TEXT NOT NULL,root TEXT,active TEXT);
CREATE TABLE IF NOT EXISTS blobs(sha TEXT PRIMARY KEY,body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS sources(id TEXT PRIMARY KEY,project TEXT NOT NULL REFERENCES projects(id),path TEXT NOT NULL,sha TEXT NOT NULL REFERENCES blobs(sha),acl TEXT NOT NULL,revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS versions(id TEXT PRIMARY KEY,project TEXT NOT NULL REFERENCES projects(id),bundle TEXT NOT NULL,status TEXT NOT NULL,available_at TEXT,availability_hash TEXT,approver TEXT,revoked INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS version_project ON versions(project,available_at);
CREATE TABLE IF NOT EXISTS operations(project TEXT,actor TEXT,request TEXT,request_digest TEXT NOT NULL,version TEXT NOT NULL REFERENCES versions(id),PRIMARY KEY(project,actor,request));
CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY,project TEXT NOT NULL,actor TEXT NOT NULL,bundle TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS audit(seq INTEGER PRIMARY KEY,project TEXT NOT NULL,event TEXT NOT NULL,previous TEXT NOT NULL,hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS watches(project TEXT PRIMARY KEY REFERENCES projects(id),bundle TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS watch_jobs(id TEXT PRIMARY KEY,project TEXT NOT NULL REFERENCES projects(id),status TEXT NOT NULL,bundle TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS watch_queue ON watch_jobs(status);")?;
        Ok(Self {
            db,
            path,
            _lock: lock,
        })
    }
    pub fn project(
        &self,
        id: &str,
        actor: &Actor,
    ) -> Result<(ProjectSpec, Option<PathBuf>, Option<String>)> {
        actor.access(id)?;
        let row: Option<(String, Option<String>, Option<String>)> = self
            .db
            .query_row(
                "SELECT spec,root,active FROM projects WHERE id=?",
                [id],
                |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
            )
            .optional()?;
        let (spec, root, active) = row.ok_or_else(|| fail(404, "Project not found"))?;
        let spec: ProjectSpec = serde_json::from_str(&spec)?;
        spec.validate()?;
        Ok((spec, root.map(PathBuf::from), active))
    }
    pub fn projects(&self, actor: &Actor) -> Result<Value> {
        actor.validate()?;
        let mut stmt = self
            .db
            .prepare("SELECT id,spec,active FROM projects ORDER BY id")?;
        let mut result = vec![];
        for row in stmt.query_map([], |r| {
            Ok((
                r.get::<_, String>(0)?,
                r.get::<_, String>(1)?,
                r.get::<_, Option<String>>(2)?,
            ))
        })? {
            let (id, spec, active) = row?;
            if actor.access(&id).is_ok() {
                let spec: ProjectSpec = serde_json::from_str(&spec)?;
                result.push(json!({"id":id,"name":spec.name,"active_release":active}));
            }
        }
        Ok(json!(result))
    }
    pub fn create(
        &mut self,
        spec: &ProjectSpec,
        root: Option<&Path>,
        actor: &Actor,
    ) -> Result<Value> {
        actor.operator(Role::Editor)?;
        spec.validate()?;
        if root.is_none() {
            return Err(fail(
                501,
                "Upload projects are not migrated; register a local directory",
            ));
        }
        let root = root.map(|p| p.canonicalize()).transpose()?;
        if let Some(root) = &root {
            if !root.is_dir() || self.path.starts_with(root) {
                return Err(fail(
                    422,
                    "Choose a directory separate from the private database",
                ));
            }
            files::scan(root, spec)?;
        }
        if self
            .db
            .query_row("SELECT 1 FROM projects WHERE id=?", [&spec.id], |r| {
                r.get::<_, i64>(0)
            })
            .optional()?
            .is_some()
        {
            return Err(fail(409, "Project already exists"));
        }
        let tx = self.db.transaction()?;
        tx.execute(
            "INSERT INTO projects(id,spec,root) VALUES(?,?,?)",
            params![
                spec.id,
                serde_json::to_string(spec)?,
                root.as_ref().map(|p| p.to_string_lossy().into_owned())
            ],
        )?;
        audit(
            &tx,
            &spec.id,
            actor,
            "project.created",
            json!({"spec":spec}),
        )?;
        tx.commit()?;
        Ok(json!({"id":spec.id,"name":spec.name,"root":root}))
    }
    fn raw_version(&self, project: &str, id: &str, actor: &Actor) -> Result<Version> {
        actor.access(project)?;
        let row=self.db.query_row("SELECT bundle,status,available_at,availability_hash,approver,revoked FROM versions WHERE project=? AND id=?",params![project,id],|r|Ok((r.get::<_,String>(0)?,r.get::<_,String>(1)?,r.get::<_,Option<String>>(2)?,r.get::<_,Option<String>>(3)?,r.get::<_,Option<String>>(4)?,r.get::<_,bool>(5)?))).optional()?.ok_or_else(||fail(404,"Version not found"))?;
        let snapshot: Snapshot = serde_json::from_str(&row.0)?;
        if snapshot.project_id != project
            || snapshot.schema != "filewise-rust/snapshot-v1"
            || digest(&snapshot)? != id
        {
            return Err(fail(409, "Snapshot integrity failed"));
        }
        if row.5 {
            return Err(fail(409, "Version is revoked"));
        }
        if let Some(time) = &row.2 {
            if row.1 != "completed"
                || row.3.as_deref()
                    != Some(&digest(
                        &json!({"version":id,"project_id":project,"available_at":time}),
                    )?)
            {
                return Err(fail(409, "Availability integrity failed"));
            }
        }
        for (path, file) in &snapshot.files {
            files::relative(path)?;
            if path != &file.path
                || digest(&json!({"project":project,"path":path,"sha256":file.sha256}))?
                    != file.source_id
            {
                return Err(fail(409, "File/source binding failed"));
            }
            let (_, sha, body) = self.source(&file.source_id, project, actor)?;
            if sha != file.sha256 || body.len() != file.size {
                return Err(fail(409, "File integrity failed"));
            }
            if let Some(meta) = &file.metadata {
                for e in &meta.evidence {
                    self.check_evidence(project, e, actor)?;
                }
            }
        }
        Ok(Version {
            id: id.into(),
            snapshot,
            status: row.1,
            available_at: row.2,
            approver: row.4,
            revoked: false,
        })
    }
    pub fn version(
        &self,
        project: &str,
        selector: &str,
        as_of: Option<&str>,
        actor: &Actor,
    ) -> Result<Version> {
        self.project(project, actor)?;
        let cut = as_of.map(timestamp).transpose()?;
        if cut.as_ref().is_some_and(|t| t > &now()) {
            return Err(fail(422, "as_of cannot be in the future"));
        }
        let id=match selector {
            "latest"=>self.db.query_row("SELECT id FROM versions WHERE project=? AND status='completed' AND (? IS NULL OR available_at<=?) ORDER BY available_at DESC,rowid DESC LIMIT 1",params![project,cut,cut],|r|r.get::<_,String>(0)).optional()?.ok_or_else(||fail(404,"No completed snapshot at this time"))?,
            "published"=>self.project(project,actor)?.2.ok_or_else(||fail(404,"No published version"))?,
            "HEAD"=>return Err(fail(501,"Named commits are not yet migrated to Rust")),
            value=>{crate::exact_version(value)?;value.into()},
        };
        let v = self.raw_version(project, &id, actor)?;
        if cut
            .as_ref()
            .is_some_and(|cut| v.available_at.as_ref().is_none_or(|t| t > cut))
        {
            return Err(fail(409, "Version was not available at as_of"));
        }
        self.lineage(
            project,
            v.snapshot.files.values().cloned().collect(),
            cut.as_deref(),
            actor,
        )?;
        Ok(v)
    }
    pub fn lineage(
        &self,
        project: &str,
        files: Vec<FileRecord>,
        cut: Option<&str>,
        actor: &Actor,
    ) -> Result<Vec<Value>> {
        let mut queue: Vec<Value> = files.iter().flat_map(|f| f.lineage.clone()).collect();
        let mut seen = BTreeSet::new();
        let mut result = vec![];
        let mut versions = BTreeMap::new();
        while let Some(item) = queue.pop() {
            let version = item["version"]
                .as_str()
                .ok_or_else(|| fail(409, "Invalid lineage"))?;
            let path = item["path"]
                .as_str()
                .ok_or_else(|| fail(409, "Invalid lineage"))?;
            let role = item["role"]
                .as_str()
                .ok_or_else(|| fail(409, "Invalid lineage role"))?;
            if !seen.insert((version.to_owned(), path.to_owned(), role.to_owned())) {
                continue;
            }
            if seen.len() > 1000 {
                return Err(fail(413, "Lineage exceeds 1,000 inputs"));
            }
            if !versions.contains_key(version) {
                versions.insert(
                    version.to_owned(),
                    self.raw_version(project, version, actor)?,
                );
            }
            let input = &versions[version];
            let file = input
                .snapshot
                .files
                .get(path)
                .ok_or_else(|| fail(409, "Input path missing"))?;
            if input.available_at.is_none()
                || item["available_at"] != json!(input.available_at)
                || item["sha256"] != file.sha256
                || item["source_id"] != file.source_id
            {
                return Err(fail(409, "Input binding failed"));
            }
            if cut.is_some_and(|cut| {
                input
                    .available_at
                    .as_ref()
                    .is_some_and(|t| t.as_str() > cut)
            }) {
                return Err(fail(409, "Input is newer than the historical cutoff"));
            }
            let mut record = item.clone();
            record["metadata_current"] = json!(file.metadata_current);
            record["processing"] = json!(
                file.metadata
                    .as_ref()
                    .map(|m| m.processing)
                    .unwrap_or_default()
            );
            record["quality"] = json!(file.data_quality);
            record["inputs_declared"] = json!(
                file.metadata
                    .as_ref()
                    .is_some_and(|m| !m.lineage.is_empty())
            );
            record["valid_from"] =
                json!(file.metadata.as_ref().and_then(|m| m.valid_from.as_ref()));
            record["valid_until"] =
                json!(file.metadata.as_ref().and_then(|m| m.valid_until.as_ref()));
            result.push(record);
            queue.extend(file.lineage.clone());
        }
        Ok(result)
    }
    pub fn source(
        &self,
        id: &str,
        project: &str,
        actor: &Actor,
    ) -> Result<(String, String, Vec<u8>)> {
        actor.access(project)?;
        let (path,sha,acl,revoked,bytes):(String,String,String,bool,Vec<u8>)=self.db.query_row("SELECT s.path,s.sha,s.acl,s.revoked,b.body FROM sources s JOIN blobs b ON s.sha=b.sha WHERE s.id=? AND s.project=?",params![id,project],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?,r.get(4)?))).optional()?.ok_or_else(||fail(404,"Source not found"))?;
        let acl: BTreeSet<Role> = serde_json::from_str(&acl)?;
        if revoked || acl.is_disjoint(&actor.roles) {
            return Err(fail(403, "Source revoked or access denied"));
        }
        if hash(&bytes) != sha {
            return Err(fail(409, "Source integrity failed"));
        }
        Ok((path, sha, bytes))
    }
    pub fn check_evidence(&self, project: &str, e: &Evidence, actor: &Actor) -> Result<()> {
        let (path, sha, bytes) = self.source(&e.source_id, project, actor)?;
        let valid = if e.locator == "file:sha256" {
            e.quote == sha
        } else {
            files::fragments(&path, &bytes)?
                .iter()
                .any(|f| f.locator == e.locator && f.text.contains(&e.quote))
        };
        if e.quote.is_empty() || !valid {
            return Err(fail(409, "Evidence anchor does not match source"));
        }
        Ok(())
    }
    pub fn policy(
        &mut self,
        project: &str,
        source: &str,
        acl: BTreeSet<Role>,
        revoked: bool,
        actor: &Actor,
    ) -> Result<Value> {
        actor.operator(Role::Reviewer)?;
        self.project(project, actor)?;
        if acl.is_empty() {
            return Err(fail(422, "ACL cannot be empty"));
        }
        let tx = self.db.transaction()?;
        if tx.execute(
            "UPDATE sources SET acl=?,revoked=? WHERE project=? AND id=?",
            params![serde_json::to_string(&acl)?, revoked, project, source],
        )? != 1
        {
            return Err(fail(404, "Source not found"));
        }
        audit(
            &tx,
            project,
            actor,
            "source.policy",
            json!({"source_id":source,"acl":acl,"revoked":revoked}),
        )?;
        tx.commit()?;
        Ok(json!({"source_id":source,"acl":acl,"revoked":revoked}))
    }
    pub(crate) fn latest_optional(&self, project: &str, actor: &Actor) -> Result<Option<Version>> {
        let exists=self.db.query_row("SELECT id FROM versions WHERE project=? AND status='completed' ORDER BY available_at DESC,rowid DESC LIMIT 1",[project],|r|r.get::<_,String>(0)).optional()?;
        exists
            .map(|id| self.version(project, &id, None, actor))
            .transpose()
    }
    fn recovered(&self, project: &str) -> Result<()> {
        if self.db.query_row("SELECT 1 FROM versions WHERE project=? AND status IN ('applying','recovery_required') LIMIT 1",[project],|r|r.get::<_,i64>(0)).optional()?.is_some() {return Err(fail(409,"RECOVERY_REQUIRED: recover interrupted writes first"))}
        Ok(())
    }
    fn original_matches(
        &self,
        root: Option<&Path>,
        spec: &ProjectSpec,
        version: &Version,
    ) -> Result<()> {
        if let Some(root) = root {
            let actual: BTreeMap<_, _> = files::scan(root, spec)?
                .into_iter()
                .map(|(p, b)| (p, hash(&b)))
                .collect();
            let expected: BTreeMap<_, _> = version
                .snapshot
                .files
                .iter()
                .map(|(p, f)| (p.clone(), f.sha256.clone()))
                .collect();
            if actual != expected {
                return Err(fail(
                    409,
                    "STALE: originals changed; sync or inspect before saving",
                ));
            }
        }
        Ok(())
    }
    fn prepare(
        &mut self,
        spec: &ProjectSpec,
        before: Option<&Version>,
        bodies: &BTreeMap<String, Vec<u8>>,
        metas: &BTreeMap<String, Metadata>,
        message: &str,
        actor: &Actor,
    ) -> Result<(String, Snapshot)> {
        if bodies.len() > 1000 || bodies.values().map(Vec::len).sum::<usize>() > files::MAX_TOTAL {
            return Err(fail(413, "Project size limit exceeded"));
        }
        let mut manifest = BTreeMap::new();
        for (path, body) in bodies {
            if body.len() > files::MAX_FILE
                || files::excluded(path, spec)
                || !files::included(path, spec)
            {
                return Err(fail(422, "File exceeds the registered project boundary"));
            }
            files::relative(path)?;
            files::fragments(path, body)?;
            let sha = hash(body);
            let source_id = digest(&json!({"project":spec.id,"path":path,"sha256":sha}))?;
            let old = before.and_then(|b| b.snapshot.files.get(path));
            let metadata = metas
                .get(path)
                .cloned()
                .or_else(|| old.and_then(|f| f.metadata.clone()));
            let meta_sha = if metas.contains_key(path) {
                Some(sha.clone())
            } else {
                old.and_then(|f| f.metadata_sha256.clone())
            };
            let declared_by = if metas.contains_key(path) {
                Some(actor.id.clone())
            } else {
                old.and_then(|f| f.declared_by.clone())
            };
            let current = metadata.is_none() || meta_sha.as_deref() == Some(&sha);
            let data = spec
                .data_contracts
                .get(path)
                .cloned()
                .or_else(|| metadata.as_ref().and_then(|m| m.data.clone()));
            let mut refs: Vec<Value> = metadata
                .as_ref()
                .map(|m| {
                    m.lineage
                        .iter()
                        .map(|v| serde_json::to_value(v).expect("serializable input"))
                        .collect()
                })
                .unwrap_or_default();
            let quality = if let Some(contract) = &data {
                let has_delta = contract
                    .checks
                    .iter()
                    .any(|c| c.op == QualityOp::DistinctCountChange);
                let mut baseline = if has_delta && old.is_some() {
                    before.map(|v| v.id.clone())
                } else {
                    None
                };
                if has_delta
                    && old.is_some_and(|o| {
                        o.sha256 == sha
                            && serde_json::to_value(&o.data_contract).ok()
                                == serde_json::to_value(&data).ok()
                    })
                {
                    baseline = old
                        .and_then(|o| o.data_quality.as_ref())
                        .and_then(|q| q["baseline"].as_str())
                        .map(str::to_owned);
                }
                let previous = if let Some(base) = &baseline {
                    let prior = self.version(&spec.id, base, None, actor)?;
                    if let Some(file) = prior.snapshot.files.get(path) {
                        refs.push(json!({"version":base,"path":path,"role":"baseline"}));
                        Some(self.source(&file.source_id, &spec.id, actor)?.2)
                    } else {
                        None
                    }
                } else {
                    None
                };
                let mut report = compute::quality(
                    path,
                    body,
                    contract,
                    previous.as_deref(),
                    baseline.as_deref(),
                );
                if metadata
                    .as_ref()
                    .and_then(|m| m.data.as_ref())
                    .is_some_and(|m| {
                        !current
                            || (spec.data_contracts.contains_key(path)
                                && serde_json::to_value(m).ok()
                                    != serde_json::to_value(contract).ok())
                    })
                {
                    report["decision"] = json!("BLOCKED");
                    report["issues"]
                        .as_array_mut()
                        .ok_or_else(|| fail(500, "Invalid quality report"))?
                        .push(json!({"reason":"stale_or_mismatched_data_contract"}));
                }
                Some(report)
            } else {
                None
            };
            let mut lineage = vec![];
            for mut item in refs {
                let input = self.version(
                    &spec.id,
                    item["version"]
                        .as_str()
                        .ok_or_else(|| fail(422, "Invalid input version"))?,
                    None,
                    actor,
                )?;
                let input_file = input
                    .snapshot
                    .files
                    .get(
                        item["path"]
                            .as_str()
                            .ok_or_else(|| fail(422, "Invalid input path"))?,
                    )
                    .ok_or_else(|| fail(422, "Input path missing"))?;
                if input.available_at.is_none() {
                    return Err(fail(409, "Input must be a completed snapshot"));
                }
                item["sha256"] = json!(input_file.sha256);
                item["source_id"] = json!(input_file.source_id);
                item["available_at"] = json!(input.available_at);
                lineage.push(item);
            }
            if let Some(meta) = &metadata {
                for evidence in &meta.evidence {
                    self.check_evidence(&spec.id, evidence, actor)?;
                }
            }
            let mut deps: BTreeSet<String> = spec
                .dependencies
                .get(path)
                .cloned()
                .unwrap_or_default()
                .into_iter()
                .collect();
            if let Some(m) = &metadata {
                deps.extend(m.depends_on.clone());
            }
            let tx = self.db.transaction()?;
            tx.execute(
                "INSERT OR IGNORE INTO blobs VALUES(?,?)",
                params![sha, body],
            )?;
            tx.execute(
                "INSERT OR IGNORE INTO sources(id,project,path,sha,acl) VALUES(?,?,?,?,?)",
                params![
                    source_id,
                    spec.id,
                    path,
                    sha,
                    serde_json::to_string(&vec![
                        Role::Reader,
                        Role::Editor,
                        Role::Reviewer,
                        Role::Publisher
                    ])?
                ],
            )?;
            tx.commit()?;
            self.source(&source_id, &spec.id, actor)?;
            manifest.insert(
                path.clone(),
                FileRecord {
                    path: path.clone(),
                    sha256: sha,
                    source_id,
                    size: body.len(),
                    metadata,
                    metadata_current: current,
                    declared_by,
                    metadata_sha256: meta_sha,
                    depends_on: deps.into_iter().collect(),
                    data_contract: data,
                    data_quality: quality,
                    lineage,
                },
            );
        }
        let mut verification = compute::build_checks(spec, &manifest, bodies);
        let inputs = self.lineage(&spec.id, manifest.values().cloned().collect(), None, actor)?;
        let (issues, warnings) = compute::input_guard(&inputs, None)?;
        if !issues.is_empty() {
            verification["decision"] = json!("BLOCKED");
        } else if !warnings.is_empty() && verification["decision"] == "PASS" {
            verification["decision"] = json!("NEEDS_REVIEW");
        }
        verification["issues"]
            .as_array_mut()
            .ok_or_else(|| fail(500, "Invalid verification report"))?
            .extend(issues);
        verification["warnings"] = json!(warnings);
        let snapshot = Snapshot {
            schema: "filewise-rust/snapshot-v1".into(),
            project_id: spec.id.clone(),
            base_version: before.map(|v| v.id.clone()),
            author: actor.id.clone(),
            created_at: now(),
            message: message.into(),
            files: manifest,
            verification,
        };
        let id = digest(&snapshot)?;
        Ok((id, snapshot))
    }
    pub fn sync(&mut self, project: &str, actor: &Actor) -> Result<Value> {
        actor.require(Role::Editor)?;
        let (spec, root, _) = self.project(project, actor)?;
        self.recovered(project)?;
        let root = root.ok_or_else(|| fail(422, "Sync requires a registered local directory"))?;
        let before = self.latest_optional(project, actor)?;
        let bodies = files::scan(&root, &spec)?;
        let (id, snapshot) = self.prepare(
            &spec,
            before.as_ref(),
            &bodies,
            &BTreeMap::new(),
            "Capture working files",
            actor,
        )?;
        let tx = self.db.transaction()?;
        tx.execute(
            "INSERT INTO versions(id,project,bundle,status) VALUES(?,?,?,'pending')",
            params![id, project, serde_json::to_string(&snapshot)?],
        )?;
        complete(&tx, project, &id, actor)?;
        tx.commit()?;
        self.list(project, &Query::default(), actor)
    }
    pub fn write(&mut self, project: &str, q: &WriteQuery, actor: &Actor) -> Result<Value> {
        actor.require(Role::Editor)?;
        q.validate()?;
        let (spec, root, _) = self.project(project, actor)?;
        let request_digest = digest(q)?;
        let old=self.db.query_row("SELECT request_digest,version FROM operations WHERE project=? AND actor=? AND request=?",params![project,actor.id,q.request_id],|r|Ok((r.get::<_,String>(0)?,r.get::<_,String>(1)?))).optional()?;
        if let Some((old_digest, id)) = old {
            if old_digest != request_digest {
                return Err(fail(
                    409,
                    "Idempotency key already used with different content",
                ));
            }
            let v = self.version(project, &id, None, actor)?;
            if v.status == "completed" || v.status == "candidate" {
                return self.write_result(project, &id, actor);
            }
            self.apply_working(project, &id, actor)?;
            return self.write_result(project, &id, actor);
        }
        self.recovered(project)?;
        let before = self.version(project, "latest", None, actor)?;
        if before.id != q.base_version {
            return Err(fail(409, "STALE: latest version changed"));
        }
        self.original_matches(root.as_deref(), &spec, &before)?;
        let mut bodies = BTreeMap::new();
        let mut metas = BTreeMap::new();
        for (p, f) in &before.snapshot.files {
            bodies.insert(p.clone(), self.source(&f.source_id, project, actor)?.2);
        }
        for (path, c) in &q.changes {
            if files::excluded(path, &spec) || !files::included(path, &spec) {
                return Err(fail(403, "Path is outside the managed boundary"));
            }
            if !before.snapshot.files.contains_key(path) {
                if let Some(root) = &root {
                    if files::read(root, path)?.is_some() {
                        return Err(fail(
                            409,
                            "New path collides with an existing file (including case/Unicode aliases)",
                        ));
                    }
                }
            }
            if c.delete {
                if bodies.remove(path).is_none() {
                    return Err(fail(404, "Cannot delete missing file"));
                }
            } else {
                if let Some(text) = &c.text {
                    bodies.insert(path.clone(), text.as_bytes().to_vec());
                }
                if let Some(encoded) = &c.base64 {
                    let bytes = STANDARD
                        .decode(encoded)
                        .map_err(|_| fail(422, "Invalid Base64"))?;
                    bodies.insert(path.clone(), bytes);
                }
                if !bodies.contains_key(path) {
                    return Err(fail(404, "Metadata-only update requires an existing file"));
                }
                if let Some(meta) = &c.meta {
                    metas.insert(path.clone(), meta.clone());
                }
            }
        }
        let (id, snapshot) =
            self.prepare(&spec, Some(&before), &bodies, &metas, &q.message, actor)?;
        let candidate =
            q.dry_run || (q.require_pass && snapshot.verification["decision"] != "PASS");
        let tx = self.db.transaction()?;
        tx.execute(
            "INSERT INTO versions(id,project,bundle,status) VALUES(?,?,?,?)",
            params![
                id,
                project,
                serde_json::to_string(&snapshot)?,
                if candidate { "candidate" } else { "pending" }
            ],
        )?;
        tx.execute(
            "INSERT INTO operations VALUES(?,?,?,?,?)",
            params![project, actor.id, q.request_id, request_digest, id],
        )?;
        audit(
            &tx,
            project,
            actor,
            "write.intent",
            json!({"version":id,"request_id":q.request_id,"request_digest":request_digest,"model":q.model,"task":q.task,"tool":q.tool}),
        )?;
        tx.commit()?;
        if !candidate {
            self.apply_working(project, &id, actor)?;
        }
        self.write_result(project, &id, actor)
    }
    fn write_result(&self, project: &str, id: &str, actor: &Actor) -> Result<Value> {
        let v = self.version(project, id, None, actor)?;
        Ok(
            json!({"version":id,"saved":v.status=="completed","published":false,"status":if v.status=="candidate" && v.snapshot.verification["decision"]!="PASS" {"blocked"}else{&v.status},"verification":v.snapshot.verification,"availability":v.available_at,"message":v.snapshot.message}),
        )
    }
    fn apply_working(&mut self, project: &str, id: &str, actor: &Actor) -> Result<()> {
        self.recovered(project)?;
        let (spec, root, _) = self.project(project, actor)?;
        let after = self.version(project, id, None, actor)?;
        if after.snapshot.author != actor.id || after.status != "pending" {
            return Err(fail(409, "This write cannot be resumed"));
        }
        let before = self.version(
            project,
            after
                .snapshot
                .base_version
                .as_deref()
                .ok_or_else(|| fail(409, "Missing write baseline"))?,
            None,
            actor,
        )?;
        if self.version(project, "latest", None, actor)?.id != before.id {
            return Err(fail(409, "STALE: write baseline changed"));
        }
        self.original_matches(root.as_deref(), &spec, &before)?;
        self.db
            .execute("UPDATE versions SET status='applying' WHERE id=?", [id])?;
        let mut applied = BTreeSet::new();
        let moves = file_moves(&before, &after);
        let moved_paths: BTreeSet<_> = moves.iter().flat_map(|(a, b)| [a, b]).collect();
        let result = (|| -> Result<()> {
            if let Some(root) = &root {
                for (from, to) in &moves {
                    if let Err(e) =
                        files::move_file(root, from, to, &before.snapshot.files[from].sha256)
                    {
                        if e.write_applied {
                            applied.insert(from.clone());
                            applied.insert(to.clone());
                        }
                        return Err(e);
                    }
                    applied.insert(from.clone());
                    applied.insert(to.clone());
                }
                for path in before
                    .snapshot
                    .files
                    .keys()
                    .chain(after.snapshot.files.keys())
                    .collect::<BTreeSet<_>>()
                {
                    if moved_paths.contains(path) {
                        continue;
                    }
                    let old = before.snapshot.files.get(path);
                    let new = after.snapshot.files.get(path);
                    if old.map(|f| &f.sha256) == new.map(|f| &f.sha256) {
                        continue;
                    }
                    let bytes = new
                        .map(|f| self.source(&f.source_id, project, actor).map(|r| r.2))
                        .transpose()?;
                    if let Err(error) =
                        files::replace(root, path, bytes.as_deref(), old.map(|f| f.sha256.as_str()))
                    {
                        if error.write_applied {
                            applied.insert(path.clone());
                        }
                        return Err(error);
                    }
                    applied.insert(path.clone());
                }
                self.original_matches(Some(root), &spec, &after)?;
            }
            self.version(project, id, None, actor)?;
            let tx = self.db.transaction()?;
            complete(&tx, project, id, actor)?;
            tx.commit()?;
            Ok(())
        })();
        if let Err(error) = result {
            let completed: bool = self.db.query_row(
                "SELECT available_at IS NOT NULL FROM versions WHERE id=?",
                [id],
                |r| r.get(0),
            )?;
            if completed {
                return Err(error);
            }
            let recovery =
                self.restore_originals(root.as_deref(), &before, &after, Some(&applied), actor);
            self.db.execute(
                "UPDATE versions SET status=? WHERE id=?",
                params![
                    if recovery.is_ok() {
                        "pending"
                    } else {
                        "recovery_required"
                    },
                    id
                ],
            )?;
            if recovery.is_err() {
                return Err(fail(
                    409,
                    "RECOVERY_REQUIRED: external conflict or rollback failure",
                ));
            }
            return Err(error);
        }
        Ok(())
    }
    fn restore_originals(
        &self,
        root: Option<&Path>,
        before: &Version,
        after: &Version,
        applied: Option<&BTreeSet<String>>,
        actor: &Actor,
    ) -> Result<()> {
        if let Some(root) = root {
            let moves = file_moves(before, after);
            let moved_paths: BTreeSet<_> = moves.iter().flat_map(|(a, b)| [a, b]).collect();
            for (from, to) in &moves {
                if applied.is_some_and(|paths| !paths.contains(from) && !paths.contains(to)) {
                    continue;
                }
                let expected = &before.snapshot.files[from].sha256;
                let a = files::read(root, from)?.map(|b| hash(&b));
                let b = files::read(root, to)?.map(|b| hash(&b));
                match (a.as_deref(), b.as_deref()) {
                    (Some(a), None) if a == expected => {}
                    (Some(a), Some(b)) if a == expected && b == expected => {
                        files::replace(root, to, None, Some(expected))?
                    }
                    (None, Some(b)) if b == expected => files::move_file(root, to, from, expected)?,
                    _ => return Err(fail(409, "Recovery conflict; preserve external edits")),
                }
            }
            for path in before
                .snapshot
                .files
                .keys()
                .chain(after.snapshot.files.keys())
                .collect::<BTreeSet<_>>()
            {
                if moved_paths.contains(path) || applied.is_some_and(|paths| !paths.contains(path))
                {
                    continue;
                }
                // ponytail: crash recovery uses byte CAS; identical external writes need exclusive filesystem ownership to distinguish.
                let a = before.snapshot.files.get(path);
                let b = after.snapshot.files.get(path);
                if a.map(|f| &f.sha256) == b.map(|f| &f.sha256) {
                    continue;
                }
                let actual = files::read(root, path)?.map(|bytes| hash(&bytes));
                if actual.as_deref() == a.map(|f| f.sha256.as_str()) {
                    continue;
                }
                if actual.as_deref() != b.map(|f| f.sha256.as_str()) {
                    return Err(fail(409, "Recovery conflict; preserve external edits"));
                }
                let bytes = a
                    .map(|f| {
                        self.source(&f.source_id, &before.snapshot.project_id, actor)
                            .map(|r| r.2)
                    })
                    .transpose()?;
                files::replace(root, path, bytes.as_deref(), actual.as_deref())?;
            }
        }
        Ok(())
    }
    pub fn recover(&mut self, project: &str, id: &str, actor: &Actor) -> Result<Value> {
        actor.require(Role::Editor)?;
        let (_, root, _) = self.project(project, actor)?;
        let after = self.version(project, id, None, actor)?;
        if after.snapshot.author != actor.id
            || !["applying", "recovery_required"].contains(&after.status.as_str())
        {
            return Err(fail(409, "Recovery requires your own interrupted write"));
        }
        let before = self.version(
            project,
            after
                .snapshot
                .base_version
                .as_deref()
                .ok_or_else(|| fail(409, "Missing baseline"))?,
            None,
            actor,
        )?;
        self.restore_originals(root.as_deref(), &before, &after, None, actor)?;
        let tx = self.db.transaction()?;
        tx.execute("UPDATE versions SET status='pending' WHERE id=?", [id])?;
        audit(
            &tx,
            project,
            actor,
            "write.recovered",
            json!({"version":id}),
        )?;
        tx.commit()?;
        Ok(
            json!({"status":"recovered","version":id,"retry":"Use the original request ID and identical payload"}),
        )
    }
    pub fn temporal(&self, v: &Version, as_of: Option<&str>) -> Value {
        json!({"as_of":as_of,"availability":v.available_at.as_ref().map(|t|json!({"available_at":t,"version":v.id,"basis":"filewise_completed_snapshot"})),"scope":"completed_project_snapshot; not row-level point-in-time joins"})
    }
    pub fn list(&self, project: &str, q: &Query, actor: &Actor) -> Result<Value> {
        q.validate()?;
        let v = self.version(project, &q.version, q.as_of.as_deref(), actor)?;
        Ok(
            json!({"project_id":project,"version":v.id,"files":v.snapshot.files.values().map(public_file).collect::<Vec<_>>(),"verification":v.snapshot.verification,"temporal":self.temporal(&v,q.as_of.as_deref())}),
        )
    }
    pub fn read(&mut self, project: &str, q: &Query, actor: &Actor) -> Result<Value> {
        q.validate()?;
        let v = self.version(project, &q.version, q.as_of.as_deref(), actor)?;
        let path = q
            .path
            .as_deref()
            .ok_or_else(|| fail(422, "Read requires path"))?;
        let f = v
            .snapshot
            .files
            .get(path)
            .ok_or_else(|| fail(404, "File outside selected version"))?;
        let (_, _, bytes) = self.source(&f.source_id, project, actor)?;
        let mut result = public_file(f);
        result["version"] = json!(v.id);
        result["text"] = json!(
            std::str::from_utf8(&bytes)
                .ok()
                .filter(|s| !s.contains('\0'))
        );
        result["fragments"] = json!(files::fragments(path, &bytes)?);
        result["temporal"] = self.temporal(&v, q.as_of.as_deref());
        if q.include_bytes {
            result["base64"] = json!(STANDARD.encode(bytes));
        }
        result["receipt"] = self.receipt(
            project,
            actor,
            "file.read",
            json!({"version":v.id,"path":path,"sha256":f.sha256,"as_of":q.as_of}),
        )?;
        Ok(result)
    }
    pub fn versions(&self, project: &str, actor: &Actor) -> Result<Value> {
        let (_, _, active) = self.project(project, actor)?;
        let mut stmt=self.db.prepare("SELECT id,status,available_at,approver,revoked FROM versions WHERE project=? ORDER BY rowid DESC")?;
        let rows=stmt.query_map([project],|r|Ok(json!({"version":r.get::<_,String>(0)?,"status":r.get::<_,String>(1)?,"available_at":r.get::<_,Option<String>>(2)?,"approver":r.get::<_,Option<String>>(3)?,"revoked":r.get::<_,bool>(4)?})))?.collect::<std::result::Result<Vec<_>,_>>()?;
        Ok(json!({"project_id":project,"versions":rows,"active_release":active}))
    }
    pub fn review(&mut self, project: &str, id: &str, actor: &Actor) -> Result<Value> {
        actor.operator(Role::Reviewer)?;
        let v = self.version(project, id, None, actor)?;
        if v.snapshot.author == actor.id
            || v.status != "completed"
            || v.snapshot.verification["decision"] != "PASS"
        {
            return Err(fail(
                409,
                "Review requires an independent identity and a completed passing snapshot",
            ));
        }
        self.review_guard(project, &v, actor)?;
        let tx = self.db.transaction()?;
        tx.execute(
            "UPDATE versions SET approver=? WHERE id=?",
            params![actor.id, id],
        )?;
        audit(
            &tx,
            project,
            actor,
            "version.approved",
            json!({"version":id}),
        )?;
        tx.commit()?;
        Ok(json!({"version":id,"approver":actor.id}))
    }
    pub fn publish(
        &mut self,
        project: &str,
        id: &str,
        expected: Option<&str>,
        actor: &Actor,
    ) -> Result<Value> {
        actor.operator(Role::Publisher)?;
        let (spec, root, active) = self.project(project, actor)?;
        let v = self.version(project, id, None, actor)?;
        if v.approver.is_none()
            || v.status != "completed"
            || v.snapshot.verification["decision"] != "PASS"
        {
            return Err(fail(
                409,
                "Publication requires passing checks and independent approval",
            ));
        }
        if active.as_deref() != expected {
            return Err(fail(409, "Active release changed"));
        }
        self.original_matches(root.as_deref(), &spec, &v)?;
        self.review_guard(project, &v, actor)?;
        let tx = self.db.transaction()?;
        tx.execute(
            "UPDATE projects SET active=? WHERE id=?",
            params![id, project],
        )?;
        audit(
            &tx,
            project,
            actor,
            "version.published",
            json!({"version":id,"expected_active":expected}),
        )?;
        tx.commit()?;
        Ok(json!({"project_id":project,"active_release":id}))
    }
    fn review_guard(&self, project: &str, v: &Version, actor: &Actor) -> Result<()> {
        let paths = v.snapshot.files.keys().cloned().collect::<Vec<_>>();
        let guard = compute::guard(self, project, v, &paths, &Query::default(), actor, false)?;
        if guard["decision"] != "PASS" {
            return Err(fail(
                409,
                "Current declarations or input checks require repair/review",
            ));
        }
        Ok(())
    }
    pub fn revoke(&mut self, project: &str, id: &str, actor: &Actor) -> Result<Value> {
        actor.operator(Role::Publisher)?;
        self.project(project, actor)?;
        let tx = self.db.transaction()?;
        if tx.execute(
            "UPDATE versions SET revoked=1 WHERE id=? AND project=?",
            params![id, project],
        )? != 1
        {
            return Err(fail(404, "Version not found"));
        }
        tx.execute(
            "UPDATE projects SET active=NULL WHERE id=? AND active=?",
            params![project, id],
        )?;
        audit(
            &tx,
            project,
            actor,
            "version.revoked",
            json!({"version":id}),
        )?;
        tx.commit()?;
        Ok(json!({"version":id,"revoked":true}))
    }
    pub fn receipt(
        &mut self,
        project: &str,
        actor: &Actor,
        action: &str,
        data: Value,
    ) -> Result<Value> {
        let tx = self.db.transaction()?;
        let value = audit(&tx, project, actor, action, data)?;
        tx.commit()?;
        Ok(value)
    }
    pub fn trace(&self, project: &str, as_of: Option<&str>, actor: &Actor) -> Result<Value> {
        self.project(project, actor)?;
        let cutoff = as_of.map(timestamp).transpose()?;
        if cutoff.as_ref().is_some_and(|t| t > &now()) {
            return Err(fail(422, "as_of cannot be in the future"));
        }
        let mut stmt = self
            .db
            .prepare("SELECT project,event,previous,hash FROM audit ORDER BY seq")?;
        let mut previous = String::new();
        let mut records = vec![];
        let mut readable_versions = BTreeMap::new();
        for row in stmt.query_map([], |r| {
            Ok((
                r.get::<_, String>(0)?,
                r.get::<_, String>(1)?,
                r.get::<_, String>(2)?,
                r.get::<_, String>(3)?,
            ))
        })? {
            let (scope, text, prev, signature) = row?;
            let event: Value = serde_json::from_str(&text)?;
            if prev != previous
                || event["project_id"] != scope
                || digest(&json!({"previous":prev,"event":event}))? != signature
            {
                return Err(fail(409, "Audit chain integrity failed"));
            }
            previous = signature;
            if scope == project
                && cutoff.as_ref().is_none_or(|cut| {
                    event["recorded_at"]
                        .as_str()
                        .is_some_and(|t| t <= cut.as_str())
                })
            {
                let visible = if let Some(version) = event["data"]["version"].as_str() {
                    *readable_versions
                        .entry(version.to_owned())
                        .or_insert_with(|| self.version(project, version, as_of, actor).is_ok())
                } else if let Some(source) = event["data"]["source_id"].as_str() {
                    self.source(source, project, actor).is_ok()
                } else {
                    false
                };
                if visible {
                    records.push(event)
                }
            }
        }
        Ok(
            json!({"project_id":project,"events":records.into_iter().rev().take(200).collect::<Vec<_>>(),"chain_verified":true}),
        )
    }
}
fn file_moves(before: &Version, after: &Version) -> BTreeMap<String, String> {
    let mut moves = BTreeMap::new();
    let mut used = BTreeSet::new();
    // ponytail: bounded O(n²) pairing (1,000 files); group by hash if the project ceiling grows.
    for (to, new) in &after.snapshot.files {
        if before.snapshot.files.contains_key(to) {
            continue;
        }
        let candidates: Vec<_> = before
            .snapshot
            .files
            .iter()
            .filter(|(from, old)| {
                !after.snapshot.files.contains_key(*from)
                    && !used.contains(*from)
                    && old.sha256 == new.sha256
            })
            .map(|(path, _)| path)
            .collect();
        let declared = new.lineage.iter().rev().find_map(|input| {
            candidates.iter().copied().find(|p| {
                input["role"] == "data"
                    && input["path"] == p.as_str()
                    && input["sha256"] == new.sha256
            })
        });
        let selected = declared.or_else(|| {
            if candidates.len() == 1 {
                Some(candidates[0])
            } else {
                None
            }
        });
        if let Some(from) = selected {
            used.insert(from.clone());
            moves.insert(from.clone(), to.clone());
        }
    }
    moves
}
fn public_file(file: &FileRecord) -> Value {
    let mut value = serde_json::to_value(file).expect("serializable file record");
    if let Some(meta) = value["metadata"].as_object_mut() {
        meta.insert("declared_by".into(), json!(file.declared_by));
        meta.insert("content_sha256".into(), json!(file.metadata_sha256));
    }
    value
}
fn complete(db: &Connection, project: &str, id: &str, actor: &Actor) -> Result<()> {
    let instant = now();
    let last: Option<String> = db.query_row(
        "SELECT max(available_at) FROM versions WHERE project=?",
        [project],
        |r| r.get(0),
    )?;
    if last.as_ref().is_some_and(|s| s > &instant) {
        return Err(fail(409, "Server clock moved backwards"));
    }
    let receipt = json!({"version":id,"project_id":project,"available_at":instant});
    db.execute("UPDATE versions SET status='completed',available_at=?,availability_hash=? WHERE id=? AND available_at IS NULL",params![instant,digest(&receipt)?,id])?;
    audit(db, project, actor, "snapshot.available", receipt)?;
    Ok(())
}
pub(crate) fn audit(
    db: &Connection,
    project: &str,
    actor: &Actor,
    action: &str,
    data: Value,
) -> Result<Value> {
    let previous: String = db
        .query_row(
            "SELECT hash FROM audit ORDER BY seq DESC LIMIT 1",
            [],
            |r| r.get(0),
        )
        .optional()?
        .unwrap_or_default();
    let event = json!({"project_id":project,"actor":actor.id,"action":action,"recorded_at":now(),"data":data});
    let signature = digest(&json!({"previous":previous,"event":event}))?;
    db.execute(
        "INSERT INTO audit(project,event,previous,hash) VALUES(?,?,?,?)",
        params![project, serde_json::to_string(&event)?, previous, signature],
    )?;
    Ok(json!({"id":signature,"actor":actor.id,"action":action,"data":data}))
}
fn credential_path(path: &Path) -> Result<PathBuf> {
    let path = if path.is_absolute() {
        path.to_owned()
    } else {
        std::env::current_dir()?.join(path)
    };
    Ok(path
        .parent()
        .ok_or_else(|| fail(422, "Invalid credential path"))?
        .canonicalize()?
        .join(
            path.file_name()
                .ok_or_else(|| fail(422, "Invalid credential filename"))?,
        ))
}
pub fn read_tokens(path: &Path) -> Result<Tokens> {
    use std::os::unix::fs::PermissionsExt;
    let path = credential_path(path)?;
    let meta = fs::symlink_metadata(&path)?;
    if !meta.is_file() || meta.permissions().mode() & 0o077 != 0 {
        return Err(fail(
            403,
            "Credential file must be a private regular file (0600)",
        ));
    }
    let bytes = files::read(
        path.parent()
            .ok_or_else(|| fail(422, "Invalid token path"))?,
        path.file_name()
            .and_then(|s| s.to_str())
            .ok_or_else(|| fail(422, "Invalid token filename"))?,
    )?
    .ok_or_else(|| fail(404, "Token file missing"))?;
    let tokens: Tokens = serde_json::from_value(crate::strict_json(&bytes)?)?;
    validate_tokens(&tokens)?;
    Ok(tokens)
}
pub fn auth_init(path: &Path) -> Result<Value> {
    let mut tokens = Tokens::new();
    for role in [Role::Reader, Role::Editor, Role::Reviewer, Role::Publisher] {
        tokens.insert(
            random_id(),
            Actor {
                id: format!("local-{}", format!("{role:?}").to_lowercase()),
                roles: [role].into(),
                audience: Audience::Operator,
                workspace_projects: BTreeSet::new(),
            },
        );
    }
    if let Some(p) = path.parent() {
        fs::create_dir_all(p)?;
    }
    let mut file = OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .custom_flags(rustix::fs::OFlags::NOFOLLOW.bits() as i32)
        .open(path)?;
    file.write_all(&serde_json::to_vec_pretty(&tokens)?)?;
    file.sync_all()?;
    Ok(json!({"tokens_file":path,"identities":tokens.len()}))
}
pub fn auth_agent(
    store: &Store,
    project: &str,
    path: &Path,
    read_only: bool,
    operator: &Actor,
) -> Result<Value> {
    operator.operator(Role::Editor)?;
    store.project(project, operator)?;
    let absolute = credential_path(path)?;
    read_tokens(&absolute)?;
    let parent = absolute
        .parent()
        .ok_or_else(|| fail(422, "Invalid credential path"))?;
    let name = absolute
        .file_name()
        .and_then(|s| s.to_str())
        .ok_or_else(|| fail(422, "Invalid credential name"))?;
    let bytes = files::read(parent, name)?.ok_or_else(|| fail(404, "Token file missing"))?;
    let mut tokens: Tokens = serde_json::from_value(crate::strict_json(&bytes)?)?;
    validate_tokens(&tokens)?;
    let token = random_id();
    let actor = Actor {
        id: format!("agent-{}", &random_id()[..24]),
        roles: if read_only {
            [Role::Reader].into()
        } else {
            [Role::Reader, Role::Editor].into()
        },
        audience: Audience::Agent,
        workspace_projects: [project.into()].into(),
    };
    tokens.insert(token.clone(), actor.clone());
    files::replace(
        parent,
        name,
        Some(&serde_json::to_vec_pretty(&tokens)?),
        Some(&hash(&bytes)),
    )?;
    Ok(json!({"FILEWISE_TOKEN":token,"actor":actor,"restart_required":true}))
}
