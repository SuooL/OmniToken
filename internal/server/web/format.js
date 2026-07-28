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

// Display currency. Costs are stored and computed in USD everywhere; this is a
// presentation-layer conversion only, with a hand-entered rate (no FX feed).
// Only USD and CNY are offered — see supportedCurrencies in settingsview.go.
const Currency = {
  code: "USD",
  rate: 1,
  symbol: "$",

  // Anything unrecognised falls back to USD rather than rendering numbers
  // under a currency we cannot actually convert to.
  set(code, rate) {
    this.code = code === "CNY" ? "CNY" : "USD";
    this.rate = this.code === "CNY" && rate > 0 ? rate : 1;
    this.symbol = this.code === "CNY" ? "¥" : "$";
  },
};

// money renders a USD amount in the configured display currency. Named for
// what it does, not for one currency — it returned "$" unconditionally before
// the currency setting was wired up to anything.
function money(v) {
  const x = v * Currency.rate;
  const s = Currency.symbol;
  if (x >= 1000) return s + (x / 1000).toFixed(1) + "K";
  if (x >= 100) return s + x.toFixed(0);
  if (x >= 1) return s + x.toFixed(2);
  return x > 0 ? s + x.toFixed(3) : s + "0";
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
