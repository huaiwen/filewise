//! Native, text-only document extraction. Package members never touch the filesystem.
use crate::{Result, fail, model::Fragment};
use roxmltree::{Document, Node, ParsingOptions};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    collections::{BTreeMap, BTreeSet, VecDeque},
    io::{Cursor, Read, Write},
    path::Path,
    sync::{Mutex, OnceLock},
};

const MAX_XML: usize = 8 * 1024 * 1024;
const MAX_EXPANDED: u64 = 32 * 1024 * 1024;
const MAX_TEXT: usize = 2_000_000;
const MAX_FRAGMENTS: usize = 20_000;
const W: &str = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";
const A: &str = "http://schemas.openxmlformats.org/drawingml/2006/main";
const P: &str = "http://schemas.openxmlformats.org/presentationml/2006/main";
const S: &str = "http://schemas.openxmlformats.org/spreadsheetml/2006/main";
const R: &str = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
const REL: &str = "http://schemas.openxmlformats.org/package/2006/relationships";

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ExtractionInfo {
    pub format: String,
    pub status: String,
    pub engine: String,
    pub scope: String,
    pub notes: Vec<String>,
    pub fragment_count: usize,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Cell {
    pub address: String,
    pub row: usize,
    pub column: usize,
    pub kind: String,
    pub value: Value,
    pub formula: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub formula_origin: Option<String>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Sheet {
    pub name: String,
    pub cells: Vec<Cell>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Extraction {
    pub info: ExtractionInfo,
    pub fragments: Vec<Fragment>,
    pub sheets: Vec<Sheet>,
}
impl Extraction {
    pub fn new(format: &str, status: &str) -> Self {
        Self {
            info: ExtractionInfo {
                format: format.into(),
                status: status.into(),
                engine: "filewise-native-documents-v1".into(),
                scope: "text_only; no OCR or visual layout".into(),
                notes: vec![],
                fragment_count: 0,
                error: None,
            },
            fragments: vec![],
            sheets: vec![],
        }
    }
    pub fn failed(format: &str, error: &str) -> Self {
        let mut value = Self::new(format, "failed");
        value.info.error = Some(error.chars().take(300).collect());
        value
    }
    fn note(&mut self, note: &str) {
        if !self.info.notes.iter().any(|n| n == note) {
            self.info.notes.push(note.into());
        }
    }
    fn push(&mut self, locator: String, text: String) -> Result<()> {
        if text.trim().is_empty() {
            return Ok(());
        }
        if text.contains('\u{fffd}')
            || text
                .chars()
                .any(|c| c.is_control() && !matches!(c, '\n' | '\t' | '\r'))
        {
            self.note("undecodable_text");
        }
        if self.fragments.len() >= MAX_FRAGMENTS || locator.chars().count() > 256 {
            return Err(fail(413, "Document fragment limit exceeded"));
        }
        self.fragments.push(Fragment { locator, text });
        Ok(())
    }
    pub fn validate(&self) -> Result<()> {
        let count: usize = self.fragments.iter().map(|f| f.text.chars().count()).sum();
        let unique: BTreeSet<_> = self.fragments.iter().map(|f| &f.locator).collect();
        if self.info.engine != "filewise-native-documents-v1"
            || count > MAX_TEXT
            || self.fragments.len() > MAX_FRAGMENTS
            || unique.len() != self.fragments.len()
            || self.info.fragment_count != self.fragments.len()
            || self.info.notes.len() > 600
            || self.info.notes.iter().any(|n| n.chars().count() > 300)
            || self
                .fragments
                .iter()
                .any(|f| f.locator.chars().count() > 256)
            || !["text", "partial", "no_text", "unsupported", "failed"]
                .contains(&self.info.status.as_str())
            || self.sheets.len() > 100
            || self.sheets.iter().map(|s| s.cells.len()).sum::<usize>() > MAX_FRAGMENTS
        {
            return Err(fail(413, "Document extraction exceeds limits"));
        }
        Ok(())
    }
    fn finish(mut self) -> Result<Self> {
        self.info.fragment_count = self.fragments.len();
        self.info.status = if self.fragments.is_empty() {
            if self
                .info
                .notes
                .iter()
                .any(|n| n.ends_with(":extraction_failed"))
            {
                "failed"
            } else {
                "no_text"
            }
        } else if self.info.notes.is_empty() {
            "text"
        } else {
            "partial"
        }
        .into();
        self.validate()?;
        Ok(self)
    }
}
pub fn format(path: &str) -> String {
    Path::new(path)
        .extension()
        .and_then(|s| s.to_str())
        .unwrap_or_default()
        .to_ascii_lowercase()
}
pub fn supported(path: &str) -> bool {
    matches!(format(path).as_str(), "pdf" | "docx" | "xlsx" | "pptx")
}

// ponytail: bounded process-local FIFO, not a persistent index; add persistence only
// if repeated document parsing across restarts is measured as a bottleneck.
type Cache = VecDeque<(String, Extraction, usize)>;
static CACHE: OnceLock<Mutex<Cache>> = OnceLock::new();
pub fn extract(path: &str, bytes: &[u8]) -> Extraction {
    let format = format(path);
    let key = format!("{format}:{}", crate::hash(bytes));
    let cache = CACHE.get_or_init(Default::default);
    if let Some((_, value, _)) = cache
        .lock()
        .unwrap_or_else(|e| e.into_inner())
        .iter()
        .find(|(k, _, _)| k == &key)
    {
        return value.clone();
    }
    let value = match crate::worker::extract(&format, bytes) {
        Ok(value) if value.info.format == format => value,
        Ok(_) => return Extraction::failed(&format, "Document worker format mismatch"),
        Err(e) => return Extraction::failed(&format, &e.message),
    };
    let size = serde_json::to_vec(&value).map_or(usize::MAX, |s| s.len());
    if value.info.status != "failed" && size <= 8 * 1024 * 1024 {
        let mut cache = cache.lock().unwrap_or_else(|e| e.into_inner());
        while cache.len() >= 16 || cache.iter().map(|v| v.2).sum::<usize>() + size > 8 * 1024 * 1024
        {
            cache.pop_front();
        }
        cache.push_back((key, value.clone(), size));
    }
    value
}

fn xml(bytes: &[u8]) -> Result<Document<'_>> {
    let text = std::str::from_utf8(bytes).map_err(|_| fail(422, "OOXML parts must use UTF-8"))?;
    Document::parse_with_options(
        text,
        ParsingOptions {
            allow_dtd: false,
            nodes_limit: 200_000,
            ..Default::default()
        },
    )
    .map_err(|_| fail(422, "Invalid or excessive XML; DTDs are not accepted"))
}
fn namespace(actual: Option<&str>, expected: &str) -> bool {
    actual == Some(expected)
        || actual.is_some_and(|a| {
            let strict = match expected {
                W => "http://purl.oclc.org/ooxml/wordprocessingml/main",
                A => "http://purl.oclc.org/ooxml/drawingml/main",
                P => "http://purl.oclc.org/ooxml/presentationml/main",
                S => "http://purl.oclc.org/ooxml/spreadsheetml/main",
                R => "http://purl.oclc.org/ooxml/officeDocument/relationships",
                _ => "",
            };
            !strict.is_empty() && a == strict
        })
}
fn tag(n: Node<'_, '_>, ns: &str, name: &str) -> bool {
    n.is_element() && n.tag_name().name() == name && namespace(n.tag_name().namespace(), ns)
}
fn attr<'a>(n: Node<'a, '_>, ns: &str, name: &str) -> Option<&'a str> {
    n.attributes()
        .find(|a| a.name() == name && namespace(a.namespace(), ns))
        .map(|a| a.value())
}
fn text(n: Node<'_, '_>) -> String {
    n.children().filter_map(|n| n.text()).collect()
}
fn required(value: Option<&str>) -> Result<&str> {
    value.ok_or_else(|| fail(422, "Required OOXML attribute missing"))
}
fn target(base: &str, relative: &str) -> Result<String> {
    if relative.contains(['\\', ':', '?', '#', '%', '\0']) {
        return Err(fail(422, "Unsupported package relationship target"));
    }
    let mut parts: Vec<_> = if relative.starts_with('/') {
        vec![]
    } else {
        base.rsplit_once('/')
            .map(|(d, _)| d.split('/').collect())
            .unwrap_or_default()
    };
    for part in relative.trim_start_matches('/').split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if parts.pop().is_none() {
                    return Err(fail(422, "Package relationship escapes archive"));
                }
            }
            _ => parts.push(part),
        }
    }
    let result = parts.join("/");
    crate::files::relative(&result)?;
    Ok(result)
}
struct Package {
    parts: BTreeMap<String, Vec<u8>>,
    notes: Vec<String>,
}
struct Relationship {
    kind: String,
    target: String,
    external: bool,
}
impl Package {
    fn open(bytes: &[u8]) -> Result<Self> {
        let mut archive = zip::ZipArchive::new(Cursor::new(bytes))
            .map_err(|_| fail(422, "Invalid Office archive"))?;
        if archive.len() > 4096 {
            return Err(fail(413, "Office archive has too many parts"));
        }
        // ZipArchive's name map can hide duplicate central-directory entries.
        // Walk only their fixed headers/names before trusting the library's map.
        let mut offset = usize::try_from(archive.central_directory_start())
            .map_err(|_| fail(422, "ZIP directory offset overflow"))?;
        let mut directory_names = BTreeSet::new();
        while bytes.get(offset..offset.saturating_add(4)) == Some(b"PK\x01\x02") {
            let head = bytes
                .get(offset..offset.saturating_add(46))
                .ok_or_else(|| fail(422, "Truncated ZIP directory"))?;
            let length = |i| u16::from_le_bytes([head[i], head[i + 1]]) as usize;
            let end = offset
                .checked_add(46 + length(28) + length(30) + length(32))
                .filter(|n| *n <= bytes.len())
                .ok_or_else(|| fail(422, "ZIP directory overflow"))?;
            let name = &bytes[offset + 46..offset + 46 + length(28)];
            if !directory_names.insert(name) || directory_names.len() > 4096 {
                return Err(fail(422, "Duplicate or excessive ZIP directory names"));
            }
            offset = end;
        }
        if directory_names.len() != archive.len() {
            return Err(fail(422, "Ambiguous ZIP directory"));
        }
        let mut total = 0u64;
        let mut expanded_xml = 0usize;
        let mut names = BTreeSet::new();
        let mut parts = BTreeMap::new();
        let mut notes = BTreeSet::new();
        for i in 0..archive.len() {
            let raw = archive
                .by_index_raw(i)
                .map_err(|_| fail(422, "Invalid ZIP directory"))?;
            let name = raw.name().to_owned();
            if raw.is_dir() {
                continue;
            }
            crate::files::relative(&name)?;
            total = total
                .checked_add(raw.size())
                .ok_or_else(|| fail(413, "Archive size overflow"))?;
            if total > MAX_EXPANDED
                || raw.encrypted()
                || !names.insert(name.clone())
                || raw.unix_mode().is_some_and(|m| m & 0o170000 == 0o120000)
            {
                return Err(fail(
                    422,
                    "Encrypted, duplicate, linked or oversized Office part",
                ));
            }
            if name.contains("/media/")
                || name.contains("/embeddings/")
                || name.contains("/charts/")
                || name.contains("/diagrams/")
                || name.ends_with("vbaProject.bin")
            {
                notes.insert("visual_or_embedded_content_omitted".to_owned());
            }
            if !name.ends_with(".xml") && !name.ends_with(".rels") {
                continue;
            }
            if raw.size() > MAX_XML as u64 {
                return Err(fail(413, "Office XML part exceeds 8 MiB"));
            }
            drop(raw);
            let file = archive
                .by_index(i)
                .map_err(|_| fail(422, "Unsupported ZIP compression"))?;
            let mut body = vec![];
            file.take((MAX_XML + 1) as u64)
                .read_to_end(&mut body)
                .map_err(|_| fail(422, "Office part decompression or checksum failed"))?;
            expanded_xml += body.len();
            if body.len() > MAX_XML || expanded_xml > MAX_EXPANDED as usize {
                return Err(fail(413, "Expanded Office XML exceeds limit"));
            }
            // Validate even unreferenced XML: reject malformed packages and entity expansion.
            xml(&body)?;
            parts.insert(name, body);
        }
        Ok(Self {
            parts,
            notes: notes.into_iter().collect(),
        })
    }
    fn document(&self, name: &str) -> Result<Document<'_>> {
        xml(self
            .parts
            .get(name)
            .ok_or_else(|| fail(422, "Referenced Office part is missing"))?)
    }
    fn relations(&self, part: &str) -> Result<BTreeMap<String, Relationship>> {
        let (dir, name) = part.rsplit_once('/').unwrap_or(("", part));
        let rel = if part.is_empty() {
            "_rels/.rels".into()
        } else if dir.is_empty() {
            format!("_rels/{name}.rels")
        } else {
            format!("{dir}/_rels/{name}.rels")
        };
        if !self.parts.contains_key(&rel) {
            return Ok(BTreeMap::new());
        }
        let doc = self.document(&rel)?;
        if !tag(doc.root_element(), REL, "Relationships") {
            return Err(fail(422, "Invalid relationship document"));
        }
        let mut result = BTreeMap::new();
        for n in doc
            .root_element()
            .children()
            .filter(|n| tag(*n, REL, "Relationship"))
        {
            let id = required(n.attribute("Id"))?.to_owned();
            let kind = required(n.attribute("Type"))?.to_owned();
            let external = n.attribute("TargetMode") == Some("External");
            let raw = required(n.attribute("Target"))?;
            let target = if external {
                String::new()
            } else {
                target(part, raw)?
            };
            if result
                .insert(
                    id,
                    Relationship {
                        kind,
                        target,
                        external,
                    },
                )
                .is_some()
            {
                return Err(fail(422, "Duplicate relationship ID"));
            }
        }
        Ok(result)
    }
    fn main_part(&self) -> Result<String> {
        let mut found = self.relations("")?.into_values().filter(|r| {
            r.kind == format!("{R}/officeDocument")
                || r.kind
                    == "http://purl.oclc.org/ooxml/officeDocument/relationships/officeDocument"
        });
        let r = found
            .next()
            .ok_or_else(|| fail(422, "Office main relationship missing"))?;
        if r.external || found.next().is_some() {
            return Err(fail(422, "Invalid Office main relationship"));
        }
        Ok(r.target)
    }
}
fn rel_part<'a>(rels: &'a BTreeMap<String, Relationship>, id: &str, kind: &str) -> Result<&'a str> {
    let r = rels
        .get(id)
        .ok_or_else(|| fail(422, "Office relationship ID is missing"))?;
    if r.external
        || !(r.kind == format!("{R}/{kind}")
            || r.kind == format!("http://purl.oclc.org/ooxml/officeDocument/relationships/{kind}"))
    {
        return Err(fail(422, "External or mismatched Office relationship"));
    }
    Ok(&r.target)
}
fn paragraphs(doc: &Document<'_>, ns: &str, prefix: &str, out: &mut Extraction) -> Result<()> {
    let tables: Vec<_> = doc.descendants().filter(|n| tag(*n, ns, "tbl")).collect();
    for (i, p) in doc.descendants().filter(|n| tag(*n, ns, "p")).enumerate() {
        // Final-view text: exclude deleted revisions/instructions and prevent nested
        // text-box paragraphs from also appearing in their containing paragraph.
        if p.ancestors()
            .any(|n| tag(n, W, "del") || tag(n, W, "moveFrom"))
        {
            continue;
        }
        let mut value = String::new();
        for n in p.descendants().filter(|n| n.is_element()) {
            if n.ancestors().skip(1).find(|a| tag(*a, ns, "p")) != Some(p) {
                continue;
            }
            if n.ancestors()
                .any(|n| tag(n, W, "del") || tag(n, W, "moveFrom"))
            {
                continue;
            }
            if tag(n, ns, "t") {
                value.push_str(&text(n));
            } else if tag(n, ns, "tab") {
                value.push('\t');
            } else if tag(n, ns, "br") || tag(n, ns, "cr") {
                value.push('\n');
            }
        }
        let mut locator = prefix.to_owned();
        if let Some(cell) = p.ancestors().find(|n| tag(*n, ns, "tc")) {
            if let (Some(row), Some(table)) = (
                cell.ancestors().find(|n| tag(*n, ns, "tr")),
                cell.ancestors().find(|n| tag(*n, ns, "tbl")),
            ) {
                let ti = tables.iter().position(|n| *n == table).unwrap_or(0) + 1;
                let ri = row.prev_siblings().filter(|n| tag(*n, ns, "tr")).count();
                let ci = cell.prev_siblings().filter(|n| tag(*n, ns, "tc")).count();
                locator = format!("{prefix}/table:{ti}/row:{ri}/cell:{ci}");
            }
        }
        out.push(format!("{locator}/paragraph:{}", i + 1), value)?;
    }
    if doc
        .descendants()
        .any(|n| tag(n, W, "altChunk") || tag(n, W, "subDoc"))
    {
        out.note("external_or_alternate_content_omitted");
    }
    Ok(())
}
fn word(package: &Package, part: &str, out: &mut Extraction) -> Result<()> {
    let doc = package.document(part)?;
    if !tag(doc.root_element(), W, "document") {
        return Err(fail(422, "Not a Word document"));
    }
    paragraphs(&doc, W, part, out)?;
    let rels = package.relations(part)?;
    let mut related = BTreeSet::new();
    for r in rels.values() {
        if ["header", "footer", "footnotes", "endnotes"]
            .iter()
            .any(|k| {
                r.kind == format!("{R}/{k}")
                    || r.kind
                        == format!("http://purl.oclc.org/ooxml/officeDocument/relationships/{k}")
            })
        {
            if r.external {
                return Err(fail(422, "External Word text part"));
            }
            related.insert(r.target.clone());
        }
    }
    for part in related {
        paragraphs(&package.document(&part)?, W, &part, out)?;
    }
    Ok(())
}
fn slides(package: &Package, part: &str, out: &mut Extraction) -> Result<()> {
    let doc = package.document(part)?;
    if !tag(doc.root_element(), P, "presentation") {
        return Err(fail(422, "Not a PowerPoint presentation"));
    }
    let rels = package.relations(part)?;
    let mut seen = BTreeSet::new();
    for (i, slide) in doc
        .descendants()
        .filter(|n| tag(*n, P, "sldId"))
        .enumerate()
    {
        if i >= 500 {
            return Err(fail(413, "Presentation exceeds 500 slides"));
        }
        let part = rel_part(&rels, required(attr(slide, R, "id"))?, "slide")?;
        if !seen.insert(part) {
            return Err(fail(422, "Repeated slide relationship"));
        }
        let doc = package.document(part)?;
        if !tag(doc.root_element(), P, "sld") {
            return Err(fail(422, "Invalid slide root"));
        }
        paragraphs(&doc, A, &format!("slide:{}", i + 1), out)?;
        let related = package.relations(part)?;
        if related.values().any(|r| r.kind.ends_with("/slideLayout")) {
            out.note("layout_templates_not_rendered");
        }
        for (id, _) in related
            .iter()
            .filter(|(_, r)| r.kind.ends_with("/notesSlide"))
        {
            let notes = package.document(rel_part(&related, id, "notesSlide")?)?;
            if !tag(notes.root_element(), P, "notes") {
                return Err(fail(422, "Invalid slide notes root"));
            }
            paragraphs(&notes, A, &format!("slide:{}/notes", i + 1), out)?;
        }
    }
    Ok(())
}
fn address(value: &str) -> Result<(usize, usize)> {
    let split = value
        .find(|c: char| c.is_ascii_digit())
        .ok_or_else(|| fail(422, "Invalid cell address"))?;
    let (letters, digits) = value.split_at(split);
    if letters.is_empty()
        || letters.len() > 3
        || !letters.bytes().all(|c| c.is_ascii_uppercase())
        || !digits.bytes().all(|c| c.is_ascii_digit())
    {
        return Err(fail(422, "Invalid cell address"));
    }
    let column = letters
        .bytes()
        .fold(0usize, |n, c| n * 26 + (c - b'A' + 1) as usize);
    let row = digits
        .parse::<usize>()
        .map_err(|_| fail(422, "Invalid cell row"))?;
    if row == 0 || row > 20_001 || column > 512 {
        return Err(fail(413, "Sheet exceeds 20,001 rows or 512 columns"));
    }
    Ok((row, column))
}
fn rich(n: Node<'_, '_>) -> String {
    n.descendants()
        .filter(|n| tag(*n, S, "t") && !n.ancestors().any(|a| tag(a, S, "rPh")))
        .map(text)
        .collect()
}
fn date_format(id: u32, custom: Option<&String>) -> bool {
    if matches!(id, 14..=22 | 27..=36 | 45..=47 | 50..=58) {
        return true;
    }
    let mut quoted = false;
    let mut escaped = false;
    let mut bracket = false;
    custom.is_some_and(|value| {
        value.chars().any(|c| {
            if escaped {
                escaped = false;
                return false;
            }
            if c == '\\' {
                escaped = true;
                return false;
            }
            if c == '"' {
                quoted = !quoted;
                return false;
            }
            if quoted {
                return false;
            }
            if c == '[' {
                bracket = true;
                return false;
            }
            if c == ']' {
                bracket = false;
                return false;
            }
            // Standard time tokens also occur in elapsed-time [h]/[m]/[s] formats.
            (!bracket || matches!(c, 'h' | 'm' | 's'))
                && matches!(c.to_ascii_lowercase(), 'y' | 'm' | 'd' | 'h' | 's')
        })
    })
}
fn workbook(package: &Package, part: &str, out: &mut Extraction) -> Result<()> {
    let book = package.document(part)?;
    if !tag(book.root_element(), S, "workbook") {
        return Err(fail(422, "Not an Excel workbook"));
    }
    let rels = package.relations(part)?;
    let date1904 = book
        .descendants()
        .find(|n| tag(*n, S, "workbookPr"))
        .is_some_and(|n| matches!(n.attribute("date1904"), Some("1" | "true")));
    let mut strings = vec![];
    let mut styles = vec![];
    for r in rels.values() {
        if r.kind.ends_with("/sharedStrings") {
            if r.external {
                return Err(fail(422, "External shared strings"));
            }
            let doc = package.document(&r.target)?;
            for n in doc.descendants().filter(|n| tag(*n, S, "si")) {
                if strings.len() >= MAX_FRAGMENTS {
                    return Err(fail(413, "Too many shared strings"));
                }
                strings.push(rich(n));
            }
        } else if r.kind.ends_with("/styles") {
            if r.external {
                return Err(fail(422, "External cell styles"));
            }
            let doc = package.document(&r.target)?;
            let formats: BTreeMap<u32, String> = doc
                .descendants()
                .filter(|n| tag(*n, S, "numFmt"))
                .map(|n| {
                    Ok((
                        required(n.attribute("numFmtId"))?
                            .parse()
                            .map_err(|_| fail(422, "Invalid number format"))?,
                        required(n.attribute("formatCode"))?.into(),
                    ))
                })
                .collect::<Result<_>>()?;
            for n in doc
                .descendants()
                .filter(|n| tag(*n, S, "xf") && n.parent().is_some_and(|p| tag(p, S, "cellXfs")))
            {
                let id = n
                    .attribute("numFmtId")
                    .unwrap_or("0")
                    .parse::<u32>()
                    .map_err(|_| fail(422, "Invalid style number format"))?;
                if id >= 164 && !formats.contains_key(&id) {
                    return Err(fail(422, "Missing custom number format"));
                }
                styles.push(date_format(id, formats.get(&id)));
            }
        }
    }
    let mut names = BTreeSet::new();
    for (i, node) in book
        .descendants()
        .filter(|n| tag(*n, S, "sheet"))
        .enumerate()
    {
        if i >= 100 {
            return Err(fail(413, "Too many worksheets"));
        }
        let name = required(node.attribute("name"))?;
        if name.chars().count() > 100 || !names.insert(name) {
            return Err(fail(422, "Invalid or repeated sheet name"));
        }
        let part = rel_part(&rels, required(attr(node, R, "id"))?, "worksheet")?;
        let doc = package.document(part)?;
        if !tag(doc.root_element(), S, "worksheet") {
            return Err(fail(422, "Invalid worksheet root"));
        }
        let mut sheet = Sheet {
            name: name.into(),
            cells: vec![],
        };
        let mut seen = BTreeSet::new();
        let mut formula_ranges = vec![];
        for f in doc
            .descendants()
            .filter(|n| tag(*n, S, "f") && n.attribute("ref").is_some())
        {
            let range = required(f.attribute("ref"))?;
            let (start, end) = range.split_once(':').unwrap_or((range, range));
            let (r1, c1) = address(start)?;
            let (r2, c2) = address(end)?;
            let origin = required(f.parent().and_then(|n| n.attribute("r")))?;
            let (origin_row, origin_column) = address(origin)?;
            if r1 > r2
                || c1 > c2
                || !(r1..=r2).contains(&origin_row)
                || !(c1..=c2).contains(&origin_column)
                || formula_ranges.len() >= 1000
            {
                return Err(fail(422, "Invalid or excessive formula ranges"));
            }
            formula_ranges.push((r1, c1, r2, c2, origin));
        }
        for n in doc
            .descendants()
            .filter(|n| tag(*n, S, "c") && n.parent().is_some_and(|p| tag(p, S, "row")))
        {
            let reference = required(n.attribute("r"))?;
            let (row, column) = address(reference)?;
            if n.parent()
                .and_then(|r| r.attribute("r"))
                .is_some_and(|r| r.parse::<usize>().ok() != Some(row))
            {
                return Err(fail(422, "Cell address conflicts with its row"));
            }
            if !seen.insert(reference) || seen.len() > MAX_FRAGMENTS {
                return Err(fail(422, "Duplicate or excessive cells"));
            }
            let raw = n
                .children()
                .find(|n| tag(*n, S, "v"))
                .map(text)
                .unwrap_or_default();
            let formula = n.children().find(|n| tag(*n, S, "f")).map(text);
            // Sparse cells only: never allocate the declared array/spill rectangle.
            let formula_origin = formula_ranges
                .iter()
                .find(|(r1, c1, r2, c2, _)| {
                    (*r1..=*r2).contains(&row) && (*c1..=*c2).contains(&column)
                })
                .map(|v| v.4.to_owned());
            if formula.is_some() || formula_origin.is_some() {
                out.note("formulas_not_evaluated");
            }
            let style = n
                .attribute("s")
                .unwrap_or("0")
                .parse::<usize>()
                .map_err(|_| fail(422, "Invalid cell style"))?;
            if (!styles.is_empty() && style >= styles.len()) || (styles.is_empty() && style > 0) {
                return Err(fail(422, "Missing cell style"));
            }
            let (kind, value) = match n.attribute("t").unwrap_or("n") {
                "s" => (
                    "string",
                    json!(
                        strings
                            .get(
                                raw.parse::<usize>()
                                    .map_err(|_| fail(422, "Invalid shared string index"))?
                            )
                            .ok_or_else(|| fail(422, "Missing shared string"))?
                    ),
                ),
                "inlineStr" => ("string", json!(rich(n))),
                "str" => ("string", json!(raw)),
                "b" => {
                    if raw != "0" && raw != "1" {
                        return Err(fail(422, "Invalid boolean cell"));
                    }
                    ("boolean", json!(raw == "1"))
                }
                "e" => ("error", json!(raw)),
                "d" => ("date", json!({"iso_date":raw})),
                "n" if raw.is_empty() => ("empty", Value::Null),
                "n" => {
                    if crate::compute::number(&json!(raw)).is_none() {
                        return Err(fail(422, "Invalid or excessive numeric cell"));
                    }
                    if styles.get(style) == Some(&true) {
                        (
                            "date",
                            json!({"excel_serial":raw,"date_system":if date1904 {1904} else {1900}}),
                        )
                    } else {
                        ("number", json!(raw))
                    }
                }
                _ => return Err(fail(422, "Unsupported cell type")),
            };
            if formula.is_none() && formula_origin.is_none() && (value.is_null() || value == "") {
                continue;
            }
            let display = if let Some(formula) = &formula {
                format!("={formula} [cached: {value}]")
            } else if let Some(origin) = &formula_origin {
                format!("[formula cache from {origin}: {value}]")
            } else {
                value
                    .as_str()
                    .map(str::to_owned)
                    .unwrap_or_else(|| value.to_string())
            };
            out.push(format!("sheet:{}/cell:{reference}", i + 1), display)?;
            sheet.cells.push(Cell {
                address: reference.into(),
                row,
                column,
                kind: kind.into(),
                value,
                formula,
                formula_origin,
            });
        }
        sheet.cells.sort_by_key(|c| (c.row, c.column));
        out.sheets.push(sheet);
    }
    Ok(())
}
struct TextOutput {
    text: Vec<u8>,
}
impl Write for TextOutput {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        if self.text.len() + bytes.len() > 4 * MAX_TEXT {
            return Err(std::io::Error::other("PDF text limit"));
        }
        self.text.extend_from_slice(bytes);
        Ok(bytes.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}
fn pdf(bytes: &[u8], out: &mut Extraction) -> Result<()> {
    let doc =
        lopdf::Document::load_mem(bytes).map_err(|_| fail(422, "Invalid or encrypted PDF"))?;
    if doc.is_encrypted() || doc.encryption_state.is_some() {
        return Err(fail(422, "Encrypted PDFs require an unencrypted copy"));
    }
    let expected = doc
        .catalog()
        .and_then(|catalog| catalog.get(b"Pages"))
        .and_then(lopdf::Object::as_reference)
        .and_then(|id| doc.get_dictionary(id))
        .and_then(|pages| pages.get(b"Count"))
        .and_then(lopdf::Object::as_i64)
        .map_err(|_| fail(422, "Invalid PDF page tree"))?;
    if !(1..=500).contains(&expected) || doc.objects.len() > 100_000 {
        return Err(fail(413, "PDF page/object limits exceeded"));
    }
    let pages = doc.get_pages();
    if pages.len() as i64 != expected {
        return Err(fail(422, "Incomplete PDF page tree"));
    }
    if doc.objects.values().any(|o| {
        o.as_stream().is_ok_and(|s| {
            s.dict
                .get(b"Subtype")
                .and_then(lopdf::Object::as_name)
                .is_ok_and(|s| s == b"Image")
        })
    }) {
        out.note("visual_or_embedded_content_omitted");
    }
    for page in pages.keys() {
        let mut text = TextOutput { text: vec![] };
        let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            let writer: &mut dyn Write = &mut text;
            pdf_extract::output_doc_page(
                &doc,
                &mut pdf_extract::PlainTextOutput::new(writer),
                *page,
            )
        }));
        if !matches!(result, Ok(Ok(()))) {
            out.note(&format!("page:{page}:extraction_failed"));
            continue;
        }
        let text = String::from_utf8(text.text).map_err(|_| fail(422, "PDF text is not UTF-8"))?;
        if text.trim().is_empty() {
            out.note(&format!("page:{page}:no_text"));
        }
        for (i, line) in text.lines().enumerate() {
            out.push(format!("page:{page}/line:{}", i + 1), line.into())?;
        }
    }
    Ok(())
}
pub(crate) fn parse(format: &str, bytes: &[u8]) -> Result<Extraction> {
    let mut out = Extraction::new(format, "text");
    match format {
        "pdf" => pdf(bytes, &mut out)?,
        "docx" | "pptx" | "xlsx" => {
            let package = Package::open(bytes)?;
            out.info.notes = package.notes.clone();
            let part = package.main_part()?;
            match format {
                "docx" => word(&package, &part, &mut out)?,
                "pptx" => slides(&package, &part, &mut out)?,
                _ => workbook(&package, &part, &mut out)?,
            }
        }
        _ => return Err(fail(422, "Unsupported document worker format")),
    }
    out.finish()
}

pub type Rows = Vec<BTreeMap<String, Value>>;
pub fn table(extraction: &Extraction, index: usize) -> Result<(Rows, BTreeSet<String>)> {
    if extraction.info.status == "failed" {
        return Err(fail(
            422,
            extraction
                .info
                .error
                .as_deref()
                .unwrap_or("Document extraction failed"),
        ));
    }
    let sheet = extraction
        .sheets
        .get(
            index
                .checked_sub(1)
                .ok_or_else(|| fail(422, "Sheet numbers start at one"))?,
        )
        .ok_or_else(|| fail(422, "Worksheet is unavailable"))?;
    let first = sheet
        .cells
        .first()
        .ok_or_else(|| fail(422, "Worksheet has no header"))?
        .row;
    let last = sheet.cells.last().map_or(first, |c| c.row);
    let width = sheet.cells.iter().map(|c| c.column).max().unwrap_or(0);
    let headers: BTreeMap<_, _> = sheet
        .cells
        .iter()
        .filter(|c| c.row == first)
        .map(|c| (c.column, c))
        .collect();
    let mut names = vec![];
    let mut unique = BTreeSet::new();
    for column in 1..=width {
        let cell = headers
            .get(&column)
            .ok_or_else(|| fail(422, "Worksheet header has empty cells"))?;
        let name = cell
            .value
            .as_str()
            .filter(|s| !s.trim().is_empty())
            .ok_or_else(|| fail(422, "Worksheet headers must be strings"))?;
        if cell.kind != "string"
            || cell.formula.is_some()
            || cell.formula_origin.is_some()
            || name.chars().count() > 300
            || !unique.insert(name)
        {
            return Err(fail(
                422,
                "Worksheet headers must be unique literal strings",
            ));
        }
        names.push(name.to_owned());
    }
    let mut rows = vec![BTreeMap::new(); last - first];
    for cell in sheet.cells.iter().filter(|c| c.row > first) {
        // Never treat an unevaluated formula/error (or its stale cache) as data.
        let value =
            if cell.formula.is_some() || cell.formula_origin.is_some() || cell.kind == "error" {
                Value::Null
            } else {
                cell.value.clone()
            };
        rows[cell.row - first - 1].insert(names[cell.column - 1].clone(), value);
    }
    Ok((rows, names.into_iter().collect()))
}
