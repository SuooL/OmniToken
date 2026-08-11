//! macOS-only: re-assert the panel window's transparency at the NSWindow level.
//!
//! The popover is a transparent, borderless window whose rounded card is drawn
//! in CSS (ADR-0014 §3): the window fills the screen rectangle, the card fills
//! the window, and the four corner triangles outside the card's 24px radius are
//! meant to be clear so only the rounded card shows.
//!
//! On macOS 26 (Tahoe) `transparent: true` alone no longer clears those corners:
//! the window ships an opaque light backing plus a rectangular frame behind the
//! card — a known wry/tao regression on recent macOS
//! (tauri-apps/tauri#14394, and the older #3481 / #4243 / #11635 family). The
//! fix that still works is to talk to the NSWindow directly and re-assert what
//! `transparent: true` was supposed to set: non-opaque, clear background, and a
//! shadow recomputed from the now-transparent corners so it traces the card's
//! rounded alpha instead of the stale opaque square.
//!
//! Re-applied on every show, not just at setup: Tahoe re-establishes the opaque
//! backing when the window is ordered front again, so a one-shot call at launch
//! does not hold.

use objc2_app_kit::{NSColor, NSWindow};

/// Clear the opaque backing behind the CSS card on the given panel window.
///
/// No-op off macOS and on any window without a live NSWindow handle.
pub fn harden(window: &tauri::WebviewWindow) {
    let Ok(ptr) = window.ns_window() else {
        return;
    };
    if ptr.is_null() {
        return;
    }
    // SAFETY: on macOS `ns_window()` hands back a valid, Tauri-owned NSWindow
    // pointer for a live window. We only send it AppKit messages declared on
    // NSWindow and never take ownership or free it.
    let ns_window: &NSWindow = unsafe { &*ptr.cast::<NSWindow>() };
    ns_window.setOpaque(false);
    ns_window.setBackgroundColor(Some(&NSColor::clearColor()));
    // Native drop shadow, traced from the card's rounded alpha and rendered
    // *outside* the window — the popover lift ADR-0014 §3 wanted. The CSS
    // `box-shadow` that used to supply it is gone (style.css): rendered inside
    // the webview it bled into the transparent corner triangles as a grey L
    // over light backgrounds. Invalidate so the shape recomputes from the now
    // fully-transparent corners.
    ns_window.setHasShadow(true);
    ns_window.invalidateShadow();
}
