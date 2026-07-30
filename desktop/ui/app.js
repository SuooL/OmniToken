"use strict";
// Menubar popover (ADR-0008, ADR-0014).
//
// Shows what is worth a glance — how close the nearest quota wall is, how fast
// the models are generating right now, what today adds up to — and leaves
// analysis to the nine-page panel in a browser.
//
// Number and time formatting comes from format-core.js, shared verbatim with
// that panel: the popover must never describe the same ten minutes differently
// from the page it links to.

// Requires `withGlobalTauri` in tauri.conf.json — Tauri v2 does not inject
// window.__TAURI__ by default. Without the guard a missing bridge throws on
// this line and the panel renders as an empty shell with no clue why.
const invoke = window.__TAURI__ && window.__TAURI__.core.invoke;
const listen = window.__TAURI__ && window.__TAURI__.event.listen;

// Set from the stored settings before the first snapshot. Rust owns the value
// and the normalisation; this is only the copy the panel reads between saves.
let SERVER = "";

// Live data is pushed, not pulled: Rust holds one /api/v1/stream connection and
// forwards each snapshot here, to the tray glyph and to the quota alerts at the
// same time (ADR-0014). Nothing in this file polls for it.
//
// Today's total is the exception. It comes from /api/v1/overview, which is not
// on the stream, and a figure that only changes as the day accumulates does not
// need the live cadence.
const OVERVIEW_MS = 60000;
// Relative times ("3m 前") go stale on their own, with no event to prompt a
// redraw — same reason the Live page keeps a timer alongside its stream.
const REDRAW_MS = 10000;
let overview = null;
let latest = null;

// All HTTP goes through Rust: the webview is on tauri://localhost and the
// server sends no CORS headers on purpose (ADR-0008).
async function apiGet(path) {
  return invoke("api_get", { base: SERVER, path });
}

const $ = (id) => document.getElementById(id);

// ── window height ─────────────────────────────────────────────────────────

// The popover is anchored by its top edge, just under the tray icon, so it can
// grow downwards without being re-positioned — which is why this can live in JS
// and does not need the tray rect Rust holds.
//
// A fixed height was the alternative, and it is wrong in both directions: with
// one quota it left a third of the popover blank, and with four it pushed the
// readout into an inner scroll while the footer — the only way into settings —
// stayed pinned.
const PANEL_W = 340;
const H_MIN = 180;
// Past this a menubar popover stops being a glance. #main keeps its own
// overflow so an unusually long list scrolls inside instead of running off
// the bottom of the screen.
const H_MAX = 560;

let lastH = 0;

function contentHeight() {
  const panel = document.querySelector(".panel");
  const cs = getComputedStyle(panel);
  const kids = [...panel.children].filter((el) => !el.hidden);
  // scrollHeight, not offsetHeight: #main is a flex child stretched to whatever
  // the current window allows, so its laid-out height is the answer we are
  // trying to replace, and only its content height is meaningful here.
  const inner = kids.reduce((h, el) => h + el.scrollHeight, 0) +
    (parseFloat(cs.rowGap) || 0) * Math.max(0, kids.length - 1);
  return Math.ceil(inner +
    parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom) +
    parseFloat(cs.borderTopWidth) + parseFloat(cs.borderBottomWidth));
}

// Only resize when the number actually moved: a transparent always-on-top
// window repainting on every poll is visible, and most polls change nothing
// about the layout.
function fitWindow() {
  const w = window.__TAURI__ && window.__TAURI__.window;
  const dpi = window.__TAURI__ && window.__TAURI__.dpi;
  if (!w || !dpi) return;
  const h = Math.min(H_MAX, Math.max(H_MIN, contentHeight()));
  if (h === lastH) return;
  lastH = h;
  w.getCurrentWindow().setSize(new dpi.LogicalSize(PANEL_W, h)).catch(() => {
    // Not fatal: the popover simply keeps the size it has.
  });
}

// ── connection state ──────────────────────────────────────────────────────

// The popover must say which channel its figures arrived on. A number alone
// cannot distinguish fresh from frozen, and a silently dead stream showing a
// plausible figure forever is the failure mode worth designing against — which
// is why "降级为轮询" is stated rather than hidden (ADR-0014).
const LINK_TEXT = { live: "实时", polling: "降级轮询", offline: "连接失败" };
function setLink(mode) {
  const el = $("link");
  el.dataset.mode = mode;
  const stamp = mode === "offline"
    ? ""
    : " · " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
  el.textContent = (LINK_TEXT[mode] || mode) + stamp;
}

// ── hero: the tightest live quota, on the menubar's own dial ───────────────

// 270° of a circle at r=41.75 — the visible sweep of the tray glyph.
const RING_SWEEP = 196.71;
const RING_CIRCUM = 262.28;

// Expired entries describe a window that has already rolled over, so their
// number is stale; showing one would pin the popover to a spent window.
function liveQuotas(list) {
  return (list || []).filter((q) => !q.expired);
}

// Order matches the Live page: 5h first, then the weekly windows.
const SCOPE_ORDER = { five_hour: 0, seven_day: 1 };
function sortQuotas(list) {
  return [...list].sort((a, b) =>
    (SCOPE_ORDER[a.scope] ?? 9) - (SCOPE_ORDER[b.scope] ?? 9) ||
    a.window_minutes - b.window_minutes);
}

function quotaName(q) {
  const scope = String(q.scope || "").startsWith("seven_day:") ? q.scope.slice(10) : "";
  return sourceLabel(q.source) + " · " + (q.window_label || "") + (scope ? " · " + scope : "");
}

// The dash length goes on style, not as an attribute: the stylesheet sets an
// initial stroke-dasharray so the dial starts empty, and CSS always beats an SVG
// presentation attribute — written with setAttribute the arc silently never
// drew at all.
function setRing(fill, pct, cls) {
  fill.style.strokeDasharray = `${pct === null ? 0 : pct} ${RING_CIRCUM}`;
  fill.setAttribute("class", cls);
}

// `why` separates the two ways there can be no dial reading. They are not the
// same statement and must not render as one: "no quota data" is something we
// know about the server, "cannot reach the server" is an admission that we know
// nothing — the UI-side of 权威优先,推断标注.
function renderHero(q, why) {
  const fill = $("ring-fill");
  $("ring").classList.toggle("no-signal", !q);
  if (!q) {
    $("hero-value").textContent = "—";
    $("hero-name").textContent = why || "暂无配额数据";
    $("hero-sub").textContent = "";
    setRing(fill, null, "fill");
    return;
  }
  // Authoritative quota can legitimately report past 100 (ADR-0007), so the
  // dial clamps while the printed figure does not lie about it.
  const pct = Math.max(0, q.used_percent || 0);
  $("hero-value").innerHTML = `${pct.toFixed(0)}<span class="unit">%</span>`;
  $("hero-name").textContent = quotaName(q);
  $("hero-sub").textContent = untilReset(q.remaining_minutes);
  setRing(fill, (Math.min(100, pct) / 100 * RING_SWEEP).toFixed(2),
    "fill " + severity(pct));
}

// Everything the hero is not. Repeating the tightest window in the list below
// would spend a third of the popover saying the same thing twice.
function renderQuotas(rest) {
  const root = $("quotas");
  root.innerHTML = rest.slice(0, 3).map((q) => {
    const pct = Math.max(0, q.used_percent || 0);
    return `
      <div class="quota">
        <div class="head">
          <span class="name">${esc(quotaName(q))}</span>
          <span class="pct">${pct.toFixed(0)}%</span>
        </div>
        <div class="meter" role="meter" aria-label="${esc(quotaName(q))}"
             aria-valuenow="${pct.toFixed(0)}" aria-valuemin="0" aria-valuemax="100">
          <i class="${severity(pct)}" style="width:${Math.min(100, pct)}%"></i>
        </div>
      </div>`;
  }).join("");
}

// ── the three live figures ────────────────────────────────────────────────

// Generation speed (ADR-0009): divided by the union of generation intervals, so
// it says how fast the model emits — not how much of the window was busy. That
// is what makes it a different number from the burn rate beside it.
function renderSpeed(sp) {
  const tps = sp.tps || 0;
  const busy = tps > 0;
  $("tile-speed").classList.toggle("idle", !busy);
  $("speed-value").innerHTML = busy
    ? `${tps.toFixed(1)}<span class="unit"> t/s</span>`
    : "—";
  const n = (sp.sessions || []).length;
  $("speed-sub").textContent = busy ? `${n} 个会话在生成` : "近 10 分钟没有生成";
  renderStrip(sp);
}

// Union of generation intervals across the window, as blocks on a track — the
// Live page's lane element at popover scale.
function renderStrip(sp) {
  const el = $("strip");
  const t0 = sp.window_start_ms, t1 = sp.window_end_ms;
  const spans = sp.spans || [];
  if (!t0 || !t1 || t1 <= t0 || !spans.length) {
    el.hidden = true;
    return;
  }
  el.hidden = false;
  const span = t1 - t0;
  $("strip-track").innerHTML = spans.map(([a, b]) => {
    const left = ((Math.max(a, t0) - t0) / span) * 100;
    const width = ((Math.min(b, t1) - Math.max(a, t0)) / span) * 100;
    return `<i class="blk" style="left:${left}%;width:${Math.max(width, 0.6)}%"></i>`;
  }).join("");
  $("strip-note").textContent =
    `${Math.round(((sp.active_ms || 0) / span) * 100)}% 在生成`;
}

// Burn divides by the whole window and so includes idle time — "how much am I
// consuming". Naming what is counted matters: the figure excludes cache reads,
// and without saying so it invites comparison with the speed beside it.
function renderBurn(b) {
  $("burn-value").innerHTML =
    `${compact(b.per_minute || 0)}<span class="unit">/min</span>`;
  $("burn-sub").textContent = b.window_minutes
    ? `${b.window_minutes}m 内新增 ${compact(b.tokens || 0)}`
    : "";
}

function renderToday(ov) {
  const t = (ov && ov.today) || {};
  $("today-value").textContent = compact(t.total_tokens || 0);
  // Costs live at the top level, not inside `today`. Prefer the equivalent
  // figure: on a subscription the real spend is 0, which would read as "free".
  const c = (ov && ov.costs && ov.costs.today) || {};
  const cost = c.equivalent_usd || c.real_usd || 0;
  $("today-sub").textContent = cost > 0 ? usd(cost) : (t.events ? t.events + " 请求" : "");
}

// Ground truth from the machines' own process tables (ADR-0012). "No reporter"
// is a different statement from "nothing is open" and must never be rendered as
// one — the wording here matches the Live page for exactly that reason.
function renderProcs(procs, devices) {
  const reporters = procs.reporters || [];
  const el = $("procs");
  if (!reporters.length) {
    const n = (devices || []).length;
    el.textContent = n ? `${n} 台设备 · 无进程数据` : "";
    return;
  }
  const open = (procs.sessions || []).length;
  el.textContent = `${reporters.length} 台机器可见 · ` +
    (open ? `${open} 个会话开着` : "没有会话开着");
}

// ── receiving ─────────────────────────────────────────────────────────────

function fail(msg) {
  setLink("offline");
  // The hint matters most on a fresh install pointed at the default address:
  // an unreachable server and a wrong address look identical from here.
  renderHero(null, "连不上服务端");
  $("quotas").innerHTML =
    `<div class="error">${esc(msg)}</div>` +
    `<div class="empty">点右下角「设置」检查服务端地址。</div>`;
  $("strip").hidden = true;
  $("procs").textContent = "";
  for (const id of ["speed-value", "burn-value", "today-value"]) {
    $(id).textContent = "—";
  }
  for (const id of ["speed-sub", "burn-sub", "today-sub"]) {
    $(id).textContent = "";
  }
  fitWindow();
}

function render(live) {
  const quotas = sortQuotas(liveQuotas(live.quotas));
  renderHero(quotas[0]);
  renderQuotas(quotas.slice(1));
  renderSpeed(live.speed || {});
  renderBurn(live.burn || {});
  renderProcs(live.processes || {}, live.devices);
  renderToday(overview);
  fitWindow();
}

// Rust publishes { mode, data? }. `data` is absent when it has nothing new to
// show — a channel change on its own — and that must update the label without
// blanking figures that are merely a minute old.
function onUpdate(update) {
  if (update.data) {
    latest = update.data;
    render(latest);
  } else if (update.mode === "offline") {
    fail("连不上服务端");
    return;
  }
  setLink(update.mode);
}

// One failing endpoint should not blank the other half of the popover, so this
// keeps whatever it last had on failure.
async function pullOverview() {
  try {
    overview = await apiGet("/api/v1/overview?days=1");
    if (latest) render(latest);
  } catch (e) {
    // The stream, not this, is what reports connectivity.
  }
}

// Re-render from the last snapshot so relative times move on without an event.
function redraw() {
  if (latest) render(latest);
}

// ── settings ──────────────────────────────────────────────────────────────

const els = {
  main: $("main"),
  settings: $("settings"),
  input: $("server-input"),
  msg: $("settings-msg"),
  save: $("settings-save"),
};

function showServer() {
  $("server").textContent = SERVER.replace(/^https?:\/\//, "");
}

// A connection error can run to several lines, so the message changing is a
// layout change like any other.
function say(text, kind) {
  els.msg.textContent = text || "";
  els.msg.className = "msg" + (kind ? " " + kind : "");
  fitWindow();
}

function openSettings() {
  els.input.value = SERVER;
  say("");
  els.main.hidden = true;
  els.settings.hidden = false;
  fitWindow();
  els.input.focus();
  els.input.select();
}

// Always reconnect on the way out. A save can succeed even when the probe fails,
// so leaving by way of 取消 can still mean the address changed — and the readout
// would otherwise keep showing another server's numbers until that server
// happens to broadcast something.
function closeSettings() {
  els.settings.hidden = true;
  els.main.hidden = false;
  invoke("refresh_now").catch(() => {});
  pullOverview();
  fitWindow();
}

async function saveSettings() {
  els.save.disabled = true;
  say("保存中…");
  try {
    // Persist first, then check. The address may well be right and the server
    // simply not started yet, and throwing away what was just typed would be a
    // poor way to report that.
    const stored = await invoke("settings_set", { server: els.input.value });
    SERVER = stored.server;
    showServer();
    els.input.value = SERVER;

    try {
      await apiGet("/api/v1/health");
    } catch (e) {
      say("已保存,但连接失败:" + String(e), "bad");
      return;
    }
    // settings_set already restarted the bridge against the new address.
    closeSettings();
  } catch (e) {
    // settings_set rejected the input, or could not write the file.
    say(String(e), "bad");
  } finally {
    els.save.disabled = false;
  }
}

$("open-settings").addEventListener("click", openSettings);
$("settings-cancel").addEventListener("click", closeSettings);
els.save.addEventListener("click", saveSettings);
els.input.addEventListener("keydown", (e) => {
  if (e.key === "Enter") saveSettings();
  if (e.key === "Escape") closeSettings();
});

// The full panel is one click from the popover as well as from the tray menu:
// looking at a figure and wanting the history behind it is the common path, and
// making it right-click-only would hide it.
$("open-full").addEventListener("click", () => {
  invoke("open_full_panel").catch(() => {});
});

// Esc dismisses the popover, like any menubar panel. The settings view takes Esc
// for its own cancel, so this only applies to the readout.
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && els.settings.hidden) {
    const w = window.__TAURI__ && window.__TAURI__.window;
    if (w) w.getCurrentWindow().hide().catch(() => {});
  }
});

// ── boot ──────────────────────────────────────────────────────────────────

async function boot() {
  const stored = await invoke("settings_get");
  SERVER = stored.server;
  showServer();

  // Subscribe before anything else: the bridge is already running by the time
  // the webview loads, and its next snapshot is the first thing to render.
  await listen("live", (e) => onUpdate(e.payload));
  // Opening settings from the tray menu is the same view as the footer button.
  await listen("open-settings", () => openSettings());

  await pullOverview();
  setInterval(pullOverview, OVERVIEW_MS);
  setInterval(redraw, REDRAW_MS);
}

if (!invoke || !listen) {
  fail("Tauri IPC 不可用:请检查 tauri.conf.json 的 withGlobalTauri");
} else {
  boot().catch((e) => fail(String(e)));
}
