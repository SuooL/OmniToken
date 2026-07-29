"use strict";
// Menubar popover. Shows only what is worth a glance — quota, today's usage —
// and leaves analysis to the full panel in a browser (ADR-0008).

// Requires `withGlobalTauri` in tauri.conf.json — Tauri v2 does not inject
// window.__TAURI__ by default. Without the guard a missing bridge throws on
// this line and the panel renders as an empty shell with no clue why.
const invoke = window.__TAURI__ && window.__TAURI__.core.invoke;

// Set from the stored settings before the first poll. Rust owns the value and
// the normalisation; this is only the copy the panel reads between saves.
let SERVER = "";

const POLL_MS = 15000;

// All HTTP goes through Rust: the webview is on tauri://localhost and the
// server sends no CORS headers on purpose (ADR-0008).
async function apiGet(path) {
  return invoke("api_get", { base: SERVER, path });
}

function compact(n) {
  if (n >= 1e9) return (n / 1e9).toFixed(n >= 1e10 ? 0 : 1) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1) + "K";
  return String(n);
}

function usd(v) {
  if (v >= 1000) return "$" + (v / 1000).toFixed(1) + "K";
  if (v >= 100) return "$" + v.toFixed(0);
  if (v >= 1) return "$" + v.toFixed(2);
  return v > 0 ? "$" + v.toFixed(3) : "$0";
}

// Fixed steps rather than a gradient: the reader needs "is this fine or not",
// not a hue to interpolate.
function severity(pct) {
  if (pct >= 90) return "critical";
  if (pct >= 75) return "serious";
  if (pct >= 50) return "warn";
  return "";
}

function untilReset(ms) {
  const s = Math.max(0, Math.round((ms - Date.now()) / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h > 0 ? `${h}h${m}m 后重置` : `${m}m 后重置`;
}

function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

function renderQuotas(list) {
  const root = document.getElementById("quotas");
  // Expired entries describe a window that has already rolled over; showing
  // them would be worse than showing nothing.
  const live = (list || []).filter((q) => !q.expired);
  if (!live.length) {
    root.innerHTML = `<div class="empty">暂无配额数据</div>`;
    return;
  }
  root.innerHTML = live.slice(0, 4).map((q) => {
    const pct = Math.round(q.used_percent || 0);
    return `
      <div class="quota">
        <div class="head">
          <span class="name">${esc(q.source)} · ${esc(q.window_label || "")}</span>
          <span class="pct">${pct}%</span>
        </div>
        <div class="meter"><i class="${severity(pct)}" style="width:${Math.min(100, pct)}%"></i></div>
        <div class="reset">${q.resets_at ? untilReset(q.resets_at) : ""}</div>
      </div>`;
  }).join("");
}

// Burn comes from the same payload the Live page renders, so the popover and
// the browser cannot report different numbers for the same ten minutes.
function renderBurn(burn) {
  const b = burn || {};
  document.getElementById("burn-rate").textContent = compact(b.per_minute || 0) + "/min";
  const window = b.window_minutes || 0;
  document.getElementById("burn-sub").textContent = window
    ? `${window} 分钟内 ${compact(b.tokens || 0)} · 输出 ${compact(b.output_tokens || 0)}`
    : "";
}

function renderToday(overview) {
  const t = (overview && overview.today) || {};
  document.getElementById("today-tokens").textContent = compact(t.total_tokens || 0);

  const bits = [];
  if (t.output_tokens) bits.push("输出 " + compact(t.output_tokens));
  if (t.events) bits.push(t.events + " 请求");
  // Costs live at the top level, not inside `today`. Prefer the equivalent
  // figure: on a subscription the real spend is 0, which would read as "free".
  const c = (overview && overview.costs && overview.costs.today) || {};
  const cost = c.equivalent_usd || c.real_usd || 0;
  if (cost > 0) bits.push(usd(cost));
  document.getElementById("today-sub").textContent = bits.join(" · ");
}

function fail(msg) {
  // The hint matters most on a fresh install pointed at the default address:
  // an unreachable server and a wrong address look identical from here.
  document.getElementById("quotas").innerHTML =
    `<div class="error">${esc(msg)}</div>` +
    `<div class="empty">点右下角「设置」检查服务端地址。</div>`;
  for (const id of ["today-tokens", "burn-rate"]) {
    document.getElementById(id).textContent = "—";
  }
  for (const id of ["today-sub", "burn-sub"]) {
    document.getElementById(id).textContent = "";
  }
}

async function refresh() {
  try {
    // One failing endpoint should not blank the other half of the panel.
    // /api/v1/live carries quotas and burn in one consistent snapshot — the
    // REST twin of what the Live page streams.
    const [live, overview] = await Promise.all([
      apiGet("/api/v1/live"),
      apiGet("/api/v1/overview?days=1").catch(() => null),
    ]);
    renderQuotas(live && live.quotas);
    renderBurn(live && live.burn);
    if (overview) renderToday(overview);
    document.getElementById("stamp").textContent =
      new Date().toLocaleTimeString("zh-CN", { hour12: false });
  } catch (e) {
    fail(String(e));
  }
}

// ---- settings ----

const els = {
  main: document.getElementById("main"),
  settings: document.getElementById("settings"),
  input: document.getElementById("server-input"),
  msg: document.getElementById("settings-msg"),
  save: document.getElementById("settings-save"),
};

function showServer() {
  document.getElementById("server").textContent = SERVER.replace(/^https?:\/\//, "");
}

function say(text, kind) {
  els.msg.textContent = text || "";
  els.msg.className = "msg" + (kind ? " " + kind : "");
}

function openSettings() {
  els.input.value = SERVER;
  say("");
  els.main.hidden = true;
  els.settings.hidden = false;
  els.input.focus();
  els.input.select();
}

// Always re-poll on the way out. A save can succeed even when the probe fails,
// so leaving by way of 取消 can still mean the address changed — and the
// readout would otherwise show another server's numbers until the next poll.
function closeSettings() {
  els.settings.hidden = true;
  els.main.hidden = false;
  refresh();
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
    closeSettings();
  } catch (e) {
    // settings_set rejected the input, or could not write the file.
    say(String(e), "bad");
  } finally {
    els.save.disabled = false;
  }
}

document.getElementById("open-settings").addEventListener("click", openSettings);
document.getElementById("settings-cancel").addEventListener("click", closeSettings);
els.save.addEventListener("click", saveSettings);
els.input.addEventListener("keydown", (e) => {
  if (e.key === "Enter") saveSettings();
  if (e.key === "Escape") closeSettings();
});

// ---- boot ----

async function boot() {
  const stored = await invoke("settings_get");
  SERVER = stored.server;
  showServer();
  await refresh();
  setInterval(refresh, POLL_MS);
}

if (!invoke) {
  fail("Tauri IPC 不可用:请检查 tauri.conf.json 的 withGlobalTauri");
} else {
  boot().catch((e) => fail(String(e)));
}
