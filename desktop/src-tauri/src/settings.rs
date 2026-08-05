//! Where the panel points, and how that survives a restart.
//!
//! ADR-0008 said "address only, no token", on the grounds that every read
//! endpoint was unauthenticated. ADR-0016 revised that: a server reachable from
//! another machine authenticates its reads, and a menubar client pointed at one
//! is exactly that case. So a token lives here too.
//!
//! It is stored in plain text in the app config dir, like the address. That is a
//! deliberate, limited choice: the token is a LAN/tailnet bearer credential the
//! user also pastes into a browser's localStorage, and the Keychain would add a
//! prompt and a platform dependency without changing who can read the file — any
//! process running as this user already can. Documented rather than hidden.

use std::path::PathBuf;

use serde::{Deserialize, Serialize};
use tauri::Manager;

/// Where `omnitoken serve` listens out of the box. Defaulting to it means the
/// common case — server on this machine — needs no setup at all.
pub const DEFAULT_SERVER: &str = "http://127.0.0.1:8787";

/// What, if anything, the tray prints beside the gauge.
///
/// Off by default: the menubar is scarce space shared with every other utility,
/// and the arc already answers "roughly how full". Wanting the exact figure on
/// screen is a preference, not the default (ADR-0014).
#[derive(Serialize, Deserialize, Clone, Copy, Debug, PartialEq, Eq, Default)]
#[serde(rename_all = "lowercase")]
pub enum TrayTitle {
    #[default]
    Off,
    /// The tightest quota, as a percentage.
    Quota,
    /// Live generation speed in tokens per second.
    Speed,
}

/// Every field carries `#[serde(default)]`, and that is load-bearing rather than
/// tidy: `load` treats any deserialisation failure as "not configured" and falls
/// back to `Default`. Adding a field without a default would make every existing
/// `settings.json` — which has only `server` — fail to parse, and the user's
/// saved address would silently revert to localhost on upgrade. There is a
/// regression test for exactly that.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq)]
pub struct Settings {
    #[serde(default = "default_server")]
    pub server: String,
    /// Bearer token for a server that authenticates reads (ADR-0016). Empty for
    /// the common case — a server on this machine, listening on loopback.
    #[serde(default)]
    pub token: String,
    /// Where the "完整面板" button opens in a browser, when that differs from the
    /// API address. Empty — the common case — falls back to `server`.
    ///
    /// The two can legitimately diverge: the app polls the API at a direct or
    /// mesh address with a bearer token it injects itself, while the human-facing
    /// dashboard may sit behind a reverse proxy that runs its own browser auth
    /// (e.g. Authelia) and injects the read token at its edge. Pointing `server`
    /// at that proxy would break the app's own polling — it cannot pass the
    /// browser login — so the panel destination is a separate field, not a reuse.
    #[serde(default)]
    pub panel_url: String,
    #[serde(default)]
    pub tray_title: TrayTitle,
    /// Warn before the wall, not after. Enabled by default because it is the
    /// most useful thing this app does unprompted, and it costs at most two
    /// notifications per quota window. macOS permission is requested lazily, on
    /// the first alert that would actually fire — so someone who never
    /// approaches a limit is never asked.
    #[serde(default = "enabled")]
    pub notify: bool,
    #[serde(default)]
    pub autostart: bool,
    /// Off by default: a global chord belongs to the user, so claiming one
    /// without being asked is not ours to do.
    #[serde(default)]
    pub hotkey: bool,
}

fn default_server() -> String {
    DEFAULT_SERVER.to_string()
}

fn enabled() -> bool {
    true
}

impl Default for Settings {
    fn default() -> Self {
        Self {
            server: default_server(),
            token: String::new(),
            panel_url: String::new(),
            tray_title: TrayTitle::default(),
            notify: true,
            autostart: false,
            hotkey: false,
        }
    }
}

impl Settings {
    /// The URL the "完整面板" button should open. Falls back to `server` so the
    /// common case — one address for both API and browser — needs no extra setup.
    pub fn panel_target(&self) -> String {
        if self.panel_url.trim().is_empty() {
            self.server.clone()
        } else {
            self.panel_url.clone()
        }
    }
}

/// The settings shape exposed over IPC. The bearer credential never crosses
/// into the webview; it gets only enough information to explain that a blank
/// token field will retain an existing credential.
#[derive(Serialize, Clone, Debug, PartialEq)]
pub struct SettingsView {
    pub server: String,
    pub has_token: bool,
    /// Not a secret, unlike the token: it is a plain browser destination the
    /// settings form needs to pre-fill, so it crosses into the webview as-is.
    pub panel_url: String,
    pub tray_title: TrayTitle,
    pub notify: bool,
    pub autostart: bool,
    pub hotkey: bool,
}

impl From<&Settings> for SettingsView {
    fn from(settings: &Settings) -> Self {
        Self {
            server: settings.server.clone(),
            has_token: !settings.token.is_empty(),
            panel_url: settings.panel_url.clone(),
            tray_title: settings.tray_title,
            notify: settings.notify,
            autostart: settings.autostart,
            hotkey: settings.hotkey,
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

/// Build and authenticate a replacement before the persisted settings change.
///
/// A blank token means "keep the credential already stored", because the
/// webview receives only `has_token` and can never pre-fill the secret itself.
pub async fn validate_candidate(
    current: &Settings,
    server: &str,
    token: &str,
    panel_url: &str,
) -> Result<Settings, String> {
    let mut candidate = current.clone();
    candidate.server = normalize(server)?;
    let token = token.trim();
    if !token.is_empty() {
        candidate.token = token.to_string();
    }
    // A blank panel URL is not a validation failure: it means "open the API
    // address in the browser", so it is stored as empty and `panel_target`
    // falls back to `server`. A non-blank one is normalized like any base URL —
    // but never probed, because the browser (not this app) authenticates it.
    candidate.panel_url = if panel_url.trim().is_empty() {
        String::new()
    } else {
        normalize(panel_url)?
    };

    crate::get_json(
        &candidate.server,
        "/api/v1/overview?days=1",
        &candidate.token,
    )
    .await
    .map_err(|error| error.to_string())?;
    Ok(candidate)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::net::TcpListener;
    use std::sync::mpsc;
    use std::thread;

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

    // The file written by the version that only knew about `server`. `load`
    // falls back to Default on any parse error, so a missing field here would
    // not surface as an error — it would quietly move the user's server back to
    // localhost on upgrade. That is why every field has a default.
    #[test]
    fn reads_a_file_written_before_the_other_fields_existed() {
        let s: Settings = serde_json::from_str(r#"{"server":"http://192.168.1.10:8787"}"#)
            .expect("legacy settings must still deserialise");
        assert_eq!(s.server, "http://192.168.1.10:8787");
        assert_eq!(s.tray_title, TrayTitle::Off);
        assert!(s.notify);
        assert!(!s.autostart);
        assert!(!s.hotkey);
        // Same trap as the others: a missing panel_url must default to empty so
        // the button keeps opening `server`, not revert anything.
        assert!(s.panel_url.is_empty());
    }

    // Also covers the other direction: an empty object is a plausible result of
    // a torn write, and it must not be an error either.
    #[test]
    fn an_empty_object_is_the_default() {
        let s: Settings = serde_json::from_str("{}").expect("empty object must deserialise");
        assert_eq!(s, Settings::default());
    }

    // A field this version does not know about must not fail the parse either —
    // otherwise downgrading, or a file touched by a newer build, would wipe the
    // address for the same reason.
    #[test]
    fn ignores_fields_it_does_not_know() {
        let s: Settings =
            serde_json::from_str(r#"{"server":"http://a.example:8787","from_the_future":42}"#)
                .expect("unknown fields must be ignored");
        assert_eq!(s.server, "http://a.example:8787");
    }

    #[test]
    fn tray_title_round_trips_through_json() {
        for t in [TrayTitle::Off, TrayTitle::Quota, TrayTitle::Speed] {
            let s = Settings {
                tray_title: t,
                ..Settings::default()
            };
            let back: Settings = serde_json::from_slice(&serde_json::to_vec(&s).unwrap()).unwrap();
            assert_eq!(back.tray_title, t);
        }
    }

    fn authenticated_server(
        required_token: &'static str,
    ) -> (String, mpsc::Receiver<String>, thread::JoinHandle<()>) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let address = format!("http://{}", listener.local_addr().unwrap());
        let (request_tx, request_rx) = mpsc::channel();
        let handle = thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut bytes = Vec::new();
            let mut buf = [0_u8; 1024];
            loop {
                let n = stream.read(&mut buf).unwrap();
                bytes.extend_from_slice(&buf[..n]);
                if n == 0 || bytes.windows(4).any(|window| window == b"\r\n\r\n") {
                    break;
                }
            }
            let request = String::from_utf8(bytes).unwrap();
            let authorized = request
                .lines()
                .any(|line| line == format!("authorization: Bearer {required_token}"));
            request_tx.send(request).unwrap();
            let (status, body) = if authorized {
                ("200 OK", "{}")
            } else {
                ("401 Unauthorized", r#"{"error":"unauthorized"}"#)
            };
            write!(
                stream,
                "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            )
            .unwrap();
        });
        (address, request_rx, handle)
    }

    #[test]
    fn valid_bearer_credential_passes_the_overview_probe() {
        let (server, request_rx, handle) = authenticated_server("valid-token");

        let candidate = tauri::async_runtime::block_on(validate_candidate(
            &Settings::default(),
            &server,
            "valid-token",
            "",
        ))
        .unwrap();

        let request = request_rx.recv().unwrap();
        handle.join().unwrap();
        assert!(request.starts_with("GET /api/v1/overview?days=1 HTTP/1.1\r\n"));
        assert!(request.contains("\r\nauthorization: Bearer valid-token\r\n"));
        assert_eq!(candidate.server, server);
        assert_eq!(candidate.token, "valid-token");
    }

    #[test]
    fn invalid_bearer_credential_fails_before_settings_can_be_saved() {
        let (server, request_rx, handle) = authenticated_server("valid-token");

        let result = tauri::async_runtime::block_on(validate_candidate(
            &Settings::default(),
            &server,
            "wrong-token",
            "",
        ));

        let request = request_rx.recv().unwrap();
        handle.join().unwrap();
        assert!(request.contains("\r\nauthorization: Bearer wrong-token\r\n"));
        assert!(result.unwrap_err().contains("401"));
    }

    #[test]
    fn missing_bearer_credential_fails_an_authenticated_probe() {
        let (server, request_rx, handle) = authenticated_server("valid-token");

        let result = tauri::async_runtime::block_on(validate_candidate(
            &Settings::default(),
            &server,
            "",
            "",
        ));

        let request = request_rx.recv().unwrap();
        handle.join().unwrap();
        assert!(!request.to_ascii_lowercase().contains("\r\nauthorization:"));
        assert!(result.unwrap_err().contains("401"));
    }

    #[test]
    fn blank_token_keeps_the_existing_credential() {
        let (server, request_rx, handle) = authenticated_server("saved-token");
        let current = Settings {
            server: server.clone(),
            token: "saved-token".into(),
            ..Settings::default()
        };

        let candidate =
            tauri::async_runtime::block_on(validate_candidate(&current, &server, "   ", ""))
                .unwrap();

        let request = request_rx.recv().unwrap();
        handle.join().unwrap();
        assert!(request.contains("\r\nauthorization: Bearer saved-token\r\n"));
        assert_eq!(candidate.token, "saved-token");
    }

    #[test]
    fn serialized_settings_view_redacts_the_token() {
        let settings = Settings {
            token: "never-send-this".into(),
            ..Settings::default()
        };

        let json = serde_json::to_value(SettingsView::from(&settings)).unwrap();

        assert_eq!(json["has_token"], true);
        assert!(json.get("token").is_none());
        assert!(!json.to_string().contains("never-send-this"));
    }

    #[test]
    fn panel_target_falls_back_to_server_when_unset() {
        let s = Settings {
            server: "http://192.168.10.124:8787".into(),
            panel_url: String::new(),
            ..Settings::default()
        };
        assert_eq!(s.panel_target(), "http://192.168.10.124:8787");

        // Whitespace is treated the same as empty, not as a real destination.
        let blank = Settings {
            panel_url: "   ".into(),
            ..s.clone()
        };
        assert_eq!(blank.panel_target(), "http://192.168.10.124:8787");
    }

    #[test]
    fn panel_target_prefers_the_explicit_panel_url() {
        let s = Settings {
            server: "http://192.168.10.124:8787".into(),
            panel_url: "https://omni.example.net".into(),
            ..Settings::default()
        };
        assert_eq!(s.panel_target(), "https://omni.example.net");
    }

    #[test]
    fn a_blank_panel_url_input_clears_the_field() {
        let (server, request_rx, handle) = authenticated_server("valid-token");
        let current = Settings {
            panel_url: "https://stale.example.net".into(),
            ..Settings::default()
        };

        let candidate = tauri::async_runtime::block_on(validate_candidate(
            &current,
            &server,
            "valid-token",
            "   ",
        ))
        .unwrap();

        request_rx.recv().unwrap();
        handle.join().unwrap();
        // Clearing the field means "open the API address again", so a previously
        // saved panel URL must not linger.
        assert!(candidate.panel_url.is_empty());
        assert_eq!(candidate.panel_target(), candidate.server);
    }

    #[test]
    fn a_panel_url_is_normalized_but_never_probed() {
        // The probe server answers exactly one request. If validate_candidate
        // ever probed the panel URL too, this test would hang or the single
        // accept would be the wrong one — so passing proves only `server` is hit.
        let (server, request_rx, handle) = authenticated_server("valid-token");

        let candidate = tauri::async_runtime::block_on(validate_candidate(
            &Settings::default(),
            &server,
            "valid-token",
            "  omni.example.net/dash/  ",
        ))
        .unwrap();

        let request = request_rx.recv().unwrap();
        handle.join().unwrap();
        // Scheme filled in, trailing slash trimmed — same rules as `server`.
        assert_eq!(candidate.panel_url, "http://omni.example.net/dash");
        // The one probe that happened was the overview call to `server`.
        assert!(request.starts_with("GET /api/v1/overview?days=1 HTTP/1.1\r\n"));
    }
}
