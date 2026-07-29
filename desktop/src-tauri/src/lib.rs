//! OmniToken menubar client (ADR-0008).
//!
//! A thin client: it renders what an `omnitoken serve` instance already knows
//! and collects nothing itself. Collection stays with `serve` and `agent`, so
//! there is only ever one writer behind event_id dedup and offset advancement.

mod gauge;
mod settings;

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::OnceLock;
use std::time::{Duration, Instant};

use serde_json::Value;
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{Manager, PhysicalPosition, WindowEvent};

/// Reused across polls so connections are pooled; building a Client per call
/// would open a fresh connection every few seconds.
fn http() -> &'static reqwest::Client {
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
async fn get_json(base: &str, path: &str) -> Result<Value, String> {
    let url = format!("{}{}", base.trim_end_matches('/'), path);
    let res = http()
        .get(&url)
        .send()
        .await
        .map_err(|e| format!("{url}: {e}"))?;
    if !res.status().is_success() {
        return Err(format!("{url}: HTTP {}", res.status()));
    }
    res.json::<Value>().await.map_err(|e| format!("{url}: {e}"))
}

#[tauri::command]
async fn api_get(base: String, path: String) -> Result<Value, String> {
    get_json(&base, &path).await
}

#[tauri::command]
fn settings_get(app: tauri::AppHandle) -> settings::Settings {
    settings::load(&app)
}

/// Normalize, persist, and hand back what was actually stored.
///
/// Saving happens before the panel has confirmed the address answers: the
/// server may simply not be running yet, and losing the address the user just
/// typed would be the wrong way to say so. The frontend probes afterwards and
/// reports the result separately.
#[tauri::command]
fn settings_set(app: tauri::AppHandle, server: String) -> Result<settings::Settings, String> {
    let next = settings::Settings {
        server: settings::normalize(&server)?,
    };
    settings::save(&app, &next)?;

    // Repaint now instead of waiting out the poll interval: the icon would
    // otherwise keep reporting the old server for up to a minute after the
    // user pointed it somewhere else.
    let handle = app.clone();
    tauri::async_runtime::spawn(async move { refresh_tray(&handle).await });

    Ok(next)
}

const TRAY_ID: &str = "gauge";

/// Coarser than the panel's 15s: the icon only moves between five buckets, and
/// a menubar glyph that twitches is worse than one that lags a minute.
const TRAY_POLL: Duration = Duration::from_secs(60);

/// Repaint the tray from the server's current quota.
///
/// Reads the address every time rather than caching it, so changing it in
/// settings takes effect on the next tick with no extra plumbing.
async fn refresh_tray(app: &tauri::AppHandle) {
    let base = settings::load(app).server;
    let percent = match get_json(&base, "/api/v1/live").await {
        Ok(v) => v
            .get("quotas")
            .and_then(|q| q.as_array())
            .and_then(|q| gauge::tightest_percent(q)),
        // Unreachable, or serving something that is not the API. Either way
        // there is no reading, and the offline glyph says so.
        Err(_) => None,
    };

    let Some(tray) = app.tray_by_id(TRAY_ID) else {
        return;
    };
    let Ok(image) = gauge::icon(percent) else {
        return;
    };
    if tray.set_icon(Some(image)).is_ok() {
        // tray-icon 0.24 hardcodes `false` for the template flag inside
        // set_icon (platform_impl/macos/mod.rs), silently undoing what the
        // builder set. Without re-asserting it here every swap would ship a
        // literal dark-grey glyph that all but vanishes on a dark menubar.
        let _ = tray.set_icon_as_template(true);
    }
}

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
fn show_under_tray(app: &tauri::AppHandle, window: &tauri::WebviewWindow, icon_rect: tauri::Rect) {
    let size = window.outer_size().unwrap_or_default();
    if let (tauri::Position::Physical(pos), tauri::Size::Physical(icon)) =
        (icon_rect.position, icon_rect.size)
    {
        let x = pos.x + (icon.width as i32 / 2) - (size.width as i32 / 2);
        let y = pos.y + icon.height as i32;
        let _ = window.set_position(PhysicalPosition::new(x, y));
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

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![
            api_get,
            settings_get,
            settings_set
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
            // See show_under_tray for the activation this makes necessary.
            #[cfg(target_os = "macos")]
            app.set_activation_policy(tauri::ActivationPolicy::Accessory);

            let handle = app.handle().clone();
            TrayIconBuilder::with_id(TRAY_ID)
                // Starts with no reading: the first poll has not landed yet,
                // and an arbitrary fill level would be a number we made up.
                .icon(gauge::icon(None)?)
                // Template mode lets macOS recolour the icon for light and dark
                // menubars instead of us shipping two assets.
                .icon_as_template(true)
                .tooltip("OmniToken")
                .on_tray_icon_event(move |_tray, event| {
                    // Act on release, not press: a press also begins a drag,
                    // which should not toggle the panel.
                    if let TrayIconEvent::Click {
                        button: MouseButton::Left,
                        button_state: MouseButtonState::Up,
                        rect,
                        ..
                    } = event
                    {
                        let Some(window) = handle.get_webview_window("panel") else {
                            return;
                        };
                        let just_dismissed = now_ms()
                            .saturating_sub(LAST_DISMISS_MS.load(Ordering::Relaxed))
                            < DISMISS_DEBOUNCE_MS;
                        if window.is_visible().unwrap_or(false) || just_dismissed {
                            let _ = window.hide();
                        } else {
                            show_under_tray(&handle, &window, rect);
                        }
                    }
                })
                .build(app)?;

            // Own thread rather than a timer on the main loop: the poll blocks
            // on the network, and the menubar must stay responsive while a
            // dead server times out. set_icon marshals itself back to the main
            // thread, so painting from here is safe.
            let poller = app.handle().clone();
            std::thread::spawn(move || loop {
                tauri::async_runtime::block_on(refresh_tray(&poller));
                std::thread::sleep(TRAY_POLL);
            });

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
