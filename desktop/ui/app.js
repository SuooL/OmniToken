"use strict";

const invoke = window.__TAURI__ && window.__TAURI__.core.invoke;
const listen = window.__TAURI__ && window.__TAURI__.event.listen;
const $ = (id) => document.getElementById(id);

const TELEMETRY_MS = 30000;
const REDRAW_MS = 10000;
const PANEL_W = 420;
const H_MIN = 240;
const H_TARGET_MAX = 760;
const WORK_AREA_MARGIN = 48;
const MAX_CONTRIBUTORS = 50;
const DETAIL_WINDOW =
  typeof location !== "undefined" &&
  new URLSearchParams(location.search).has("details");
let selectedDetail = null;
let inlineDetail = false;
let detailGeneration = 0;
let detailUpdatePending = false;
let lastDetailHeight = 0;
let lastSourceHTML = "";
let lastQuotaHTML = "";
let pendingRender = false;

let SERVER = "";
let HAS_TOKEN = false;
// Where "完整面板" opens. Empty means "use SERVER", matching the backend fallback.
let PANEL_URL = "";
// Whether the app registers itself to launch at login (LaunchAgent).
let AUTOSTART = false;
let TRAY_TITLE = "off";
let NOTIFY = true;
let HOTKEY = false;
let latestLive = null;
let latestTelemetry = null;
let telemetryError = "";
let telemetryGeneration = 0;
let lastHeight = 0;

const CONNECTION_LABEL = {
  live: "实时",
  polling: "轮询",
  stale: "数据陈旧",
  unauthorized: "未授权",
  offline: "离线",
};

const sourceName = (source) =>
  ({
    "claude-code": "Claude",
    codex: "Codex",
    dsh: "DeepSeek",
    antigravity: "Antigravity",
    gemini: "Gemini",
    api: "Other/API",
    proxy: "Other/API",
    "openai-api": "Other/API",
    "anthropic-api": "Other/API",
    "google-api": "Other/API",
  })[source] ||
  source ||
  "未知来源";

const finite = (value) => Number.isFinite(value);
const tokenLabel = (value) => {
  if (!finite(value)) return "—";
  for (const [unit, suffix] of [
    [1e9, "B"],
    [1e6, "M"],
    [1e3, "K"],
  ])
    if (value >= unit) return `${(value / unit).toFixed(1)}${suffix}`;
  return compact(value);
};
const rateLabel = (value) =>
  finite(value) ? value.toFixed(value >= 100 ? 0 : 1) : "—";
const timeLabel = (value) =>
  !value
    ? "—"
    : new Date(value).toLocaleTimeString("zh-CN", {
        hour12: false,
        hour: "2-digit",
        minute: "2-digit",
      });

function dataAgeMs() {
  const times = [
    latestLive &&
      latestLive.connection &&
      latestLive.connection.generated_at_ms,
    latestTelemetry && latestTelemetry.generated_at_ms,
  ].filter(finite);
  return times.length ? Math.max(0, Date.now() - Math.min(...times)) : null;
}

function contentHeight() {
  const panel = document.querySelector(".panel");
  const clone = panel.cloneNode(true);
  Object.assign(clone.style, {
    position: "fixed",
    visibility: "hidden",
    pointerEvents: "none",
    width: `${PANEL_W}px`,
    height: "auto",
    maxHeight: "none",
    inset: "0 auto auto -10000px",
  });
  document.body.appendChild(clone);
  const height = Math.ceil(clone.scrollHeight);
  clone.remove();
  return height;
}

async function availableMonitorHeight() {
  const api = window.__TAURI__ && window.__TAURI__.window;
  if (!api || !api.currentMonitor) return H_TARGET_MAX + WORK_AREA_MARGIN;
  try {
    const monitor = await api.currentMonitor();
    const workArea = monitor && monitor.workArea;
    const physicalHeight =
      (workArea && workArea.size && workArea.size.height) ||
      (monitor && monitor.size && monitor.size.height);
    const scale = (monitor && monitor.scaleFactor) || 1;
    return physicalHeight
      ? physicalHeight / scale
      : H_TARGET_MAX + WORK_AREA_MARGIN;
  } catch (_) {
    return H_TARGET_MAX + WORK_AREA_MARGIN;
  }
}

async function fitWindow() {
  if (DETAIL_WINDOW) {
    resizeDetail();
    return;
  }
  const api = window.__TAURI__ && window.__TAURI__.window;
  const dpi = window.__TAURI__ && window.__TAURI__.dpi;
  if (!api || !dpi) return;
  const workHeight = await availableMonitorHeight();
  const maxHeight = Math.max(
    240,
    Math.min(H_TARGET_MAX, workHeight - WORK_AREA_MARGIN),
  );
  const height = Math.min(
    maxHeight,
    Math.max(Math.min(H_MIN, maxHeight), contentHeight()),
  );
  if (height === lastHeight) return;
  lastHeight = height;
  api
    .getCurrentWindow()
    .setSize(new dpi.LogicalSize(PANEL_W, height))
    .catch(() => {});
}

function renderFreshness() {
  const connection = latestLive && latestLive.connection;
  const kind = (connection && connection.kind) || "offline";
  $("connection-state").dataset.mode = kind;
  const telemetryStale = !!(latestTelemetry && latestTelemetry.is_stale);
  // Channel only. While the stream is healthy the age is always "1s 前", so
  // spelling it out here is a number that never moves; when it stops being
  // healthy the footer says the age, where it actually carries information.
  $("connection-state").textContent =
    `${CONNECTION_LABEL[kind] || kind}${telemetryStale ? " · 历史数据陈旧" : ""}`;

  const online = latestLive && latestLive.device_online;
  const total = latestLive && latestLive.device_total;
  $("fleet-state").textContent =
    finite(online) && finite(total) ? `${online}/${total} 台在线` : "设备未知";

  document.body.dataset.mode = kind;
  const degraded = kind !== "live";
  document.body.classList.toggle("degraded", degraded);
  const age = dataAgeMs();
  $("footer-age").textContent = telemetryError
    ? `历史数据：${telemetryError}`
    : degraded
      ? age == null
        ? "数据年龄未知"
        : `数据年龄 ${Math.round(age / 1000)}s`
      : "";
}

function renderHero() {
  const activity = latestLive && latestLive.activity;
  const rate = activity && activity.rate;
  $("current-speed").textContent = rateLabel(rate);
  $("active-sessions").textContent = !activity
    ? "会话未知"
    : activity.kind === "unknown"
      ? `${activity.session_count} 个打开会话 · 活跃未知`
      : `${activity.session_count} 个贡献会话`;
  $("contributing-devices").textContent = activity
    ? `${activity.contributing_devices} 台贡献设备`
    : "设备未知";
}

const DAILY_SOURCES = ["claude-code", "codex", "dsh", "antigravity", "gemini"];

function brand(source) {
  const file = {
    "claude-code": "claude",
    codex: "codex",
    dsh: "deepseek",
    antigravity: "antigravity",
    gemini: "gemini",
  }[source];
  return `<span class="brand">${file ? `<img src="icons/${file}.svg" alt="">` : '<span class="other-logo" aria-hidden="true">◇</span>'}<span class="brand-name">${esc(sourceName(source))}</span></span>`;
}

function dailyCard(source, row, known) {
  const change =
    row && row.previous_tokens > 0 && finite(row.change_percent)
      ? row.change_percent
      : null;
  return `<button class="source-row" data-source="${esc(source)}" data-detail="source:${esc(source)}" aria-controls="detail-panel" aria-expanded="${selectedDetail === `source:${source}`}">
    ${brand(source)}<strong class="fig">${row ? tokenLabel(row.tokens) : known ? "0" : "—"}</strong>
    <span class="delta" title="与昨日同时段相比">${change == null ? "" : `${change > 0 ? "+" : ""}${change.toFixed(1)}%`}</span><span class="chevron" aria-hidden="true">›</span></button>`;
}

function renderDailyUsage() {
  const today = latestTelemetry && latestTelemetry.today;
  const known = !!(today && Array.isArray(today.sources));
  const sources = known ? today.sources : [];
  $("today-total").textContent = today ? tokenLabel(today.total_tokens) : "—";
  $("today-title").title =
    latestTelemetry && latestTelemetry.timezone
      ? `日界时区：${latestTelemetry.timezone}`
      : "";
  const markup = DAILY_SOURCES.map((source) =>
    dailyCard(
      source,
      sources.find((row) => row.source === source),
      known,
    ),
  ).join("");
  if (markup !== lastSourceHTML) {
    const focused =
      document.activeElement &&
      document.activeElement.dataset &&
      document.activeElement.dataset.detail;
    $("today-sources").innerHTML = markup;
    lastSourceHTML = markup;
    if (focused)
      document
        .querySelector(`[data-detail="${CSS.escape(focused)}"]`)
        ?.focus({ preventScroll: true });
  }
  const others = sources.filter(
    (row) => !DAILY_SOURCES.includes(row.source) && row.tokens > 0,
  );
  $("today-other").hidden = !others.length;
  $("today-other").innerHTML = others
    .map((row) => dailyCard(row.source, row, true))
    .join("");
}

const QUOTA_WINDOWS = [
  { key: "five_hour", label: "5h" },
  { key: "weekly", label: "Week" },
];

const SOURCE_TONE = {
  "claude-code": "claude",
  codex: "codex",
  dsh: "dsh",
  antigravity: "antigravity",
  gemini: "gemini",
};
const percentLabel = (value) =>
  finite(value) ? `${value.toFixed(0)}%` : "暂无";

// Compare official readings before rounding. Each window carries its own risk.
function quotaSeverity(percent) {
  if (!finite(percent)) return "";
  if (percent >= 90) return "critical";
  if (percent >= 80) return "serious";
  if (percent >= 70) return "warn";
  return "";
}

function quotaResetLabel(deadline) {
  if (!finite(deadline) || deadline <= 0) return "未知";
  const minutes = Math.ceil(Math.max(0, deadline - Date.now()) / 60000);
  return `${Math.floor(minutes / 60)}h${minutes % 60}m`;
}

function quotaPool(source, scope) {
  const s = String(scope || "").trim().toLowerCase();
  if (source === "antigravity") {
    if (s.includes("argon")) return "gemini-argon";
    if (s.startsWith("gemini")) return "gemini";
    if (s.startsWith("3p")) return "3p";
  } else if (source === "claude-code") {
    if (s.includes("opus")) return "opus";
    if (s.includes("sonnet")) return "sonnet";
    if (s.includes("haiku")) return "haiku";
  }
  return "all";
}

function quotaRemaining(quota, key) {
  const remaining = quota[`${key}_remaining_tokens`];
  const known = finite(remaining) && remaining >= 0;
  if (key !== "five_hour") return known ? remaining : null;
  if (quotaPool(quota.source, quota.five_hour_scope) !== quotaPool(quota.source, quota.weekly_scope)) {
    return known ? remaining : null;
  }
  const weekly = quota.weekly_remaining_tokens;
  if (finite(weekly) && weekly >= 0) {
    if (weekly === 0 && finite(quota.five_hour_percent)) return 0;
    return known ? Math.min(remaining, weekly) : null;
  }
  return known ? remaining : null;
}

function quotaElapsed(percent, reset, minutes) {
  const now = Date.now();
  if (!finite(percent) || percent < 0 || !finite(reset) || reset <= now) return null;
  return Math.max(0, Math.min(100, 100 * (1 - (reset - now) / (minutes * 60000))));
}

function renderQuotas() {
  const quotas = latestLive && latestLive.quotas;
  const root = $("quota-grid");
  const markup = !quotas || !quotas.length
    ? '<div class="empty">官方配额不可用</div>'
    : quotas
    .map((quota) => {
      const hasBuckets = Array.isArray(quota.buckets) && quota.buckets.length > 0;
      const detailKey = `quota:${quota.source}`;
      const header = hasBuckets
        ? `<button type="button" class="quota-source quota-source-btn" data-detail="${esc(detailKey)}" aria-controls="detail-panel" aria-expanded="${selectedDetail === detailKey}">${brand(quota.source)}<span class="quota-bucket-hint">分组</span><span class="chevron" aria-hidden="true">›</span></button>`
        : `<div class="quota-source">${brand(quota.source)}</div>`;
      return `<section class="quota-group" data-source="${esc(quota.source)}">${header}${QUOTA_WINDOWS.map(
          (window) => {
            const percent = quota[`${window.key}_percent`];
            const severity = quotaSeverity(percent);
            const known = finite(percent) && percent >= 0;
            const remaining =
              finite(percent) && percent >= 100
                ? 0
                : quotaRemaining(quota, window.key);
            const hint = !known
              ? ""
              : percent >= 100
                ? "已满"
                : percent >= 90
                  ? "紧张"
                  : percent >= 80
                    ? "较高"
                    : percent >= 70
                      ? "留意"
                      : "";
            const risk = !known ? "missing" : severity || "normal";
            const elapsed = quotaElapsed(percent, quota[`${window.key}_resets_at_ms`], window.key === "five_hour" ? 300 : 10080);
            const timeText = elapsed == null ? "" : `时间已过 ${elapsed.toFixed(0)}%`;
            const marker = elapsed == null ? "" : `<i class="quota-time-marker" style="left:${Number(elapsed.toFixed(2))}%" tabindex="0" role="img" title="${timeText}" aria-label="${timeText}"></i>`;
            return `<div class="quota-window" data-window="${window.key}" data-risk="${risk}">
      <span class="quota-window-label">${window.label}</span><div class="quota-reading">
      <strong class="fig" data-severity="${severity}">${known ? percentLabel(percent) : "暂无"}</strong>${hint ? `<small>${hint}</small>` : ""}
      <div class="quota-meter${known ? "" : " missing"}" ${known ? `role="progressbar" aria-label="${esc(quota.label)} ${window.label} 官方已用" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.min(percent, 100)}" aria-valuetext="${percentLabel(percent)}${hint ? ` ${hint}` : ""}${timeText ? ` · ${timeText}` : ""}"` : 'role="img" aria-label="配额暂无读数"'}><div class="quota-meter-fill" style="width:${known ? Math.min(percent, 100) : 0}%"></div>${marker}</div></div>
      <span class="quota-remainder" title="预估剩余额度（tokens）">${finite(remaining) ? compact(remaining) : "暂无估算"}</span>
      <span class="quota-reset">${quotaResetLabel(quota[`${window.key}_resets_at_ms`])}</span></div><!--quota-window-->`;
          },
        ).join("")}</section>`;
    })
    .join("");
  if (markup !== lastQuotaHTML) {
    const focused =
      document.activeElement &&
      document.activeElement.dataset &&
      document.activeElement.dataset.detail;
    root.innerHTML = markup;
    lastQuotaHTML = markup;
    if (focused && focused.startsWith("quota:"))
      document
        .querySelector(`[data-detail="${CSS.escape(focused)}"]`)
        ?.focus({ preventScroll: true });
  }
}

function sourceRows(bucket) {
  return bucket && Array.isArray(bucket.sources) ? bucket.sources : [];
}

function coverageLabel(speed) {
  const coverage = speed && Array.isArray(speed.coverage) ? speed.coverage : [];
  if (!coverage.length) return "";
  return `覆盖 ${coverage
    .map(
      (row) =>
        `${sourceName(row.source)} ${row.measured_events}/${row.total_events}`,
    )
    .join(" · ")}`;
}

// Speed views list sources the way the daily cards do: Claude, Codex,
// DeepSeek, then the rest alphabetically.
function bySourceOrder(a, b) {
  const rank = (source) =>
    DAILY_SOURCES.includes(source)
      ? DAILY_SOURCES.indexOf(source)
      : DAILY_SOURCES.length;
  return rank(a) - rank(b) || (a < b ? -1 : a > b ? 1 : 0);
}

function renderSpeedLanes() {
  const speed = latestTelemetry && latestTelemetry.speed;
  const root = $("speed-lanes");
  if (!speed) {
    root.innerHTML = `<div class="empty">速度遥测不可用</div>`;
    $("speed-coverage").textContent = "测量覆盖未知";
    return;
  }
  const series = speed.series || [];
  const measured = new Set(speed.measured_sources || []);
  const unmeasured = new Set(speed.unmeasured_sources || []);
  // Show only sources with a measured contribution in this time window.
  const ordered = [...new Set(series.flatMap((bucket) =>
    sourceRows(bucket).filter((row) => finite(row.contribution_tps) && row.contribution_tps > 0)
      .map((row) => row.source),
  ))].sort(bySourceOrder);
  const peak = finite(speed.peak_tps)
    ? speed.peak_tps
    : series.reduce(
        (value, bucket) => Math.max(value, bucket.aggregate_tps || 0),
        0,
      );
  const scale = Math.max(
    1,
    ...series.flatMap((bucket) =>
      sourceRows(bucket).map((row) => row.contribution_tps || 0),
    ),
  );
  root.innerHTML = ordered.length ? ordered
    .map((source) => {
      const unavailable = unmeasured.has(source) && !measured.has(source);
      if (unavailable) {
        return `<div class="lane lane-${esc(source)} unavailable">
        <span class="lane-label">${esc(sourceName(source))}</span>
        <span class="lane-unavailable">速度不可用</span>
      </div>`;
      }
      const bars = series
        .map((bucket) => {
          const row = sourceRows(bucket).find((item) => item.source === source);
          const value =
            row && finite(row.contribution_tps) ? row.contribution_tps : 0;
          const height = value > 0 ? Math.max(3, (value / scale) * 100) : 0;
          const peakClass =
            peak > 0 && Math.abs((bucket.aggregate_tps || 0) - peak) < 1e-6
              ? " is-peak"
              : "";
          return `<i class="lane-bar${peakClass}" style="height:${height.toFixed(2)}%"
        title="${esc(timeLabel(bucket.start_ms))} · ${esc(rateLabel(value))} tok/s"></i>`;
        })
        .join("");
      return `<div class="lane lane-${esc(source)}">
      <span class="lane-label">${esc(sourceName(source))}</span>
      <span class="lane-bars">${bars}</span>
    </div>`;
    })
    .join("") : '<div class="empty">此范围暂无已测来源速度</div>';
  const exactCoverage = coverageLabel(speed);
  const measuredLabel =
    ordered
      .filter((source) => measured.has(source))
      .map(sourceName)
      .join("、") || "无";
  const unavailableLabel = [...unmeasured].sort(bySourceOrder)
    .map(sourceName)
    .join("、");
  $("speed-coverage").textContent =
    exactCoverage ||
    (unavailableLabel
      ? `已测 ${measuredLabel} · ${unavailableLabel} 不可用`
      : `已测 ${measuredLabel}`);
}

function renderAggregate() {
  const speed = latestTelemetry && latestTelemetry.speed;
  const series = (speed && speed.series) || [];
  const scale = Math.max(
    1,
    ...series.map((bucket) =>
      finite(bucket.aggregate_tps) ? bucket.aggregate_tps : 0,
    ),
  );
  $("aggregate-history").innerHTML = series.length
    ? series
        .map(
          (bucket) =>
            `<span class="aggregate-bar" style="height:${finite(bucket.aggregate_tps) ? (Math.max(0, bucket.aggregate_tps) / scale) * 100 : 0}%" title="${esc(timeLabel(bucket.start_ms))} · ${finite(bucket.aggregate_tps) ? rateLabel(bucket.aggregate_tps) + " tok/s" : "未知"}"></span>`,
        )
        .join("")
    : '<span class="empty">速度遥测不可用</span>';
  $("summary-peak").textContent =
    speed && finite(speed.peak_tps)
      ? `${rateLabel(speed.peak_tps)} tok/s`
      : "—";
  $("summary-active").textContent =
    speed && finite(speed.active_ratio)
      ? `${(speed.active_ratio * 100).toFixed(0)}%`
      : "—";
  const measured = [...((speed && speed.measured_sources) || [])].sort(
    bySourceOrder,
  );
  const unavailable = [...((speed && speed.unmeasured_sources) || [])].sort(
    bySourceOrder,
  );
  $("summary-coverage").textContent = speed
    ? `已测：${measured.map(sourceName).join("、") || "无"}${unavailable.length ? ` · ${unavailable.map(sourceName).join("、")} 速度不可用` : ""}`
    : "测量覆盖未知";
}

function renderStats() {
  const speed = latestTelemetry && latestTelemetry.speed;
  $("peak-1h").textContent =
    speed && finite(speed.peak_tps)
      ? `${rateLabel(speed.peak_tps)} tok/s`
      : "—";
  $("active-ratio").textContent =
    speed && finite(speed.active_ratio)
      ? `${(speed.active_ratio * 100).toFixed(0)}%`
      : "—";
  $("measured-source-count").textContent = speed
    ? String((speed.measured_sources || []).length)
    : "—";
}

function renderContributors() {
  const sessions = ((latestLive && latestLive.sessions) || [])
    .filter((session) => finite(session.contribution_rate))
    .sort((a, b) => b.contribution_rate - a.contribution_rate);
  $("contributor-list").innerHTML = !latestLive
    ? `<div class="empty">贡献数据不可用</div>`
    : sessions.length
      ? sessions
          .slice(0, MAX_CONTRIBUTORS)
          .map((session) => {
            const native = finite(session.native_rate)
              ? `自身 ${rateLabel(session.native_rate)} tok/s`
              : "自身速度未知";
            return `<div class="contributor-row">

      <span class="contributor-main">
        <b title="${esc(session.repository)}">${esc(session.repository)}</b>
        <small>${esc(sourceName(session.tool))} · ${esc(session.model)} · ${esc(session.device)} · ${esc(native)}</small>
      </span>
      <span class="fig">${rateLabel(session.contribution_rate)} tok/s</span>
    </div>`;
          })
          .join("")
      : `<div class="empty">当前无已测贡献</div>`;
  const hidden = Math.max(
    (latestLive && latestLive.sessions_more) || 0,
    sessions.length - MAX_CONTRIBUTORS,
  );
  $("contributors-more").textContent = hidden > 0 ? `另有 ${hidden} 项` : "";
}

function renderDevices() {
  const view = latestLive;
  const devices = (view && view.devices) || [];
  $("device-summary").textContent = view ? view.device_summary : "设备未知";
  $("device-list").innerHTML = !view
    ? `<div class="empty">设备数据不可用</div>`
    : devices.length
      ? devices
          .map((device) => {
            const processState = !device.has_procs
              ? "不可观测"
              : device.running > 0
                ? `${device.running} 个会话`
                : "无打开会话";
            const state =
              { active: "活跃", idle: "空闲", stale: "陈旧", unknown: "未知" }[
                device.state
              ] || device.state;
            return `<div class="device-row">
      <span class="device-dot" data-state="${esc(device.state)}"></span>
      <span class="device-name" title="${esc(device.name)}">${esc(device.name)}</span>
      <span class="device-state" data-state="${esc(device.state)}">${esc(state)}</span><small>${processState}</small>
    </div>`;
          })
          .join("")
      : `<div class="empty">无设备数据</div>`;
  const stale = devices.filter(
    (device) => device.state === "stale" || device.state === "unknown",
  ).length;
  $("device-main-summary").textContent = view
    ? `${view.device_online}/${view.device_total} 在线${stale ? ` · ${stale} 台未及时上报` : ""}`
    : "设备未知";
  const trigger = document.querySelector('[data-detail="devices"]');
  if (trigger && trigger.dataset) trigger.dataset.warning = String(stale > 0);
  const more = (view && view.devices_more) || 0;
  $("devices-more").hidden = !more;
  $("devices-more").textContent = more ? `另有 ${more} 台设备` : "";
}

function renderAll() {
  if (typeof document !== "undefined" && document.hidden && !DETAIL_WINDOW) {
    pendingRender = true;
    return;
  }
  pendingRender = false;
  renderFreshness();
  renderHero();
  renderDailyUsage();
  renderQuotas();
  renderSpeedLanes();
  renderStats();
  renderAggregate();
  renderContributors();
  renderDevices();
  renderSelectedDetail();
  syncDetail();
  fitWindow();
}

function onLive(update) {
  latestLive = (update && update.view) || null;
  renderAll();
}

function onTelemetry(telemetry) {
  latestTelemetry = telemetry;
  telemetryError = (telemetry && telemetry.error) || "";
  renderAll();
}

async function pullTelemetry() {
  const generation = ++telemetryGeneration;
  try {
    const telemetry = await invoke("telemetry_get", { range: "1h" });
    if (generation !== telemetryGeneration) return;
    onTelemetry(telemetry);
  } catch (error) {
    if (generation !== telemetryGeneration) return;
    telemetryError = String(error);
    if (telemetryError.includes("401") || telemetryError.includes("未授权")) {
      latestTelemetry = null;
    }
  }
  if (telemetryError && latestTelemetry) {
    latestTelemetry.is_stale = true;
    latestTelemetry.error = telemetryError;
  }
  renderAll();
}

function renderSelectedDetail() {
  const visible = !!selectedDetail && (DETAIL_WINDOW || inlineDetail);
  $("detail-panel").hidden = !visible;
  if (!DETAIL_WINDOW) {
    $("main").hidden = !!inlineDetail || !settingsEls.settings.hidden;
    $("panel-footer").hidden = !!inlineDetail;
  }
  $("source-detail").hidden = !selectedDetail?.startsWith("source:");
  $("quota-detail").hidden = !selectedDetail?.startsWith("quota:");
  $("speed-detail").hidden = selectedDetail !== "speed";
  $("devices-detail").hidden = selectedDetail !== "devices";
  if (document.querySelectorAll)
    document
      .querySelectorAll("[data-detail]")
      .forEach((button) =>
        button.setAttribute(
          "aria-expanded",
          String(button.dataset.detail === selectedDetail),
        ),
      );
  if (!selectedDetail) return;
  const oldScroll = $("detail-scroll").scrollTop || 0;
  const panel = document.querySelector(".panel");
  if (selectedDetail.startsWith("source:")) {
    const source = selectedDetail.slice(7);
    panel.dataset.source = source;
    const today = latestTelemetry && latestTelemetry.today;
    const known = today && Array.isArray(today.sources);
    const row = known && today.sources.find((item) => item.source === source);
    const models = row && Array.isArray(row.models) ? row.models : [];
    $("detail-title").innerHTML =
      `<span class="detail-title-brand">${brand(source)} · 今日使用</span>`;
    $("detail-subtitle").textContent =
      `今日零点至今 · ${latestTelemetry?.timezone || "服务端时区"}${latestTelemetry?.is_stale ? " · 历史数据陈旧" : ""}`;
    const change =
      row && row.previous_tokens > 0 && finite(row.change_percent)
        ? row.change_percent
        : null;
    $("source-detail").innerHTML =
      `<div class="source-total"><span class="fig">${row ? tokenLabel(row.tokens) : known ? "0" : "—"}</span> <span class="unit">tokens</span></div>${change == null ? "" : `<div class="secondary">昨日同时段 ${change > 0 ? "+" : ""}${change.toFixed(1)}%</div>`}<section><h3>模型明细</h3>${
        models.length
          ? models
              .map((model) => {
                const part =
                  row.tokens > 0
                    ? Math.min(
                        100,
                        Math.max(0, (model.tokens / row.tokens) * 100),
                      )
                    : 0;
                return `<div class="model-row"><div class="model-line"><span class="model-name">${esc(model.model)}</span><span class="fig">${tokenLabel(model.tokens)}</span></div><div class="model-track"><div class="model-fill" style="width:${part}%"></div></div></div>`;
              })
              .join("")
          : `<div class="empty">${known ? "今日暂无模型用量" : "来源明细不可用"}</div>`
      }</section><section class="detail-note">包含输入、输出与缓存 tokens；与官方配额计量口径不同。</section>`;
  } else if (selectedDetail.startsWith("quota:")) {
    const source = selectedDetail.slice(6);
    panel.dataset.source = source;
    const quota = ((latestLive && latestLive.quotas) || []).find(
      (item) => item && item.source === source,
    );
    const buckets = (quota && Array.isArray(quota.buckets) && quota.buckets) || [];
    $("detail-title").innerHTML =
      `<span class="detail-title-brand">${brand(source)} · 官方配额分组</span>`;
    $("detail-subtitle").textContent = "各模型池官方已用百分比与重置倒计时";
    $("quota-detail").innerHTML = buckets.length
      ? `${buckets
          .map((b) => {
            const pct = b.used_percent;
            const severity = quotaSeverity(pct);
            const risk = severity || "normal";
            const hint =
              pct >= 100
                ? "已满"
                : pct >= 90
                  ? "紧张"
                  : pct >= 80
                    ? "较高"
                    : pct >= 70
                      ? "留意"
                      : "";
            const winLabel =
              b.window_minutes === 300
                ? "5h"
                : b.window_minutes === 10080
                  ? "Week"
                  : `${b.window_minutes || "?"}m`;
            const elapsed = quotaElapsed(pct, b.resets_at_ms, b.window_minutes);
            const timeText = elapsed == null ? "" : `时间已过 ${elapsed.toFixed(0)}%`;
            const marker =
              elapsed == null
                ? ""
                : `<i class="quota-time-marker" style="left:${Number(elapsed.toFixed(2))}%" tabindex="0" role="img" title="${timeText}" aria-label="${timeText}"></i>`;
            const obsText = finite(b.observed_at_ms) && b.observed_at_ms > 0 ? ` · 观测于 ${relTime(b.observed_at_ms)}` : "";
            return `<div class="quota-bucket-row" data-risk="${risk}"><div class="quota-bucket-head"><strong>${esc(b.label || b.scope)} · ${winLabel}</strong><span class="quota-bucket-val"><strong class="fig" data-severity="${severity}">${percentLabel(pct)}</strong>${hint ? `<small>${hint}</small>` : ""}</span></div><div class="quota-meter" role="progressbar" aria-label="${esc(b.label || b.scope)} ${winLabel} 官方已用" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.min(pct, 100)}" aria-valuetext="${percentLabel(pct)}${hint ? ` ${hint}` : ""}${timeText ? ` · ${timeText}` : ""}"><div class="quota-meter-fill" style="width:${Math.min(pct, 100)}%"></div>${marker}</div><div class="secondary quota-bucket-reset">距重置 ${quotaResetLabel(b.resets_at_ms)}${obsText}</div></div>`;
          })
          .join("")}<section class="detail-note">来自官方 statusLine 实时上报；蓝色刻度为窗口已过去时间比例。</section>`
      : '<div class="empty">暂无官方配额分桶读数</div>';
  } else {
    delete panel.dataset.source;
    $("detail-title").textContent =
      selectedDetail === "speed" ? "速度详情" : "设备状态";
    $("detail-subtitle").textContent =
      latestLive?.connection?.kind === "live"
        ? selectedDetail === "speed"
          ? "当前贡献与最近 60 分钟趋势"
          : "设备在线与会话活跃分别统计"
        : `连接${CONNECTION_LABEL[latestLive?.connection?.kind] || "未知"} · 保留最后观测数据`;
  }
  $("detail-scroll").scrollTop = oldScroll;
}

function detailPayload() {
  return { kind: selectedDetail, live: latestLive, telemetry: latestTelemetry };
}
function syncDetail() {
  if (
    DETAIL_WINDOW ||
    !selectedDetail ||
    inlineDetail ||
    detailUpdatePending ||
    !invoke
  )
    return;
  detailUpdatePending = true;
  Promise.resolve(
    invoke("details_set", { payload: detailPayload(), activate: false }),
  )
    .catch((error) => {
      $("footer-age").textContent = `详情刷新失败：${error}`;
    })
    .finally(() => {
      detailUpdatePending = false;
    });
}
async function openDetail(kind) {
  if (selectedDetail === kind) {
    closeDetail();
    return;
  }
  selectedDetail = kind;
  const generation = ++detailGeneration;
  if (invoke) {
    try {
      inlineDetail = !(await invoke("details_set", {
        payload: detailPayload(),
        activate: true,
      }));
    } catch (error) {
      inlineDetail = true;
      $("footer-age").textContent = `使用窗口内详情：${error}`;
    }
  } else inlineDetail = true;
  if (generation !== detailGeneration) return;
  renderSelectedDetail();
  renderDailyUsage();
  renderQuotas();
  fitWindow();
  if (inlineDetail) $("detail-close")?.focus({ preventScroll: true });
}
function closeDetail() {
  ++detailGeneration;
  const previous = selectedDetail;
  selectedDetail = null;
  inlineDetail = false;
  if (invoke) invoke("details_close").catch(() => {});
  renderSelectedDetail();
  if (!DETAIL_WINDOW) {
    renderDailyUsage();
    renderQuotas();
    fitWindow();
    if (previous && document.querySelectorAll)
      [...document.querySelectorAll("[data-detail]")]
        .find((button) => button.dataset.detail === previous)
        ?.focus({ preventScroll: true });
  }
}
function receiveDetail(payload) {
  if (!payload || !payload.kind) return;
  if (selectedDetail !== payload.kind) $("detail-scroll").scrollTop = 0;
  selectedDetail = payload.kind;
  latestLive = payload.live;
  latestTelemetry = payload.telemetry;
  renderContributors();
  renderSpeedLanes();
  renderStats();
  renderDevices();
  renderSelectedDetail();
  resizeDetail();
}
function resizeDetail() {
  if (!DETAIL_WINDOW || !selectedDetail || !invoke) return;
  const content = $(
    selectedDetail.startsWith("source:")
      ? "source-detail"
      : selectedDetail.startsWith("quota:")
        ? "quota-detail"
        : selectedDetail === "speed"
          ? "speed-detail"
          : "devices-detail",
  );
  const height = Math.ceil(
    content.scrollHeight +
      document.querySelector(".detail-header").getBoundingClientRect().height +
      44,
  );
  if (height === lastDetailHeight) return;
  lastDetailHeight = height;
  invoke("details_resize", { height }).catch((error) => {
    $("detail-subtitle").textContent = `详情窗口调整失败：${error}`;
  });
}

const settingsEls = {
  main: $("main"),
  settings: $("settings"),
  input: $("server-input"),
  token: $("token-input"),
  clearTokenWrap: $("clear-token-wrap"),
  clearToken: $("clear-token-input"),
  panel: $("panel-input"),
  trayTitle: $("tray-title-input"),
  notify: $("notify-input"),
  hotkey: $("hotkey-input"),
  autostart: $("autostart-input"),
  message: $("settings-msg"),
  save: $("settings-save"),
  checkUpdate: $("check-update"),
  updateVersion: $("update-version"),
  updateMessage: $("update-msg"),
};

function updateMessage(text, kind) {
  settingsEls.updateMessage.textContent = text || "";
  settingsEls.updateMessage.className = "msg" + (kind ? ` ${kind}` : "");
  fitWindow();
}

// The button answers in place. A menubar popover has no room for a modal, and
// firing only a notification would send the user hunting for the reply to
// something they are looking straight at.
async function checkUpdate() {
  if (settingsEls.checkUpdate.disabled) return;
  settingsEls.checkUpdate.disabled = true;
  updateMessage("检查中…");
  try {
    const result = await invoke("check_update");
    settingsEls.updateVersion.textContent = `当前 ${result.version}`;
    updateMessage(result.message, result.status === "failed" ? "err" : "ok");
    // On "installed" the app is about to be replaced by its successor, so the
    // button stays disabled: a second check would race the restart.
    if (result.status !== "installed") settingsEls.checkUpdate.disabled = false;
  } catch (e) {
    updateMessage(`检查失败:${e}`, "err");
    settingsEls.checkUpdate.disabled = false;
  }
}

function settingsMessage(text, kind) {
  settingsEls.message.textContent = text || "";
  settingsEls.message.className = "msg" + (kind ? ` ${kind}` : "");
  fitWindow();
}

function openSettings() {
  closeDetail();
  settingsEls.input.value = SERVER;
  settingsEls.token.value = "";
  settingsEls.token.placeholder = HAS_TOKEN
    ? "已保存访问令牌；留空保持不变"
    : "服务端只监听本机时留空";
  if (settingsEls.clearToken) settingsEls.clearToken.checked = false;
  if (settingsEls.clearTokenWrap) settingsEls.clearTokenWrap.hidden = !HAS_TOKEN;
  settingsEls.panel.value = PANEL_URL;
  if (settingsEls.trayTitle) settingsEls.trayTitle.value = TRAY_TITLE;
  if (settingsEls.notify) settingsEls.notify.checked = NOTIFY;
  if (settingsEls.hotkey) settingsEls.hotkey.checked = HOTKEY;
  settingsEls.autostart.checked = AUTOSTART;
  settingsEls.checkUpdate.disabled = false;
  settingsEls.updateVersion.textContent = "";
  updateMessage("");
  settingsMessage("");
  settingsEls.main.hidden = true;
  settingsEls.settings.hidden = false;
  settingsEls.input.focus();
  settingsEls.input.select();
  fitWindow();
}

function closeSettings() {
  settingsEls.settings.hidden = true;
  settingsEls.main.hidden = false;
  pullTelemetry();
  fitWindow();
}

async function saveSettings() {
  if (settingsEls.save.disabled) return;
  settingsEls.save.disabled = true;
  settingsMessage("验证中…");
  try {
    const stored = await invoke("settings_set", {
      server: settingsEls.input.value,
      token: settingsEls.token.value,
      panelUrl: settingsEls.panel.value,
      autostart: settingsEls.autostart.checked,
      clearToken: !!(settingsEls.clearToken && settingsEls.clearToken.checked),
      trayTitle: settingsEls.trayTitle ? settingsEls.trayTitle.value : TRAY_TITLE,
      notify: settingsEls.notify ? settingsEls.notify.checked : NOTIFY,
      hotkey: settingsEls.hotkey ? settingsEls.hotkey.checked : HOTKEY,
    });
    SERVER = stored.server;
    HAS_TOKEN = stored.has_token;
    PANEL_URL = stored.panel_url;
    AUTOSTART = !!stored.autostart;
    if (stored.tray_title != null) TRAY_TITLE = stored.tray_title;
    if (stored.notify != null) NOTIFY = !!stored.notify;
    if (stored.hotkey != null) HOTKEY = !!stored.hotkey;
    latestTelemetry = null;
    closeSettings();
  } catch (error) {
    settingsMessage(String(error), "bad");
  } finally {
    settingsEls.save.disabled = false;
  }
}

$("open-settings").addEventListener("click", openSettings);
$("settings-cancel").addEventListener("click", closeSettings);
settingsEls.save.addEventListener("click", saveSettings);
settingsEls.checkUpdate.addEventListener("click", checkUpdate);
for (const input of [settingsEls.input, settingsEls.token, settingsEls.panel]) {
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") saveSettings();
  });
}
$("open-full").addEventListener("click", () =>
  invoke("open_full_panel").catch((error) => {
    $("footer-age").textContent = `无法打开完整面板：${error}`;
  }),
);

document.addEventListener("click", (event) => {
  const trigger = event.target.closest && event.target.closest("[data-detail]");
  if (trigger) openDetail(trigger.dataset.detail);
});
$("detail-close").addEventListener("click", closeDetail);
document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  event.preventDefault();
  event.stopPropagation();
  if (DETAIL_WINDOW || selectedDetail) closeDetail();
  else if (!settingsEls.settings.hidden) closeSettings();
  else invoke("panel_dismiss").catch(() => {});
});

async function boot() {
  if (DETAIL_WINDOW) {
    document.body.classList.add("details-window");
    await listen("detail-view", (event) => receiveDetail(event.payload));
    const initial = await invoke("details_get");
    if (!selectedDetail) receiveDetail(initial);
    return;
  }
  await listen("detail-closed", () => {
    selectedDetail = null;
    inlineDetail = false;
    renderSelectedDetail();
    renderDailyUsage();
    renderQuotas();
  });
  const stored = await invoke("settings_get");
  SERVER = stored.server;
  HAS_TOKEN = !!stored.has_token;
  PANEL_URL = stored.panel_url || "";
  AUTOSTART = !!stored.autostart;
  if (stored.tray_title != null) TRAY_TITLE = stored.tray_title;
  if (stored.notify != null) NOTIFY = !!stored.notify;
  if (stored.hotkey != null) HOTKEY = !!stored.hotkey;
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden && pendingRender) renderAll();
  });
  await listen("live", (event) => onLive(event.payload));
  await listen("open-settings", openSettings);
  await pullTelemetry();
  setInterval(pullTelemetry, TELEMETRY_MS);
  setInterval(renderAll, REDRAW_MS);
}

if (!invoke || !listen) {
  telemetryError = "Tauri IPC 不可用";
  renderAll();
} else {
  boot().catch((error) => {
    telemetryError = String(error);
    renderAll();
  });
}

if (typeof module !== "undefined") {
  module.exports = {
    coverageLabel,
    onLive,
    onTelemetry,
    renderAll,
    receiveDetail,
  };
}
