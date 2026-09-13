use crate::{Result, bounded, exact_version, fail, id, timestamp};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::{BTreeMap, BTreeSet};

#[derive(
    Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq, PartialOrd, Ord, clap::ValueEnum,
)]
#[serde(rename_all = "lowercase")]
pub enum Role {
    Reader,
    Editor,
    Reviewer,
    Publisher,
}
#[derive(Debug, Default, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum Audience {
    #[default]
    Operator,
    Agent,
}
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
pub struct Actor {
    pub id: String,
    pub roles: BTreeSet<Role>,
    #[serde(default)]
    pub audience: Audience,
    #[serde(default)]
    pub workspace_projects: BTreeSet<String>,
}
impl Actor {
    pub fn validate(&self) -> Result<()> {
        id(&self.id)?;
        if self.roles.is_empty() || self.workspace_projects.len() > 100 {
            return Err(fail(422, "Invalid actor grants"));
        }
        if self.audience == Audience::Agent
            && (self.roles.contains(&Role::Reviewer) || self.roles.contains(&Role::Publisher))
        {
            return Err(fail(403, "Agents cannot approve or publish"));
        }
        for p in &self.workspace_projects {
            id(p)?;
        }
        Ok(())
    }
    pub fn require(&self, role: Role) -> Result<()> {
        self.validate()?;
        if !self.roles.contains(&role) {
            return Err(fail(403, format!("Role required: {role:?}")));
        }
        Ok(())
    }
    pub fn access(&self, project: &str) -> Result<()> {
        self.validate()?;
        id(project)?;
        if self.audience == Audience::Agent && !self.workspace_projects.contains(project) {
            return Err(fail(403, "This token has no workspace grant"));
        }
        Ok(())
    }
    pub fn operator(&self, role: Role) -> Result<()> {
        self.require(role)?;
        if self.audience != Audience::Operator {
            return Err(fail(403, "Operator identity required"));
        }
        Ok(())
    }
}
pub type Tokens = BTreeMap<String, Actor>;
pub fn validate_tokens(tokens: &Tokens) -> Result<()> {
    if tokens.is_empty() {
        return Err(fail(422, "Empty token file"));
    }
    let mut actors = BTreeMap::new();
    for (token, actor) in tokens {
        if token.len() < 32 || token.len() > 256 || !token.bytes().all(|c| c.is_ascii_graphic()) {
            return Err(fail(422, "Invalid credential"));
        }
        actor.validate()?;
        if actors
            .insert(&actor.id, actor)
            .is_some_and(|old| old != actor)
        {
            return Err(fail(422, "Actor identities must be consistent"));
        }
    }
    Ok(())
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Evidence {
    pub source_id: String,
    pub locator: String,
    pub quote: String,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DataInput {
    pub version: String,
    pub path: String,
    pub role: InputRole,
}
#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum InputRole {
    Data,
    Mapping,
    Code,
    Model,
}
#[derive(Debug, Default, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum Processing {
    #[default]
    Source,
    Deterministic,
    Model,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Metadata {
    pub summary: String,
    pub tags: Vec<String>,
    pub entities: Vec<String>,
    pub depends_on: Vec<String>,
    pub facts: BTreeMap<String, Value>,
    pub evidence: Vec<Evidence>,
    pub owner: String,
    pub authority: u16,
    pub valid_from: Option<String>,
    pub valid_until: Option<String>,
    pub data: Option<DataContract>,
    pub processing: Processing,
    pub lineage: Vec<DataInput>,
}
impl Default for Metadata {
    fn default() -> Self {
        Self {
            summary: String::new(),
            tags: vec![],
            entities: vec![],
            depends_on: vec![],
            facts: BTreeMap::new(),
            evidence: vec![],
            owner: String::new(),
            authority: 100,
            valid_from: None,
            valid_until: None,
            data: None,
            processing: Processing::Source,
            lineage: vec![],
        }
    }
}
impl Metadata {
    pub fn validate(&self) -> Result<()> {
        bounded(&self.summary, 0, 2000, "summary")?;
        bounded(&self.owner, 0, 120, "owner")?;
        if self.tags.len() > 30
            || self.entities.len() > 30
            || self.depends_on.len() > 100
            || self.evidence.len() > 30
            || self.lineage.len() > 100
            || self.facts.len() > 40
            || self.authority > 1000
            || serde_json::to_vec(self)?.len() > 64000
            || serde_json::to_vec(&self.facts)?.len() > 16000
        {
            return Err(fail(422, "Metadata exceeds limits"));
        }
        for key in self.facts.keys() {
            bounded(key, 1, 100, "fact key")?;
        }
        for tag in &self.tags {
            bounded(tag, 1, 300, "tag")?;
        }
        for entity in &self.entities {
            id(entity)?;
        }
        for path in &self.depends_on {
            crate::files::relative(path)?;
        }
        for e in &self.evidence {
            id(&e.source_id)?;
            bounded(&e.locator, 1, 256, "locator")?;
            bounded(&e.quote, 1, 4000, "quote")?;
        }
        for item in &self.lineage {
            exact_version(&item.version)?;
            crate::files::relative(&item.path)?;
        }
        let start = self.valid_from.as_deref().map(timestamp).transpose()?;
        let end = self.valid_until.as_deref().map(timestamp).transpose()?;
        if start
            .as_ref()
            .zip(end.as_ref())
            .is_some_and(|(a, b)| a >= b)
        {
            return Err(fail(422, "Invalid validity interval"));
        }
        if let Some(data) = &self.data {
            data.validate()?;
        }
        Ok(())
    }
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FileCheck {
    pub id: String,
    pub file: String,
    #[serde(default)]
    pub pointer: String,
    #[serde(default)]
    pub op: CheckOp,
    #[serde(default)]
    pub expected: Value,
    #[serde(default)]
    pub reference_file: Option<String>,
    #[serde(default)]
    pub reference_pointer: String,
}
#[derive(Debug, Default, Clone, Copy, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CheckOp {
    #[default]
    Eq,
    Gte,
    Lte,
    Contains,
    Exists,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct OutputCheck {
    pub id: String,
    pub object_id: String,
    pub field: String,
    #[serde(default)]
    pub op: CheckOp,
    #[serde(default)]
    pub expected: Value,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ProjectSpec {
    pub id: String,
    pub name: String,
    #[serde(default)]
    pub dependencies: BTreeMap<String, Vec<String>>,
    #[serde(default)]
    pub checks: Vec<FileCheck>,
    #[serde(default)]
    pub excludes: Vec<String>,
    #[serde(default = "all_paths")]
    pub includes: Vec<String>,
    #[serde(default)]
    pub data_contracts: BTreeMap<String, DataContract>,
}
fn all_paths() -> Vec<String> {
    vec!["**".into()]
}
impl ProjectSpec {
    pub fn validate(&self) -> Result<()> {
        id(&self.id)?;
        bounded(&self.name, 1, 120, "project name")?;
        if self.includes.is_empty()
            || self.includes.len() > 50
            || self.excludes.len() > 50
            || self.dependencies.len() > 1000
            || self.checks.len() > 40
            || self.data_contracts.len() > 1000
        {
            return Err(fail(422, "Project limits exceeded"));
        }
        for p in self.includes.iter().chain(&self.excludes) {
            bounded(p, 1, 240, "pattern")?;
            globset::Glob::new(p).map_err(|e| fail(422, e.to_string()))?;
        }
        for (p, deps) in &self.dependencies {
            crate::files::relative(p)?;
            if deps.len() > 1000 {
                return Err(fail(422, "Too many dependencies"));
            }
            for d in deps {
                crate::files::relative(d)?;
            }
        }
        let mut ids = BTreeSet::new();
        for c in &self.checks {
            id(&c.id)?;
            pointer(&c.pointer)?;
            pointer(&c.reference_pointer)?;
            crate::files::relative(&c.file)?;
            if !ids.insert(&c.id) {
                return Err(fail(422, "Duplicate check ID"));
            }
            if let Some(p) = &c.reference_file {
                crate::files::relative(p)?;
            }
        }
        for (p, c) in &self.data_contracts {
            crate::files::relative(p)?;
            c.validate()?;
        }
        Ok(())
    }
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct QualityRule {
    pub id: String,
    pub op: QualityOp,
    #[serde(default)]
    pub column: Option<String>,
    #[serde(default)]
    pub minimum: Option<serde_json::Number>,
    #[serde(default)]
    pub maximum: Option<serde_json::Number>,
    #[serde(default)]
    pub max_change: Option<serde_json::Number>,
}
#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum QualityOp {
    NotNull,
    Unique,
    Range,
    RowCount,
    DistinctCountChange,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct DataContract {
    pub meaning: BTreeMap<String, String>,
    pub allowed_uses: Vec<String>,
    pub rows_pointer: String,
    pub sheet: usize,
    pub checks: Vec<QualityRule>,
}
impl Default for DataContract {
    fn default() -> Self {
        Self {
            meaning: BTreeMap::new(),
            allowed_uses: vec![],
            rows_pointer: String::new(),
            sheet: 1,
            checks: vec![],
        }
    }
}
impl DataContract {
    pub fn validate(&self) -> Result<()> {
        let keys = [
            "entity_type",
            "entity_id",
            "identifier_system",
            "measure",
            "unit",
            "currency",
            "period",
            "calendar",
            "grain",
            "population",
            "sampling",
            "coverage",
        ];
        if self.checks.len() > 40
            || self.allowed_uses.len() > 30
            || self.sheet == 0
            || self.sheet > 100
        {
            return Err(fail(422, "Invalid data contract bounds"));
        }
        for (k, v) in &self.meaning {
            if !keys.contains(&k.as_str()) {
                return Err(fail(422, "Unknown meaning field"));
            }
            bounded(v, 1, 300, "meaning")?;
        }
        for v in &self.allowed_uses {
            bounded(v, 1, 300, "use")?;
        }
        pointer(&self.rows_pointer)?;
        let mut ids = BTreeSet::new();
        for r in &self.checks {
            id(&r.id)?;
            if !ids.insert(&r.id) || (r.op != QualityOp::RowCount) != r.column.is_some() {
                return Err(fail(422, "Invalid quality rule"));
            }
            if let Some(c) = &r.column {
                bounded(c, 1, 300, "column")?;
            }
            let bound = |n: &Option<serde_json::Number>| {
                n.as_ref()
                    .map(|n| {
                        crate::compute::number(&Value::Number(n.clone())).ok_or_else(|| {
                            fail(422, "Numeric bound exceeds supported precision/magnitude")
                        })
                    })
                    .transpose()
            };
            let minimum = bound(&r.minimum)?;
            let maximum = bound(&r.maximum)?;
            let max_change = bound(&r.max_change)?;
            if minimum.zip(maximum).is_some_and(|(a, b)| a > b) || max_change.is_some_and(|v| v < 0)
            {
                return Err(fail(422, "Invalid numeric interval"));
            }
            if matches!(r.op, QualityOp::Range | QualityOp::RowCount) {
                if r.minimum.is_none() && r.maximum.is_none() {
                    return Err(fail(422, "Supply a bound"));
                }
            } else if r.minimum.is_some() || r.maximum.is_some() {
                return Err(fail(422, "Bounds apply only to range/count"));
            }
            if (r.op == QualityOp::DistinctCountChange) != r.max_change.is_some() {
                return Err(fail(422, "Count change requires max_change"));
            }
        }
        Ok(())
    }
}
pub fn pointer(value: &str) -> Result<()> {
    bounded(value, 0, 500, "JSON pointer")?;
    if !value.is_empty() && !value.starts_with('/') {
        return Err(fail(422, "Use an RFC6901 pointer"));
    }
    let mut chars = value.chars();
    while let Some(c) = chars.next() {
        if c == '~' && !matches!(chars.next(), Some('0' | '1')) {
            return Err(fail(422, "Invalid JSON pointer escape"));
        }
    }
    Ok(())
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct DataUse {
    pub expect: BTreeMap<String, String>,
    pub purpose: Option<String>,
    pub require_quality: bool,
}
impl Default for DataUse {
    fn default() -> Self {
        Self {
            expect: BTreeMap::new(),
            purpose: None,
            require_quality: true,
        }
    }
}
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct FileChange {
    pub text: Option<String>,
    pub base64: Option<String>,
    pub delete: bool,
    pub meta: Option<Metadata>,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WriteQuery {
    pub base_version: String,
    pub request_id: String,
    pub changes: BTreeMap<String, FileChange>,
    pub message: String,
    #[serde(default)]
    pub task: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub tool: String,
    #[serde(default)]
    pub dry_run: bool,
    #[serde(default)]
    pub require_pass: bool,
}
impl WriteQuery {
    pub fn validate(&self) -> Result<()> {
        exact_version(&self.base_version)?;
        id(&self.request_id)?;
        bounded(&self.message, 1, 500, "message")?;
        bounded(&self.task, 0, 500, "task")?;
        bounded(&self.model, 0, 200, "model")?;
        bounded(&self.tool, 0, 200, "tool")?;
        if self.changes.is_empty() || self.changes.len() > 1000 {
            return Err(fail(422, "Supply 1–1,000 changes"));
        }
        for (path, c) in &self.changes {
            crate::files::relative(path)?;
            let n = usize::from(c.text.is_some())
                + usize::from(c.base64.is_some())
                + usize::from(c.delete);
            if n > 1 || (n == 0 && c.meta.is_none()) || (c.delete && c.meta.is_some()) {
                return Err(fail(422, "Choose text/base64/delete or metadata-only"));
            }
            if let Some(m) = &c.meta {
                m.validate()?;
            }
        }
        Ok(())
    }
}
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Query {
    pub version: String,
    pub paths: Vec<String>,
    pub as_of: Option<String>,
    pub path: Option<String>,
    pub include_bytes: bool,
    pub query: Option<String>,
    pub mode: String,
    pub limit: usize,
    pub tags: Vec<String>,
    pub max_per_file: usize,
    pub before: Option<String>,
    pub after: String,
    pub base_version: Option<String>,
    pub direction: String,
    pub goal: Option<String>,
    pub max_chars: usize,
    pub output_checks: Vec<OutputCheck>,
    pub retrieval_mode: String,
    pub retrieval_limit: usize,
    pub requirements: BTreeMap<String, DataUse>,
    pub model_use: String,
    pub phase: String,
    pub task_id: Option<String>,
    pub operation: String,
    pub output_version: Option<String>,
    pub outputs: BTreeMap<String, String>,
    pub result: BTreeMap<String, Value>,
    pub citations: Vec<Evidence>,
}
impl Default for Query {
    fn default() -> Self {
        Self {
            version: "latest".into(),
            paths: vec![],
            as_of: None,
            path: None,
            include_bytes: false,
            query: None,
            mode: "hybrid".into(),
            limit: 30,
            tags: vec![],
            max_per_file: 3,
            before: None,
            after: "latest".into(),
            base_version: None,
            direction: "forward".into(),
            goal: None,
            max_chars: 12000,
            output_checks: vec![],
            retrieval_mode: "hybrid".into(),
            retrieval_limit: 5,
            requirements: BTreeMap::new(),
            model_use: "none".into(),
            phase: "build".into(),
            task_id: None,
            operation: "read".into(),
            output_version: None,
            outputs: BTreeMap::new(),
            result: BTreeMap::new(),
            citations: vec![],
        }
    }
}
impl Query {
    pub fn validate(&self) -> Result<()> {
        bounded(&self.version, 1, 128, "version")?;
        if self.paths.len() > 1000
            || self.requirements.len() > 1000
            || self.outputs.len() > 1000
            || self.citations.len() > 100
            || self.output_checks.len() > 40
            || self.tags.len() > 30
            || !(1..=100).contains(&self.limit)
            || !(1..=100).contains(&self.max_per_file)
            || !(100..=50000).contains(&self.max_chars)
            || !(1..=20).contains(&self.retrieval_limit)
        {
            return Err(fail(422, "Query limits exceeded"));
        }
        for path in self
            .paths
            .iter()
            .chain(self.path.iter())
            .chain(self.requirements.keys())
            .chain(self.outputs.keys())
        {
            crate::files::relative(path)?;
        }
        if let Some(t) = &self.as_of {
            if timestamp(t)? > crate::now() {
                return Err(fail(422, "as_of cannot be in the future"));
            }
        }
        for s in [&self.query, &self.goal].into_iter().flatten() {
            bounded(s, 1, 2000, "question/goal")?;
        }
        if self.query.as_ref().is_some_and(|s| s.chars().count() > 200) {
            return Err(fail(422, "Query exceeds 200 characters"));
        }
        if !["exact", "lexical", "hybrid", "semantic"].contains(&self.mode.as_str())
            || !["exact", "lexical", "hybrid", "semantic"].contains(&self.retrieval_mode.as_str())
            || !["forward", "reverse"].contains(&self.direction.as_str())
            || !["none", "extractive", "generative"].contains(&self.model_use.as_str())
            || !["build", "preflight", "postflight"].contains(&self.phase.as_str())
        {
            return Err(fail(422, "Invalid query mode"));
        }
        for use_ in self.requirements.values() {
            DataContract {
                meaning: use_.expect.clone(),
                ..Default::default()
            }
            .validate()?;
            if let Some(p) = &use_.purpose {
                bounded(p, 1, 300, "purpose")?;
            }
        }
        if ![
            "read", "search", "diff", "impact", "compile", "write", "publish",
        ]
        .contains(&self.operation.as_str())
        {
            return Err(fail(422, "Invalid operation"));
        }
        for tag in &self.tags {
            bounded(tag, 1, 300, "tag")?;
        }
        for value in self.outputs.values() {
            exact_version(value)?;
        }
        for e in &self.citations {
            exact_version(&e.source_id)?;
            bounded(&e.locator, 1, 256, "locator")?;
            bounded(&e.quote, 1, 4000, "quote")?;
        }
        let mut checks = BTreeSet::new();
        for check in &self.output_checks {
            id(&check.id)?;
            if !checks.insert(&check.id) {
                return Err(fail(422, "Duplicate output check ID"));
            }
            bounded(&check.field, 1, 128, "result field")?;
            if check.object_id != "result" {
                return Err(fail(422, "Output checks must target result"));
            }
        }
        Ok(())
    }
}
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Fragment {
    pub locator: String,
    pub text: String,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FileRecord {
    pub path: String,
    pub sha256: String,
    pub source_id: String,
    pub size: usize,
    pub metadata: Option<Metadata>,
    pub metadata_current: bool,
    pub declared_by: Option<String>,
    pub metadata_sha256: Option<String>,
    pub depends_on: Vec<String>,
    pub data_contract: Option<DataContract>,
    pub data_quality: Option<Value>,
    pub lineage: Vec<Value>,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Snapshot {
    pub schema: String,
    pub project_id: String,
    pub base_version: Option<String>,
    pub author: String,
    pub created_at: String,
    pub message: String,
    pub files: BTreeMap<String, FileRecord>,
    pub verification: Value,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Version {
    pub id: String,
    pub snapshot: Snapshot,
    pub status: String,
    pub available_at: Option<String>,
    pub approver: Option<String>,
    pub revoked: bool,
}
