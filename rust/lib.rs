//! Rust-native Filewise. No Python interpreter or FFI backend.

pub mod compute;
pub mod daemon;
pub mod files;
pub mod http;
pub mod model;
pub mod store;
pub mod watch;

use serde_json::{Value, json};
use sha2::{Digest, Sha256};

#[derive(Debug, thiserror::Error)]
#[error("{message}")]
pub struct Error {
    pub status: u16,
    pub message: String,
    pub(crate) write_applied: bool,
}
pub type Result<T> = std::result::Result<T, Error>;
pub fn fail(status: u16, message: impl Into<String>) -> Error {
    Error {
        status,
        message: message.into(),
        write_applied: false,
    }
}
impl From<std::io::Error> for Error {
    fn from(e: std::io::Error) -> Self {
        fail(409, e.to_string())
    }
}
impl From<rusqlite::Error> for Error {
    fn from(e: rusqlite::Error) -> Self {
        fail(500, e.to_string())
    }
}
impl From<serde_json::Error> for Error {
    fn from(e: serde_json::Error) -> Self {
        fail(422, e.to_string())
    }
}
impl From<rustix::io::Errno> for Error {
    fn from(e: rustix::io::Errno) -> Self {
        fail(409, e.to_string())
    }
}
/// Stable UI error identifiers; diagnostic messages remain language-neutral for API clients.
pub fn error_code(message: &str) -> Option<&'static str> {
    let exact = [
        (
            "Folder rules changed; reload before saving",
            "rules_changed",
        ),
        (
            "Folder overlaps an existing project; register a separate folder",
            "overlap",
        ),
        (
            "Choose a directory separate from the private database",
            "separate",
        ),
        (
            "Manual metadata is preserved; automatic analysis will not refresh or replace it",
            "manual_metadata",
        ),
        (
            "Source access or historical version is no longer available",
            "source_unavailable",
        ),
        (
            "No extracted text; this format cannot be analyzed by the configured rules",
            "model_no_text",
        ),
        (
            "Rename blocked by project checks; originals were not changed",
            "rename_blocked",
        ),
        ("Undo conflict; preserve the current files", "undo_conflict"),
        (
            "Rules changed or monitoring paused; retry to use current rules",
            "rules_paused",
        ),
        (
            "Folder paused or rules changed; retry to use current rules",
            "rules_paused",
        ),
        (
            "Proposed name is outside the configured file scope",
            "scope",
        ),
        (
            "File or declarations changed after analysis; retry",
            "original_changed",
        ),
        (
            "Folder changed during capture; waiting for stability",
            "download",
        ),
        ("Resume monitoring before retrying", "resume"),
        (
            "Folder selection cancelled or unavailable; paste the path instead",
            "pick",
        ),
        (
            "Monitored root was replaced; pause and inspect the folder",
            "folder_replaced",
        ),
    ];
    exact
        .iter()
        .find(|(text, _)| *text == message)
        .map(|(_, code)| *code)
        .or_else(|| {
            if message.starts_with("Local model unavailable:") {
                Some("model_unavailable")
            } else if message.starts_with("JSON field is missing or format unsupported:") {
                Some("json_field")
            } else {
                None
            }
        })
}
impl axum::response::IntoResponse for Error {
    fn into_response(self) -> axum::response::Response {
        let status = axum::http::StatusCode::from_u16(self.status)
            .unwrap_or(axum::http::StatusCode::INTERNAL_SERVER_ERROR);
        let message = if self.status == 500 {
            "Internal storage error".to_owned()
        } else {
            self.message
        };
        (
            status,
            axum::Json(json!({"code":error_code(&message),"error":message})),
        )
            .into_response()
    }
}
pub fn hash(bytes: &[u8]) -> String {
    format!("{:x}", Sha256::digest(bytes))
}
pub fn digest(value: &impl serde::Serialize) -> Result<String> {
    Ok(hash(&serde_json::to_vec(value)?))
}
pub fn now() -> String {
    chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Micros, false)
}
pub fn timestamp(value: &str) -> Result<String> {
    chrono::DateTime::parse_from_rfc3339(value)
        .map(|d| {
            d.with_timezone(&chrono::Utc)
                .to_rfc3339_opts(chrono::SecondsFormat::Micros, false)
        })
        .map_err(|_| fail(422, "Timestamp must be RFC3339 with a timezone"))
}
pub fn random_id() -> String {
    use rand::RngCore;
    let mut bytes = [0u8; 32];
    rand::rngs::OsRng.fill_bytes(&mut bytes);
    hash(&bytes)
}
pub fn bounded(value: &str, min: usize, max: usize, name: &str) -> Result<()> {
    if value.chars().count() < min
        || value.chars().count() > max
        || (min > 0 && value.trim().is_empty())
    {
        return Err(fail(422, format!("Invalid {name} length")));
    }
    Ok(())
}
pub fn id(value: &str) -> Result<()> {
    bounded(value, 1, 128, "ID")?;
    if !value.as_bytes()[0].is_ascii_alphanumeric()
        || !value
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || b"_.:-".contains(&c))
    {
        return Err(fail(422, "Invalid ID"));
    }
    Ok(())
}
pub fn exact_version(value: &str) -> Result<()> {
    if value.len() != 64
        || !value
            .bytes()
            .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
    {
        return Err(fail(422, "A concrete 64-character version ID is required"));
    }
    Ok(())
}
/// Reject duplicate object keys before parsing request/data values.
pub fn strict_json(bytes: &[u8]) -> Result<Value> {
    struct Unique;
    impl<'de> serde::Deserialize<'de> for Unique {
        fn deserialize<D: serde::Deserializer<'de>>(d: D) -> std::result::Result<Self, D::Error> {
            struct Visitor;
            impl<'de> serde::de::Visitor<'de> for Visitor {
                type Value = Unique;
                fn expecting(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
                    f.write_str("JSON without duplicate keys")
                }
                fn visit_map<A: serde::de::MapAccess<'de>>(
                    self,
                    mut a: A,
                ) -> std::result::Result<Unique, A::Error> {
                    let mut keys = std::collections::BTreeSet::new();
                    while let Some((k, _)) = a.next_entry::<String, Unique>()? {
                        if !keys.insert(k) {
                            return Err(serde::de::Error::custom("Duplicate JSON key"));
                        }
                    }
                    Ok(Unique)
                }
                fn visit_seq<A: serde::de::SeqAccess<'de>>(
                    self,
                    mut a: A,
                ) -> std::result::Result<Unique, A::Error> {
                    while a.next_element::<Unique>()?.is_some() {}
                    Ok(Unique)
                }
                fn visit_bool<E: serde::de::Error>(
                    self,
                    _: bool,
                ) -> std::result::Result<Unique, E> {
                    Ok(Unique)
                }
                fn visit_i64<E: serde::de::Error>(self, _: i64) -> std::result::Result<Unique, E> {
                    Ok(Unique)
                }
                fn visit_u64<E: serde::de::Error>(self, _: u64) -> std::result::Result<Unique, E> {
                    Ok(Unique)
                }
                fn visit_f64<E: serde::de::Error>(self, v: f64) -> std::result::Result<Unique, E> {
                    if v.is_finite() {
                        Ok(Unique)
                    } else {
                        Err(E::custom("Nonfinite number"))
                    }
                }
                fn visit_str<E: serde::de::Error>(self, _: &str) -> std::result::Result<Unique, E> {
                    Ok(Unique)
                }
                fn visit_unit<E: serde::de::Error>(self) -> std::result::Result<Unique, E> {
                    Ok(Unique)
                }
            }
            d.deserialize_any(Visitor)
        }
    }
    let _: Unique = serde_json::from_slice(bytes)?;
    let value: Value = serde_json::from_slice(bytes)?;
    let mut pending = vec![&value];
    while let Some(item) = pending.pop() {
        match item {
            Value::Number(n) if n.as_f64().is_none_or(|v| !v.is_finite()) => {
                return Err(fail(
                    422,
                    "JSON numeric magnitude exceeds the finite interoperability range",
                ));
            }
            Value::Object(m) => pending.extend(m.values()),
            Value::Array(a) => pending.extend(a),
            _ => {}
        }
    }
    Ok(value)
}
