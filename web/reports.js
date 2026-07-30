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

  enter() {
    if (!this._rendered) {
      this.renderShell();
      this._rendered = true;
    }
    this.load();
  },

  leave() {},

  apiPath(format) {
    return `/api/v1/reports?granularity=${this.granularity}&days=${this.days}&format=${format}`;
  },

  renderShell() {
    const root = document.getElementById("view-reports");
    root.innerHTML = `
    <section class="card">
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
      <div id="reports-table" class="data-table"></div>
      <p class="subtle" id="reports-status" hidden></p>
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
    const status = document.getElementById("reports-status");
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
    const status = document.getElementById("reports-status");
    status.hidden = true;
    try {
      const d = await Api.get(this.apiPath("json"));
      if (d.granularity === "session") this.renderSessions(d.rows || []);
      else this.renderPeriods(d.rows || []);
    } catch (e) {
      status.textContent = "加载失败:服务不可达,请稍后重试";
      status.hidden = false;
    }
  },

  renderPeriods(rows) {
    const el = document.getElementById("reports-table");
    if (!rows.length) { el.innerHTML = `<p class="subtle">暂无数据</p>`; return; }
    const label = { daily: "日期", weekly: "周", monthly: "月份" }[this.granularity] || "时间段";
    el.innerHTML = `<table><thead><tr><th>${label}</th><th>输入</th><th>输出</th><th>缓存读取</th><th>缓存写入</th><th>合计</th><th>请求</th></tr></thead><tbody>` +
      [...rows].reverse().map((r) =>
        `<tr><td>${esc(r.bucket)}</td><td>${full(r.input_tokens)}</td><td>${full(r.output_tokens)}</td><td>${full(r.cache_read_tokens)}</td><td>${full(r.cache_creation_tokens)}</td><td>${full(r.total_tokens)}</td><td>${full(r.events)}</td></tr>`
      ).join("") + `</tbody></table>`;
  },

  renderSessions(rows) {
    const el = document.getElementById("reports-table");
    if (!rows.length) { el.innerHTML = `<p class="subtle">暂无数据</p>`; return; }
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
  },
};
