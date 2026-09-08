// In-app update: check a signed manifest on GitHub Releases, download the new
// bundle, install it, relaunch.
//
// Shape borrowed from the sibling OmniStats project, which solves the same
// problem with Sparkle:
//
//   - the manifest is a STATIC release asset, not a REST API call. GitHub's API
//     is rate-limited per IP and would start failing on a shared network for a
//     reason the user cannot see; `releases/latest/download/latest.json` is a
//     plain redirect to a file on the CDN.
//   - the payload is signed with an Ed25519 key whose private half exists only
//     in a CI secret. The public half is compiled into the app
//     (`plugins.updater.pubkey`), so a tampered or truncated download is
//     rejected before anything is replaced. TLS alone would not be enough: it
//     authenticates the host, not the artefact.
//   - checks run on a schedule as well as on demand, because an update nobody
//     clicks for is an update nobody gets.
//
// Both apps are ad-hoc signed (`codesign --sign -`), with no Apple Developer ID
// and no notarisation. That is a deliberate constraint of this fleet rather than
// an oversight, and it is why the install path is exercised end to end in
// `make desktop-updater-spike` rather than assumed to work.

use std::time::Duration;

use tauri::{AppHandle, Wry};
use tauri_plugin_updater::UpdaterExt;

/// How often a long-running menubar app looks for a new version. Six hours
/// matches the sibling project's `SUScheduledCheckInterval`; the app is meant to
/// sit in the menubar for weeks, so anything rarer means updates arrive only
/// when someone thinks to ask.
const CHECK_INTERVAL: Duration = Duration::from_secs(6 * 60 * 60);

/// Delay before the first check. Startup is already busy bringing up the tray,
/// the SSE bridge and the first snapshot; the update check is the least urgent
/// thing happening and has no business competing with them.
const FIRST_CHECK_DELAY: Duration = Duration::from_secs(30);

/// What a check concluded, for the caller that asked for it explicitly.
#[derive(Debug)]
pub enum Outcome {
    UpToDate,
    Installed { version: String },
    Failed(String),
}

/// Runs checks forever on the schedule above. Spawned once at setup.
pub fn schedule(app: AppHandle<Wry>) {
    tauri::async_runtime::spawn(async move {
        tokio::time::sleep(FIRST_CHECK_DELAY).await;
        loop {
            match check_and_install(&app).await {
                Outcome::UpToDate => log::info!("update: 已是最新"),
                Outcome::Installed { version } => {
                    // Installing replaces the BUNDLE; it does not touch the
                    // running process. Measured against the real v0.2.0 release:
                    // the app on disk went 0.1.0 -> 0.2.0 while the pid never
                    // changed. Without the restart below a menubar app that sits
                    // there for weeks would keep running the old code
                    // indefinitely — the update would land and never apply.
                    //
                    // Restarting unannounced is acceptable here for the reason
                    // check_now gives: an accessory with no documents and no
                    // unsaved state, where the visible cost is the tray icon
                    // blinking. It is still announced, because a menubar item
                    // vanishing and reappearing with no explanation reads as a
                    // crash.
                    log::info!("update: 已安装 {version},正在重启");
                    notify(&app, "OmniToken 已更新", format!("{version},正在重启"));
                    app.restart();
                }
                // A failed check is not an error the user needs to see — the
                // network is allowed to be down. It is logged and retried on the
                // next tick, exactly like the SSE bridge treats a dropped stream.
                Outcome::Failed(why) => log::warn!("update: 检查失败({why}),下次周期重试"),
            }
            tokio::time::sleep(CHECK_INTERVAL).await;
        }
    });
}

/// One check. Returns once the update is installed — which on macOS means the
/// bundle on disk has already been replaced, so the caller's next move is to
/// restart, not to keep working.
pub async fn check_and_install(app: &AppHandle<Wry>) -> Outcome {
    let updater = match app.updater() {
        Ok(u) => u,
        Err(e) => return Outcome::Failed(e.to_string()),
    };
    let update = match updater.check().await {
        Ok(Some(update)) => update,
        Ok(None) => return Outcome::UpToDate,
        Err(e) => return Outcome::Failed(e.to_string()),
    };
    let version = update.version.clone();
    log::info!("update: 发现 {version}(当前 {})", update.current_version);
    // download_and_install verifies the Ed25519 signature against the compiled-in
    // public key before it touches the installed bundle. The two closures are
    // progress hooks we do not need — a menubar app has nowhere to put a
    // progress bar, and the download is one bundle, not a queue.
    if let Err(e) = update.download_and_install(|_, _| {}, || {}).await {
        return Outcome::Failed(e.to_string());
    }
    Outcome::Installed { version }
}

/// The "检查更新" menu item's handler: check, and if something was installed,
/// restart into it.
///
/// Restarting immediately is the right default *here* specifically because the
/// app is a menubar accessory with no documents and no unsaved state — the cost
/// of a restart is a tray icon blinking. An app with a text buffer open would
/// have to ask first.
pub fn check_now(app: &AppHandle<Wry>) {
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        match check_and_install(&app).await {
            Outcome::Installed { version } => {
                log::info!("update: 已安装 {version},重启中");
                app.restart();
            }
            Outcome::UpToDate => notify(&app, "已是最新版本", current_version(&app)),
            Outcome::Failed(why) => notify(&app, "检查更新失败", why),
        }
    });
}

fn current_version(app: &AppHandle<Wry>) -> String {
    app.package_info().version.to_string()
}

/// The menubar has no window to put a result in, so a manual check reports
/// through the same channel the quota alerts use.
fn notify(app: &AppHandle<Wry>, title: &str, body: impl AsRef<str>) {
    use tauri_plugin_notification::NotificationExt;
    if let Err(e) = app
        .notification()
        .builder()
        .title(title)
        .body(body.as_ref())
        .show()
    {
        log::warn!("update: 通知发送失败: {e}");
    }
}
