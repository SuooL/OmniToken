"use strict";
// Details view: filterable, paginated raw-event drill-down (F13).

const Details = {
  filters: { device: "", source: "", model: "", repo: "", session: "", days: "7" },
  limit: 100,
  offset: 0,
  total: 0,
  built: false,
  seq: 0, // guards against out-of-order fetch responses

  enter() {
    if (!this.built) {
      this.build();
      this.loadOptions();
      this.built = true;
    }
    this.load();
  },

  leave() {},

  build() {
    document.getElementById("view-details").innerHTML = `
    <section class="card">
      <div class="card-head">
        <h2>事件明细</h2>
        <div class="head-tools"><span class="subtle" id="d-note"></span></div>
      </div>
      <div class="filter-row">
        <select id="d-f-device"><option value="">全部设备</option></select>
        <select id="d-f-source"><option value="">全部来源</option></select>
        <select id="d-f-model"><option value="">全部模型</option></select>
        <select id="d-f-repo"><option value="">全部项目</option></select>
        <select id="d-f-days">
          <option value="7">近 7 天</option>
          <option value="30">近 30 天</option>
          <option value="90">近 90 天</option>
        </select>
        <span class="chip-row" id="d-chips"></span>
      </div>
      <div id="d-table" class="data-table"></div>
      <div class="pager">
        <span class="subtle" id="d-page"></span>
        <button id="d-prev" class="ghost-btn">上一页</button>
        <button id="d-next" class="ghost-btn">下一页</button>
      </div>
    </section>`;

    for (const [id, key] of [["d-f-device", "device"], ["d-f-source", "source"],
                             ["d-f-model", "model"], ["d-f-repo", "repo"], ["d-f-days", "days"]]) {
      document.getElementById(id).addEventListener("change", (ev) => {
        this.filters[key] = ev.target.value;
        this.offset = 0;
        this.load();
      });
    }
    document.getElementById("d-prev").addEventListener("click", () => {
      if (this.offset > 0) {
        this.offset = Math.max(0, this.offset - this.limit);
        this.load();
      }
    });
    document.getElementById("d-next").addEventListener("click", () => {
      if (this.offset + this.limit < this.total) {
        this.offset += this.limit;
        this.load();
      }
    });
    // Session drill-down: click a session id in the table to filter by it.
    document.getElementById("d-table").addEventListener("click", (ev) => {
      const el = ev.target.closest("[data-session]");
      if (!el) return;
      this.filters.session = el.dataset.session;
      this.offset = 0;
      this.load();
    });
    document.getElementById("d-chips").addEventListener("click", (ev) => {
      if (!ev.target.closest("#d-clear-session")) return;
      this.filters.session = "";
      this.offset = 0;
      this.load();
    });
  },

  // Filter options come from the 90-day breakdown so rarely-used values
  // still show up regardless of the currently selected time range.
  async loadOptions() {
    const dims = [["device", "d-f-device"], ["source", "d-f-source"],
                  ["model", "d-f-model"], ["repo", "d-f-repo"]];
    await Promise.all(dims.map(async ([by, id]) => {
      try {
        const res = await fetch(`/api/v1/breakdown?by=${by}&days=90`);
        const d = await res.json();
        const sel = document.getElementById(id);
        sel.innerHTML = sel.firstElementChild.outerHTML +
          (d.rows || []).filter((r) => r.key).map((r) =>
            `<option value="${esc(r.key)}">${esc(by === "repo" ? repoLabel(r.key) : r.key)}</option>`
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
      const res = await fetch("/api/v1/events?" + p);
      const d = await res.json();
      if (my !== this.seq) return; // superseded by a newer request
      this.total = d.total || 0;
      this.render(d.events || []);
      note.textContent = "更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    } catch (e) {
      if (my === this.seq) note.textContent = "服务不可达";
    }
  },

  render(events) {
    this.renderChips();
    const el = document.getElementById("d-table");
    el.innerHTML = events.length
      ? `<table><thead><tr>
          <th>时间</th><th>设备</th><th>来源</th><th>模型</th><th>项目</th>
          <th>输入</th><th>输出</th><th>缓存读</th><th>缓存写</th><th>成本</th><th>会话</th>
        </tr></thead><tbody>` + events.map((e) => this.rowHTML(e)).join("") + `</tbody></table>`
      : `<p class="subtle">无匹配事件</p>`;

    const from = this.total === 0 ? 0 : this.offset + 1;
    document.getElementById("d-page").textContent =
      `第 ${from}–${this.offset + events.length} 条 / 共 ${full(this.total)} 条`;
    document.getElementById("d-prev").disabled = this.offset <= 0;
    document.getElementById("d-next").disabled = this.offset + this.limit >= this.total;
  },

  rowHTML(e) {
    const t = new Date(e.ts);
    const time = t.toLocaleString("zh-CN", {
      month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false,
    });
    const sid = e.session_id || "";
    const num = (v) => `<td title="${full(v || 0)}">${compact(v || 0)}</td>`;
    return `<tr>
      <td title="${esc(t.toLocaleString("zh-CN", { hour12: false }))} · ${relTime(e.ts)}">${time}</td>
      <td>${esc(e.device || "—")}</td>
      <td>${esc(e.source || "—")}</td>
      <td>${esc(e.model || "—")}</td>
      <td title="${esc(e.cwd || "")}">${esc(this.trunc(repoLabel(e.repo, e.cwd), 28))}</td>
      ${num(e.input_tokens)}${num(e.output_tokens)}${num(e.cache_read_tokens)}${num(e.cache_creation_tokens)}
      <td>${e.cost_usd != null ? usd(e.cost_usd) : "—"}</td>
      <td>${sid
        ? `<a class="session-link" data-session="${esc(sid)}" title="按此会话过滤:${esc(sid)}">${esc(sid.slice(0, 8))}</a>`
        : "—"}</td>
    </tr>`;
  },

  renderChips() {
    document.getElementById("d-chips").innerHTML = this.filters.session
      ? `<span class="chip">会话 ${esc(this.filters.session.slice(0, 8))}
          <button id="d-clear-session" class="chip-x" title="清除会话过滤">×</button></span>`
      : "";
  },

  trunc(s, n) {
    s = String(s);
    return s.length > n ? "…" + s.slice(-(n - 1)) : s;
  },
};
