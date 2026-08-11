//! OmniToken menubar client (ADR-0008, ADR-0014).
//!
//! A thin client: it renders what an `omnitoken serve` instance already knows
//! and collects nothing itself. Collection stays with `serve` and `agent`, so
//! there is only ever one writer behind event_id dedup and offset advancement.
//! Everything added since v1 — the stream, the menu, the notifications — is
//! display and interaction only, which is the condition that made the third
//! toolchain acceptable in the first place.

mod gauge;
mod live;
#[cfg(target_os = "macos")]
mod macos_window;
mod notify;
mod settings;
mod telemetry;
mod tray;

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::OnceLock;
use std::time::Instant;

use serde_json::Value;
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{Manager, PhysicalPosition, WindowEvent};
use tauri_plugin_global_shortcut::{Code, Modifiers, Shortcut, ShortcutState};

/// Reused across requests so connections are pooled; building a Client per call
/// would open a fresh connection every few seconds.
pub(crate) fn http() -> &'static reqwest::Client {
    static CLIENT: OnceLock<reqwest::Client> = OnceLock::new();
    CLIENT.get_or_init(reqwest::Client::new)
}

/// Fetch JSON from the configured server, on the frontend's behalf.
///
/// The webview cannot call the API directly: it runs on `tauri://localhost`,
/// the server is elsewhere, and the server sends no CORS headers on purpose —
/// its read endpoints are unauthenticated, so allowing arbitrary origins would
/// let any page the user visits read their usage data. Rust is not bound by
/// the same-origin policy, so the request happens here instead.
#[derive(Debug)]
pub(crate) enum FetchError {
    Unauthorized(String),
    Other(String),
}

impl std::fmt::Display for FetchError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Unauthorized(message) | Self::Other(message) => f.write_str(message),
        }
    }
}

pub(crate) async fn get_json(base: &str, path: &str, token: &str) -> Result<Value, FetchError> {
    let url = format!("{}{}", base.trim_end_matches('/'), path);
    let mut req = http().get(&url);
    if !token.is_empty() {
        req = req.bearer_auth(token);
    }
    let res = req
        .send()
        .await
        .map_err(|e| FetchError::Other(format!("{url}: {e}")))?;
    if res.status() == reqwest::StatusCode::UNAUTHORIZED {
        // Named, because "wrong token" and "wrong address" are different
        // problems and a bare 401 does not say which (ADR-0016).
        return Err(FetchError::Unauthorized(format!(
            "{url}: 401 未授权 —— 请在设置里填写服务端的 token"
        )));
    }
    if !res.status().is_success() {
        return Err(FetchError::Other(format!("{url}: HTTP {}", res.status())));
    }
    res.json::<Value>()
        .await
        .map_err(|e| FetchError::Other(format!("{url}: {e}")))
}

/// The frontend passes only the path: the address and the token are the Rust
/// side's business, and handing a credential to the webview to hand back would
/// be a way for it to leak into a rendered string.
#[tauri::command]
async fn api_get(app: tauri::AppHandle, path: String) -> Result<Value, String> {
    let s = settings::load(&app);
    get_json(&s.server, &path, &s.token)
        .await
        .map_err(|error| error.to_string())
}

#[tauri::command]
async fn telemetry_get(
    app: tauri::AppHandle,
    state: tauri::State<'_, telemetry::State>,
    range: String,
) -> Result<telemetry::TelemetryView, String> {
    telemetry::get(app, state, range).await
}

#[tauri::command]
fn settings_get(app: tauri::AppHandle) -> settings::SettingsView {
    settings::SettingsView::from(&settings::load(&app))
}

/// Authenticate the complete candidate before replacing persisted settings.
/// The response is redacted even after success: the webview learns only whether
/// a credential exists, never what it is.
#[tauri::command]
async fn settings_set(
    app: tauri::AppHandle,
    server: String,
    token: String,
    panel_url: String,
) -> Result<settings::SettingsView, String> {
    let current = settings::load(&app);
    let next = settings::validate_candidate(&current, &server, &token, &panel_url).await?;
    settings::save(&app, &next)?;

    // Point the bridge at the new address now instead of waiting for the old
    // connection to break on its own — otherwise the tray would keep reporting
    // the previous server, possibly for as long as it stays up.
    live::respawn(&app);
    Ok(settings::SettingsView::from(&next))
}

/// The tray's own poll is gone: the bridge pushes every snapshot to both the
/// glyph and the popover, so "refresh" means "reconnect", not "fetch once".
#[tauri::command]
fn refresh_now(app: tauri::AppHandle) {
    live::respawn(&app);
}

/// Open the full nine-page panel in the default browser.
///
/// The popover deliberately is not the panel (ADR-0008 §4, kept by ADR-0014):
/// statistics, reports and heatmaps stay in a browser, and this is the door.
#[tauri::command]
fn open_full_panel(app: tauri::AppHandle) -> Result<(), String> {
    use tauri_plugin_opener::OpenerExt;
    // Not `server`: the browser dashboard can live behind a reverse proxy that
    // does its own auth, distinct from the API address the app polls directly
    // (see Settings::panel_url). Falls back to `server` when unset.
    let url = settings::load(&app).panel_target();
    app.opener()
        .open_url(url, None::<&str>)
        .map_err(|e| e.to_string())
}

// ── the popover ───────────────────────────────────────────────────────────

fn now_ms() -> u64 {
    static START: OnceLock<Instant> = OnceLock::new();
    START.get_or_init(Instant::now).elapsed().as_millis() as u64
}

/// When the focus-loss handler last dismissed the panel.
///
/// Clicking the tray while the panel is open fires two events in order: the
/// press takes focus away (dismissing the panel), then the release reaches the
/// tray handler, which would see a hidden window and reopen it. The panel
/// would flicker and never close. Treating a just-dismissed panel as "it was
/// open" makes that click close it, which is what the user asked for.
static LAST_DISMISS_MS: AtomicU64 = AtomicU64::new(0);
const DISMISS_DEBOUNCE_MS: u64 = 250;

/// Place the panel just under the tray icon and horizontally centred on it,
/// which is where a menubar popover is expected to appear.
///
/// Anchoring by the *top* edge is what lets the frontend resize the window to
/// fit its content without re-positioning: growing downwards leaves this corner
/// where it is.
fn show_panel(app: &tauri::AppHandle, rect: Option<tauri::Rect>) {
    let Some(window) = app.get_webview_window("panel") else {
        return;
    };
    // Tahoe re-establishes the opaque backing each time the window is ordered
    // front, so the corners have to be re-cleared on every show, not only once
    // at setup (see macos_window).
    #[cfg(target_os = "macos")]
    macos_window::harden(&window);
    if let Some(rect) = rect {
        let size = window.outer_size().unwrap_or_default();
        if let (tauri::Position::Physical(pos), tauri::Size::Physical(icon)) =
            (rect.position, rect.size)
        {
            let x = pos.x + (icon.width as i32 / 2) - (size.width as i32 / 2);
            let y = pos.y + icon.height as i32;
            let _ = window.set_position(PhysicalPosition::new(x, y));
        }
    }
    let _ = window.show();

    // An Accessory app is not active, and macOS will not bring an inactive
    // app's window forward — the panel stays off screen even though
    // is_visible() reports true, which is exactly what happened while building
    // this. Activating explicitly is what actually makes it appear.
    #[cfg(target_os = "macos")]
    let _ = app.show();
    let _ = app; // only read on macOS

    let _ = window.set_focus();
}

fn toggle_panel(app: &tauri::AppHandle, rect: Option<tauri::Rect>) {
    let Some(window) = app.get_webview_window("panel") else {
        return;
    };
    let just_dismissed =
        now_ms().saturating_sub(LAST_DISMISS_MS.load(Ordering::Relaxed)) < DISMISS_DEBOUNCE_MS;
    if window.is_visible().unwrap_or(false) || just_dismissed {
        let _ = window.hide();
    } else {
        show_panel(app, rect.or_else(|| app.state::<tray::State>().rect()));
    }
}

/// ⌥⌘O. Not a const: `Shortcut::new` is not const, and a `OnceLock` keeps the
/// single definition the handler and the register/unregister paths compare
/// against — three copies of a chord is how they drift.
fn hotkey() -> &'static Shortcut {
    static HOTKEY: OnceLock<Shortcut> = OnceLock::new();
    HOTKEY.get_or_init(|| Shortcut::new(Some(Modifiers::ALT.union(Modifiers::SUPER)), Code::KeyO))
}

/// Register or drop the global shortcut to match the setting.
///
/// Failures are ignored on purpose: another app may already own the chord, and
/// that is not something this app should refuse to start over — the menu tick
/// simply will not do anything.
fn apply_hotkey(app: &tauri::AppHandle, on: bool) {
    use tauri_plugin_global_shortcut::GlobalShortcutExt;
    let gs = app.global_shortcut();
    if on {
        let _ = gs.register(*hotkey());
    } else {
        let _ = gs.unregister(*hotkey());
    }
}

fn apply_autostart(app: &tauri::AppHandle, on: bool) {
    use tauri_plugin_autostart::ManagerExt;
    let m = app.autolaunch();
    let _ = if on { m.enable() } else { m.disable() };
}

// ── menu handling ─────────────────────────────────────────────────────────

/// Mutate the stored settings, then re-apply everything that reads them.
fn update_settings(app: &tauri::AppHandle, f: impl FnOnce(&mut settings::Settings)) {
    let mut s = settings::load(app);
    f(&mut s);
    if settings::save(app, &s).is_err() {
        // Could not write the file. Re-syncing the ticks from what is actually
        // stored keeps the menu honest instead of showing a change that did not
        // survive.
        tray::sync_checks(app);
        return;
    }
    tray::sync_checks(app);
}

fn on_menu(app: &tauri::AppHandle, id: &str) {
    use tauri::Emitter;

    match id {
        "open_panel" => {
            let _ = open_full_panel(app.clone());
        }
        "refresh" => live::respawn(app),
        "settings" => {
            show_panel(app, app.state::<tray::State>().rect());
            // The popover owns its own view switching; telling it to show
            // settings is one event rather than a second entry point into the
            // same UI state.
            let _ = app.emit("open-settings", ());
        }
        "title_off" => set_title_mode(app, settings::TrayTitle::Off),
        "title_quota" => set_title_mode(app, settings::TrayTitle::Quota),
        "title_speed" => set_title_mode(app, settings::TrayTitle::Speed),
        "notify" => update_settings(app, |s| s.notify = !s.notify),
        "autostart" => {
            update_settings(app, |s| s.autostart = !s.autostart);
            apply_autostart(app, settings::load(app).autostart);
        }
        "hotkey" => {
            update_settings(app, |s| s.hotkey = !s.hotkey);
            apply_hotkey(app, settings::load(app).hotkey);
        }
        _ => {}
    }
}

fn set_title_mode(app: &tauri::AppHandle, which: settings::TrayTitle) {
    update_settings(app, |s| s.tray_title = which);
    // Reconnect so the new figure appears immediately rather than at the next
    // change the server happens to broadcast — on an idle machine that could be
    // a long wait, and a menubar that ignores a setting looks broken.
    live::respawn(app);
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_notification::init())
        // The LaunchAgent flavour, not a Login Item: it survives without the app
        // having been in /Applications, which matters for a binary distributed
        // through GitHub Releases rather than the App Store.
        .plugin(tauri_plugin_autostart::init(
            tauri_plugin_autostart::MacosLauncher::LaunchAgent,
            None,
        ))
        .plugin(
            tauri_plugin_global_shortcut::Builder::new()
                .with_handler(|app, shortcut, event| {
                    // Act on press only. Without the state check this fires
                    // twice per keystroke and the popover toggles back shut.
                    if shortcut == hotkey() && event.state() == ShortcutState::Pressed {
                        toggle_panel(app, app.state::<tray::State>().rect());
                    }
                })
                .build(),
        )
        .manage(live::State::default())
        .manage(telemetry::State::default())
        .manage(tray::State::default())
        .invoke_handler(tauri::generate_handler![
            api_get,
            telemetry_get,
            settings_get,
            settings_set,
            refresh_now,
            open_full_panel
        ])
        .setup(|app| {
            if cfg!(debug_assertions) {
                app.handle().plugin(
                    tauri_plugin_log::Builder::default()
                        .level(log::LevelFilter::Info)
                        .build(),
                )?;
            }

            // Accessory: menubar-only, no dock icon and no app-switcher entry.
            // See show_panel for the activation this makes necessary.
            #[cfg(target_os = "macos")]
            app.set_activation_policy(tauri::ActivationPolicy::Accessory);

            let stored = settings::load(app.handle());

            let (menu, items) = tray::menu(app.handle(), &stored)?;
            let handle = app.handle().clone();
            TrayIconBuilder::with_id(tray::TRAY_ID)
                // Starts with no reading: the first snapshot has not landed yet,
                // and an arbitrary fill level would be a number we made up.
                .icon(gauge::icon(None)?)
                // Template mode lets macOS recolour the icon for light and dark
                // menubars instead of us shipping two assets.
                .icon_as_template(true)
                .tooltip("OmniToken")
                .menu(&menu)
                // Left click belongs to the popover; the menu is the right-click
                // gesture. With the default (true) a left click would open the
                // menu and the popover would be unreachable by mouse.
                .show_menu_on_left_click(false)
                .on_menu_event(|app, event| on_menu(app, event.id().as_ref()))
                .on_tray_icon_event(move |tray, event| {
                    // Remember where the icon is from any event that reports it,
                    // so the menu and the shortcut can anchor the popover too —
                    // neither of them arrives with a rect.
                    if let Some(rect) = event_rect(&event) {
                        tray.app_handle().state::<tray::State>().remember_rect(rect);
                    }
                    // Act on release, not press: a press also begins a drag,
                    // which should not toggle the panel.
                    if let TrayIconEvent::Click {
                        button: MouseButton::Left,
                        button_state: MouseButtonState::Up,
                        rect,
                        ..
                    } = event
                    {
                        toggle_panel(&handle, Some(rect));
                    }
                })
                .build(app)?;
            tray::remember_items(app.handle(), items);

            // Clear the opaque backing the transparent window ships behind the
            // CSS card on macOS 26 before it is ever shown (macos_window).
            #[cfg(target_os = "macos")]
            if let Some(panel) = app.get_webview_window("panel") {
                macos_window::harden(&panel);
            }

            // Keep the OS in step with what the file says. Someone may have
            // removed the LaunchAgent by hand, or the chord may have been taken
            // by another app since last launch.
            apply_autostart(app.handle(), stored.autostart);
            apply_hotkey(app.handle(), stored.hotkey);

            // One stream, feeding the popover, the glyph, the figure and the
            // alerts. Replaces both of v1's polling loops.
            live::respawn(app.handle());

            Ok(())
        })
        .on_window_event(|window, event| {
            // Clicking away dismisses the panel, as a menubar popover should.
            if let WindowEvent::Focused(false) = event {
                LAST_DISMISS_MS.store(now_ms(), Ordering::Relaxed);
                let _ = window.hide();
            }
        })
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}

/// The icon's screen rect, for whichever events carry one.
fn event_rect(event: &TrayIconEvent) -> Option<tauri::Rect> {
    match event {
        TrayIconEvent::Click { rect, .. }
        | TrayIconEvent::DoubleClick { rect, .. }
        | TrayIconEvent::Enter { rect, .. }
        | TrayIconEvent::Move { rect, .. }
        | TrayIconEvent::Leave { rect, .. } => Some(*rect),
        _ => None,
    }
}

#[cfg(test)]
mod ui_contract_tests {
    const HTML: &str = include_str!("../../ui/index.html");
    const APP: &str = include_str!("../../ui/app.js");
    const CONFIG: &str = include_str!("../tauri.conf.json");

    #[test]
    fn ui_contract_contains_the_a2_glance_surface_once() {
        for hook in [
            r#"id="freshness""#,
            r#"id="current-speed""#,
            "已测总吞吐",
            r#"id="claude-5h""#,
            r#"id="codex-5h""#,
            r#"id="quota-grid""#,
            r#"id="speed-lanes""#,
            r#"id="peak-1h""#,
            r#"id="active-ratio""#,
            r#"id="measured-source-count""#,
            r#"id="today-total""#,
            r#"id="model-list""#,
            r#"id="contributor-list""#,
            r#"id="device-list""#,
            r#"id="open-full""#,
            r#"id="open-settings""#,
        ] {
            assert!(HTML.contains(hook), "missing A2 UI hook {hook}");
        }
        assert_eq!(HTML.matches(r#"id="current-speed""#).count(), 1);
    }

    #[test]
    fn ui_contract_removes_risk_forecast_and_polls_typed_hourly_telemetry() {
        for obsolete in ["risk-source", "track-future", "track-breach", "drawRisk("] {
            assert!(
                !HTML.contains(obsolete) && !APP.contains(obsolete),
                "obsolete risk forecast remains: {obsolete}"
            );
        }
        assert!(APP.contains(r#"invoke("telemetry_get", { range: "1h" })"#));
        assert!(APP.contains("TELEMETRY_MS = 30000"));
        assert!(APP.contains("generation !== telemetryGeneration"));
        assert!(APP.contains("contribution_rate"));
    }

    #[test]
    fn ui_contract_targets_a_wider_taller_popover() {
        let config: serde_json::Value = serde_json::from_str(CONFIG).unwrap();
        let panel = &config["app"]["windows"][0];
        assert_eq!(panel["width"], 420);
        assert!(panel["height"].as_u64().unwrap_or_default() >= 680);
        assert!(APP.contains("availableMonitor"));
    }

    #[test]
    fn ui_contract_leaves_the_transparent_window_background_undrawn() {
        let config: serde_json::Value = serde_json::from_str(CONFIG).unwrap();
        let panel = config["app"]["windows"]
            .as_array()
            .and_then(|windows| windows.iter().find(|window| window["label"] == "panel"))
            .expect("tauri.conf.json must define the panel window");

        assert_eq!(panel["transparent"], true);
        assert!(
            panel.get("windowEffects").is_none(),
            "the CSS panel owns the background and corners; a native effect leaks outside them"
        );
    }
}
