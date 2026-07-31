"use strict";

// Shared ECharts lifecycle and presentation contract. Page modules own their
// analytical questions; this registry owns palette, accessibility, resizing,
// reduced motion, and the companion table used when a canvas is unavailable.
const ChartRegistry = {
  _instances: new Map(),
  _observers: new Map(),

  palette() {
    return {
      claude: cssVar("--source-claude"),
      codex: cssVar("--source-codex"),
      api: cssVar("--source-api"),
      aggregate: cssVar("--source-aggregate"),
      healthy: cssVar("--status-healthy"),
      muted: cssVar("--text-muted"),
      grid: cssVar("--grid"),
    };
  },

  sourceColor(source) {
    const palette = this.palette();
    if (source === "claude-code" || source === "claude") return palette.claude;
    if (source === "codex") return palette.codex;
    return palette.api;
  },

  prune() {
    this._instances.forEach((chart, el) => {
      if (el.isConnected) return;
      this._observers.get(el)?.disconnect();
      this._observers.delete(el);
      chart.dispose();
      this._instances.delete(el);
    });
  },

  get(el) {
    if (!el || typeof echarts === "undefined") return null;
    this.prune();
    let chart = this._instances.get(el);
    if (!chart || chart.isDisposed()) {
      chart = echarts.init(el, null, { renderer: "canvas" });
      this._instances.set(el, chart);
      if (typeof ResizeObserver !== "undefined") {
        const observer = new ResizeObserver(() => chart.resize());
        observer.observe(el);
        this._observers.set(el, observer);
      }
    }
    return chart;
  },

  base(title) {
    return {
      aria: { enabled: true, description: title || "Telemetry chart" },
      animation: !matchMedia("(prefers-reduced-motion: reduce)").matches,
      color: Object.values(this.palette()),
      tooltip: { trigger: "axis", ...tooltipStyle() },
      textStyle: { fontFamily: chartFont(), color: cssVar("--text-secondary") },
    };
  },

  set(el, option, fallback) {
    const chart = this.get(el);
    if (chart) chart.setOption(Object.assign(this.base(option.titleText), option), true);
    if (fallback) this.renderFallback(el, fallback);
    return chart;
  },

  renderFallback(el, { caption = "图表数据", columns = [], rows = [] }) {
    const host = el.closest(".chart-card, .instrument-card, .card") || el.parentElement;
    if (!host) return;
    let details = host.querySelector(`details[data-chart-fallback="${el.id}"]`);
    if (!details) {
      details = document.createElement("details");
      details.className = "chart-fallback data-table-shell";
      details.dataset.chartFallback = el.id;
      host.appendChild(details);
    }
    details.innerHTML = `<summary>${esc(caption)}</summary><table><thead><tr>` +
      columns.map((column) => `<th scope="col">${esc(column.label)}</th>`).join("") +
      `</tr></thead><tbody>` +
      rows.map((row) => `<tr>${columns.map((column) =>
        `<td>${esc(column.format ? column.format(row[column.key], row) : row[column.key])}</td>`).join("")}</tr>`).join("") +
      `</tbody></table>`;
  },

  resizeWithin(root) {
    if (!root) return;
    this._instances.forEach((chart, el) => {
      if (root.contains(el) && !chart.isDisposed()) chart.resize();
    });
  },

  disposeWithin(root) {
    this._instances.forEach((chart, el) => {
      if (!root.contains(el)) return;
      this._observers.get(el)?.disconnect();
      this._observers.delete(el);
      chart.dispose();
      this._instances.delete(el);
    });
  },
};
