// In-app update: check a signed manifest on GitHub Releases, download the new
// bundle, install it, and relaunch into it.
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

/// How long the popover gets to show "正在重启…" before the process goes away.
const RELAUNCH_GRACE: Duration = Duration::from_secs(2);

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
                    relaunch(&app);
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

/// What a settings-initiated check concluded, in the shape the popover renders.
#[derive(serde::Serialize)]
pub struct CheckResult {
    pub status: &'static str,
    pub version: String,
    pub message: String,
}

/// The check behind both the tray item and the settings button.
///
/// On success this returns BEFORE relaunching, and the relaunch is scheduled a
/// moment later. The popover is a webview inside this process: relaunch first
/// and the window dies mid-call, so the user's last frame is a button stuck on
/// "检查中…" and an app that vanished. Answering first costs two seconds and
/// makes the restart look like the consequence of what they just clicked.
pub async fn check(app: AppHandle<Wry>) -> CheckResult {
    let current = current_version(&app);
    match check_and_install(&app).await {
        Outcome::UpToDate => CheckResult {
            status: "up_to_date",
            version: current.clone(),
            message: format!("已是最新版本({current})"),
        },
        Outcome::Installed { version } => {
            log::info!("update: 已安装 {version},正在重启");
            let handle = app.clone();
            tauri::async_runtime::spawn(async move {
                tokio::time::sleep(RELAUNCH_GRACE).await;
                relaunch(&handle);
            });
            CheckResult {
                status: "installed",
                version: version.clone(),
                message: format!("已更新到 {version},正在重启…"),
            }
        }
        Outcome::Failed(why) => CheckResult {
            status: "failed",
            version: current,
            message: format!("检查失败:{why}"),
        },
    }
}

/// The tray item's handler. The tray has no surface to render a result on, so
/// it reports through the same notifications the quota alerts use.
pub fn check_now(app: &AppHandle<Wry>) {
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        let result = check(app.clone()).await;
        let title = match result.status {
            "installed" => "OmniToken 已更新",
            "up_to_date" => "已是最新版本",
            _ => "检查更新失败",
        };
        notify(&app, title, result.message);
    });
}

/// Restart into the freshly installed bundle.
///
/// NOT `AppHandle::restart`, which spawns the replacement as a child and exits.
/// The menubar app is a launchd job (`~/Library/LaunchAgents/OmniToken.plist`)
/// and launchd owns the job's lifecycle: measured, the process exited, the child
/// went with it, and nothing came back — `launchctl list` showed `- 0 OmniToken`
/// and the icon was gone until the job was bootstrapped by hand. Adding
/// `KeepAlive` to the plist is not an option either: tauri-plugin-autostart owns
/// that file and rewrites it with exactly Label/ProgramArguments/RunAtLoad.
///
/// `open` hands the launch to LaunchServices, which starts the app outside this
/// job's process tree. Two details make it work:
///
///   - `status()`, not `spawn()`: `open` has to finish talking to LaunchServices
///     before this process exits, or it dies with the job mid-handoff — the same
///     failure as `restart`, just a moment later.
///   - `-n`, or `open` finds the still-running instance and merely activates it,
///     and the exit below then leaves nothing running at all.
#[cfg(target_os = "macos")]
fn relaunch(app: &AppHandle<Wry>) {
    let Some(bundle) = bundle_path() else {
        log::error!("update: 找不到 .app 路径,新版本将在下次启动时生效");
        return;
    };
    match std::process::Command::new("/usr/bin/open")
        .arg("-n")
        .arg(&bundle)
        .status()
    {
        // Exit only once the replacement is genuinely on its way. Exiting after
        // a failed launch would kill the menubar for an update that is already
        // on disk and would have applied at the next start anyway.
        Ok(status) if status.success() => app.exit(0),
        Ok(status) => log::error!("update: open 退出码 {status},新版本将在下次启动时生效"),
        Err(e) => log::error!("update: 重启失败({e}),新版本将在下次启动时生效"),
    }
}

#[cfg(not(target_os = "macos"))]
fn relaunch(app: &AppHandle<Wry>) {
    app.restart();
}

/// The `.app` three levels above the executable
/// (`OmniToken.app/Contents/MacOS/omnitoken-desktop`). None when the binary is
/// not inside a bundle — a `cargo run` build, which has no bundle to reopen.
#[cfg(target_os = "macos")]
fn bundle_path() -> Option<std::path::PathBuf> {
    let exe = std::env::current_exe().ok()?;
    let bundle = exe.parent()?.parent()?.parent()?;
    (bundle.extension()? == "app").then(|| bundle.to_path_buf())
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
