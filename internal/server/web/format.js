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
