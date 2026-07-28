//! OmniToken menubar client (ADR-0008).
//!
//! A thin client: it renders what an `omnitoken serve` instance already knows
//! and collects nothing itself. Collection stays with `serve` and `agent`, so
//! there is only ever one writer behind event_id dedup and offset advancement.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::OnceLock;
use std::time::Instant;

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
#[tauri::command]
async fn api_get(base: String, path: String) -> Result<Value, String> {
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
        .invoke_handler(tauri::generate_handler![api_get])
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
            TrayIconBuilder::new()
                .icon(app.default_window_icon().unwrap().clone())
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
