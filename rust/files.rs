//! Descriptor-relative, no-follow filesystem access on supported Unix hosts.
use crate::{
    Result, bounded, fail, hash,
    model::{Fragment, ProjectSpec},
    random_id,
};
use globset::Glob;
use rustix::fs::{self as at, Mode, OFlags};
use std::{
    collections::BTreeMap,
    fs::{self, File},
    io::{Read, Write},
    os::{fd::OwnedFd, unix::fs::PermissionsExt},
    path::{Component, Path},
};

pub const MAX_FILE: usize = 10 * 1024 * 1024;
pub const MAX_TOTAL: usize = 50 * 1024 * 1024;
pub fn relative(path: &str) -> Result<()> {
    bounded(path, 1, 240, "path")?;
    if path.contains(['\\', '\0', ':'])
        || path
            .split('/')
            .any(|p| p.is_empty() || p == "." || p == "..")
    {
        return Err(fail(422, "Path must be normalized and project-relative"));
    }
    Ok(())
}
pub fn excluded(path: &str, spec: &ProjectSpec) -> bool {
    let fixed = [
        ".git",
        ".filewise",
        ".filewise-rust",
        ".codex",
        ".claude",
        ".pi",
        ".venv",
        "node_modules",
        "target",
        "__pycache__",
        ".ds_store",
        ".env",
        ".env.*",
        "*tokens*.json",
        "*.pem",
        "*.key",
        "*.db",
        "*.db-*",
        "*.sqlite*",
        "*.p12",
        "*.pfx",
        "id_rsa",
        "id_ed25519",
        ".ssh",
        ".aws",
        ".filewise-write-*",
    ];
    let matches = |pattern: &str, path: &str| {
        let Ok(glob) = Glob::new(pattern) else {
            return true;
        };
        let matcher = glob.compile_matcher();
        matcher.is_match(path) || path.split('/').any(|part| matcher.is_match(part))
    };
    let folded = path.to_ascii_lowercase();
    fixed.iter().any(|p| matches(p, &folded)) || spec.excludes.iter().any(|p| matches(p, path))
}
pub fn included(path: &str, spec: &ProjectSpec) -> bool {
    spec.includes.iter().any(|p| {
        Glob::new(p).is_ok_and(|g| g.compile_matcher().is_match(path))
            || p.strip_prefix("**/")
                .is_some_and(|p| Glob::new(p).is_ok_and(|g| g.compile_matcher().is_match(path)))
    })
}
fn directory(path: &Path, create: bool) -> Result<OwnedFd> {
    if !path.is_absolute() || path.components().any(|p| matches!(p, Component::ParentDir)) {
        return Err(fail(422, "Normalized absolute directory required"));
    }
    let mut dir = at::open(
        "/",
        OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        Mode::empty(),
    )?;
    for part in path.components() {
        let Component::Normal(part) = part else {
            continue;
        };
        match at::openat(
            &dir,
            part,
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::empty(),
        ) {
            Ok(next) => dir = next,
            Err(e) if create && e == rustix::io::Errno::NOENT => {
                at::mkdirat(&dir, part, Mode::from_raw_mode(0o700))?;
                dir = at::openat(
                    &dir,
                    part,
                    OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
                    Mode::empty(),
                )?;
            }
            Err(e) => return Err(e.into()),
        }
    }
    Ok(dir)
}
fn read_at(dir: &OwnedFd, name: &str) -> Result<Option<(Vec<u8>, u32)>> {
    let fd = match at::openat(
        dir,
        name,
        OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC,
        Mode::empty(),
    ) {
        Ok(fd) => fd,
        Err(e) if e == rustix::io::Errno::NOENT => return Ok(None),
        Err(e) => return Err(e.into()),
    };
    let file = File::from(fd);
    let meta = file.metadata()?;
    if !meta.is_file() || meta.len() > MAX_FILE as u64 {
        return Err(fail(413, "Not a bounded regular file"));
    }
    let mut bytes = vec![];
    file.take((MAX_FILE + 1) as u64).read_to_end(&mut bytes)?;
    if bytes.len() > MAX_FILE {
        return Err(fail(413, "File exceeds 10 MiB"));
    }
    Ok(Some((bytes, meta.permissions().mode() & 0o777)))
}
pub fn read(root: &Path, path: &str) -> Result<Option<Vec<u8>>> {
    relative(path)?;
    let full = root.join(path);
    let parent = match directory(full.parent().ok_or_else(|| fail(422, "No parent"))?, false) {
        Ok(p) => p,
        Err(e) if e.message.contains("No such file") => return Ok(None),
        Err(e) => return Err(e),
    };
    Ok(read_at(
        &parent,
        full.file_name()
            .and_then(|s| s.to_str())
            .ok_or_else(|| fail(422, "Invalid name"))?,
    )?
    .map(|x| x.0))
}
pub fn replace(
    root: &Path,
    path: &str,
    bytes: Option<&[u8]>,
    expected: Option<&str>,
) -> Result<()> {
    relative(path)?;
    // Open the registered root before creating any relative directories.
    let _root = directory(root, false)?;
    let full = root.join(path);
    let parent = directory(
        full.parent().ok_or_else(|| fail(422, "No parent"))?,
        bytes.is_some(),
    )?;
    let name = full
        .file_name()
        .and_then(|s| s.to_str())
        .ok_or_else(|| fail(422, "Invalid name"))?;
    let old = read_at(&parent, name)?;
    if old.as_ref().map(|x| hash(&x.0)).as_deref() != expected {
        return Err(fail(
            409,
            format!("Original changed before writeback: {path}"),
        ));
    }
    if let Some(bytes) = bytes {
        if bytes.len() > MAX_FILE {
            return Err(fail(413, "File exceeds 10 MiB"));
        }
        let temporary = format!(".filewise-write-{}", random_id());
        let mode = old.map(|x| x.1).unwrap_or(0o600);
        let result = (|| -> Result<()> {
            let fd = at::openat(
                &parent,
                temporary.as_str(),
                OFlags::WRONLY | OFlags::CREATE | OFlags::EXCL | OFlags::NOFOLLOW | OFlags::CLOEXEC,
                Mode::from_raw_mode(mode as _),
            )?;
            let mut file = File::from(fd);
            file.write_all(bytes)?;
            file.set_permissions(fs::Permissions::from_mode(mode))?;
            file.sync_all()?;
            if read_at(&parent, name)?
                .as_ref()
                .map(|x| hash(&x.0))
                .as_deref()
                != expected
            {
                return Err(fail(409, "Original changed during writeback"));
            }
            at::renameat(&parent, temporary.as_str(), &parent, name)?;
            sync_written_directory(&parent)?;
            Ok(())
        })();
        if result.is_err() {
            let _ = at::unlinkat(&parent, temporary.as_str(), at::AtFlags::empty());
        }
        result?;
    } else if old.is_some() {
        at::unlinkat(&parent, name, at::AtFlags::empty())?;
        sync_written_directory(&parent)?;
    }
    Ok(())
}
/// A no-clobber, same-filesystem move that preserves the inode, ACLs and xattrs.
/// The short-lived second hard link is covered by Store's durable recovery intent.
pub fn move_file(root: &Path, from: &str, to: &str, expected: &str) -> Result<()> {
    relative(from)?;
    relative(to)?;
    let source = root.join(from);
    let target = root.join(to);
    let src_dir = directory(
        source
            .parent()
            .ok_or_else(|| fail(422, "Missing source parent"))?,
        false,
    )?;
    let dst_dir = directory(
        target
            .parent()
            .ok_or_else(|| fail(422, "Missing target parent"))?,
        true,
    )?;
    let src = source
        .file_name()
        .and_then(|n| n.to_str())
        .ok_or_else(|| fail(422, "Invalid source name"))?;
    let dst = target
        .file_name()
        .and_then(|n| n.to_str())
        .ok_or_else(|| fail(422, "Invalid target name"))?;
    if read_at(&src_dir, src)?
        .as_ref()
        .map(|v| hash(&v.0))
        .as_deref()
        != Some(expected)
        || read_at(&dst_dir, dst)?.is_some()
    {
        return Err(fail(409, "Move conflict; original or destination changed"));
    }
    // linkat is exclusive at the destination; unlike renameat it cannot overwrite a racing file.
    at::linkat(&src_dir, src, &dst_dir, dst, at::AtFlags::empty())?;
    let result = (|| -> Result<()> {
        let a = at::statat(&src_dir, src, at::AtFlags::SYMLINK_NOFOLLOW)?;
        let b = at::statat(&dst_dir, dst, at::AtFlags::SYMLINK_NOFOLLOW)?;
        if a.st_dev != b.st_dev
            || a.st_ino != b.st_ino
            || read_at(&src_dir, src)?
                .as_ref()
                .map(|v| hash(&v.0))
                .as_deref()
                != Some(expected)
            || read_at(&dst_dir, dst)?
                .as_ref()
                .map(|v| hash(&v.0))
                .as_deref()
                != Some(expected)
        {
            return Err(fail(409, "Move conflict; preserve external edits"));
        }
        at::fsync(&dst_dir)?;
        at::unlinkat(&src_dir, src, at::AtFlags::empty())?;
        at::fsync(&src_dir)?;
        Ok(())
    })();
    result.map_err(|mut e| {
        e.write_applied = true;
        e
    })
}
fn sync_written_directory(parent: &OwnedFd) -> Result<()> {
    at::fsync(parent).map_err(|error| {
        let mut error = crate::Error::from(error);
        error.write_applied = true;
        error
    })
}
pub fn scan(root: &Path, spec: &ProjectSpec) -> Result<BTreeMap<String, Vec<u8>>> {
    let _ = directory(root, false)?;
    let mut pending = vec![root.to_path_buf()];
    let mut files = BTreeMap::new();
    let mut total = 0;
    while let Some(dir) = pending.pop() {
        for entry in fs::read_dir(&dir)? {
            let entry = entry?;
            let full = entry.path();
            let rel = full
                .strip_prefix(root)
                .map_err(|_| fail(409, "Root changed"))?
                .to_str()
                .ok_or_else(|| fail(422, "Paths must be UTF-8"))?
                .to_owned();
            if excluded(&rel, spec) {
                continue;
            }
            let kind = entry.file_type()?;
            if kind.is_symlink() {
                continue;
            }
            if kind.is_dir() {
                let _ = directory(&full, false)?;
                pending.push(full);
                continue;
            }
            if !kind.is_file() || !included(&rel, spec) {
                continue;
            }
            let bytes =
                read(root, &rel)?.ok_or_else(|| fail(409, "File disappeared during scan"))?;
            total += bytes.len();
            if total > MAX_TOTAL || files.len() >= 1000 {
                return Err(fail(413, "Project exceeds 1,000 files or 50 MiB"));
            }
            files.insert(rel, bytes);
        }
    }
    Ok(files)
}
pub fn extract(path: &str, bytes: &[u8]) -> Result<crate::documents::Extraction> {
    if crate::documents::supported(path) {
        return Ok(crate::documents::extract(path, bytes));
    }
    let fragments = text_fragments(path, bytes)?;
    let format = crate::documents::format(path);
    let is_text = std::str::from_utf8(bytes).is_ok_and(|s| !s.contains('\0'))
        && !bytes.starts_with(b"%PDF-")
        && !bytes.starts_with(b"PK\x03\x04")
        && !matches!(
            format.as_str(),
            "doc"
                | "xls"
                | "ppt"
                | "odt"
                | "ods"
                | "odp"
                | "epub"
                | "zip"
                | "gz"
                | "7z"
                | "png"
                | "jpg"
                | "jpeg"
                | "gif"
                | "webp"
                | "mp3"
                | "mp4"
                | "mov"
                | "wav"
        );
    let mut result = crate::documents::Extraction::new(
        &format,
        if is_text {
            if fragments.is_empty() {
                "no_text"
            } else {
                "text"
            }
        } else {
            "unsupported"
        },
    );
    if is_text {
        result.fragments = fragments;
    }
    result.info.fragment_count = result.fragments.len();
    Ok(result)
}
pub fn fragments(path: &str, bytes: &[u8]) -> Result<Vec<Fragment>> {
    Ok(extract(path, bytes)?.fragments)
}
fn text_fragments(path: &str, bytes: &[u8]) -> Result<Vec<Fragment>> {
    // Containers and mismatched extensions are never treated as raw UTF-8 text.
    if bytes.starts_with(b"%PDF-") || bytes.starts_with(b"PK\x03\x04") {
        return Ok(vec![]);
    }
    let extension = Path::new(path)
        .extension()
        .and_then(|s| s.to_str())
        .unwrap_or_default()
        .to_ascii_lowercase();
    if matches!(
        extension.as_str(),
        "pdf"
            | "doc"
            | "docx"
            | "xls"
            | "xlsx"
            | "ppt"
            | "pptx"
            | "odt"
            | "ods"
            | "odp"
            | "epub"
            | "zip"
            | "gz"
            | "7z"
            | "png"
            | "jpg"
            | "jpeg"
            | "gif"
            | "webp"
            | "mp3"
            | "mp4"
            | "mov"
            | "wav"
    ) {
        return Ok(vec![]);
    }
    let Ok(text) = std::str::from_utf8(bytes) else {
        return Ok(vec![]);
    };
    if text.contains('\0') {
        return Ok(vec![]);
    }
    let text = text.strip_prefix('\u{feff}').unwrap_or(text);
    if text.chars().count() > 2_000_000 {
        return Err(fail(413, "Extracted text exceeds limit"));
    }
    let mut result = vec![];
    if path.to_ascii_lowercase().ends_with(".csv") {
        let mut reader = csv::ReaderBuilder::new()
            .has_headers(false)
            .flexible(true)
            .from_reader(text.as_bytes());
        for (r, row) in reader.records().enumerate() {
            let row = row.map_err(|e| fail(422, e.to_string()))?;
            for (c, value) in row.iter().enumerate() {
                if !value.trim().is_empty() {
                    result.push(Fragment {
                        locator: format!("row:{}/col:{}", r + 1, c + 1),
                        text: value.into(),
                    });
                }
            }
            if result.len() > 20000 {
                return Err(fail(413, "Too many fragments"));
            }
        }
    } else {
        for (i, line) in text.lines().enumerate() {
            if !line.trim().is_empty() {
                result.push(Fragment {
                    locator: format!("line:{}", i + 1),
                    text: line.into(),
                });
            }
            if i >= 20000 {
                return Err(fail(413, "Too many text lines"));
            }
        }
    }
    Ok(result)
}
