"use strict";

const invoke = window.__TAURI__ && window.__TAURI__.core.invoke;
const listen = window.__TAURI__ && window.__TAURI__.event.listen;
const $ = (id) => document.getElementById(id);

const TELEMETRY_MS = 30000;
const REDRAW_MS = 10000;
const PANEL_W = 420;
const H_MIN = 520;
const H_TARGET_MAX = 760;
const WORK_AREA_MARGIN = 48;
const MAX_CONTRIBUTORS = 5;

let SERVER = "";
let HAS_TOKEN = false;
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

const sourceName = (source) => ({
  "claude-code": "Claude",
  codex: "Codex",
  api: "Other/API",
  proxy: "Other/API",
  "openai-api": "Other/API",
  "anthropic-api": "Other/API",
}[source] || source || "未知来源");

const finite = (value) => Number.isFinite(value);
const tokenLabel = (value) => finite(value) ? compact(value) : "—";
const rateLabel = (value) => finite(value) ? value.toFixed(value >= 100 ? 0 : 1) : "—";
const timeLabel = (value) => !value ? "—" : new Date(value).toLocaleTimeString("zh-CN", {
  hour12: false, hour: "2-digit", minute: "2-digit",
});

function dataAgeMs() {
  const times = [
    latestLive && latestLive.connection && latestLive.connection.generated_at_ms,
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
    const physicalHeight = (workArea && workArea.size && workArea.size.height)
      || (monitor && monitor.size && monitor.size.height);
    const scale = monitor && monitor.scaleFactor || 1;
    return physicalHeight ? physicalHeight / scale : H_TARGET_MAX + WORK_AREA_MARGIN;
  } catch (_) {
    return H_TARGET_MAX + WORK_AREA_MARGIN;
  }
}

async function fitWindow() {
  const api = window.__TAURI__ && window.__TAURI__.window;
  const dpi = window.__TAURI__ && window.__TAURI__.dpi;
  if (!api || !dpi) return;
  const workHeight = await availableMonitorHeight();
  const maxHeight = Math.max(240, Math.min(H_TARGET_MAX, workHeight - WORK_AREA_MARGIN));
  const height = Math.min(maxHeight, Math.max(Math.min(H_MIN, maxHeight), contentHeight()));
  if (height === lastHeight) return;
  lastHeight = height;
  api.getCurrentWindow().setSize(new dpi.LogicalSize(PANEL_W, height)).catch(() => {});
}

function renderFreshness() {
  const connection = latestLive && latestLive.connection;
  const kind = connection && connection.kind || "offline";
  $("connection-state").dataset.mode = kind;
  const telemetryStale = !!(latestTelemetry && latestTelemetry.is_stale);
  // Channel only. While the stream is healthy the age is always "1s 前", so
  // spelling it out here is a number that never moves; when it stops being
  // healthy the footer says the age, where it actually carries information.
  $("connection-state").textContent =
    `${CONNECTION_LABEL[kind] || kind}${telemetryStale ? " · 历史数据陈旧" : ""}`;

  const online = latestLive && latestLive.device_online;
  const total = latestLive && latestLive.device_total;
  $("fleet-state").textContent = finite(online) && finite(total)
    ? `${online}/${total} 台在线`
    : "设备未知";

  document.body.dataset.mode = kind;
  const degraded = kind !== "live";
  document.body.classList.toggle("degraded", degraded);
  const age = dataAgeMs();
  $("footer-age").textContent = telemetryError
    ? `历史数据：${telemetryError}`
    : degraded ? (age == null ? "数据年龄未知" : `数据年龄 ${Math.round(age / 1000)}s`) : "";
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

function renderUsageCard(source, prefix) {
  const rows = latestTelemetry && latestTelemetry.rolling_5h.sources || [];
  const row = rows.find((item) => item.source === source);
  $(`${prefix}-5h-value`).textContent = row ? tokenLabel(row.tokens) : "—";
  const delta = $(`${prefix}-5h-change`);
  if (!row) {
    delta.textContent = "用量不可用";
    delta.dataset.direction = "unknown";
    return;
  }
  if (!finite(row.change_percent)) {
    delta.textContent = "前窗为 0 · 比较不可用";
    delta.dataset.direction = "unknown";
    return;
  }
  const sign = row.change_percent > 0 ? "+" : "";
  delta.textContent = `较前 5h ${sign}${row.change_percent.toFixed(1)}%`;
  delta.dataset.direction = row.change_percent > 0 ? "up" : row.change_percent < 0 ? "down" : "flat";
}

// Which window a quota card is leading with, and how that window is named on
// screen. The server decides the basis; the popover only has to say it out loud,
// because "24% of the 5 hours" and "24% of the week" are not the same warning.
const QUOTA_BASIS = {
  five_hour: { percent: "five_hour_percent", label: "5h" },
  weekly: { percent: "weekly_percent", label: "周" },
};

const SOURCE_TONE = { "claude-code": "claude", codex: "codex" };

// Whole percent, matching how the Live page writes the same authoritative
// numbers. A quota moves in points, not in tenths.
const percentLabel = (value) => finite(value) ? `${value.toFixed(0)}%` : "—";

function quotaDetail(quota) {
  if (quota.basis === "five_hour") {
    // The projection is absent until the window has enough elapsed time to
    // extrapolate from — an em dash, not 0%, because nothing has been ruled out.
    return `预估 ${percentLabel(quota.projected_percent)} · 周 ${percentLabel(quota.weekly_percent)}`;
  }
  // A weekly-only card used to spell out "无官方 5h 数据". The basis beside the
  // number already says 周, so the sentence only repeated it — and this card is
  // 170px wide.
  if (quota.basis === "weekly") return "";
  return "官方端点未报配额";
}

// Compact remaining time for a card this narrow. Not untilReset(): that one is
// shared with the web panel (ADR-0014), which has the room to say 后重置.
function quotaReset(minutes) {
  if (!(minutes > 0)) return "即将重置";
  const hours = Math.floor(minutes / 60);
  return hours >= 24 ? `${hours}h` : `${hours}h${minutes % 60}m`;
}

function renderQuotas() {
  const quotas = latestLive && latestLive.quotas;
  const root = $("quota-grid");
  if (!quotas || !quotas.length) {
    root.innerHTML = `<div class="empty">官方配额不可用</div>`;
    return;
  }
  root.innerHTML = quotas.map((quota) => {
    const basis = QUOTA_BASIS[quota.basis];
    const percent = basis ? quota[basis.percent] : null;
    const reset = basis && finite(quota.resets_in_minutes) ? quotaReset(quota.resets_in_minutes) : "";
    // The percentage answers "how full", the tokens answer it again in the unit
    // the user actually writes in. Both describe the same window, so they share
    // the headline line rather than being stacked.
    const used = finite(quota.used_tokens) ? compact(quota.used_tokens) : "";
    // The remainder is inferred (ADR-0025) and simply absent until the
    // calibration has evidence; the line closes up around it.
    const sub = [
      basis ? basis.label : "无官方配额",
      finite(quota.remaining_tokens) ? `还剩 ${compact(quota.remaining_tokens)}` : "",
      reset,
    ].filter(Boolean).join(" · ");
    const detail = quotaDetail(quota);
    return `<article class="usage-card quota-card ${esc(SOURCE_TONE[quota.source] || "other")}"
      data-basis="${esc(quota.basis)}">
      <div class="source-title"><span class="source-dot"></span>${esc(quota.label)} · 官方配额</div>
      <strong class="fig" data-severity="${finite(percent) ? severity(percent) : ""}">${
        finite(percent) ? percentLabel(percent) : "暂无"}${
        used ? `<span class="fig-aside">${esc(used)}</span>` : ""}</strong>
      <span class="quota-basis">${esc(sub)}</span>
      ${detail ? `<span class="delta">${esc(detail)}</span>` : ""}
    </article>`;
  }).join("");
}

function sourceRows(bucket) {
  return bucket && Array.isArray(bucket.sources) ? bucket.sources : [];
}

function coverageLabel(speed) {
  const coverage = speed && Array.isArray(speed.coverage) ? speed.coverage : [];
  if (!coverage.length) return "";
  return `覆盖 ${coverage.map((row) =>
    `${sourceName(row.source)} ${row.measured_events}/${row.total_events}`
  ).join(" · ")}`;
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
  const discovered = new Set(series.flatMap((bucket) => sourceRows(bucket).map((row) => row.source)));
  const ordered = ["claude-code", "codex"];
  [...discovered].filter((source) => !ordered.includes(source)).sort().forEach((source) => ordered.push(source));
  const peak = finite(speed.peak_tps)
    ? speed.peak_tps
    : series.reduce((value, bucket) => Math.max(value, bucket.aggregate_tps || 0), 0);
  const scale = Math.max(1, ...series.flatMap((bucket) =>
    sourceRows(bucket).map((row) => row.contribution_tps || 0)));
  root.innerHTML = ordered.map((source) => {
    const unavailable = unmeasured.has(source) && !measured.has(source);
    if (unavailable) {
      return `<div class="lane lane-${esc(source)} unavailable">
        <span class="lane-label">${esc(sourceName(source))}</span>
        <span class="lane-unavailable">速度不可用</span>
      </div>`;
    }
    const bars = series.map((bucket) => {
      const row = sourceRows(bucket).find((item) => item.source === source);
      const value = row && finite(row.contribution_tps) ? row.contribution_tps : 0;
      const height = value > 0 ? Math.max(3, value / scale * 100) : 0;
      const peakClass = peak > 0 && Math.abs((bucket.aggregate_tps || 0) - peak) < 1e-6
        ? " is-peak" : "";
      return `<i class="lane-bar${peakClass}" style="height:${height.toFixed(2)}%"
        title="${esc(timeLabel(bucket.start_ms))} · ${esc(rateLabel(value))} tok/s"></i>`;
    }).join("");
    return `<div class="lane lane-${esc(source)}">
      <span class="lane-label">${esc(sourceName(source))}</span>
      <span class="lane-bars">${bars}</span>
    </div>`;
  }).join("");
  const exactCoverage = coverageLabel(speed);
  const measuredLabel = [...measured].map(sourceName).join("、") || "无";
  const unavailableLabel = [...unmeasured].map(sourceName).join("、");
  $("speed-coverage").textContent = exactCoverage || (unavailableLabel
    ? `已测 ${measuredLabel} · ${unavailableLabel} 不可用`
    : `已测 ${measuredLabel}`);
}

function renderStats() {
  const speed = latestTelemetry && latestTelemetry.speed;
  $("peak-1h").textContent = speed && finite(speed.peak_tps)
    ? `${rateLabel(speed.peak_tps)} tok/s` : "—";
  $("active-ratio").textContent = speed && finite(speed.active_ratio)
    ? `${(speed.active_ratio * 100).toFixed(0)}%` : "—";
  $("measured-source-count").textContent = speed
    ? String((speed.measured_sources || []).length) : "—";
}

function renderModels() {
  const today = latestTelemetry && latestTelemetry.today;
  const models = today && today.models || [];
  $("today-total").textContent = today ? tokenLabel(today.total_tokens) : "—";
  $("model-count").textContent = today ? `${models.length} 个模型` : "—";
  $("model-strip").innerHTML = models.map((model, index) =>
    `<span class="model-segment tone-${index % 5}" style="flex-grow:${Math.max(0, model.tokens)}"></span>`
  ).join("");
  $("model-list").innerHTML = !today ? `<div class="empty">今日用量不可用</div>` : models.length ? models.map((model, index) => `
    <div class="model-row">
      <span class="model-dot tone-${index % 5}"></span>
      <span class="model-name" title="${esc(model.model)}">${esc(model.model)}</span>
      <span class="model-share">${finite(model.share) ? (model.share * 100).toFixed(1) + "%" : "—"}</span>
      <span class="fig">${tokenLabel(model.tokens)}</span>
    </div>`).join("") : `<div class="empty">今日暂无用量</div>`;
}

function renderContributors() {
  const sessions = (latestLive && latestLive.sessions || [])
    .filter((session) => finite(session.contribution_rate))
    .sort((a, b) => b.contribution_rate - a.contribution_rate);
  $("contributor-list").innerHTML = !latestLive ? `<div class="empty">贡献数据不可用</div>` : sessions.length ? sessions.slice(0, MAX_CONTRIBUTORS).map((session) => {
    const native = finite(session.native_rate) ? `自身 ${rateLabel(session.native_rate)} tok/s` : "自身速度未知";
    return `<div class="contributor-row">
      <span class="contributor-source">${esc(sourceName(session.tool))}</span>
      <span class="contributor-main">
        <b title="${esc(session.repository)}">${esc(session.repository)}</b>
        <small>${esc(session.model)} · ${esc(session.device)} · ${esc(native)}</small>
      </span>
      <span class="fig">${rateLabel(session.contribution_rate)} tok/s</span>
    </div>`;
  }).join("") : `<div class="empty">当前无已测贡献</div>`;
  const hidden = Math.max(latestLive && latestLive.sessions_more || 0, sessions.length - MAX_CONTRIBUTORS);
  $("contributors-more").textContent = hidden > 0 ? `另有 ${hidden} 项` : "";
}

function renderDevices() {
  const view = latestLive;
  const devices = view && view.devices || [];
  $("device-summary").textContent = view ? view.device_summary : "设备未知";
  $("device-list").innerHTML = !view ? `<div class="empty">设备数据不可用</div>` : devices.length ? devices.map((device) => {
    const processState = !device.has_procs ? "不可观测"
      : device.running > 0 ? `${device.running} 个会话` : "无打开会话";
    const state = { active: "活跃", idle: "空闲", stale: "陈旧", unknown: "未知" }[device.state] || device.state;
    return `<div class="device-row">
      <span class="device-dot" data-state="${esc(device.state)}"></span>
      <span class="device-name" title="${esc(device.name)}">${esc(device.name)}</span>
      <span>${processState}</span><span>${state}</span>
    </div>`;
  }).join("") : `<div class="empty">无设备数据</div>`;
  const more = view && view.devices_more || 0;
  $("devices-more").hidden = !more;
  $("devices-more").textContent = more ? `另有 ${more} 台设备` : "";
}

function renderAll() {
  renderFreshness();
  renderHero();
  renderUsageCard("claude-code", "claude");
  renderUsageCard("codex", "codex");
  renderQuotas();
  renderSpeedLanes();
  renderStats();
  renderModels();
  renderContributors();
  renderDevices();
  fitWindow();
}

function onLive(update) {
  latestLive = update && update.view || null;
  renderAll();
}

async function pullTelemetry() {
  const generation = ++telemetryGeneration;
  try {
    const telemetry = await invoke("telemetry_get", { range: "1h" });
    if (generation !== telemetryGeneration) return;
    latestTelemetry = telemetry;
    telemetryError = latestTelemetry.error || "";
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

const settingsEls = {
  main: $("main"),
  settings: $("settings"),
  input: $("server-input"),
  token: $("token-input"),
  message: $("settings-msg"),
  save: $("settings-save"),
};

function settingsMessage(text, kind) {
  settingsEls.message.textContent = text || "";
  settingsEls.message.className = "msg" + (kind ? ` ${kind}` : "");
  fitWindow();
}

function openSettings() {
  settingsEls.input.value = SERVER;
  settingsEls.token.value = "";
  settingsEls.token.placeholder = HAS_TOKEN
    ? "已保存访问令牌；留空保持不变"
    : "服务端只监听本机时留空";
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
  settingsEls.save.disabled = true;
  settingsMessage("验证中…");
  try {
    const stored = await invoke("settings_set", {
      server: settingsEls.input.value,
      token: settingsEls.token.value,
    });
    SERVER = stored.server;
    HAS_TOKEN = stored.has_token;
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
for (const input of [settingsEls.input, settingsEls.token]) {
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") saveSettings();
    if (event.key === "Escape") closeSettings();
  });
}
$("open-full").addEventListener("click", () => invoke("open_full_panel").catch(() => {}));

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && settingsEls.settings.hidden) {
    const api = window.__TAURI__ && window.__TAURI__.window;
    if (api) api.getCurrentWindow().hide().catch(() => {});
  }
});

async function boot() {
  const stored = await invoke("settings_get");
  SERVER = stored.server;
  HAS_TOKEN = !!stored.has_token;
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
  module.exports = { coverageLabel, onLive };
}
