//! Quota warnings as macOS notifications.
//!
//! The permission prompt is requested lazily — on the first alert that would
//! actually be shown — rather than at launch. Asking on first run costs a modal
//! before the user has seen the app do anything, and someone who never
//! approaches a limit never needs to be asked at all (ADR-0014).

use tauri_plugin_notification::{NotificationExt, PermissionState};

use crate::live::Alert;

pub fn send(app: &tauri::AppHandle, alert: &Alert) {
    // An Accessory app (no dock icon) can post notifications, but only once the
    // user has allowed it. Asking here means the prompt arrives attached to a
    // real event the user can make sense of.
    match app.notification().permission_state() {
        Ok(PermissionState::Granted) => {}
        Ok(PermissionState::Denied) => return,
        // Unknown / prompt: ask, and give up quietly if the answer is no. A
        // declined notification must never become an error the user has to
        // dismiss — they just said they did not want to be interrupted.
        _ => match app.notification().request_permission() {
            Ok(PermissionState::Granted) => {}
            _ => return,
        },
    }

    let _ = app
        .notification()
        .builder()
        .title(&alert.title)
        .body(&alert.body)
        .show();
}
