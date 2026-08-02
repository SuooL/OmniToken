"use strict";
// Reports view (F12): daily/weekly/monthly/session tables with CSV/JSON export.

const REPORT_GRANULARITIES = [
  ["daily", "日"], ["weekly", "周"], ["monthly", "月"], ["session", "会话"],
];
const REPORT_RANGES = [7, 30, 90];

const Reports = {
  granularity: "daily",
  days: 30,
  _rendered: false,
  _loadGeneration: 0,
  lastData: null,

  enter() {
    if (!this._rendered) {
      this.renderShell();
      this._rendered = true;
    }
    this.load();
  },

  leave() {
    this._loadGeneration += 1;
  },

  apiPath(format) {
    return `/api/v1/reports?granularity=${this.granularity}&days=${this.days}&format=${format}`;
  },

  renderShell() {
    const root = document.getElementById("view-reports");
    root.innerHTML = `
    <section class="instrument-card">
      <div class="card-head">
        <h2>用量报表 · <span id="reports-note"></span></h2>
        <div class="head-tools">
          <div class="btn-group" id="reports-gran">${REPORT_GRANULARITIES.map(([k, l]) =>
            `<button class="ghost-btn" data-gran="${k}">${l}</button>`).join("")}</div>
          <div class="btn-group" id="reports-range">${REPORT_RANGES.map((d) =>
            `<button class="ghost-btn" data-days="${d}">近 ${d} 天</button>`).join("")}</div>
          <div class="btn-group" id="reports-export">
            <button class="ghost-btn" type="button" data-format="csv">导出 CSV</button>
            <button class="ghost-btn" type="button" data-format="json">导出 JSON</button>
          </div>
        </div>
      </div>
      <p class="subtle" id="reports-export-status" aria-live="polite" hidden></p>
    </section>
    <section class="chart-card">
      <div class="card-head"><h2>区间趋势</h2><span class="subtle">随当前粒度与范围更新</span></div>
      <div id="reports-trend" style="height:300px" data-chart="report-trend"></div>
    </section>
    <section class="instrument-card">
      <div id="reports-table" class="data-table data-table-shell"></div>
    </section>`;
    root.querySelector("#reports-gran").addEventListener("click", (ev) => {
      const btn = ev.target.closest("[data-gran]");
      if (!btn) return;
      this.granularity = btn.dataset.gran;
      this.load();
    });
    root.querySelector("#reports-range").addEventListener("click", (ev) => {
      const btn = ev.target.closest("[data-days]");
      if (!btn) return;
      this.days = +btn.dataset.days;
      this.load();
    });
    root.querySelector("#reports-export").addEventListener("click", (ev) => {
      const btn = ev.target.closest("[data-format]");
      if (btn) this.download(btn.dataset.format, btn);
    });
  },

  syncControls() {
    document.querySelectorAll("#reports-gran [data-gran]").forEach((b) =>
      b.setAttribute("aria-pressed", String(b.dataset.gran === this.granularity)));
    document.querySelectorAll("#reports-range [data-days]").forEach((b) =>
      b.setAttribute("aria-pressed", String(+b.dataset.days === this.days)));
    const g = REPORT_GRANULARITIES.find(([k]) => k === this.granularity);
    document.getElementById("reports-note").textContent = `近 ${this.days} 天 · 按${g[1]}`;
  },

  async download(format, button) {
    if (button.disabled) return;
    const status = document.getElementById("reports-export-status");
    button.disabled = true;
    button.setAttribute("aria-busy", "true");
    status.textContent = "正在导出";
    status.hidden = false;
    try {
      await downloadAPI(this.apiPath(format), `omnitoken-${this.granularity}-${this.days}d.${format}`);
      status.hidden = true;
    } catch (e) {
      status.textContent = e instanceof APIError && e.status === 401
        ? "导出失败:未授权,请在设置页填写读取 token"
        : `导出失败:${e.message}`;
      status.hidden = false;
    } finally {
      button.disabled = false;
      button.removeAttribute("aria-busy");
    }
  },

  async load() {
    this.syncControls();
    const loadID = ++this._loadGeneration;
    const path = this.apiPath("json");
    const status = document.getElementById("reports-export-status");
    status.hidden = true;
    try {
      const d = await Api.get(path);
      if (!isCurrentGeneration(this._loadGeneration, loadID)) return;
      this.lastData = d;
      if (d.granularity === "session") this.renderSessions(d.rows || []);
      else this.renderPeriods(d.rows || []);
      renderState(document.getElementById("view-reports"), {kind: "ready", title: ""});
    } catch (e) {
      if (!isCurrentGeneration(this._loadGeneration, loadID)) return;
      const issue = classifyAPIError(e);
      renderState(document.getElementById("view-reports"), {
        kind: this.lastData ? "stale" : issue.kind,
        title: this.lastData ? "报表数据可能已过期" : issue.title,
        detail: this.lastData ? "保留上次成功趋势与表格。" : issue.detail,
        action: {label: "重试", run: () => this.load()},
      });
    }
  },

  renderPeriods(rows) {
    const el = document.getElementById("reports-table");
    if (!rows.length) {
      el.innerHTML = `<p class="subtle">暂无数据</p>`;
      this.renderTrend([], "时间");
      return;
    }
    const label = { daily: "日期", weekly: "周", monthly: "月份" }[this.granularity] || "时间段";
    el.innerHTML = `<table><thead><tr><th>${label}</th><th>输入</th><th>输出</th><th>缓存读取</th><th>缓存写入</th><th>合计</th><th>请求</th></tr></thead><tbody>` +
      [...rows].reverse().map((r) =>
        `<tr><td>${esc(r.bucket)}</td><td>${full(r.input_tokens)}</td><td>${full(r.output_tokens)}</td><td>${full(r.cache_read_tokens)}</td><td>${full(r.cache_creation_tokens)}</td><td>${full(r.total_tokens)}</td><td>${full(r.events)}</td></tr>`
      ).join("") + `</tbody></table>`;
    this.renderTrend([...rows].reverse(), label);
  },

  renderSessions(rows) {
    const el = document.getElementById("reports-table");
    if (!rows.length) {
      el.innerHTML = `<p class="subtle">暂无数据</p>`;
      this.renderTrend([], "会话");
      return;
    }
    el.innerHTML = `<table><thead><tr><th>会话</th><th>设备</th><th>来源</th><th>项目</th><th>模型</th><th>最后活跃</th><th>合计</th><th>请求</th></tr></thead><tbody>` +
      rows.map((r) => {
        const sid = r.session_id || "(无会话)";
        const started = new Date(r.first_ts).toLocaleString("zh-CN", { hour12: false });
        return `<tr>
          <td title="${esc(sid)}">${esc(sid.slice(0, 8))}</td>
          <td>${esc(r.device)}</td>
          <td>${esc(r.source)}</td>
          <td title="${esc(repoLabel(r.repo))}">${esc(repoLabel(r.repo))}</td>
          <td>${esc(r.model)}</td>
          <td title="${esc(started)} 开始">${relTime(r.last_ts)}</td>
          <td>${full(r.total_tokens)}</td>
          <td>${full(r.events)}</td>
        </tr>`;
      }).join("") + `</tbody></table>`;
    this.renderTrend(rows.slice(0, 30), "会话", (row) => (row.session_id || "(无会话)").slice(0, 8), "bar");
  },

  renderTrend(rows, label, labelOf = (row) => row.bucket, type = "line") {
    const el = document.getElementById("reports-trend");
    if (!rows.length) {
      el.innerHTML = `<p class="empty">当前范围暂无趋势数据。</p>`;
      return;
    }
    ChartRegistry.set(el, {
      titleText: `报表${label}趋势`,
      grid: {left: 12, right: 18, top: 18, bottom: 42, containLabel: true},
      xAxis: {type: "category", data: rows.map(labelOf), axisLabel: {color: cssVar("--text-muted"), rotate: rows.length > 14 ? 30 : 0}, axisLine: {lineStyle: {color: cssVar("--baseline")}}},
      yAxis: {type: "value", splitLine: {lineStyle: {color: cssVar("--grid")}}, axisLabel: {color: cssVar("--text-muted"), formatter: compact}},
      series: [{
        type, smooth: false, symbol: "circle", symbolSize: 5, barMaxWidth: 20,
        data: rows.map((row) => row.total_tokens || 0),
        lineStyle: {color: cssVar("--source-aggregate"), width: 2},
        areaStyle: {color: mixWithSurface(cssVar("--source-aggregate"), .12)},
      }],
    });
  },
};
