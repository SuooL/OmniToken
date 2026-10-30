"use strict";
// Details view: filterable, paginated raw-event drill-down (F13).

function detailsSessionHref(sessionID) {
  return "#details?session=" + encodeURIComponent(sessionID);
}

const Details = {
  filters: { device: "", source: "", model: "", repo: "", session: "", days: "7" },
  // identity → display name, from the same breakdown that fills the filter.
  // A v2 device is filed under a UUID, so without this every row, option and
  // chip on this page reads as 36 characters of hex.
  deviceNames: {},
  events: [],
  selectedEvent: null,
  lastUpdated: null,
  optionsSeq: 0,
  limit: 100,
  offset: 0,
  total: 0,
  built: false,
  seq: 0, // guards against out-of-order fetch responses

  enter() {
    if (!this.built) {
      this.build();
      this.built = true;
    }
    this.loadOptions();
    this.load();
  },

  leave() {
    this.seq += 1;
    this.optionsSeq += 1;
    this.selectedEvent = null;
  },

  applyRoute(params, shouldLoad = true) {
    const before = this.routeHref();
    for (const key of ["device", "source", "model", "repo", "session"]) {
      this.filters[key] = params.get(key) || "";
    }
    const days = params.get("days");
    this.filters.days = ["7", "30", "90"].includes(days) ? days : "7";
    const offset = Number(params.get("offset") || 0);
    this.offset = Number.isSafeInteger(offset) && offset >= 0 ? offset : 0;
    if (this.built) {
      this.syncFilters();
      this.renderChips();
    }
    if (shouldLoad && this.built && before !== this.routeHref()) this.load();
  },

  routeHref(overrides = {}) {
    const state = { ...this.filters, offset: this.offset, ...overrides };
    const params = new URLSearchParams();
    for (const key of ["device", "source", "model", "repo", "session", "days", "offset"]) {
      if (state[key] !== "" && !(key === "days" && state[key] === "7") && !(key === "offset" && state[key] === 0)) {
        params.set(key, state[key]);
      }
    }
    return "#details" + (params.size ? "?" + params : "");
  },

  navigate(overrides) {
    // Let the router apply state so browser Back sees the previous filters.
    location.hash = this.routeHref(overrides);
  },

  syncFilters() {
    for (const key of ["device", "source", "model", "repo", "days"]) {
      const select = document.getElementById("d-f-" + key);
      const value = this.filters[key];
      if (value && !Array.from(select.options || []).some((option) => option.value === value)) {
        select.insertAdjacentHTML("beforeend", `<option value="${esc(value)}">${esc(key === "source" ? SourceUI.label(value) : value)}</option>`);
      }
      select.value = value;
    }
  },

  build() {
    document.getElementById("view-details").innerHTML = `
    <section class="instrument-card">
      <div class="card-head">
        <h2>事件明细</h2>
        <div class="head-tools"><span class="subtle" id="d-note"></span></div>
      </div>
      <div class="filter-row tool-toolbar">
        <select id="d-f-device" aria-label="设备筛选"><option value="">全部设备</option></select>
        <select id="d-f-source" aria-label="来源筛选"><option value="">全部来源</option></select>
        <select id="d-f-model" aria-label="模型筛选"><option value="">全部模型</option></select>
        <select id="d-f-repo" aria-label="项目筛选"><option value="">全部项目</option></select>
        <select id="d-f-days" aria-label="时间范围">
          <option value="7">近 7 天</option>
          <option value="30">近 30 天</option>
          <option value="90">近 90 天</option>
        </select>
        <span class="chip-row" id="d-chips" data-role="filter-summary"></span>
      </div>
    </section>
    <section class="instrument-card">
      <div id="d-table" class="data-table data-table-shell"></div>
      <div class="pager">
        <span class="subtle" id="d-page"></span>
        <button id="d-prev" class="ghost-btn">上一页</button>
        <button id="d-next" class="ghost-btn">下一页</button>
      </div>
    </section>
    ${ToolUI.analysis("details-analysis", "当前页分析", `<p class="subtle">仅覆盖当前页事件，展示 token 最多的 12 个模型；不代表全部筛选结果。</p><div id="d-distribution" style="height:260px" data-chart="event-distribution"></div>` )}`;

    for (const [id, key] of [["d-f-device", "device"], ["d-f-source", "source"],
                             ["d-f-model", "model"], ["d-f-repo", "repo"], ["d-f-days", "days"]]) {
      document.getElementById(id).addEventListener("change", (ev) => {
        this.navigate({ [key]: ev.target.value, offset: 0 });
      });
    }
    document.getElementById("d-prev").addEventListener("click", () => {
      if (this.offset > 0) {
        this.navigate({ offset: Math.max(0, this.offset - this.limit) });
      }
    });
    document.getElementById("d-next").addEventListener("click", () => {
      if (this.offset + this.limit < this.total) {
        this.navigate({ offset: this.offset + this.limit });
      }
    });
    const table = document.getElementById("d-table");
    const activate = (ev) => {
      if (ev.target.closest("a")) return;
      const row = ev.target.closest("[data-event-key]");
      if (row) this.openEvent(row.dataset.eventKey, row);
    };
    table.addEventListener("click", activate);
    table.addEventListener("keydown", (ev) => {
      if (ev.target.closest("a")) return;
      if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); activate(ev); }
    });
    this.syncFilters();
    this.renderChips();
  },

  // Filter options come from the 90-day breakdown so rarely-used values
  // still show up regardless of the currently selected time range.
  async loadOptions() {
    const generation = ++this.optionsSeq;
    const dims = [["device", "d-f-device"], ["source", "d-f-source"],
                  ["model", "d-f-model"], ["repo", "d-f-repo"]];
    await Promise.all(dims.map(async ([by, id]) => {
      try {
        const d = await Api.get(`/api/v1/breakdown?by=${by}&days=90`);
        if (generation !== this.optionsSeq) return;
        const sel = document.getElementById(id);
        if (by === "device") {
          this.deviceNames = {};
          for (const r of d.rows || []) {
            if (r.key && r.display_name) this.deviceNames[r.key] = r.display_name;
          }
        }
        // The option's value stays the identity — it is what /api/v1/events
        // filters on; only the text the user reads is renamed.
        const rows = (d.rows || []).filter((r) => r.key);
        if (this.filters[by] && !rows.some((r) => r.key === this.filters[by])) rows.push({key: this.filters[by]});
        sel.innerHTML = sel.firstElementChild.outerHTML +
          rows.map((r) =>
            `<option value="${esc(r.key)}">${esc(
              by === "repo" ? repoLabel(r.key) : by === "device" ? this.deviceLabel(r.key) : by === "source" ? SourceUI.label(r.key) : r.key)}</option>`
          ).join("");
        sel.value = this.filters[by];
      } catch (e) { /* keep the bare "全部" option */ }
    }));
  },

  async load() {
    const my = ++this.seq;
    const p = new URLSearchParams({ days: this.filters.days, limit: this.limit, offset: this.offset });
    for (const k of ["device", "source", "model", "repo", "session"]) {
      if (this.filters[k]) p.set(k, this.filters[k]);
    }
    const note = document.getElementById("d-note");
    try {
      const d = await Api.get("/api/v1/events?" + p);
      if (my !== this.seq) return; // superseded by a newer request
      this.total = d.total || 0;
      this.render(d.events || []);
      this.lastUpdated = new Date().toLocaleTimeString("zh-CN", { hour12: false });
      note.textContent = "更新于 " + this.lastUpdated;
    } catch (e) {
      if (my === this.seq) {
        const issue = classifyAPIError(e);
        if (e.status === 401 || e.status === 403) {
          this.clearProtectedData();
          note.textContent = `HTTP ${e.status} · ${issue.title}`;
          return;
        }
        note.textContent = this.lastUpdated ? `更新失败 · 保留 ${this.lastUpdated} 的数据 · ${issue.title}` : issue.title;
      }
    }
  },

  clearProtectedData() {
    this.optionsSeq += 1; // invalidate option requests started before rejection
    this.events = [];
    this.deviceNames = {};
    this.total = 0;
    this.lastUpdated = null;
    this.selectedEvent = null;
    DetailDrawer.close();
    const chart = document.getElementById("d-distribution");
    ChartRegistry.disposeWithin(chart);
    chart.innerHTML = "";
    ToolUI.updateHTML(document.getElementById("d-table"), '<p class="subtle">需要读取权限；请检查凭据后重试。</p>');
    document.getElementById("d-page").textContent = "";
    document.getElementById("d-prev").disabled = true;
    document.getElementById("d-next").disabled = true;
    for (const key of ["device", "source", "model", "repo"]) {
      const select = document.getElementById("d-f-" + key);
      select.innerHTML = select.firstElementChild.outerHTML;
    }
    // Keep the URL's selected scope, but discard display names obtained from
    // protected responses and any unselected options.
    this.syncFilters();
    this.renderChips();
  },

  render(events) {
    this.events = events;
    for (const event of events) {
      if (event.device && event.device_display_name) this.deviceNames[event.device] = event.device_display_name;
    }
    this.renderChips();
    this.renderDistribution(events);
    const el = document.getElementById("d-table");
    ToolUI.updateHTML(el, events.length
      ? `<table><thead><tr>
          <th>时间</th><th>设备</th><th>来源</th><th>模型</th><th>项目</th>
          <th>输入</th><th>输出</th><th>缓存读</th><th>缓存写</th><th>成本</th><th>会话</th>
        </tr></thead><tbody>` + events.map((e) => this.rowHTML(e)).join("") + `</tbody></table>`
      : `<p class="subtle">无匹配事件</p>`);
    if (this.selectedEvent) {
      const selected = events.find((event) => this.eventKey(event) === this.selectedEvent);
      // Keep a selected event's metadata even when pagination changes.
      if (selected) DetailDrawer.update("event:" + this.selectedEvent, this.eventHTML(selected));
    }

    const from = events.length ? this.offset + 1 : 0;
    const to = events.length ? this.offset + events.length : 0;
    document.getElementById("d-page").textContent =
      `第 ${from}–${to} 条 / 共 ${full(this.total)} 条`;
    document.getElementById("d-prev").disabled = this.offset <= 0;
    document.getElementById("d-next").disabled = this.offset + this.limit >= this.total;
  },

  renderDistribution(events) {
    const el = document.getElementById("d-distribution");
    if (!events.length) {
      el.innerHTML = `<p class="empty">当前页没有可绘制事件。</p>`;
      return;
    }
    const byModel = new Map();
    events.forEach((event) => {
      const key = event.model || "日志未记录模型";
      const row = byModel.get(key) || {model: key, input: 0, output: 0, events: 0};
      row.input += (event.input_tokens || 0) + (event.cache_read_tokens || 0) + (event.cache_creation_tokens || 0);
      row.output += event.output_tokens || 0;
      row.events += 1;
      byModel.set(key, row);
    });
    const rows = [...byModel.values()].sort((a, b) => b.input + b.output - a.input - a.output).slice(0, 12);
    ChartRegistry.set(el, {
      titleText: "筛选事件按模型的 token 分布",
      grid: {left: 12, right: 16, top: 28, bottom: 42, containLabel: true},
      legend: {top: 0, textStyle: {color: cssVar("--text-secondary")}},
      xAxis: {type: "category", data: rows.map((row) => row.model), axisLabel: {color: cssVar("--text-muted"), rotate: rows.length > 6 ? 25 : 0}},
      yAxis: {type: "value", splitLine: {lineStyle: {color: cssVar("--grid")}}, axisLabel: {color: cssVar("--text-muted"), formatter: compact}},
      series: [
        {name: "输入与缓存", type: "bar", stack: "tokens", data: rows.map((row) => row.input), itemStyle: {color: cssVar("--series-1")}},
        {name: "输出", type: "bar", stack: "tokens", data: rows.map((row) => row.output), itemStyle: {color: cssVar("--series-2")}},
      ],
    });
  },

  rowHTML(e) {
    const t = new Date(e.ts);
    const time = t.toLocaleString("zh-CN", {
      month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false,
    });
    const sid = e.session_id || "";
    const num = (v) => `<td class="numeric" title="${full(v || 0)}">${compact(v || 0)}</td>`;
    return `<tr tabindex="0" class="tool-detail-row" data-event-key="${esc(this.eventKey(e))}" aria-label="查看事件 ${esc(time)}">
      <td title="${esc(t.toLocaleString("zh-CN", { hour12: false }))} · ${relTime(e.ts)}">${time}</td>
      <td title="${esc(e.device || "")}">${esc(e.device ? (e.device_display_name || this.deviceLabel(e.device)) : "—")}</td>
      <td>${SourceUI.brand(e.source)}</td>
      <td>${esc(e.model || "日志未记录模型")}</td>
      <td title="${esc(e.cwd || "")}">${esc(this.trunc(repoLabel(e.repo, e.cwd), 28))}</td>
      ${num(e.input_tokens)}${num(e.output_tokens)}${num(e.cache_read_tokens)}${num(e.cache_creation_tokens)}
      <td class="numeric">${e.cost_usd != null ? usd(e.cost_usd) : "—"}</td>
      <td>${sid
        ? `<a class="session-link" href="${esc(this.routeHref({session: sid, offset: 0}))}" title="按此会话过滤:${esc(sid)}">${esc(sid.slice(0, 8))}</a>`
        : "—"}</td>
    </tr>`;
  },

  eventKey(event) {
    return event.event_id || [event.device, event.source, event.session_id, event.ts].join(":");
  },

  openEvent(key, trigger) {
    const event = this.events.find((item) => this.eventKey(item) === key);
    if (!event) return;
    this.selectedEvent = key;
    DetailDrawer.open({ key: "event:" + key, title: "事件详情",
      subtitle: SourceUI.label(event.source) + " · " + (event.model || "日志未记录模型"),
      html: this.eventHTML(event), trigger });
  },

  eventHTML(event) {
    const labels = {event_id: "事件标识", ts: "时间", device: "设备标识", device_display_name: "设备名称",
      source: "来源", model: "模型", provider: "提供方", account_label: "账户标签", session_id: "会话",
      cwd: "工作目录", repo: "项目", git_branch: "分支", input_tokens: "输入 tokens", output_tokens: "输出 tokens",
      cache_read_tokens: "缓存读取", cache_creation_tokens: "缓存写入", cost_usd: "成本 (USD)",
      duration_ms: "耗时 (ms)", gen_ms: "生成耗时 (ms)", ttft_ms: "首 token 耗时 (ms)", app_version: "应用版本"};
    const rows = Object.entries(event).map(([key, value]) => {
      const display = value == null ? "—" : key === "ts" ? new Date(value).toLocaleString("zh-CN", {hour12: false})
        : typeof value === "object" ? JSON.stringify(value) : String(value);
      return `<tr><th scope="row">${esc(labels[key] || key)}</th><td>${esc(display)}</td></tr>`;
    }).join("");
    return `<p class="subtle">仅展示用量元数据。</p><div class="data-table"><table><tbody>${rows}</tbody></table></div>`;
  },

  deviceLabel(identity) {
    return this.deviceNames[identity] || identity;
  },

  renderChips() {
    const labels = {
      device: "设备", source: "来源", model: "模型", repo: "项目", days: "范围",
    };
    const chips = Object.entries(this.filters)
      .filter(([key, value]) => key !== "session" && value && !(key === "days" && value === "7"))
      .map(([key, value]) => `<span class="chip">${labels[key]} ${esc(
        key === "device" ? this.deviceLabel(value) : key === "source" ? SourceUI.label(value) : value)}</span>`);
    if (this.filters.session) {
      chips.push(`<span class="chip">会话 ${esc(this.filters.session.slice(0, 8))}
        <a id="d-clear-session" class="chip-x" href="${esc(this.routeHref({session: "", offset: 0}))}" aria-label="清除会话过滤">×</a></span>`);
    }
    document.getElementById("d-chips").innerHTML = chips.join("");
  },

  trunc(s, n) {
    s = String(s);
    return s.length > n ? "…" + s.slice(-(n - 1)) : s;
  },
};
