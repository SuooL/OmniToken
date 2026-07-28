"use strict";
// Menubar popover. Shows only what is worth a glance — quota, today's usage —
// and leaves analysis to the full panel in a browser (ADR-0008).

// Requires `withGlobalTauri` in tauri.conf.json — Tauri v2 does not inject
// window.__TAURI__ by default. Without the guard a missing bridge throws on
// this line and the panel renders as an empty shell with no clue why.
const invoke = window.__TAURI__ && window.__TAURI__.core.invoke;

// TODO: move to a settings screen. Hard-coded until the panel has somewhere to
// enter it; localStorage lets it be overridden from the devtools meanwhile.
const SERVER = localStorage.getItem("omnitoken.server") || "http://127.0.0.1:8787";

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
  document.getElementById("quotas").innerHTML = `<div class="error">${esc(msg)}</div>`;
  document.getElementById("today-tokens").textContent = "—";
  document.getElementById("today-sub").textContent = "";
}

async function refresh() {
  try {
    // One failing endpoint should not blank the other half of the panel.
    const [quota, overview] = await Promise.all([
      apiGet("/api/v1/quota"),
      apiGet("/api/v1/overview?days=1").catch(() => null),
    ]);
    renderQuotas(quota && quota.quotas);
    if (overview) renderToday(overview);
    document.getElementById("stamp").textContent =
      new Date().toLocaleTimeString("zh-CN", { hour12: false });
  } catch (e) {
    fail(String(e));
  }
}

document.getElementById("server").textContent = SERVER.replace(/^https?:\/\//, "");

if (!invoke) {
  fail("Tauri IPC 不可用:请检查 tauri.conf.json 的 withGlobalTauri");
} else {
  refresh();
  setInterval(refresh, POLL_MS);
}
