"use strict";
// Activity-first menubar popover (ADR-0014, system redesign §7).
// Rust owns the deterministic payload → view-model transformation. This file
// renders that contract and keeps only presentation-time concerns: relative
// age, SVG geometry, overview refresh, settings, and window sizing.

const invoke = window.__TAURI__ && window.__TAURI__.core.invoke;
const listen = window.__TAURI__ && window.__TAURI__.event.listen;
const $ = (id) => document.getElementById(id);

let SERVER = "";
let HAS_TOKEN = false;
let overview = null;
let latestView = null;

const OVERVIEW_MS = 60000;
const REDRAW_MS = 10000;
const PANEL_W = 380;
const H_MIN = 280;
const H_MAX = 520;
let lastH = 0;

async function apiGet(path) {
  return invoke("api_get", { path });
}

function contentHeight() {
  const panel = document.querySelector(".panel");
  // Measure an intrinsic clone. Measuring the live flex item makes #main's
  // current clientHeight a lower bound, so a popover can grow but never shrink.
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

function fitWindow() {
  const w = window.__TAURI__ && window.__TAURI__.window;
  const dpi = window.__TAURI__ && window.__TAURI__.dpi;
  if (!w || !dpi) return;
  const height = Math.min(H_MAX, Math.max(H_MIN, contentHeight()));
  if (height === lastH) return;
  lastH = height;
  w.getCurrentWindow().setSize(new dpi.LogicalSize(PANEL_W, height)).catch(() => {});
}

// ── connection / freshness ───────────────────────────────────────────────

const CONNECTION_LABEL = {
  live: "实时",
  polling: "轮询",
  stale: "数据陈旧",
  unauthorized: "未授权",
  offline: "离线",
};

function ageLabel(generatedAt) {
  if (!generatedAt) return "年龄未知";
  return relTime(generatedAt);
}

function renderConnection(connection) {
  const kind = connection.kind || "offline";
  const el = $("link");
  el.dataset.mode = kind;
  el.textContent = `${CONNECTION_LABEL[kind] || kind} · ${ageLabel(connection.generated_at_ms)}`;
  document.body.dataset.mode = kind;
  document.body.classList.toggle("degraded", kind !== "live");
  document.body.classList.toggle("stale", !!connection.is_stale);
}

// ── promoted risk ────────────────────────────────────────────────────────

const TRACK = { x0: 2, x1: 352, base: 37.5, top: 3, max: 110 };
const trackY = (percent) =>
  TRACK.base - (Math.min(Math.max(percent, 0), TRACK.max) / TRACK.max) *
  (TRACK.base - TRACK.top);

function clockAt(ms) {
  if (!ms) return "";
  return new Date(ms).toLocaleTimeString("zh-CN", {
    hour12: false, hour: "2-digit", minute: "2-digit",
  });
}

function drawRisk(risk) {
  const root = $("risk");
  root.hidden = !risk;
  if (!risk) return;

  const used = Math.max(0, risk.used_percent || 0);
  const projected = Math.max(used, risk.projected_percent || 0);
  const breach = projected > 100;
  root.classList.toggle("breach", breach);
  $("risk-source").textContent = risk.source;
  $("risk-now").textContent = `${used.toFixed(0)}%`;
  $("risk-projected").textContent = `预计 ${projected.toFixed(0)}%`;
  $("risk-reset").textContent = untilReset(risk.remaining_minutes);

  const span = Math.max(1, risk.resets_at - risk.start_ms);
  const x = (ms) => TRACK.x0 +
    ((Math.min(Math.max(ms, risk.start_ms), risk.resets_at) - risk.start_ms) / span) *
    (TRACK.x1 - TRACK.x0);
  const xNow = x(risk.end_ms);
  const xEnd = x(risk.resets_at);
  const yUsed = trackY(used);
  const yProjected = trackY(projected);
  const ceilingY = trackY(100);

  $("track-past").setAttribute("d", `M ${TRACK.x0} ${TRACK.base} L ${xNow} ${yUsed}`);
  $("track-nowdot").setAttribute("cx", xNow);
  $("track-nowdot").setAttribute("cy", yUsed);
  $("axis-start").textContent = clockAt(risk.start_ms);
  $("axis-end").textContent = `${clockAt(risk.resets_at)} 重置`;
  $("axis-hit").textContent = "";

  if (breach && projected > used) {
    const fraction = Math.min(1, Math.max(0, (100 - used) / (projected - used)));
    const xHit = xNow + (xEnd - xNow) * fraction;
    $("track-future").setAttribute("d", `M ${xNow} ${yUsed} L ${xHit} ${ceilingY}`);
    $("track-breach").setAttribute("d", `M ${xHit} ${ceilingY} L ${xEnd} ${yProjected}`);
    const hitAt = risk.end_ms + (risk.resets_at - risk.end_ms) * fraction;
    $("axis-hit").textContent = `${clockAt(hitAt)} 触顶`;
  } else {
    $("track-future").setAttribute("d", `M ${xNow} ${yUsed} L ${xEnd} ${yProjected}`);
    $("track-breach").setAttribute("d", "");
  }

  $("track-desc").textContent =
    `${risk.source}：当前 ${used.toFixed(0)}%，预计 ${projected.toFixed(0)}%。`;
}

// ── activity / quota / devices ───────────────────────────────────────────

function showMore(id, count) {
  const el = $(id);
  el.hidden = !count;
  el.textContent = count ? `另有 ${count} 项` : "";
}

function renderSessions(view) {
  $("activity").dataset.kind = view.activity.kind;
  $("activity-hero").textContent = view.activity.text;
  $("session-list").innerHTML = view.sessions.map((session) => `
    <div class="session-row">
      <span class="session-tool" title="${esc(session.tool)}">${esc(session.tool)}</span>
      <span title="${esc(session.repository)}">${esc(session.repository)}</span>
      <span title="${esc(session.model)}">${esc(session.model)}</span>
      <span title="${esc(session.device)}">${esc(session.device)}</span>
      <span class="session-rate">${session.rate.toFixed(0)} t/s</span>
    </div>`).join("");
  showMore("sessions-more", view.sessions_more);
}

function renderQuota(view) {
  $("quota-summary").textContent = view.quota_summary || "暂无配额数据";
  $("quota-reset").textContent = view.quota_reset_minutes == null
    ? ""
    : untilReset(view.quota_reset_minutes);
  showMore("quotas-more", view.quotas_more);
}

function renderDevices(view) {
  $("device-head").textContent = view.device_summary;
  $("device-summary").textContent = view.device_summary;
  $("device-list").innerHTML = view.devices.map((device) => {
    const processState = !device.has_procs
      ? "不可观测"
      : device.running > 0 ? `${device.running} 个会话` : "无打开会话";
    const stateLabel = {
      active: "活跃", idle: "空闲", stale: "陈旧", unknown: "未知",
    }[device.state] || device.state;
    return `
      <div class="device-row">
        <span class="device-dot" data-state="${esc(device.state)}"></span>
        <span class="device-name" title="${esc(device.name)}">${esc(device.name)}</span>
        <span>${processState}</span>
        <span>${stateLabel}</span>
      </div>`;
  }).join("");
  showMore("devices-more", view.devices_more);
}

function renderReadouts(view) {
  $("burn-value").textContent = view.burn_per_minute == null
    ? "—"
    : `${compact(view.burn_per_minute)}/min`;
  const total = overview && overview.today && overview.today.total_tokens;
  $("today-value").textContent = Number.isFinite(total) ? compact(total) : "—";
}

function renderView(view) {
  latestView = view;
  renderConnection(view.connection);
  drawRisk(view.risk);
  renderSessions(view);
  renderQuota(view);
  renderReadouts(view);
  renderDevices(view);
  fitWindow();
}

function renderUnknown(message) {
  renderConnection({
    kind: "offline", generated_at_ms: null, age_ms: null, is_stale: false,
  });
  $("risk").hidden = true;
  $("activity").dataset.kind = "unknown";
  $("activity-hero").textContent = "活动未知";
  $("session-list").innerHTML = message ? `<div class="error">${esc(message)}</div>` : "";
  for (const id of ["sessions-more", "quotas-more", "devices-more"]) $(id).hidden = true;
  $("quota-summary").textContent = "—";
  $("quota-reset").textContent = "";
  $("burn-value").textContent = "—";
  $("today-value").textContent = "—";
  $("device-head").textContent = "设备未知";
  $("device-summary").textContent = "设备未知";
  $("device-list").innerHTML = "";
  fitWindow();
}

function onUpdate(update) {
  if (update.view) renderView(update.view);
  else renderUnknown("客户端与服务端版本不匹配");
}

async function pullOverview() {
  try {
    overview = await apiGet("/api/v1/overview?days=1");
  } catch (_) {
    // Unknown is deliberately not zero: overview can fail while live remains.
    overview = null;
  }
  if (latestView) renderReadouts(latestView);
}

function redraw() {
  if (latestView) {
    renderConnection(latestView.connection);
    renderReadouts(latestView);
  }
}

// ── settings / actions ───────────────────────────────────────────────────

const els = {
  main: $("main"),
  settings: $("settings"),
  input: $("server-input"),
  token: $("token-input"),
  msg: $("settings-msg"),
  save: $("settings-save"),
};

function say(text, kind) {
  els.msg.textContent = text || "";
  els.msg.className = "msg" + (kind ? " " + kind : "");
  fitWindow();
}

function openSettings() {
  els.input.value = SERVER;
  els.token.value = "";
  els.token.placeholder = HAS_TOKEN
    ? "已保存访问令牌；留空保持不变"
    : "服务端只监听本机时留空";
  say("");
  els.main.hidden = true;
  els.settings.hidden = false;
  fitWindow();
  els.input.focus();
  els.input.select();
}

function closeSettings() {
  els.settings.hidden = true;
  els.main.hidden = false;
  // settings_set already respawns the bridge. Cancel changes nothing, and a
  // second refresh here used to abort the connection that had just started.
  pullOverview();
  fitWindow();
}

async function saveSettings() {
  els.save.disabled = true;
  say("验证中…");
  try {
    const stored = await invoke("settings_set", {
      server: els.input.value,
      token: els.token.value,
    });
    SERVER = stored.server;
    HAS_TOKEN = stored.has_token;
    closeSettings();
  } catch (error) {
    say(String(error), "bad");
  } finally {
    els.save.disabled = false;
  }
}

$("open-settings").addEventListener("click", openSettings);
$("settings-cancel").addEventListener("click", closeSettings);
els.save.addEventListener("click", saveSettings);
for (const input of [els.input, els.token]) {
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") saveSettings();
    if (event.key === "Escape") closeSettings();
  });
}

$("open-full").addEventListener("click", () => {
  invoke("open_full_panel").catch(() => {});
});

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && els.settings.hidden) {
    const w = window.__TAURI__ && window.__TAURI__.window;
    if (w) w.getCurrentWindow().hide().catch(() => {});
  }
});

async function boot() {
  const stored = await invoke("settings_get");
  SERVER = stored.server;
  HAS_TOKEN = !!stored.has_token;
  await listen("live", (event) => onUpdate(event.payload));
  await listen("open-settings", openSettings);
  await pullOverview();
  setInterval(pullOverview, OVERVIEW_MS);
  setInterval(redraw, REDRAW_MS);
}

if (!invoke || !listen) {
  renderUnknown("Tauri IPC 不可用");
} else {
  boot().catch((error) => renderUnknown(String(error)));
}
