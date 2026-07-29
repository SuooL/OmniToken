"use strict";
// Shared formatting helpers (token-monitor-style renderer decomposition).

function cssVar(name) {
  return getComputedStyle(document.querySelector(".viz-root")).getPropertyValue(name).trim();
}

function compact(n) {
  if (n >= 1e9) return (n / 1e9).toFixed(n >= 1e10 ? 0 : 1) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1) + "K";
  return String(n);
}
const full = (n) => n.toLocaleString("en-US");

// Everything on the panel is USD: prices are entered in USD, costs are
// computed in USD, and there is no display-currency conversion.
function usd(v) {
  if (v >= 1000) return "$" + (v / 1000).toFixed(1) + "K";
  if (v >= 100) return "$" + v.toFixed(0);
  if (v >= 1) return "$" + v.toFixed(2);
  return v > 0 ? "$" + v.toFixed(3) : "$0";
}

function hours(sec) {
  if (!sec) return "";
  if (sec >= 3600) return (sec / 3600).toFixed(1) + "h";
  return Math.round(sec / 60) + "m";
}

function relTime(ms) {
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 60) return s + "s 前";
  if (s < 3600) return Math.round(s / 60) + "m 前";
  if (s < 86400) return (s / 3600).toFixed(1) + "h 前";
  return Math.round(s / 86400) + "d 前";
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function repoLabel(key, cwd) {
  if (key) return key.replace(/^local:/, "");
  if (cwd) return cwd;
  return "(非 git 目录)";
}

// ── Chart helpers (ADR-0010) ────────────────────────────────────────────
// ECharts knows nothing about the panel's tokens, so every chart reads its
// colours through these rather than hard-coding hex. One place to change when
// the theme flips, and no chart can drift out of the palette.

const _charts = new WeakMap();

// One instance per element, reused across renders. Disposing and recreating on
// every poll would throw away the animation state and leak canvases.
function echartsFor(el) {
  let c = _charts.get(el);
  if (!c || c.isDisposed()) {
    c = echarts.init(el, null, { renderer: "canvas" });
    _charts.set(el, c);
    // Charts live in a fluid grid, so they have to follow their container
    // rather than the window's breakpoints.
    if (typeof ResizeObserver !== "undefined") {
      new ResizeObserver(() => c.resize()).observe(el);
    }
  }
  return c;
}

// Views are toggled with `hidden`, so a chart built for a view that was not on
// screen measures its container as 0 wide and draws nothing. ECharts only
// re-measures when told to, so every view re-measures its charts on entry.
function resizeChartsIn(root) {
  if (!root || typeof echarts === "undefined") return;
  root.querySelectorAll("div").forEach((el) => {
    const inst = echarts.getInstanceByDom(el);
    if (inst) inst.resize();
  });
}

function chartFont() {
  return getComputedStyle(document.querySelector(".viz-root")).fontFamily;
}

// Vertical fade of one hue: solid where the value is, softer at the base, so a
// stack has depth instead of reading as flat blocks.
function gradientOf(hex) {
  return {
    type: "linear", x: 0, y: 0, x2: 0, y2: 1,
    colorStops: [
      { offset: 0, color: hex },
      { offset: 1, color: mixWithSurface(hex, 0.55) },
    ],
  };
}

// Blends toward transparency; ECharts accepts rgba, so no need to know the
// backdrop colour.
function mixWithSurface(hex, alpha) {
  const h = hex.trim().replace("#", "");
  const n = h.length === 3 ? h.split("").map((c) => c + c).join("") : h;
  const r = parseInt(n.slice(0, 2), 16), g = parseInt(n.slice(2, 4), 16), b = parseInt(n.slice(4, 6), 16);
  return `rgba(${r},${g},${b},${alpha})`;
}

function tooltipStyle() {
  return {
    backgroundColor: cssVar("--page"),
    borderColor: cssVar("--border-strong"),
    borderWidth: 1,
    padding: [8, 10],
    textStyle: { color: cssVar("--text-primary"), fontSize: 11, fontFamily: chartFont() },
    extraCssText: "border-radius:10px;box-shadow:0 12px 32px rgba(0,0,0,0.4);backdrop-filter:blur(12px);",
  };
}

// Display name for a collection source. The stored value stays `claude-code`;
// this only makes it read as one word beside `codex`.
const SOURCE_LABELS = { "claude-code": "claude" };
function sourceLabel(s) {
  return SOURCE_LABELS[s] || s;
}
