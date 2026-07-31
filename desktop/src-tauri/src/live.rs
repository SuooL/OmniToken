//! The realtime bridge: one SSE connection, four consumers (ADR-0014).
//!
//! ADR-0008 shipped v1 on polling — the panel every 15s, the tray every 60s, both
//! hitting `/api/v1/live` independently. That is two requests for one answer, and
//! it lets the two surfaces show snapshots from different minutes. Worse, on a
//! product whose whole thesis (M5) is realtime, the menubar was the laggiest
//! thing in it.
//!
//! So the stream lands here, in Rust, once. Not in the webview, for two reasons:
//! the server deliberately sends no CORS headers (ADR-0008), and the tray icon
//! can only be painted from this side anyway. One snapshot then feeds the
//! webview, the tray glyph, the tray title and the quota alerts — which is what
//! makes it impossible for them to disagree.
//!
//! When the stream cannot be held, this falls back to polling and **says so**.
//! A silently dead stream is more dangerous than polling ever was: polling that
//! stops shows a stale timestamp, whereas a frozen stream shows a plausible
//! number forever.

use std::collections::HashSet;
use std::sync::Mutex;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use eventsource_stream::Eventsource;
use futures_util::StreamExt;
use serde::Serialize;
use serde_json::Value;
use tauri::{Emitter, Manager};

use crate::settings::{self, TrayTitle};
use crate::{gauge, tray};

/// Which channel the figures on screen arrived on. Reported to the frontend so
/// the popover can label itself honestly.
#[derive(Serialize, Clone, Copy, PartialEq, Eq, Debug)]
#[serde(rename_all = "lowercase")]
pub enum Mode {
    /// Holding the stream; updates arrive within a second of the event.
    Live,
    /// Stream unavailable, but plain GET works. Figures move, just slowly.
    Polling,
}

/// The transport state for the snapshot currently displayed by the menu bar.
///
/// A failed refresh changes this state but does not destroy the last successful
/// payload. Consumers can therefore dim the old values and show their age
/// rather than rendering an outage as zero activity.
#[derive(Serialize, Clone, PartialEq, Eq, Debug)]
#[serde(rename_all = "lowercase")]
pub enum ConnectionKind {
    Live,
    Polling,
    Stale,
    Unauthorized,
    Offline,
}

impl ConnectionKind {
    fn as_str(&self) -> &'static str {
        match self {
            Self::Live => "live",
            Self::Polling => "polling",
            Self::Stale => "stale",
            Self::Unauthorized => "unauthorized",
            Self::Offline => "offline",
        }
    }
}

#[derive(Serialize, Clone, PartialEq, Eq, Debug)]
pub struct ConnectionState {
    pub kind: ConnectionKind,
    pub last_success_at_ms: Option<i64>,
    pub error: Option<String>,
}

impl Default for ConnectionState {
    fn default() -> Self {
        Self {
            kind: ConnectionKind::Offline,
            last_success_at_ms: None,
            error: None,
        }
    }
}

impl Mode {
    fn connection_kind(self) -> ConnectionKind {
        match self {
            Mode::Live => ConnectionKind::Live,
            Mode::Polling => ConnectionKind::Polling,
        }
    }
}

/// The last payload plus the state that tells consumers how trustworthy it is.
#[derive(Default, Clone)]
struct Snapshot {
    endpoint: Option<String>,
    data: Option<Value>,
    connection: ConnectionState,
}

impl Snapshot {
    fn bind_endpoint(&mut self, endpoint: &str) -> bool {
        let endpoint = normalize_endpoint_identity(endpoint);
        if self.endpoint.as_deref() == Some(endpoint.as_str()) {
            return false;
        }
        self.endpoint = Some(endpoint);
        self.data = None;
        self.connection = ConnectionState::default();
        true
    }

    fn accepts_endpoint(&mut self, endpoint: &str) -> bool {
        let endpoint = normalize_endpoint_identity(endpoint);
        match self.endpoint.as_deref() {
            Some(active) => active == endpoint,
            None => {
                self.endpoint = Some(endpoint);
                true
            }
        }
    }

    fn record_success(&mut self, endpoint: &str, payload: Value, now_ms: i64, mode: Mode) -> bool {
        if !self.accepts_endpoint(endpoint) {
            return false;
        }
        self.data = Some(payload);
        self.connection.last_success_at_ms = Some(now_ms);
        self.connection.error = None;
        self.connection.kind = mode.connection_kind();
        true
    }

    fn record_failure(
        &mut self,
        endpoint: &str,
        kind: ConnectionKind,
        error: impl Into<String>,
        now_ms: i64,
    ) -> bool {
        if !self.accepts_endpoint(endpoint) {
            return false;
        }
        self.connection.kind = if kind == ConnectionKind::Offline
            && self.data.is_some()
            && self
                .age_ms(now_ms)
                .is_some_and(|age| age >= stale_after_ms())
        {
            ConnectionKind::Stale
        } else {
            kind
        };
        self.connection.error = Some(error.into());
        true
    }

    fn data(&self) -> Option<&Value> {
        self.data.as_ref()
    }

    fn age_ms(&self, now_ms: i64) -> Option<i64> {
        self.connection
            .last_success_at_ms
            .map(|then| now_ms.saturating_sub(then))
    }
}

fn normalize_endpoint_identity(endpoint: &str) -> String {
    endpoint.trim_end_matches('/').to_string()
}

const MAX_POPOVER_ITEMS: usize = 3;

#[derive(Serialize, Clone, PartialEq, Eq, Debug)]
#[serde(rename_all = "lowercase")]
enum ActivityKind {
    Active,
    Idle,
    Unknown,
}

#[derive(Serialize, Clone, Debug)]
struct PopoverConnection {
    kind: ConnectionKind,
    generated_at_ms: Option<i64>,
    age_ms: Option<i64>,
    is_stale: bool,
    error: Option<String>,
}

#[derive(Serialize, Clone, Debug)]
struct ActivityView {
    kind: ActivityKind,
    text: String,
    rate: Option<f64>,
    session_count: usize,
    contributing_devices: usize,
}

#[derive(Serialize, Clone, Debug)]
struct SessionView {
    tool: String,
    repository: String,
    model: String,
    device: String,
    contribution_rate: Option<f64>,
    native_rate: Option<f64>,
}

#[derive(Serialize, Clone, Debug)]
struct DeviceView {
    name: String,
    state: String,
    has_procs: bool,
    running: i64,
}

/// Which official window the card's leading percentage comes from.
///
/// Three states, not two, because "the provider reports no 5-hour window" and
/// "the provider reports nothing at all" are different facts and only one of
/// them means the user is flying blind. Neither may be drawn as 0%.
#[derive(Serialize, Clone, Copy, PartialEq, Eq, Debug)]
#[serde(rename_all = "snake_case")]
enum QuotaBasis {
    FiveHour,
    Weekly,
    None,
}

/// One subscription source's official quota, in the shape the popover renders.
///
/// Every figure here comes from the provider. Our own rolling counts stay out:
/// side by side on a 420px card an inferred percentage is indistinguishable from
/// an authoritative one, which is exactly what "authoritative first, inferred
/// labelled" forbids.
#[derive(Serialize, Clone, Debug)]
struct QuotaView {
    source: String,
    label: String,
    basis: QuotaBasis,
    five_hour_percent: Option<f64>,
    projected_percent: Option<f64>,
    weekly_percent: Option<f64>,
    /// Minutes left on whichever window `basis` names.
    resets_in_minutes: Option<i64>,
}

#[derive(Serialize, Clone, Debug)]
struct PopoverView {
    connection: PopoverConnection,
    activity: ActivityView,
    quotas: Vec<QuotaView>,
    sessions: Vec<SessionView>,
    sessions_more: usize,
    devices: Vec<DeviceView>,
    devices_more: usize,
    device_online: usize,
    device_total: usize,
    device_summary: String,
}

fn source_label(source: &str) -> String {
    match source {
        "claude-code" => "Claude".to_string(),
        "codex" => "Codex".to_string(),
        "api" | "proxy" | "openai-api" | "anthropic-api" => "Other/API".to_string(),
        "" => "—".to_string(),
        other => other.to_string(),
    }
}

fn short_repository(repository: &str) -> String {
    repository
        .trim_end_matches(['/', '\\'])
        .rsplit(['/', '\\'])
        .next()
        .filter(|name| !name.is_empty())
        .unwrap_or("—")
        .to_string()
}

fn compact_rate(rate: f64) -> String {
    if (rate - rate.round()).abs() < 0.05 {
        format!("{rate:.0}")
    } else {
        format!("{rate:.1}")
    }
}

/// The sources that get a quota card, in display order. The same two
/// subscription channels `buildWindowCards` emits — pay-per-use has no quota to
/// report, so it has no card.
const QUOTA_SOURCES: [&str; 2] = ["claude-code", "codex"];

const WEEKLY_WINDOW_MINUTES: i64 = 10080;

/// The provider's live 5-hour window for one source, read off `windows[]`.
///
/// `windows[]` rather than `quotas[]` because the projection has to come from
/// the same card as the percentage it projects: the server scales it by that
/// card's own `used_percent` (`internal/server/windows.go`), so pairing the
/// projection with a percentage picked elsewhere would put the two figures in
/// different units.
///
/// Presence is decided by `authoritative`, never by `used_percent`: the latter
/// is `omitempty` on the wire, so a genuine, untouched 0% arrives as an absent
/// key and must not be mistaken for an absent window.
fn five_hour_window<'a>(windows: &'a [Value], source: &str) -> Option<&'a Value> {
    windows.iter().find(|window| {
        window.get("key").and_then(Value::as_str) == Some(source)
            && window
                .get("authoritative")
                .and_then(Value::as_bool)
                .unwrap_or(false)
    })
}

/// The tightest live weekly window for one source, and the minutes left on it.
///
/// Tightest rather than first: Claude reports `seven_day`, `seven_day_opus` and
/// `seven_day_sonnet` as separate limits and every device reports its own copy,
/// so the wall that actually stops the user is the highest of them. Same rule as
/// `gauge::tightest_percent`, which is what keeps this card and the tray glyph
/// from showing different numbers for the same account.
///
/// Windows that have already rolled over are dropped by the server before they
/// reach us (`internal/server/live.go`), so there is no expiry check here.
fn weekly_quota(quotas: &[Value], source: &str) -> Option<(f64, Option<i64>)> {
    quotas
        .iter()
        .filter(|quota| quota.get("source").and_then(Value::as_str) == Some(source))
        .filter(|quota| {
            quota.get("window_minutes").and_then(Value::as_i64) == Some(WEEKLY_WINDOW_MINUTES)
        })
        .filter_map(|quota| {
            Some((
                quota.get("used_percent").and_then(Value::as_f64)?,
                quota.get("remaining_minutes").and_then(Value::as_i64),
            ))
        })
        .fold(
            None,
            |tightest: Option<(f64, Option<i64>)>, current| match tightest {
                Some(best) if best.0 >= current.0 => Some(best),
                _ => Some(current),
            },
        )
}

/// One card per subscription source, leading with the shortest window the
/// provider actually reports.
///
/// The 5-hour window wins when it exists because it is the one that bites within
/// the working day — but it is opportunistic on Claude (captured only while
/// Claude Code renders a status line, and dropped the moment it resets) and gone
/// entirely on Codex since `primary` became a weekly window on 2026-07-09. So
/// the weekly window is not an error path, it is where Codex lives.
fn quota_views(payload: Option<&Value>) -> Vec<QuotaView> {
    let Some(payload) = payload else {
        // Nothing was ever received. Two cards reading 暂无 would claim the
        // provider said nothing, when the truth is that nobody asked yet.
        return Vec::new();
    };
    let array = |key: &str| {
        payload
            .get(key)
            .and_then(Value::as_array)
            .map(Vec::as_slice)
            .unwrap_or(&[])
    };
    let (quotas, windows) = (array("quotas"), array("windows"));

    QUOTA_SOURCES
        .iter()
        .map(|&source| {
            let five_hour = five_hour_window(windows, source);
            let five_hour_percent = five_hour.map(|window| {
                window
                    .get("used_percent")
                    .and_then(Value::as_f64)
                    .unwrap_or(0.0)
            });
            let weekly = weekly_quota(quotas, source);
            let basis = if five_hour_percent.is_some() {
                QuotaBasis::FiveHour
            } else if weekly.is_some() {
                QuotaBasis::Weekly
            } else {
                QuotaBasis::None
            };
            QuotaView {
                source: source.to_string(),
                label: source_label(source),
                basis,
                five_hour_percent,
                projected_percent: five_hour
                    .and_then(|window| window.get("projected_percent").and_then(Value::as_f64)),
                weekly_percent: weekly.map(|(percent, _)| percent),
                resets_in_minutes: match basis {
                    QuotaBasis::FiveHour => five_hour
                        .and_then(|window| window.get("remaining_minutes").and_then(Value::as_i64)),
                    QuotaBasis::Weekly => weekly.and_then(|(_, minutes)| minutes),
                    QuotaBasis::None => None,
                },
            }
        })
        .collect()
}

fn popover_view(payload: Option<&Value>, connection: &ConnectionState, now_ms: i64) -> PopoverView {
    let generated_at_ms = payload
        .and_then(|data| data.get("generated_at"))
        .and_then(Value::as_i64);
    let age_ms = generated_at_ms.map(|generated| now_ms.saturating_sub(generated));
    let is_stale = payload.is_some()
        && matches!(
            connection.kind,
            ConnectionKind::Stale | ConnectionKind::Offline | ConnectionKind::Unauthorized
        );

    let speed = payload.and_then(|data| data.get("speed"));
    let rate = speed
        .and_then(|speed| speed.get("tps"))
        .and_then(Value::as_f64);
    let speed_sessions = speed
        .and_then(|speed| speed.get("sessions"))
        .and_then(Value::as_array)
        .map(Vec::as_slice)
        .unwrap_or(&[]);
    let open_count = payload
        .and_then(|data| data.get("processes"))
        .and_then(|processes| processes.get("sessions"))
        .and_then(Value::as_array)
        .map(Vec::len)
        .unwrap_or(0);
    let contributing_devices = speed_sessions
        .iter()
        .filter(|session| {
            session
                .get("contribution_tps")
                .and_then(Value::as_f64)
                .is_some_and(|rate| rate > 0.0)
        })
        .filter_map(|session| session.get("device").and_then(Value::as_str))
        .filter(|device| !device.is_empty())
        .collect::<HashSet<_>>()
        .len();

    let activity = if rate.is_some_and(|rate| rate > 0.0) {
        let rate = rate.unwrap_or_default();
        ActivityView {
            kind: ActivityKind::Active,
            text: format!(
                "近 10m {} t/s · {} 个贡献会话",
                compact_rate(rate),
                speed_sessions.len()
            ),
            rate: Some(rate),
            session_count: speed_sessions.len(),
            contributing_devices,
        }
    } else if open_count > 0 {
        ActivityView {
            kind: ActivityKind::Unknown,
            text: format!("{open_count} 个会话已打开 · 速度未知"),
            rate,
            session_count: open_count,
            contributing_devices,
        }
    } else if rate.is_some() {
        ActivityView {
            kind: ActivityKind::Idle,
            text: "近 10m 无已测生成".to_string(),
            rate,
            session_count: 0,
            contributing_devices,
        }
    } else {
        ActivityView {
            kind: ActivityKind::Unknown,
            text: "活动未知".to_string(),
            rate: None,
            session_count: 0,
            contributing_devices,
        }
    };

    let mut sessions: Vec<SessionView> = speed_sessions
        .iter()
        .map(|session| SessionView {
            tool: source_label(session.get("source").and_then(Value::as_str).unwrap_or("")),
            repository: short_repository(session.get("repo").and_then(Value::as_str).unwrap_or("")),
            model: session
                .get("model")
                .and_then(Value::as_str)
                .filter(|model| !model.is_empty())
                .unwrap_or("—")
                .to_string(),
            device: session
                .get("device")
                .and_then(Value::as_str)
                .filter(|device| !device.is_empty())
                .unwrap_or("—")
                .to_string(),
            contribution_rate: session.get("contribution_tps").and_then(Value::as_f64),
            native_rate: session.get("tps").and_then(Value::as_f64),
        })
        .collect();
    sessions.sort_by(|a, b| {
        b.contribution_rate
            .unwrap_or(f64::NEG_INFINITY)
            .total_cmp(&a.contribution_rate.unwrap_or(f64::NEG_INFINITY))
    });
    let sessions_more = sessions.len().saturating_sub(MAX_POPOVER_ITEMS);
    sessions.truncate(MAX_POPOVER_ITEMS);

    let mut devices: Vec<DeviceView> = payload
        .and_then(|data| data.get("devices"))
        .and_then(Value::as_array)
        .map(Vec::as_slice)
        .unwrap_or(&[])
        .iter()
        .map(|device| DeviceView {
            name: device
                .get("display_name")
                .and_then(Value::as_str)
                .filter(|name| !name.is_empty())
                .or_else(|| device.get("device").and_then(Value::as_str))
                .unwrap_or("—")
                .to_string(),
            state: device
                .get("state")
                .and_then(Value::as_str)
                .unwrap_or("unknown")
                .to_string(),
            has_procs: device
                .get("has_procs")
                .and_then(Value::as_bool)
                .unwrap_or(false),
            running: device.get("running").and_then(Value::as_i64).unwrap_or(0),
        })
        .collect();
    devices.sort_by(|a, b| {
        let rank = |state: &str| match state {
            "active" => 0,
            "idle" => 1,
            "stale" => 2,
            _ => 3,
        };
        rank(&a.state)
            .cmp(&rank(&b.state))
            .then_with(|| b.running.cmp(&a.running))
            .then_with(|| a.name.cmp(&b.name))
    });
    let device_total = devices.len();
    let device_online = devices
        .iter()
        .filter(|device| matches!(device.state.as_str(), "active" | "idle"))
        .count();
    let observable = devices.iter().filter(|device| device.has_procs).count();
    let devices_more = device_total.saturating_sub(MAX_POPOVER_ITEMS);
    devices.truncate(MAX_POPOVER_ITEMS);
    let device_summary = if device_total == 0 {
        "无设备数据".to_string()
    } else {
        format!("{observable}/{device_total} 台设备可观测")
    };

    PopoverView {
        connection: PopoverConnection {
            kind: connection.kind.clone(),
            generated_at_ms,
            age_ms,
            is_stale,
            error: connection.error.clone(),
        },
        activity,
        quotas: quota_views(payload),
        sessions,
        sessions_more,
        devices,
        devices_more,
        device_online,
        device_total,
        device_summary,
    }
}

/// What the frontend receives. `data` is absent when there is nothing to show,
/// which is different from an empty payload and must stay distinguishable.
#[derive(Serialize, Clone)]
struct Update<'a> {
    mode: &'a str,
    connection: &'a ConnectionState,
    view: PopoverView,
    #[serde(skip_serializing_if = "Option::is_none")]
    data: Option<&'a Value>,
}

/// The event name the popover listens on.
pub const EVENT: &str = "live";

/// The SSE event names the server sends (`internal/server/live.go`): `snapshot`
/// on connect, `live` on every change afterwards. Both carry the identical
/// payload — the distinction matters to nobody on this side, and treating only
/// one of them as real would drop either the first frame or all the rest.
fn snapshot_from(event: &str, data: &str) -> Option<Value> {
    if event != "snapshot" && event != "live" {
        return None;
    }
    serde_json::from_str(data).ok()
}

// ── reconnection ──────────────────────────────────────────────────────────

const BACKOFF_MIN: Duration = Duration::from_secs(1);
const BACKOFF_MAX: Duration = Duration::from_secs(30);

/// Poll interval while the stream is unavailable. Deliberately the same 60s the
/// v1 tray used: this is the degraded path, and hammering a server that is
/// already refusing a stream helps nobody.
const POLL_WHILE_DEGRADED: Duration = Duration::from_secs(60);

/// How long the connection may be completely silent before it is presumed dead.
///
/// Three of the server's 30s comment heartbeats. This is the defence against the
/// one failure that is worse than an outage: a peer that stops sending but never
/// closes the socket. TCP will happily hold that open indefinitely, and the
/// popover would keep displaying a frozen figure under a label reading 实时 —
/// which is the exact lie the mode indicator exists to prevent.
///
/// Observed while building this: a server that abandoned the response without
/// closing left the bridge blocked forever with no way to notice.
const IDLE_LIMIT: Duration = Duration::from_secs(90);

fn stale_after_ms() -> i64 {
    IDLE_LIMIT.as_millis().try_into().unwrap_or(i64::MAX)
}

// ── alerts ────────────────────────────────────────────────────────────────

/// Where to warn. Two steps, not a curve: the point is "you are close" and
/// "you are nearly out", and a notification per percent would be noise that
/// trains the user to dismiss it.
pub const THRESHOLDS: [u8; 2] = [75, 90];

/// One quota window, at one threshold. `resets_at` is part of the identity on
/// purpose: when the window rolls over the server reports a new reset instant, so
/// the same threshold re-arms by itself and no expiry bookkeeping is needed.
type AlertKey = (String, String, i64, u8);

#[derive(Default)]
pub struct Alerts {
    fired: HashSet<AlertKey>,
}

#[derive(Debug, PartialEq)]
pub struct Alert {
    pub title: String,
    pub body: String,
}

fn quota_name(q: &Value) -> String {
    let source = q.get("source").and_then(Value::as_str).unwrap_or("");
    let source = if source == "claude-code" {
        "claude"
    } else {
        source
    };
    let window = q.get("window_label").and_then(Value::as_str).unwrap_or("");
    format!("{source} · {window}")
}

impl Alerts {
    /// Thresholds newly crossed since the last call.
    ///
    /// Only the highest crossed step notifies; the lower ones are marked as
    /// fired without a message. Otherwise opening the app when a quota is
    /// already at 92% would deliver two notifications at once, and "75%" arriving
    /// alongside "90%" is just noise.
    pub fn crossings(&mut self, quotas: &[Value]) -> Vec<Alert> {
        let mut out = Vec::new();
        for q in quotas {
            if q.get("expired").and_then(Value::as_bool).unwrap_or(false) {
                continue;
            }
            let Some(pct) = q.get("used_percent").and_then(Value::as_f64) else {
                continue;
            };
            let source = q.get("source").and_then(Value::as_str).unwrap_or("");
            let scope = q.get("scope").and_then(Value::as_str).unwrap_or("");
            let resets = q.get("resets_at").and_then(Value::as_i64).unwrap_or(0);

            let mut highest: Option<u8> = None;
            for t in THRESHOLDS {
                if pct < t as f64 {
                    continue;
                }
                let key = (source.to_string(), scope.to_string(), resets, t);
                if self.fired.insert(key) {
                    highest = Some(t);
                }
            }
            if let Some(t) = highest {
                out.push(Alert {
                    title: format!("{} 已用 {:.0}%", quota_name(q), pct),
                    body: match q.get("remaining_minutes").and_then(Value::as_i64) {
                        Some(m) if m > 0 => {
                            format!("超过 {t}% · {}h{}m 后重置", m / 60, m % 60)
                        }
                        _ => format!("超过 {t}% · 即将重置"),
                    },
                });
            }
        }
        out
    }

    /// Forget windows that have already rolled over, so the set cannot grow for
    /// as long as the app stays open.
    pub fn forget_before(&mut self, now_ms: i64) {
        self.fired
            .retain(|(_, _, resets, _)| *resets == 0 || *resets >= now_ms);
    }
}

/// Bridge state that has to outlive a single connection.
#[derive(Default)]
pub struct State {
    /// The running bridge task, so changing the server address can stop the old
    /// one instead of leaving two streams writing to the same tray.
    task: Mutex<Option<tauri::async_runtime::JoinHandle<()>>>,
    alerts: Mutex<Alerts>,
    /// Serializes snapshot acceptance and every externally visible publication.
    ///
    /// Lock order is `task` (respawn only) → `publication` → `snapshot`.
    /// No publication path acquires `task`, so the order cannot be reversed.
    publication: Mutex<()>,
    snapshot: Mutex<Snapshot>,
}

impl State {
    /// Apply a snapshot mutation and publish its accepted result as one ordered
    /// operation. The snapshot lock is released before calling external APIs,
    /// but the publication gate remains held through tray painting and emit.
    fn publish<R>(
        &self,
        update: impl FnOnce(&mut Snapshot) -> bool,
        publish: impl FnOnce(&Snapshot) -> R,
    ) -> Option<R> {
        let _publication = self.publication.lock().ok()?;
        let snapshot = {
            let mut snapshot = self.snapshot.lock().ok()?;
            if !update(&mut snapshot) {
                return None;
            }
            snapshot.clone()
        };
        Some(publish(&snapshot))
    }
}

// ── the bridge ────────────────────────────────────────────────────────────

/// Start (or restart) the bridge against whatever address is currently stored.
///
/// Called at startup and after every settings save. Aborting the previous task
/// first is the whole reason the handle is kept: without it, pointing the app at
/// a second server would leave the first stream running and the tray would
/// flip between two machines' numbers.
pub fn respawn(app: &tauri::AppHandle) {
    let Ok(mut slot) = app.state::<State>().inner().task.lock() else {
        return;
    };
    if let Some(old) = slot.take() {
        old.abort();
    }
    let endpoint = settings::load(app).server;
    publish_endpoint_change(app, &endpoint);
    let handle = app.clone();
    *slot = Some(tauri::async_runtime::spawn(
        async move { run(handle).await },
    ));
}

async fn run(app: tauri::AppHandle) {
    let mut backoff = BACKOFF_MIN;
    loop {
        let stored = settings::load(&app);
        let (base, token) = (stored.server, stored.token);

        // Normal path. Returns when the server closes the stream or the
        // connection breaks; either way we come back round and reconnect.
        let streamed = stream_once(&app, &base, &token).await;
        if streamed {
            // We were connected, so this is not a failing server — reconnect
            // promptly rather than treating a clean end as an outage.
            backoff = BACKOFF_MIN;
            continue;
        }

        // Degraded. Keep the figures moving over plain GET, and say which
        // channel they came from.
        let polled = poll_once(&app, &base, &token).await;

        let wait = if polled {
            // Plain GET works, so it is the stream specifically that is
            // unavailable — a reverse proxy buffering text/event-stream, say.
            // Retrying that every second would be pointless; poll on the
            // degraded cadence and re-attempt the stream alongside it.
            backoff = BACKOFF_MIN;
            POLL_WHILE_DEGRADED
        } else {
            backoff = (backoff * 2).min(BACKOFF_MAX);
            backoff
        };
        tokio::time::sleep(wait).await;
    }
}

/// Hold one SSE connection, applying every snapshot as it arrives.
///
/// Returns whether the connection was ever established, which is what tells the
/// caller apart "the stream ended" from "the stream never started".
async fn stream_once(app: &tauri::AppHandle, base: &str, token: &str) -> bool {
    let url = format!("{}/api/v1/stream", base.trim_end_matches('/'));
    let mut req = crate::http().get(&url);
    if !token.is_empty() {
        // A header, not `?access_token=` — the browser panel needs the query
        // fallback because EventSource cannot set headers, but this side can and
        // a credential in a URL lands in access logs (ADR-0016).
        req = req.bearer_auth(token);
    }
    let res = match req
        .header("Accept", "text/event-stream")
        // No read timeout: an idle stream is the normal state of this endpoint —
        // it sends a comment heartbeat every 30s and nothing else until usage
        // changes. A timeout here would tear down a perfectly healthy connection.
        .timeout(Duration::from_secs(60 * 60 * 24))
        .send()
        .await
    {
        Ok(r) if r.status().is_success() => r,
        _ => return false,
    };

    // The watchdog sits on the *bytes*, not on the parsed events: the server's
    // `: hb` comments are liveness proof but produce no event, so timing the
    // event stream would tear down a healthy connection on any quiet stretch.
    // At this level a heartbeat counts, and only real silence ends the stream.
    let guarded = futures_util::stream::unfold(res.bytes_stream(), |mut bytes| async move {
        match tokio::time::timeout(IDLE_LIMIT, bytes.next()).await {
            Ok(Some(chunk)) => Some((chunk, bytes)),
            // Ended, or silent for too long. Either way: stop, and let the
            // caller reconnect.
            Ok(None) | Err(_) => None,
        }
    });

    // Boxed because the guarded stream holds an async block and so is not Unpin,
    // which `StreamExt::next` requires.
    let mut events = Box::pin(guarded.eventsource());
    let mut connected = false;
    while let Some(event) = events.next().await {
        let Ok(event) = event else { break };
        if let Some(payload) = snapshot_from(&event.event, &event.data) {
            connected = true;
            apply(app, base, &payload, Mode::Live);
        }
    }
    connected
}

/// The degraded path: the single-shot GET twin of the stream, built by the same
/// `livePayload` on the server, so a fallback cannot report different numbers.
async fn poll_once(app: &tauri::AppHandle, base: &str, token: &str) -> bool {
    match crate::get_json(base, "/api/v1/live", token).await {
        Ok(payload) => {
            apply(app, base, &payload, Mode::Polling);
            true
        }
        Err(error) => {
            let kind = connection_kind_for(&error);
            publish_failure(app, base, kind, error.to_string());
            false
        }
    }
}

/// One snapshot, four consumers.
fn apply(app: &tauri::AppHandle, endpoint: &str, payload: &Value, mode: Mode) {
    let state = app.state::<State>();
    if state
        .publish(
            |snapshot| snapshot.record_success(endpoint, payload.clone(), now_ms(), mode),
            |snapshot| {
                paint_snapshot(app, snapshot);
                let _ = app.emit(
                    EVENT,
                    Update {
                        mode: snapshot.connection.kind.as_str(),
                        connection: &snapshot.connection,
                        view: popover_view(snapshot.data(), &snapshot.connection, now_ms()),
                        data: snapshot.data(),
                    },
                );
            },
        )
        .is_none()
    {
        return;
    }
    notify(app, payload);
}

fn tray_readings(snapshot: &Snapshot) -> (Option<f64>, Option<f64>) {
    if !matches!(
        snapshot.connection.kind,
        ConnectionKind::Live | ConnectionKind::Polling
    ) {
        return (None, None);
    }
    let percent = snapshot
        .data()
        .and_then(|data| data.get("quotas"))
        .and_then(Value::as_array)
        .and_then(|q| gauge::tightest_percent(q));
    let tps = snapshot
        .data()
        .and_then(|data| data.get("speed"))
        .and_then(|s| s.get("tps"))
        .and_then(Value::as_f64);

    (percent, tps)
}

fn paint_snapshot(app: &tauri::AppHandle, snapshot: &Snapshot) {
    let (percent, tps) = tray_readings(snapshot);
    let _ = tray::paint(app, percent, tps);
}

/// Publish a transport failure while keeping every consumer on the same
/// last-good snapshot. The UI receives that payload for an aged/dimmed view,
/// while the tray deliberately suppresses its figures so stale values cannot
/// masquerade as live ones.
fn publish_failure(app: &tauri::AppHandle, endpoint: &str, kind: ConnectionKind, error: String) {
    let state = app.state::<State>();
    let _ = state.publish(
        |snapshot| snapshot.record_failure(endpoint, kind, error, now_ms()),
        |snapshot| {
            paint_snapshot(app, snapshot);
            let _ = app.emit(
                EVENT,
                Update {
                    mode: snapshot.connection.kind.as_str(),
                    connection: &snapshot.connection,
                    view: popover_view(snapshot.data(), &snapshot.connection, now_ms()),
                    data: snapshot.data(),
                },
            );
        },
    );
}

fn publish_endpoint_change(app: &tauri::AppHandle, endpoint: &str) {
    let state = app.state::<State>();
    let _ = state.publish(
        |snapshot| snapshot.bind_endpoint(endpoint),
        |snapshot| {
            paint_snapshot(app, snapshot);
            let _ = app.emit(
                EVENT,
                Update {
                    mode: snapshot.connection.kind.as_str(),
                    connection: &snapshot.connection,
                    view: popover_view(snapshot.data(), &snapshot.connection, now_ms()),
                    data: snapshot.data(),
                },
            );
        },
    );
}

fn connection_kind_for(error: &crate::FetchError) -> ConnectionKind {
    match error {
        crate::FetchError::Unauthorized(_) => ConnectionKind::Unauthorized,
        crate::FetchError::Other(_) => ConnectionKind::Offline,
    }
}

fn now_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis()
        .try_into()
        .unwrap_or(i64::MAX)
}

fn notify(app: &tauri::AppHandle, payload: &Value) {
    if !settings::load(app).notify {
        return;
    }
    let Some(quotas) = payload.get("quotas").and_then(Value::as_array) else {
        return;
    };
    let now = payload
        .get("generated_at")
        .and_then(Value::as_i64)
        .unwrap_or(0);

    let alerts = {
        let Ok(mut a) = app.state::<State>().inner().alerts.lock() else {
            return;
        };
        if now > 0 {
            a.forget_before(now);
        }
        a.crossings(quotas)
    };
    if alerts.is_empty() {
        return;
    }
    // Off the bridge's task. The first alert can put a permission prompt on
    // screen, and `request_permission` does not return until the user answers —
    // on the stream's own task that would stall the tray and the popover behind
    // a modal for as long as it stays up.
    let handle = app.clone();
    tauri::async_runtime::spawn(async move {
        for alert in alerts {
            crate::notify::send(&handle, &alert);
        }
    });
}

/// What the tray prints, for the current setting and reading.
///
/// Kept here beside the numbers it formats rather than in tray.rs, so the two
/// figures the menubar can show are defined next to where they are read off the
/// payload.
pub fn title_for(which: TrayTitle, percent: Option<f64>, tps: Option<f64>) -> Option<String> {
    match which {
        TrayTitle::Off => None,
        // Dashes rather than a stale number: with no reading, printing the last
        // one we saw would be the same lie the offline glyph exists to avoid.
        TrayTitle::Quota => Some(match percent {
            Some(p) => format!("{:.0}%", p),
            None => "—".to_string(),
        }),
        // Idle is 0, not "no data": the server answered, nothing is generating.
        TrayTitle::Speed => Some(match tps {
            Some(t) if t > 0.0 => format!("{t:.0}/s"),
            Some(_) => "0/s".to_string(),
            None => "—".to_string(),
        }),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use std::sync::{mpsc, Arc};
    use std::thread;

    #[test]
    fn takes_both_event_names_the_server_sends() {
        let data = r#"{"quotas":[]}"#;
        assert!(snapshot_from("snapshot", data).is_some());
        assert!(snapshot_from("live", data).is_some());
    }

    // A `: hb\n\n` heartbeat is a comment, so eventsource-stream never yields it
    // as an event; anything else with an unexpected name must be ignored rather
    // than parsed as a payload.
    #[test]
    fn ignores_events_it_does_not_know() {
        assert!(snapshot_from("", r#"{"quotas":[]}"#).is_none());
        assert!(snapshot_from("ping", r#"{"quotas":[]}"#).is_none());
    }

    #[test]
    fn ignores_a_frame_whose_data_is_not_json() {
        assert!(snapshot_from("live", "not json").is_none());
        assert!(snapshot_from("live", "").is_none());
    }

    fn quota(pct: f64, resets: i64) -> Value {
        json!({
            "source": "claude-code", "scope": "five_hour", "window_label": "5 小时窗口",
            "used_percent": pct, "resets_at": resets, "remaining_minutes": 95,
        })
    }

    #[test]
    fn warns_once_per_threshold() {
        let mut a = Alerts::default();
        assert_eq!(a.crossings(&[quota(60.0, 1000)]).len(), 0);

        let first = a.crossings(&[quota(76.0, 1000)]);
        assert_eq!(first.len(), 1);
        assert!(first[0].title.contains("claude · 5 小时窗口"));
        assert!(first[0].body.contains("75%"));

        // Still above, already said so.
        assert_eq!(a.crossings(&[quota(80.0, 1000)]).len(), 0);

        // Next step up is a new thing to say.
        assert_eq!(a.crossings(&[quota(91.0, 1000)]).len(), 1);
        assert_eq!(a.crossings(&[quota(99.0, 1000)]).len(), 0);
    }

    // Opening the app at 92% should say "90%" once, not fire 75 and 90 together.
    #[test]
    fn reports_only_the_highest_step_crossed_at_once() {
        let mut a = Alerts::default();
        let out = a.crossings(&[quota(92.0, 1000)]);
        assert_eq!(out.len(), 1);
        assert!(out[0].body.contains("90%"));
        // And the step it skipped is considered said, so it cannot fire later.
        assert_eq!(a.crossings(&[quota(78.0, 1000)]).len(), 0);
    }

    // A rolled-over window reports a new reset instant, which is what re-arms
    // the warning without any expiry bookkeeping.
    #[test]
    fn rearms_when_the_window_rolls_over() {
        let mut a = Alerts::default();
        assert_eq!(a.crossings(&[quota(80.0, 1000)]).len(), 1);
        assert_eq!(a.crossings(&[quota(80.0, 2000)]).len(), 1);
    }

    #[test]
    fn never_warns_about_an_expired_window() {
        let mut a = Alerts::default();
        let mut q = quota(99.0, 1000);
        q["expired"] = json!(true);
        assert_eq!(a.crossings(&[q]).len(), 0);
    }

    // Distinct scopes are distinct walls: a weekly per-model limit at 90% is not
    // the same warning as the 5-hour window at 90%.
    #[test]
    fn separates_scopes() {
        let mut a = Alerts::default();
        let mut weekly = quota(91.0, 1000);
        weekly["scope"] = json!("seven_day:claude-opus-5");
        assert_eq!(a.crossings(&[quota(91.0, 1000), weekly]).len(), 2);
    }

    #[test]
    fn forgets_windows_that_have_reset() {
        let mut a = Alerts::default();
        assert_eq!(a.crossings(&[quota(80.0, 1000)]).len(), 1);
        a.forget_before(5000);
        // The entry is gone, so the same window would warn again — which is
        // correct: at that point it is a window we have no memory of.
        assert_eq!(a.crossings(&[quota(80.0, 1000)]).len(), 1);
    }

    // resets_at == 0 means the server gave no boundary. Those must not be swept,
    // or the warning would repeat on every single snapshot.
    #[test]
    fn keeps_entries_with_no_reset_instant() {
        let mut a = Alerts::default();
        assert_eq!(a.crossings(&[quota(80.0, 0)]).len(), 1);
        a.forget_before(9_999_999);
        assert_eq!(a.crossings(&[quota(80.0, 0)]).len(), 0);
    }

    #[test]
    fn tray_title_off_prints_nothing() {
        assert_eq!(title_for(TrayTitle::Off, Some(42.0), Some(9.0)), None);
    }

    #[test]
    fn tray_title_rounds_the_quota() {
        assert_eq!(
            title_for(TrayTitle::Quota, Some(42.4), None).unwrap(),
            "42%"
        );
        assert_eq!(
            title_for(TrayTitle::Quota, Some(140.0), None).unwrap(),
            "140%"
        );
    }

    // No reading is dashes, not a stale figure and not zero — the same
    // distinction the offline glyph makes.
    #[test]
    fn tray_title_says_nothing_is_known() {
        assert_eq!(title_for(TrayTitle::Quota, None, None).unwrap(), "—");
        assert_eq!(title_for(TrayTitle::Speed, None, None).unwrap(), "—");
    }

    // Idle is a reading: the server answered and nothing is generating.
    #[test]
    fn tray_title_distinguishes_idle_from_unknown() {
        assert_eq!(title_for(TrayTitle::Speed, None, Some(0.0)).unwrap(), "0/s");
        assert_eq!(
            title_for(TrayTitle::Speed, None, Some(68.4)).unwrap(),
            "68/s"
        );
    }

    // Regression: a failed transport refresh used to discard the last payload,
    // which made an outage indistinguishable from an empty system.
    #[test]
    fn failed_refresh_keeps_last_good_payload_and_ages_it() {
        let payload = json!({"speed": {"tps": 68.0}});
        let mut snapshot = Snapshot::default();

        snapshot.record_success("http://server-a", payload.clone(), 1_000, Mode::Live);
        snapshot.record_failure(
            "http://server-a",
            ConnectionKind::Offline,
            "network unavailable",
            1_250,
        );

        assert_eq!(snapshot.data(), Some(&payload));
        assert_eq!(snapshot.connection.last_success_at_ms, Some(1_000));
        assert_eq!(snapshot.age_ms(1_250), Some(250));
        assert_eq!(snapshot.connection.kind, ConnectionKind::Offline);
        assert_eq!(
            snapshot.connection.error.as_deref(),
            Some("network unavailable")
        );
    }

    #[test]
    fn failed_refresh_becomes_stale_at_the_stream_freshness_boundary() {
        let mut snapshot = Snapshot::default();
        snapshot.record_success("http://server-a", json!({"quotas": []}), 1_000, Mode::Live);

        snapshot.record_failure(
            "http://server-a",
            ConnectionKind::Offline,
            "connection refused",
            90_999,
        );
        assert_eq!(snapshot.connection.kind, ConnectionKind::Offline);

        snapshot.record_failure(
            "http://server-a",
            ConnectionKind::Offline,
            "connection refused",
            91_000,
        );
        assert_eq!(snapshot.connection.kind, ConnectionKind::Stale);
        assert!(snapshot.data().is_some());
    }

    #[test]
    fn all_connection_states_have_distinct_wire_labels() {
        assert_eq!(ConnectionKind::Live.as_str(), "live");
        assert_eq!(ConnectionKind::Polling.as_str(), "polling");
        assert_eq!(ConnectionKind::Stale.as_str(), "stale");
        assert_eq!(ConnectionKind::Unauthorized.as_str(), "unauthorized");
        assert_eq!(ConnectionKind::Offline.as_str(), "offline");
    }

    #[test]
    fn fetch_errors_are_classified_as_unauthorized_or_offline() {
        let unauthorized = crate::FetchError::Unauthorized("401 unauthorized".into());
        let unavailable = crate::FetchError::Other("connection refused".into());

        assert_eq!(
            connection_kind_for(&unauthorized),
            ConnectionKind::Unauthorized
        );
        assert_eq!(connection_kind_for(&unavailable), ConnectionKind::Offline);
    }

    #[test]
    fn tray_hides_last_good_numbers_when_connection_is_degraded() {
        let payload = json!({
            "quotas": [{"used_percent": 42.0}],
            "speed": {"tps": 68.0}
        });
        let mut snapshot = Snapshot::default();
        snapshot.record_success("http://server-a", payload, 1_000, Mode::Live);
        assert_eq!(tray_readings(&snapshot), (Some(42.0), Some(68.0)));

        snapshot.connection.kind = ConnectionKind::Polling;
        assert_eq!(tray_readings(&snapshot), (Some(42.0), Some(68.0)));

        for kind in [
            ConnectionKind::Stale,
            ConnectionKind::Offline,
            ConnectionKind::Unauthorized,
        ] {
            snapshot.connection.kind = kind;
            assert_eq!(tray_readings(&snapshot), (None, None));
            assert!(snapshot.data().is_some());
        }
    }

    #[test]
    fn normalized_equivalent_endpoint_keeps_last_good_snapshot() {
        let payload = json!({"speed": {"tps": 68.0}});
        let mut snapshot = Snapshot::default();
        snapshot.record_success("http://server-a/", payload.clone(), 1_000, Mode::Live);

        snapshot.record_failure(
            "http://server-a",
            ConnectionKind::Offline,
            "connection refused",
            1_250,
        );

        assert_eq!(snapshot.data(), Some(&payload));
        assert_eq!(snapshot.connection.last_success_at_ms, Some(1_000));
    }

    #[test]
    fn changing_endpoint_discards_the_previous_servers_snapshot() {
        let mut snapshot = Snapshot::default();
        snapshot.record_success(
            "http://server-a",
            json!({"speed": {"tps": 68.0}}),
            1_000,
            Mode::Live,
        );

        snapshot.bind_endpoint("http://server-b");
        snapshot.record_failure(
            "http://server-b",
            ConnectionKind::Offline,
            "connection refused",
            1_250,
        );

        assert_eq!(snapshot.data(), None);
        assert_eq!(snapshot.connection.last_success_at_ms, None);
        assert_eq!(snapshot.connection.kind, ConnectionKind::Offline);
    }

    #[test]
    fn late_response_from_previous_endpoint_is_ignored() {
        let mut snapshot = Snapshot::default();
        snapshot.record_success(
            "http://server-a",
            json!({"speed": {"tps": 68.0}}),
            1_000,
            Mode::Live,
        );
        snapshot.bind_endpoint("http://server-b");

        snapshot.record_success(
            "http://server-a",
            json!({"speed": {"tps": 99.0}}),
            2_000,
            Mode::Live,
        );

        assert_eq!(snapshot.data(), None);
        assert_eq!(snapshot.connection.last_success_at_ms, None);
        assert_eq!(snapshot.endpoint.as_deref(), Some("http://server-b"));
    }

    #[test]
    fn endpoint_rebind_cannot_overtake_an_accepted_publication() {
        let state = Arc::new(State::default());
        let published = Arc::new(Mutex::new(Vec::new()));
        let (accepted_tx, accepted_rx) = mpsc::channel();
        let (release_tx, release_rx) = mpsc::channel();
        let (rebind_started_tx, rebind_started_rx) = mpsc::channel();
        let (rebind_done_tx, rebind_done_rx) = mpsc::channel();

        let old_state = Arc::clone(&state);
        let old_published = Arc::clone(&published);
        let old = thread::spawn(move || {
            old_state.publish(
                |snapshot| {
                    let accepted = snapshot.record_success(
                        "http://server-a",
                        json!({"speed": {"tps": 68.0}}),
                        1_000,
                        Mode::Live,
                    );
                    accepted_tx.send(()).unwrap();
                    accepted
                },
                |snapshot| {
                    release_rx.recv().unwrap();
                    old_published
                        .lock()
                        .unwrap()
                        .push(snapshot.endpoint.clone().unwrap());
                },
            );
        });

        accepted_rx.recv().unwrap();
        assert!(matches!(
            state.publication.try_lock(),
            Err(std::sync::TryLockError::WouldBlock)
        ));

        let new_state = Arc::clone(&state);
        let new_published = Arc::clone(&published);
        let new = thread::spawn(move || {
            rebind_started_tx.send(()).unwrap();
            new_state.publish(
                |snapshot| snapshot.bind_endpoint("http://server-b"),
                |snapshot| {
                    new_published
                        .lock()
                        .unwrap()
                        .push(snapshot.endpoint.clone().unwrap());
                },
            );
            rebind_done_tx.send(()).unwrap();
        });

        rebind_started_rx.recv().unwrap();
        assert!(matches!(
            rebind_done_rx.recv_timeout(Duration::from_millis(50)),
            Err(mpsc::RecvTimeoutError::Timeout)
        ));

        release_tx.send(()).unwrap();
        old.join().unwrap();
        new.join().unwrap();

        assert_eq!(
            published.lock().unwrap().as_slice(),
            ["http://server-a", "http://server-b"]
        );
    }

    fn view_for(payload: Option<&Value>, kind: ConnectionKind, now_ms: i64) -> PopoverView {
        popover_view(
            payload,
            &ConnectionState {
                kind,
                last_success_at_ms: Some(1_000),
                error: None,
            },
            now_ms,
        )
    }

    #[test]
    fn active_view_prioritizes_fastest_three_sessions_and_truncates_lists() {
        let payload = json!({
            "generated_at": 10_000,
            "speed": {
                "tps": 68.0,
                "sessions": [
                    {"source":"codex","repo":"org/research","model":"gpt-5","device":"workstation","tps":20.0,"contribution_tps":8.0},
                    {"source":"claude-code","repo":"org/omni-api","model":"sonnet","device":"macmini","tps":48.0,"contribution_tps":32.0},
                    {"source":"api","repo":"org/third","model":"gpt-4.1","device":"server","tps":12.0,"contribution_tps":18.0},
                    {"source":"codex","repo":"org/fourth","model":"gpt-5-mini","device":"laptop","tps":4.0,"contribution_tps":10.0}
                ]
            },
            "processes": {"sessions":[]},
            "devices": [
                {"device":"018f2d5a-7b31-7d98-bf8e-3c2f35a1a001","display_name":"macmini","state":"active","has_procs":true,"running":1},
                {"device":"workstation","state":"active","has_procs":true,"running":1},
                {"device":"server","state":"idle","has_procs":false,"running":0},
                {"device":"laptop","state":"stale","has_procs":true,"running":0}
            ],
            "quotas": [
                {"source":"claude-code","scope":"five_hour","window_label":"5 小时窗口","device":"macmini","used_percent":42.0,"resets_at":99_999,"remaining_minutes":192},
                {"source":"codex","scope":"weekly","window_label":"7 天窗口","device":"workstation","used_percent":18.0,"resets_at":99_999,"remaining_minutes":192}
            ],
            "windows": [],
            "burn": {"per_minute":3100}
        });

        let view = view_for(Some(&payload), ConnectionKind::Live, 13_000);

        assert_eq!(view.activity.kind, ActivityKind::Active);
        assert_eq!(view.activity.text, "近 10m 68 t/s · 4 个贡献会话");
        assert_eq!(view.activity.contributing_devices, 4);
        assert_eq!(view.sessions.len(), 3);
        assert_eq!(view.sessions[0].tool, "Claude");
        assert_eq!(view.sessions[0].repository, "omni-api");
        assert_eq!(view.sessions[0].contribution_rate, Some(32.0));
        assert_eq!(view.sessions[0].native_rate, Some(48.0));
        assert_eq!(view.sessions_more, 1);
        assert_eq!(view.devices_more, 1);
        assert_eq!(view.device_online, 3);
        assert_eq!(view.device_total, 4);
        assert_eq!(view.devices[0].name, "macmini");
    }

    /// The A2 popover renders speed, 5h usage, today's models, contributors,
    /// devices and — since the official-quota card — one quota row per
    /// subscription source. Everything else quota-shaped keeps its consumers
    /// elsewhere: the tray glyph and title go through `tray_readings`, the
    /// warnings through `Alerts`, and neither touches this view.
    ///
    /// A serialized field no reader consumes is worse than absent: it reads like
    /// a supported contract and the next person wires a view to it. So the key
    /// set is pinned rather than spot-checked — adding a field here has to come
    /// with the reader in `desktop/ui/app.js` that justifies it.
    #[test]
    fn popover_payload_carries_no_field_the_webview_cannot_read() {
        let payload = json!({
            "generated_at": 10_000,
            "speed": {"tps":68.0,"sessions":[]},
            "processes": {"sessions":[]},
            "devices": [],
            "quotas": [
                {"source":"claude-code","scope":"five_hour","window_label":"5 小时窗口","device":"macmini","used_percent":42.0,"resets_at":99_999,"remaining_minutes":192}
            ],
            "windows": [
                {"key":"breach","label":"Claude","authoritative":true,"used_percent":62.0,"projected_percent":121.0,"remaining_minutes":80,"start_ms":1,"end_ms":2,"resets_at":3}
            ],
            "burn": {"per_minute":3100}
        });

        let view = view_for(Some(&payload), ConnectionKind::Live, 13_000);
        let encoded = serde_json::to_value(&view).expect("popover view serializes");
        let fields = encoded.as_object().expect("popover view is an object");

        let mut keys: Vec<&str> = fields.keys().map(String::as_str).collect();
        keys.sort_unstable();
        assert_eq!(
            keys,
            [
                "activity",
                "connection",
                "device_online",
                "device_summary",
                "device_total",
                "devices",
                "devices_more",
                "quotas",
                "sessions",
                "sessions_more",
            ]
        );

        let quota = encoded["quotas"][0]
            .as_object()
            .expect("quota row is an object");
        let mut quota_keys: Vec<&str> = quota.keys().map(String::as_str).collect();
        quota_keys.sort_unstable();
        assert_eq!(
            quota_keys,
            [
                "basis",
                "five_hour_percent",
                "label",
                "projected_percent",
                "resets_in_minutes",
                "source",
                "weekly_percent",
            ]
        );

        // The fields #64 removed. Named explicitly so re-adding one fails with a
        // message rather than a diff of ten sorted strings.
        for dead in [
            "risk",
            "quota_summary",
            "quotas_more",
            "quota_reset_minutes",
            "burn_per_minute",
        ] {
            assert!(
                !fields.contains_key(dead),
                "popover payload still carries `{dead}`, which desktop/ui/app.js never reads"
            );
        }
    }

    // ── official quota card ───────────────────────────────────────────────

    fn quotas_of(payload: &Value) -> Vec<QuotaView> {
        view_for(Some(payload), ConnectionKind::Live, 13_000).quotas
    }

    /// Claude's 5-hour window is opportunistic — it exists only while Claude Code
    /// happens to be rendering a status line — so when it IS there it is the
    /// tightest thing the user has and leads the card.
    #[test]
    fn five_hour_window_leads_when_the_provider_reports_one() {
        let payload = json!({
            "generated_at": 10_000,
            "speed": {"tps":0.0,"sessions":[]},
            "processes": {"sessions":[]},
            "devices": [],
            "quotas": [
                {"source":"claude-code","scope":"five_hour","window_minutes":300,"used_percent":24.0,"resets_at":99_999,"remaining_minutes":102},
                {"source":"claude-code","scope":"seven_day","window_minutes":10080,"used_percent":12.0,"resets_at":99_999,"remaining_minutes":8403}
            ],
            "windows": [
                {"key":"claude-code","authoritative":true,"placeholder":false,"used_percent":24.0,"projected_percent":36.5,"remaining_minutes":102},
                {"key":"codex","authoritative":false,"placeholder":true}
            ],
            "burn": {"per_minute":0}
        });

        let claude = &quotas_of(&payload)[0];

        assert_eq!(claude.source, "claude-code");
        assert_eq!(claude.label, "Claude");
        assert_eq!(claude.basis, QuotaBasis::FiveHour);
        assert_eq!(claude.five_hour_percent, Some(24.0));
        assert_eq!(claude.projected_percent, Some(36.5));
        // The weekly figure rides along: the card shows all three numbers, the
        // basis only decides which one leads.
        assert_eq!(claude.weekly_percent, Some(12.0));
        assert_eq!(claude.resets_in_minutes, Some(102));
    }

    /// Codex's `primary` window became 10080 minutes on 2026-07-09 and
    /// `secondary` stopped being reported the same day, so Codex has no 5-hour
    /// quota at all any more. That is the steady state, not a fault: the card
    /// falls back to the weekly window and says which window it is showing.
    #[test]
    fn weekly_window_carries_the_card_when_there_is_no_five_hour_one() {
        let payload = json!({
            "generated_at": 10_000,
            "speed": {"tps":0.0,"sessions":[]},
            "processes": {"sessions":[]},
            "devices": [],
            "quotas": [
                {"source":"codex","scope":"primary","window_minutes":10080,"used_percent":72.0,"resets_at":99_999,"remaining_minutes":9575}
            ],
            "windows": [
                {"key":"claude-code","authoritative":false,"placeholder":true},
                {"key":"codex","authoritative":false,"placeholder":true,"note":"该来源当前未提供 5 小时配额数据"}
            ],
            "burn": {"per_minute":0}
        });

        let codex = &quotas_of(&payload)[1];

        assert_eq!(codex.source, "codex");
        assert_eq!(codex.basis, QuotaBasis::Weekly);
        assert_eq!(codex.five_hour_percent, None);
        assert_eq!(codex.projected_percent, None);
        assert_eq!(codex.weekly_percent, Some(72.0));
        assert_eq!(codex.resets_in_minutes, Some(9575));
    }

    /// A rolling look-back is our own counting, not the provider's, and this card
    /// is the authoritative one. `placeholder` therefore means "no official
    /// 5-hour window", never "0%".
    #[test]
    fn a_rolling_placeholder_window_is_not_an_authoritative_reading() {
        let payload = json!({
            "generated_at": 10_000,
            "speed": {"tps":0.0,"sessions":[]},
            "processes": {"sessions":[]},
            "devices": [],
            "quotas": [],
            "windows": [
                {"key":"claude-code","authoritative":false,"placeholder":true,"tokens":61_953_203,"projected_percent":36.5},
                {"key":"codex","authoritative":false,"placeholder":true,"tokens":107_729_683}
            ],
            "burn": {"per_minute":0}
        });

        for quota in quotas_of(&payload) {
            assert_eq!(quota.basis, QuotaBasis::None);
            assert_eq!(quota.five_hour_percent, None);
            assert_eq!(quota.projected_percent, None);
            assert_eq!(quota.weekly_percent, None);
            assert_eq!(quota.resets_in_minutes, None);
        }
    }

    /// `used_percent` is `omitempty` on the wire, so an untouched authoritative
    /// window arrives with the key missing. Reading that as "no window" would
    /// downgrade a real reading of 0% into "暂无".
    #[test]
    fn an_untouched_authoritative_window_reads_as_zero_not_as_absent() {
        let payload = json!({
            "generated_at": 10_000,
            "speed": {"tps":0.0,"sessions":[]},
            "processes": {"sessions":[]},
            "devices": [],
            "quotas": [],
            "windows": [
                {"key":"claude-code","authoritative":true,"placeholder":false,"remaining_minutes":300},
                {"key":"codex","authoritative":false,"placeholder":true}
            ],
            "burn": {"per_minute":0}
        });

        let claude = &quotas_of(&payload)[0];

        assert_eq!(claude.basis, QuotaBasis::FiveHour);
        assert_eq!(claude.five_hour_percent, Some(0.0));
    }

    /// Claude reports `seven_day`, `seven_day_opus` and `seven_day_sonnet`
    /// separately and every device reports its own copy. The wall the user hits
    /// first is the highest of them — the same "tightest" rule the tray glyph
    /// uses, so the popover and the menubar cannot show different numbers.
    #[test]
    fn the_weekly_figure_is_the_tightest_window_across_scopes_and_devices() {
        let payload = json!({
            "generated_at": 10_000,
            "speed": {"tps":0.0,"sessions":[]},
            "processes": {"sessions":[]},
            "devices": [],
            "quotas": [
                {"source":"claude-code","scope":"seven_day","window_minutes":10080,"used_percent":7.0,"remaining_minutes":8403},
                {"source":"claude-code","scope":"seven_day_opus","window_minutes":10080,"used_percent":31.0,"remaining_minutes":8402},
                {"source":"claude-code","scope":"seven_day_sonnet","window_minutes":10080,"used_percent":12.0,"remaining_minutes":8403},
                {"source":"codex","scope":"primary","window_minutes":10080,"used_percent":99.0,"remaining_minutes":9575}
            ],
            "windows": [],
            "burn": {"per_minute":0}
        });

        let quotas = quotas_of(&payload);

        assert_eq!(quotas[0].weekly_percent, Some(31.0));
        assert_eq!(quotas[0].resets_in_minutes, Some(8402));
        assert_eq!(quotas[1].weekly_percent, Some(99.0));
    }

    /// Both cards always exist so the grid does not reflow, and a source with no
    /// official numbers at all says so instead of borrowing the other's.
    #[test]
    fn every_subscription_source_gets_a_card_even_with_no_quota_at_all() {
        let payload = json!({
            "generated_at": 10_000,
            "speed": {"tps":0.0,"sessions":[]},
            "processes": {"sessions":[]},
            "devices": [],
            "quotas": [],
            "windows": [],
            "burn": {"per_minute":0}
        });

        let quotas = quotas_of(&payload);

        assert_eq!(quotas.len(), 2);
        assert_eq!(quotas[0].label, "Claude");
        assert_eq!(quotas[1].label, "Codex");
        assert!(quotas.iter().all(|q| q.basis == QuotaBasis::None));
    }

    /// No payload is not an empty payload: with nothing to read the popover must
    /// not draw two cards claiming the provider reported nothing.
    #[test]
    fn no_payload_produces_no_quota_cards() {
        assert!(view_for(None, ConnectionKind::Offline, 13_000)
            .quotas
            .is_empty());
    }

    #[test]
    fn idle_view_is_a_known_zero_not_unknown() {
        let payload = json!({
            "generated_at": 10_000,
            "speed": {"tps":0.0,"sessions":[]},
            "processes":{"sessions":[]},
            "devices":[],
            "quotas":[],
            "windows":[],
            "burn":{"per_minute":0}
        });

        let view = view_for(Some(&payload), ConnectionKind::Polling, 11_000);

        assert_eq!(view.activity.kind, ActivityKind::Idle);
        assert_eq!(view.activity.text, "近 10m 无已测生成");
        assert_eq!(view.connection.kind, ConnectionKind::Polling);
    }

    #[test]
    fn missing_speed_with_no_open_process_is_unknown() {
        let payload = json!({
            "generated_at": 10_000,
            "processes":{"sessions":[]},
            "devices":[],
            "quotas":[],
            "windows":[],
            "burn":{}
        });

        let view = view_for(Some(&payload), ConnectionKind::Live, 11_000);
        assert_eq!(view.activity.kind, ActivityKind::Unknown);
        assert_eq!(view.activity.text, "活动未知");
    }

    /// Quota payloads must stay harmless to the popover: it neither renders them
    /// nor may it choke on the shapes the tray and the alerts still care about
    /// (missing `resets_at`, several scopes under one window label).
    #[test]
    fn quota_shapes_do_not_disturb_the_popover_view() {
        let unknown_reset = json!({
            "generated_at": 10_000,
            "speed":{"tps":0.0,"sessions":[]},
            "processes":{"sessions":[]},
            "devices":[],
            "quotas":[
                {
                    "source":"claude-code","scope":"seven_day_sonnet",
                    "window_label":"7 天窗口","device":"macmini",
                    "used_percent":42.0,"resets_at":0,"remaining_minutes":0
                },
                {
                    "source":"claude-code","scope":"seven_day_opus",
                    "window_label":"7 天窗口","device":"macmini",
                    "used_percent":18.0,"resets_at":0,"remaining_minutes":0
                }
            ],
            "windows":[{
                "key":"unknown-reset","authoritative":true,
                "used_percent":42.0,"projected_percent":121.0,
                "resets_at":0,"remaining_minutes":0
            }],
            "burn":{"per_minute":0}
        });
        let view = view_for(Some(&unknown_reset), ConnectionKind::Live, 11_000);

        assert_eq!(view.activity.kind, ActivityKind::Idle);
        assert_eq!(view.connection.kind, ConnectionKind::Live);
        assert!(view.sessions.is_empty());
    }

    #[test]
    fn stale_view_keeps_payload_but_marks_its_generated_age() {
        let payload = json!({
            "generated_at": 10_000,
            "speed":{"tps":20.0,"sessions":[{"source":"codex","tps":20.0}]},
            "processes":{"sessions":[]},
            "devices":[],
            "quotas":[],
            "windows":[],
            "burn":{"per_minute":10}
        });

        let view = view_for(Some(&payload), ConnectionKind::Stale, 100_000);

        assert_eq!(view.connection.kind, ConnectionKind::Stale);
        assert_eq!(view.connection.age_ms, Some(90_000));
        assert!(view.connection.is_stale);
        assert_eq!(view.activity.kind, ActivityKind::Active);
    }

    #[test]
    fn unknown_view_and_open_process_without_speed_are_not_idle() {
        let unknown = view_for(None, ConnectionKind::Offline, 100_000);
        assert_eq!(unknown.activity.kind, ActivityKind::Unknown);
        assert_eq!(unknown.activity.text, "活动未知");
        assert_eq!(unknown.connection.age_ms, None);

        let payload = json!({
            "generated_at": 10_000,
            "speed":{"sessions":[]},
            "processes":{"sessions":[{"source":"codex","device":"macmini","pid":42}]},
            "devices":[],
            "quotas":[],
            "windows":[],
            "burn":{}
        });
        let open = view_for(Some(&payload), ConnectionKind::Live, 11_000);
        assert_eq!(open.activity.kind, ActivityKind::Unknown);
        assert_eq!(open.activity.text, "1 个会话已打开 · 速度未知");
    }
}
