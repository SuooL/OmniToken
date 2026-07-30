"use strict";
// Menubar popover (ADR-0008, ADR-0014).
//
// A menubar glance has one question in it: am I going to run out before the
// window resets. So the popover is built around that forecast, and leaves
// analysis to the nine-page panel in a browser.
//
// The forecast comes from the `windows` array of /api/v1/live, which the server
// already computes (F11 burn projection, internal/server/windows.go) and which
// v1 of this popover ignored entirely — it showed a fullness ring instead, which
// is a second reading of what the 18pt tray glyph already says.
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
// The secret itself never crosses IPC. This bit only lets settings explain that
// leaving the password field blank retains the credential already stored.
let HAS_TOKEN = false;

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
// server sends no CORS headers on purpose (ADR-0008). Rust also holds the token
// (ADR-0016) — the webview never sees it, so it cannot end up in a rendered
// string or an error message.
async function apiGet(path) {
  return invoke("api_get", { path });
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

// ── the signature: collision forecast ─────────────────────────────────────

// `hidden` is an HTMLElement property; on an SVG element assigning it silently
// creates a plain JS property and leaves the attribute in place, so the
// stylesheet's [hidden] rule keeps the node invisible. The ceiling line was
// missing for exactly that reason. Same trap as writing stroke-dasharray as an
// attribute when the stylesheet also sets it: for SVG, go through attributes.
function showSvg(el, on) {
  if (on) el.removeAttribute("hidden");
  else el.setAttribute("hidden", "");
}

// Track geometry, in the SVG's own units (viewBox 314×46).
const TR = { x0: 2, x1: 312, base: 40.5, top: 4, max: 110 };
// y for a quota percentage. The scale runs to 110 rather than 100 so a forecast
// that breaches the ceiling has somewhere to go — clamping it at the wall would
// hide exactly the case worth showing.
const trY = (pct) => TR.base - (Math.min(pct, TR.max) / TR.max) * (TR.base - TR.top);
const CEILING_Y = trY(100);

// Which window the popover leads with: the one closest to trouble.
//
// Ranked by the forecast, not by the current level. Sitting at 40% with four
// hours of this rate left is the situation worth leading with, and a
// level-ordered list puts it below a window at 60% that has already stopped
// growing.
function leadWindow(windows) {
  const authoritative = (windows || []).filter((w) => w.authoritative && w.resets_at);
  if (!authoritative.length) return null;
  return authoritative.reduce((a, b) =>
    (b.projected_percent || b.used_percent || 0) > (a.projected_percent || a.used_percent || 0)
      ? b : a);
}

// Strip the "· 5 小时窗口" the server appends: the axis underneath already says
// where the window starts and ends, so repeating it in the label is the same
// fact twice.
function windowLabel(w) {
  return String(w.label || "").replace(/\s*·\s*5 小时窗口\s*$/, "");
}

function clockAt(ms) {
  return new Date(ms).toLocaleTimeString("zh-CN", {
    hour12: false, hour: "2-digit", minute: "2-digit",
  });
}

function renderForecast(w, quotas) {
  const root = $("forecast");
  if (!w) {
    // No authoritative window means no cliff to forecast. Say that, rather than
    // drawing a forecast off a rolling look-back — an inferred boundary dressed
    // as a real one is the one thing this panel must not do.
    root.classList.remove("over");
    $("fc-window").textContent = quotas && quotas.length ? "无 5 小时窗口数据" : "暂无配额数据";
    $("fc-now").textContent = "—";
    $("fc-arrow").hidden = true;
    $("fc-proj").textContent = "";
    $("fc-reset").textContent = "";
    clearTrack();
    return;
  }

  const used = Math.max(0, w.used_percent || 0);
  const proj = Math.max(used, w.projected_percent || 0);
  const over = proj >= 100;

  root.classList.toggle("over", over);
  $("fc-window").textContent = windowLabel(w);
  $("fc-now").textContent = `${used.toFixed(0)}%`;
  $("fc-arrow").hidden = false;
  // Named, because a bare second percentage beside the first reads as a range.
  $("fc-proj").textContent = `窗口结束约 ${Math.min(999, proj).toFixed(0)}%`;
  $("fc-reset").textContent = untilReset(w.remaining_minutes);

  drawTrack(w, used, proj, over);
}

function clearTrack() {
  for (const id of ["track-past", "track-future", "track-breach"]) {
    $(id).setAttribute("d", "");
  }
  showSvg($("track-ceiling"), false);
  $("track-nowline").setAttribute("y1", "0");
  $("track-nowline").setAttribute("y2", "0");
  $("track-nowdot").setAttribute("cx", "-9");
  $("track").querySelector("desc").textContent = "";
  for (const id of ["ax-start", "ax-ceiling", "ax-end"]) $(id).textContent = "";
}

// Three line styles, each carrying a claim about how well the value is known:
//
//   dotted  the route from window start to now. Both endpoints are known — a
//           window opens at zero, and the level now is authoritative — but the
//           payload never carries the path between them. A smooth ramp is the
//           obvious chart here and it would assert a uniform burn we did not
//           observe.
//   dot     the single authoritative reading.
//   dashed  the projection, at the rate observed so far.
//
// That is 权威优先,推断标注 drawn rather than written.
function drawTrack(w, used, proj, over) {
  const span = Math.max(1, w.resets_at - w.start_ms);
  const x = (ms) => TR.x0 + ((Math.min(Math.max(ms, w.start_ms), w.resets_at) - w.start_ms) / span) * (TR.x1 - TR.x0);
  const xNow = x(w.end_ms);
  const yUsed = trY(used);
  const yProj = trY(proj);

  $("track-past").setAttribute("d", `M ${TR.x0} ${TR.base} L ${xNow} ${yUsed}`);
  $("track-nowline").setAttribute("x1", xNow);
  $("track-nowline").setAttribute("x2", xNow);
  $("track-nowline").setAttribute("y1", yUsed);
  $("track-nowline").setAttribute("y2", TR.base);
  $("track-nowdot").setAttribute("cx", xNow);
  $("track-nowdot").setAttribute("cy", yUsed);

  showSvg($("track-ceiling"), true);

  $("ax-start").textContent = clockAt(w.start_ms);

  // Split the forecast at the ceiling so the breach reads as a distinct event
  // with a time attached, not as a line that happens to end up high.
  const breaching = over && proj > used;
  if (breaching) {
    const t = (100 - used) / (proj - used); // fraction of the remaining window
    const xHit = xNow + (x(w.resets_at) - xNow) * t;
    $("track-future").setAttribute("d", `M ${xNow} ${yUsed} L ${xHit} ${CEILING_Y}`);
    $("track-breach").setAttribute("d", `M ${xHit} ${CEILING_Y} L ${x(w.resets_at)} ${yProj}`);

    const hitMs = w.end_ms + (w.resets_at - w.end_ms) * t;
    $("ax-ceiling").textContent = `${clockAt(hitMs)} 触顶`;
    // Sit the caption under the crossing. Clamped so it cannot overhang either
    // end of the panel when the breach lands at the edge of the window.
    const pos = ((xHit - TR.x0) / (TR.x1 - TR.x0)) * 100;
    $("ax-ceiling").style.left = `${Math.min(84, Math.max(16, pos)).toFixed(1)}%`;
    // Drop the reset clock from the axis while a breach is showing: the two
    // captions collide when the crossing lands late in the window, the crossing
    // is the more urgent of the two, and the line above already says how long
    // there is until reset.
    $("ax-end").textContent = "";
  } else {
    $("track-future").setAttribute("d", `M ${xNow} ${yUsed} L ${x(w.resets_at)} ${yProj}`);
    $("track-breach").setAttribute("d", "");
    // Nothing to say in the middle of the axis: the ceiling labels itself on the
    // line, so repeating "100%" here was the same fact twice. The slot exists for
    // the one thing only a breach has — when it happens.
    $("ax-ceiling").textContent = "";
    $("ax-end").textContent = `${clockAt(w.resets_at)} 重置`;
  }

  // The chart is decorative to a screen reader; the sentence is the content.
  $("track").querySelector("desc").textContent =
    `${windowLabel(w)}:当前 ${used.toFixed(0)}%,按当前速率窗口结束约 ` +
    `${proj.toFixed(0)}%${over ? ",将超出配额" : ""}。`;
}

// Everything that is not the lead window: one line each, so the forecast stays
// the only loud thing on the panel.
function renderOthers(quotas, lead) {
  const root = $("others");
  const rest = quotas.filter((q) => !isLeadQuota(q, lead));
  root.innerHTML = rest.slice(0, 3).map((q) => {
    const pct = Math.max(0, q.used_percent || 0);
    return `
      <div class="oq">
        <span class="eyebrow">${esc(quotaName(q))}</span>
        <span class="bar" role="meter" aria-label="${esc(quotaName(q))}"
              aria-valuenow="${pct.toFixed(0)}" aria-valuemin="0" aria-valuemax="100">
          <i style="width:${Math.min(100, pct)}%"></i>
        </span>
        <span class="fig">${pct.toFixed(0)}%</span>
      </div>`;
  }).join("");
}

// The lead window is identified by source + a 5-hour boundary, which is what
// buildWindowCards keyed it on; matching on the label would break the moment
// the server rewords it.
function isLeadQuota(q, lead) {
  return !!lead && q.source === lead.key && q.window_minutes === 300;
}

// Display name for one quota row.
function quotaName(q) {
  const scope = String(q.scope || "").startsWith("seven_day:") ? q.scope.slice(10) : "";
  return sourceLabel(q.source) + " · " + (q.window_label || "") + (scope ? " · " + scope : "");
}

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

// ── readouts ──────────────────────────────────────────────────────────────

// Generation speed (ADR-0009): divided by the union of generation intervals, so
// it says how fast the model emits — not how much of the window was busy. That
// is what makes it a different number from the burn rate beside it.
function renderSpeed(sp) {
  const tps = sp.tps || 0;
  const busy = tps > 0;
  $("ro-speed").classList.toggle("idle", !busy);
  $("speed-value").innerHTML = busy
    ? `${tps.toFixed(0)}<span class="unit"> t/s</span>`
    : "闲";
  renderStrip(sp);
}

// Union of generation intervals across the window, as blocks on a track.
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
    return `<i style="left:${left}%;width:${Math.max(width, 0.6)}%"></i>`;
  }).join("");
  $("strip-note").textContent =
    `近 ${Math.round(span / 60000)} 分钟 ${Math.round(((sp.active_ms || 0) / span) * 100)}% 在生成`;
}

// Burn divides by the whole window and so includes idle time — "how much am I
// consuming", as against how fast it emits.
function renderBurn(b) {
  $("burn-value").innerHTML =
    `${compact(b.per_minute || 0)}<span class="unit">/min</span>`;
}

function renderToday(ov) {
  const t = (ov && ov.today) || {};
  $("today-value").textContent = compact(t.total_tokens || 0);
}

// Ground truth from the machines' own process tables (ADR-0012). "No reporter"
// is a different statement from "nothing is open" and must never be rendered as
// one — the wording here matches the Live page for exactly that reason.
function renderProcs(procs, devices) {
  const reporters = procs.reporters || [];
  const el = $("procs");
  const n = (devices || []).length;
  if (!reporters.length) {
    el.textContent = n ? `${n} 台设备 · 无进程数据` : "";
    return;
  }
  const open = (procs.sessions || []).length;
  el.textContent = `${n} 台设备 · ${reporters.length} 台可见 · ` +
    (open ? `${open} 个会话开着` : "没有会话开着");
}

// ── receiving ─────────────────────────────────────────────────────────────

function fail(msg) {
  setLink("offline");
  $("forecast").classList.remove("over");
  $("fc-window").textContent = "连不上服务端";
  $("fc-now").textContent = "—";
  $("fc-arrow").hidden = true;
  $("fc-proj").textContent = "";
  $("fc-reset").textContent = "";
  clearTrack();
  // The hint matters most on a fresh install pointed at the default address:
  // an unreachable server and a wrong address look identical from here.
  $("others").innerHTML =
    `<div class="error">${esc(msg)}</div>` +
    `<div class="empty">点右下角「设置」检查服务端地址。</div>`;
  $("strip").hidden = true;
  $("procs").textContent = "";
  for (const id of ["speed-value", "burn-value", "today-value"]) {
    $(id).textContent = "—";
  }
  fitWindow();
}

function render(live) {
  const quotas = sortQuotas(liveQuotas(live.quotas));
  const lead = leadWindow(live.windows);
  renderForecast(lead, quotas);
  renderOthers(quotas, lead);
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
  token: $("token-input"),
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
  invoke("refresh_now").catch(() => {});
  pullOverview();
  fitWindow();
}

async function saveSettings() {
  els.save.disabled = true;
  say("保存中…");
  try {
    const stored = await invoke("settings_set", {
      server: els.input.value,
      token: els.token.value,
    });
    SERVER = stored.server;
    HAS_TOKEN = stored.has_token;
    showServer();
    els.input.value = SERVER;
    closeSettings();
  } catch (e) {
    // Validation happens before persistence, so prior working settings remain
    // active when the address or credentials fail.
    say(String(e), "bad");
  } finally {
    els.save.disabled = false;
  }
}

$("open-settings").addEventListener("click", openSettings);
$("settings-cancel").addEventListener("click", closeSettings);
els.save.addEventListener("click", saveSettings);
for (const el of [els.input, els.token]) {
  el.addEventListener("keydown", (e) => {
    if (e.key === "Enter") saveSettings();
    if (e.key === "Escape") closeSettings();
  });
}

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
  HAS_TOKEN = !!stored.has_token;
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
