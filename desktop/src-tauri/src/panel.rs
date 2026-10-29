use std::sync::Mutex;

use serde_json::Value;
use tauri::{Emitter, Manager};

#[derive(Clone, Copy, Debug)]
pub struct Rect {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
}

/// Geometry is computed without changing the main panel. No overlap fallback:
/// small displays use the frontend's in-window Back/close detail view.
pub fn adjacent(parent: Rect, screen: Rect, width: f64, height: f64) -> Option<Rect> {
    let left = parent.x - 8.0 - width;
    let right = parent.x + parent.width + 8.0;
    let x = if left >= screen.x + 8.0 {
        left
    } else if right + width <= screen.x + screen.width - 8.0 {
        right
    } else {
        return None;
    };
    let y = parent.y.max(screen.y).min(screen.y + screen.height - 40.0);
    Some(Rect {
        x,
        y,
        width,
        height: height.clamp(40.0, (screen.y + screen.height - y - 8.0).max(40.0)),
    })
}

pub fn should_dismiss(main_focused: bool, detail_focused: bool) -> bool {
    !main_focused && !detail_focused
}

#[derive(Default)]
pub struct State(pub Mutex<Option<Value>>, pub Mutex<f64>);

fn place(app: &tauri::AppHandle, height: f64) -> Result<bool, String> {
    let parent = app
        .get_webview_window("panel")
        .ok_or("Main panel unavailable")?;
    let detail = app
        .get_webview_window("details")
        .ok_or("Detail panel unavailable")?;
    let monitor = parent
        .current_monitor()
        .map_err(|e| e.to_string())?
        .ok_or("Monitor unavailable")?;
    let scale = monitor.scale_factor();
    let position = parent.outer_position().map_err(|e| e.to_string())?;
    let size = parent.outer_size().map_err(|e| e.to_string())?;
    let area = monitor.work_area();
    let frame = adjacent(
        Rect {
            x: f64::from(position.x) / scale,
            y: f64::from(position.y) / scale,
            width: f64::from(size.width) / scale,
            height: f64::from(size.height) / scale,
        },
        Rect {
            x: f64::from(area.position.x) / scale,
            y: f64::from(area.position.y) / scale,
            width: f64::from(area.size.width) / scale,
            height: f64::from(area.size.height) / scale,
        },
        320.0,
        height,
    );
    let Some(frame) = frame else {
        return Ok(false);
    };
    detail
        .set_position(tauri::PhysicalPosition::new(
            (frame.x * scale).round() as i32,
            (frame.y * scale).round() as i32,
        ))
        .map_err(|e| e.to_string())?;
    detail
        .set_size(tauri::LogicalSize::new(frame.width, frame.height))
        .map_err(|e| e.to_string())?;
    Ok(true)
}

#[tauri::command]
pub fn details_get(state: tauri::State<'_, State>) -> Option<Value> {
    state.0.lock().unwrap().clone()
}

#[tauri::command]
pub fn details_set(
    app: tauri::AppHandle,
    state: tauri::State<'_, State>,
    payload: Value,
    activate: bool,
) -> Result<bool, String> {
    let kind = payload["kind"].as_str().unwrap_or_default();
    if kind != "speed" && kind != "devices" && !kind.starts_with("source:") {
        return Err("Unknown detail view".into());
    }
    if !activate && state.0.lock().unwrap().is_none() {
        return Ok(true);
    }
    let height = *state.1.lock().unwrap();
    if !place(&app, if height > 0.0 { height } else { 240.0 })? {
        return Ok(false);
    }
    *state.0.lock().unwrap() = Some(payload.clone());
    let window = app
        .get_webview_window("details")
        .ok_or("Detail panel unavailable")?;
    window
        .emit("detail-view", payload)
        .map_err(|e| e.to_string())?;
    if activate {
        #[cfg(target_os = "macos")]
        crate::macos_window::harden(&window);
        window.show().map_err(|e| e.to_string())?;
        window.set_focus().map_err(|e| e.to_string())?;
    }
    Ok(true)
}

#[tauri::command]
pub fn details_resize(app: tauri::AppHandle, height: f64) -> Result<(), String> {
    if !height.is_finite() || height <= 0.0 {
        return Err("Invalid detail height".into());
    }
    if app.state::<State>().0.lock().unwrap().is_some() {
        *app.state::<State>().1.lock().unwrap() = height;
        place(&app, height)?;
    }
    Ok(())
}

#[tauri::command]
pub fn details_close(app: tauri::AppHandle) {
    if let Some(main) = app.get_webview_window("panel") {
        let _ = main.set_focus();
    }
    hide_details(&app);
}

fn hide_details(app: &tauri::AppHandle) {
    *app.state::<State>().0.lock().unwrap() = None;
    if let Some(detail) = app.get_webview_window("details") {
        let _ = detail.hide();
    }
    let _ = app.emit_to("panel", "detail-closed", ());
}

#[tauri::command]
pub fn panel_dismiss(app: tauri::AppHandle) {
    hide_details(&app);
    if let Some(window) = app.get_webview_window("panel") {
        let _ = window.hide();
    }
    crate::LAST_DISMISS_MS.store(crate::now_ms(), std::sync::atomic::Ordering::Relaxed);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn details_prefer_left_and_preserve_the_main_frame() {
        let parent = Rect {
            x: 800.0,
            y: 30.0,
            width: 420.0,
            height: 700.0,
        };
        let screen = Rect {
            x: 0.0,
            y: 25.0,
            width: 1440.0,
            height: 875.0,
        };
        let frame = adjacent(parent, screen, 320.0, 240.0).unwrap();
        assert_eq!(frame.x, 472.0);
        assert_eq!(frame.y, parent.y);
        assert_eq!(frame.height, 240.0);
    }

    #[test]
    fn right_fallback_and_short_screen_cap() {
        let parent = Rect {
            x: 10.0,
            y: 30.0,
            width: 420.0,
            height: 700.0,
        };
        let screen = Rect {
            x: 0.0,
            y: 25.0,
            width: 1440.0,
            height: 475.0,
        };
        let frame = adjacent(parent, screen, 320.0, 1200.0).unwrap();
        assert_eq!(frame.x, 438.0);
        assert_eq!(frame.height, 462.0);
        assert!(adjacent(
            parent,
            Rect {
                width: 640.0,
                ..screen
            },
            320.0,
            100.0
        )
        .is_none());
    }

    #[test]
    fn negative_monitor_origin_and_small_details_are_supported() {
        let screen = Rect {
            x: -1440.0,
            y: -900.0,
            width: 1440.0,
            height: 875.0,
        };
        let parent = Rect {
            x: -600.0,
            y: -880.0,
            width: 420.0,
            height: 700.0,
        };
        let frame = adjacent(parent, screen, 320.0, 120.0).unwrap();
        assert_eq!(frame.height, 120.0);
        assert_eq!(frame.y, -880.0);
    }

    #[test]
    fn focus_transfer_keeps_the_group_open() {
        assert!(!should_dismiss(true, false));
        assert!(!should_dismiss(false, true));
        assert!(should_dismiss(false, false));
    }
}
