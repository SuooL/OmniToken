"use strict";
// Pure formatting helpers — the single source of truth for how a number or an
// instant is written, across BOTH surfaces (ADR-0014).
//
// Consumers:
//   web/          the nine-page panel, served by `omnitoken serve`
//   desktop/ui/   the macOS menubar popover (Tauri)
//
// `desktop/ui/format-core.js` is a byte-for-byte copy of this file, and
// `make desktop-check` fails if the two drift. Do not edit the copy.
// The reasoning for copy-plus-gate over a build step is in web/tokens.css.
//
// Everything here is pure: no DOM, no globals, no `Api`. That is what makes it
// safe to load into a popover that shares none of the panel's markup. Anything
// that touches the document or ECharts belongs in web/format.js instead.
//
// The popover used to carry its own copies of compact / usd / esc /
// sourceLabel, which had already diverged: its esc left `'` unescaped.

function compact(n) {
  if (n >= 1e9) return (n / 1e9).toFixed(n >= 1e10 ? 0 : 1) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1) + "K";
  // Below 1K the value used to be printed verbatim, which is right for a token
  // count (always an integer) and wrong for a rate: `renderBars` feeds this the
  // same way for `total_tokens` and for `contribution_tps`, and a tok/s of
  // 71.37388846357693 was reaching the panel with all fourteen digits.
  return Number.isInteger(n) ? String(n) : String(Number(n.toFixed(1)));
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

// Elapsed time since an instant, for things that are still going. relTime
// says when something last happened; this says how long it has been running.
function since(ms) {
  if (!ms) return "—";
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 60) return s + "s";
  if (s < 3600) return Math.round(s / 60) + "m";
  return Math.floor(s / 3600) + "h" + Math.round((s % 3600) / 60) + "m";
}

function relTime(ms) {
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 60) return s + "s 前";
  if (s < 3600) return Math.round(s / 60) + "m 前";
  if (s < 86400) return (s / 3600).toFixed(1) + "h 前";
  return Math.round(s / 86400) + "d 前";
}

// Minutes remaining until a quota window rolls over. Written the same way on
// both surfaces so the popover and the Live page cannot describe the same
// window differently.
function untilReset(minutes) {
  if (!(minutes > 0)) return "即将重置";
  return `${Math.floor(minutes / 60)}h${minutes % 60}m 后重置`;
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function repoLabel(key, cwd) {
  if (key) return key.replace(/^local:/, "");
  if (cwd) return cwd;
  return "(非 git 目录)";
}

// Display name for a collection source. The stored value stays `claude-code`;
// this only makes it read as one word beside `codex`.
const SOURCE_LABELS = { "claude-code": "claude" };
function sourceLabel(s) {
  return SOURCE_LABELS[s] || s;
}

// Consumption intensity → meter severity class. Fixed steps rather than a
// gradient: the reader needs "is this fine or not", not a hue to interpolate.
// Matches --meter-warn / --meter-serious / --meter-critical in tokens.css.
function severity(pct) {
  if (pct >= 90) return "critical";
  if (pct >= 75) return "serious";
  if (pct >= 50) return "warn";
  return "";
}
