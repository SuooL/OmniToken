//! Where the panel points, and how that survives a restart.
//!
//! Only the server address lives here. The panel reads and never writes, and
//! every read endpoint is unauthenticated by design (ADR-0008) — so there is no
//! token to keep, and nothing here is a secret.

use std::path::PathBuf;

use serde::{Deserialize, Serialize};
use tauri::Manager;

/// Where `omnitoken serve` listens out of the box. Defaulting to it means the
/// common case — server on this machine — needs no setup at all.
pub const DEFAULT_SERVER: &str = "http://127.0.0.1:8787";

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq)]
pub struct Settings {
    pub server: String,
}

impl Default for Settings {
    fn default() -> Self {
        Self {
            server: DEFAULT_SERVER.to_string(),
        }
    }
}

/// Turn what a person would actually type into a base URL.
///
/// `192.168.1.10:8787` is what someone reads off another machine, so a missing
/// scheme is filled in rather than rejected. The path is kept — a server behind
/// a reverse proxy can legitimately live at `/omnitoken` — but the trailing
/// slash goes, because callers append `/api/v1/...` to this.
pub fn normalize(input: &str) -> Result<String, String> {
    let raw = input.trim();
    if raw.is_empty() {
        return Err("请填写服务端地址".into());
    }
    let with_scheme = if raw.contains("://") {
        raw.to_string()
    } else {
        format!("http://{raw}")
    };

    let mut url = reqwest::Url::parse(&with_scheme).map_err(|e| format!("地址无法解析:{e}"))?;
    match url.scheme() {
        "http" | "https" => {}
        other => return Err(format!("不支持的协议:{other}")),
    }
    if url.host_str().unwrap_or("").is_empty() {
        return Err("地址缺少主机名".into());
    }
    // A query or fragment on a base URL would end up in the middle of every
    // request path once `/api/v1/...` is appended.
    url.set_query(None);
    url.set_fragment(None);

    Ok(url.as_str().trim_end_matches('/').to_string())
}

fn config_path(app: &tauri::AppHandle) -> Result<PathBuf, String> {
    let dir = app
        .path()
        .app_config_dir()
        .map_err(|e| format!("无法定位配置目录:{e}"))?;
    Ok(dir.join("settings.json"))
}

/// Read the stored settings, falling back to the default.
///
/// A missing, unreadable, or malformed file all mean the same thing to the
/// user — "not configured" — and none of them should stop the panel from
/// opening. Falling back also means a torn write cannot brick startup: the next
/// save overwrites it.
pub fn load(app: &tauri::AppHandle) -> Settings {
    config_path(app)
        .ok()
        .and_then(|p| std::fs::read(p).ok())
        .and_then(|b| serde_json::from_slice::<Settings>(&b).ok())
        .unwrap_or_default()
}

pub fn save(app: &tauri::AppHandle, settings: &Settings) -> Result<(), String> {
    let path = config_path(app)?;
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
    }
    let body = serde_json::to_vec_pretty(settings).map_err(|e| e.to_string())?;
    std::fs::write(&path, body).map_err(|e| format!("{}: {e}", path.display()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fills_in_a_missing_scheme() {
        assert_eq!(
            normalize("192.168.1.10:8787").unwrap(),
            "http://192.168.1.10:8787"
        );
        assert_eq!(
            normalize("localhost:8787").unwrap(),
            "http://localhost:8787"
        );
    }

    #[test]
    fn trims_whitespace_and_trailing_slash() {
        assert_eq!(
            normalize("  http://a.example:8787/  ").unwrap(),
            "http://a.example:8787"
        );
        assert_eq!(
            normalize("https://a.example///").unwrap(),
            "https://a.example"
        );
    }

    #[test]
    fn keeps_a_reverse_proxy_path_prefix() {
        assert_eq!(
            normalize("https://a.example/omnitoken/").unwrap(),
            "https://a.example/omnitoken"
        );
    }

    #[test]
    fn drops_query_and_fragment() {
        assert_eq!(
            normalize("http://a.example:8787/?x=1#y").unwrap(),
            "http://a.example:8787"
        );
    }

    #[test]
    fn rejects_what_cannot_be_a_base() {
        assert!(normalize("").is_err());
        assert!(normalize("   ").is_err());
        assert!(normalize("ftp://a.example").is_err());
        assert!(normalize("http://").is_err());
    }

    #[test]
    fn default_is_a_valid_base() {
        assert_eq!(normalize(DEFAULT_SERVER).unwrap(), DEFAULT_SERVER);
    }
}
