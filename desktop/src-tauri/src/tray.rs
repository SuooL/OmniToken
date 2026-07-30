//! The menubar item: the gauge, the optional figure beside it, and the menu.
//!
//! v1 had no menu at all — `TrayIconBuilder` was never given one — so there was
//! no way to quit, open the full panel, or reach settings without first knowing
//! that the icon responds to a left click. A menubar-resident program that
//! cannot be quit from the menubar is missing a floor, not a feature (ADR-0014).
//!
//! Left click still toggles the popover; the menu is on right click
//! (`show_menu_on_left_click(false)`). Those are the two gestures macOS users
//! already expect of a status item, and conflating them would cost the popover.

use std::sync::Mutex;

use tauri::menu::{CheckMenuItem, Menu, MenuItem, PredefinedMenuItem, Submenu};
use tauri::tray::TrayIcon;
use tauri::{Manager, Wry};

use crate::settings::{self, Settings, TrayTitle};
use crate::{gauge, live};

pub const TRAY_ID: &str = "gauge";

/// The checkable items, kept so their tick can be updated in place. Rebuilding
/// the whole menu on every toggle would work, but it also drops the menu the
/// user currently has open.
pub struct Items {
    title_off: CheckMenuItem<Wry>,
    title_quota: CheckMenuItem<Wry>,
    title_speed: CheckMenuItem<Wry>,
    notify: CheckMenuItem<Wry>,
    autostart: CheckMenuItem<Wry>,
    hotkey: CheckMenuItem<Wry>,
}

#[derive(Default)]
pub struct State {
    items: Mutex<Option<Items>>,
    /// Where the icon was the last time we heard about it. The popover is
    /// anchored to it, and menu items ("设置…", the global shortcut) have to be
    /// able to open the popover without a click to read the rect from.
    last_rect: Mutex<Option<tauri::Rect>>,
}

impl State {
    pub fn remember_rect(&self, rect: tauri::Rect) {
        if let Ok(mut r) = self.last_rect.lock() {
            *r = Some(rect);
        }
    }

    pub fn rect(&self) -> Option<tauri::Rect> {
        self.last_rect.lock().ok().and_then(|r| *r)
    }
}

pub fn menu(app: &tauri::AppHandle, s: &Settings) -> tauri::Result<(Menu<Wry>, Items)> {
    let open = MenuItem::with_id(app, "open_panel", "打开完整面板", true, None::<&str>)?;
    let refresh = MenuItem::with_id(app, "refresh", "立即刷新", true, None::<&str>)?;

    // Three mutually exclusive choices. tray-icon has no radio item, so these
    // are check items the handler keeps exclusive — which is also why the tick
    // has to be settable after the fact.
    let title_off = CheckMenuItem::with_id(
        app,
        "title_off",
        "关闭",
        true,
        s.tray_title == TrayTitle::Off,
        None::<&str>,
    )?;
    let title_quota = CheckMenuItem::with_id(
        app,
        "title_quota",
        "配额 %",
        true,
        s.tray_title == TrayTitle::Quota,
        None::<&str>,
    )?;
    let title_speed = CheckMenuItem::with_id(
        app,
        "title_speed",
        "生成速度",
        true,
        s.tray_title == TrayTitle::Speed,
        None::<&str>,
    )?;
    let title_menu = Submenu::with_items(
        app,
        "菜单栏数字",
        true,
        &[&title_off, &title_quota, &title_speed],
    )?;

    let notify = CheckMenuItem::with_id(
        app,
        "notify",
        "配额预警(75% / 90%)",
        true,
        s.notify,
        None::<&str>,
    )?;
    let autostart = CheckMenuItem::with_id(
        app,
        "autostart",
        "开机自启",
        true,
        s.autostart,
        None::<&str>,
    )?;
    let hotkey = CheckMenuItem::with_id(
        app,
        "hotkey",
        "全局快捷键 ⌥⌘O",
        true,
        s.hotkey,
        None::<&str>,
    )?;

    let settings_item = MenuItem::with_id(app, "settings", "设置…", true, None::<&str>)?;

    let menu = Menu::with_items(
        app,
        &[
            &open,
            &refresh,
            &PredefinedMenuItem::separator(app)?,
            &title_menu,
            &notify,
            &autostart,
            &hotkey,
            &PredefinedMenuItem::separator(app)?,
            &settings_item,
            &PredefinedMenuItem::about(app, Some("关于 OmniToken"), None)?,
            &PredefinedMenuItem::quit(app, Some("退出 OmniToken"))?,
        ],
    )?;

    Ok((
        menu,
        Items {
            title_off,
            title_quota,
            title_speed,
            notify,
            autostart,
            hotkey,
        },
    ))
}

pub fn remember_items(app: &tauri::AppHandle, items: Items) {
    if let Ok(mut slot) = app.state::<State>().items.lock() {
        *slot = Some(items);
    }
}

/// Push the stored settings back onto the ticks.
///
/// Called after every toggle rather than trusting the click to have flipped the
/// right one: the tray-icon check item toggles itself on click, so the three
/// title items would end up with two ticks unless the whole group is re-synced
/// from the one place that actually holds the answer.
pub fn sync_checks(app: &tauri::AppHandle) {
    let s = settings::load(app);
    if let Ok(slot) = app.state::<State>().items.lock() {
        let Some(items) = slot.as_ref() else { return };
        let _ = items.title_off.set_checked(s.tray_title == TrayTitle::Off);
        let _ = items
            .title_quota
            .set_checked(s.tray_title == TrayTitle::Quota);
        let _ = items
            .title_speed
            .set_checked(s.tray_title == TrayTitle::Speed);
        let _ = items.notify.set_checked(s.notify);
        let _ = items.autostart.set_checked(s.autostart);
        let _ = items.hotkey.set_checked(s.hotkey);
    }
}

/// Repaint the glyph and the figure beside it.
///
/// `None` for either means "no reading", which is not the same as zero and is
/// rendered differently — see gauge::icon for why an empty ring would be a lie.
pub fn paint(app: &tauri::AppHandle, percent: Option<f64>, tps: Option<f64>) -> tauri::Result<()> {
    let Some(tray) = app.tray_by_id(TRAY_ID) else {
        return Ok(());
    };
    set_icon(&tray, percent);
    let _ = tray.set_title(live::title_for(
        settings::load(app).tray_title,
        percent,
        tps,
    ));
    Ok(())
}

fn set_icon(tray: &TrayIcon<Wry>, percent: Option<f64>) {
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
