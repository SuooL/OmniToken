"use strict";
// Cache view (F16): hit rate, dollars saved by cache reads, TTL write split.
// Renders into #view-cache; single-hue visuals reuse .bars / .data-table.

function pct(r) {
  if (!isFinite(r) || r <= 0) return "0%";
  if (r >= 0.995) return "100%";
  return (r * 100).toFixed(1) + "%";
}

function cacheIsEmpty(data) {
  return !(data.models || []).some((row) =>
    (row.input_tokens || 0) + (row.cache_read_tokens || 0) + (row.cache_creation_tokens || 0) > 0);
}

const CacheView = {
  _timer: null,
  lastData: null,
  _loadGeneration: 0,

  enter() {
    this.load();
    this._timer = setInterval(() => this.load(), 30000);
  },

  leave() {
    clearInterval(this._timer);
    this._loadGeneration += 1;
  },

  async load() {
    const loadID = ++this._loadGeneration;
    const root = document.getElementById("view-cache");
    if (!this.lastData) {
      renderState(root, { kind: "loading", title: "正在加载缓存数据" });
    }
    try {
      const data = await Api.get("/api/v1/cache?days=30");
      if (!isCurrentGeneration(this._loadGeneration, loadID)) return;
      this.render(data);
      this.lastData = data;
      if (cacheIsEmpty(data)) {
        renderState(root, {
          kind: "empty", title: "暂无缓存用量",
          detail: "近 30 天没有输入、缓存读取或缓存写入数据。",
        });
      } else {
        renderState(root, { kind: "ready", title: "" });
      }
      document.getElementById("refresh-note").textContent =
        "更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    } catch (e) {
      if (!isCurrentGeneration(this._loadGeneration, loadID)) return;
      const issue = classifyAPIError(e);
      if (e && (e.status === 401 || e.status === 403)) {
        this.lastData = null;
        this._displayData = null;
        this._selectedDevice = null;
        ChartRegistry.disposeWithin(root);
        DetailDrawer.close({restoreFocus: false});
        document.querySelector("#tool-detail-drawer .drawer-content")?.replaceChildren();
        document.querySelector("#tool-detail-title")?.replaceChildren();
        document.querySelector("#tool-detail-subtitle")?.replaceChildren();
        root.replaceChildren();
        delete root.dataset.built;
      }
      renderState(root, {
        kind: this.lastData ? "stale" : issue.kind,
        title: this.lastData ? "缓存数据可能已过期" : issue.title,
        detail: issue.detail,
        action: { label: "重试", run: () => this.load() },
      });
      document.getElementById("refresh-note").textContent = this.lastData
        ? "刷新失败,正在显示上次数据" : issue.title;
    }
  },

  render(d) {
    const root = document.getElementById("view-cache");
    const t = d.totals || {};
    const cacheComposition = (d.models || []).reduce((sum, model) => ({
      input_tokens: sum.input_tokens + (model.input_tokens || 0),
      cache_read_tokens: sum.cache_read_tokens + (model.cache_read_tokens || 0),
      cache_creation_tokens: sum.cache_creation_tokens + (model.cache_creation_tokens || 0),
    }), { input_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0 });
    const unpriced = new Set(d.unpriced || []);
    if (!root.dataset.built) {
      root.innerHTML = `
        <section class="stat-row" id="cache-tiles"></section>
        <section class="instrument-card"><h2 id="cache-table-title">按模型</h2>
          <div class="data-table data-table-shell" id="cache-model-table"></div></section>
        <section class="chart-card"><div class="card-head"><h2>输入与缓存构成</h2><span class="subtle">读取 / 写入 / 非缓存输入</span></div>
          <div id="cache-composition-chart" style="height:260px" data-chart="cache-composition"></div></section>
        ${ToolUI.analysis("cache-model-analysis", "模型缓存比较", `<div id="cache-model-chart" style="height:300px" data-chart="cache-model-comparison"></div>`)}
        ${ToolUI.analysis("cache-daily-analysis", "每日命中率 · 缓存读取 / (缓存读取 + 输入)", `<div class="bars" id="cache-daily-bars"></div>`)}
      `;
      root.dataset.built = "1";
    }
    const models = d.models || [];
    const hasPriced = models.some((model) => !unpriced.has(model.model));
    ToolUI.updateHTML(document.getElementById("cache-tiles"), this.tiles(t, cacheComposition, hasPriced, unpriced.size > 0));
    document.getElementById("cache-table-title").textContent = `按模型 · 近 ${d.days || 30} 天`;
    ToolUI.updateHTML(document.getElementById("cache-model-table"), this.modelTable(models, unpriced));
    ToolUI.updateHTML(document.getElementById("cache-daily-bars"), this.dailyBars(d.daily || []));
    this.compositionChart(cacheComposition);
    this.modelChart(d.models || [], unpriced);
  },

  compositionChart(t) {
    const el = document.getElementById("cache-composition-chart");
    const rows = [
      {name: "缓存读取", value: t.cache_read_tokens || 0, color: cssVar("--status-healthy")},
      {name: "缓存写入", value: t.cache_creation_tokens || 0, color: cssVar("--series-2")},
      {name: "非缓存输入", value: t.input_tokens || 0, color: cssVar("--series-3")},
    ];
    if (!rows.some((row) => row.value > 0)) {
      ChartRegistry.disposeWithin?.(el);
      el.innerHTML = `<p class="empty">暂无缓存构成数据。</p>`;
      return;
    }
    if (el.querySelector?.(".empty")) el.replaceChildren();
    ChartRegistry.set(el, {
      titleText: "缓存 token 构成",
      tooltip: {trigger: "item", ...tooltipStyle()},
      legend: {bottom: 0, textStyle: {color: cssVar("--text-secondary")}},
      series: [{
        type: "pie", radius: ["48%", "72%"], center: ["50%", "46%"],
        label: {color: cssVar("--text-secondary"), formatter: "{b}\n{d}%"},
        data: rows.map((row) => ({name: row.name, value: row.value, itemStyle: {color: row.color}})),
      }],
    });
  },

  modelChart(models, unpriced = new Set()) {
    const el = document.getElementById("cache-model-chart");
    const rows = models.filter((model) =>
      model.input_tokens + model.cache_read_tokens + model.cache_creation_tokens > 0);
    if (!rows.length) {
      ChartRegistry.disposeWithin?.(el);
      el.innerHTML = `<p class="empty">暂无模型缓存数据。</p>`;
      return;
    }
    if (el.querySelector?.(".empty")) el.replaceChildren();
    ChartRegistry.set(el, {
      titleText: "模型缓存命中率与节省",
      grid: {left: 12, right: 44, top: 16, bottom: 48, containLabel: true},
      xAxis: {type: "category", data: rows.map((row) => row.model || "(未知)"), axisLabel: {color: cssVar("--text-muted"), rotate: rows.length > 5 ? 25 : 0}},
      yAxis: [
        {type: "value", name: "命中率", max: 1, axisLabel: {color: cssVar("--text-muted"), formatter: (v) => pct(v)}, splitLine: {lineStyle: {color: cssVar("--grid")}}},
        {type: "value", name: "节省 USD", axisLabel: {color: cssVar("--text-muted"), formatter: usd}, splitLine: {show: false}},
      ],
      series: [
        {name: "命中率", type: "bar", barMaxWidth: 22, data: rows.map((row) => row.input_tokens + row.cache_read_tokens > 0 ? row.hit_rate : null), itemStyle: {color: cssVar("--status-healthy")}},
        {name: "节省", type: "line", yAxisIndex: 1, data: rows.map((row) => unpriced.has(row.model) ? null : row.saved_usd), itemStyle: {color: cssVar("--source-aggregate")}},
      ],
    });
  },

  tiles(t, composition = {}, hasPriced = true, partial = false) {
    const denominator = (composition.input_tokens || 0) + (composition.cache_read_tokens || 0);
    const writes = (t.cache_1h_tokens || 0) + (t.cache_5m_tokens || 0);
    const split = writes > 0
      ? `${pct(t.cache_1h_tokens / writes)} / ${pct(t.cache_5m_tokens / writes)}`
      : "—";
    const splitSub = writes > 0
      ? `1h ${compact(t.cache_1h_tokens)} · 5m ${compact(t.cache_5m_tokens)}`
      : "日志无 TTL 分量";
    return `
      <div class="stat-tile">
        <div class="label">总命中率(近 30 天)</div>
        <div class="value">${denominator > 0 ? pct(t.hit_rate) : "暂无可计算输入"}</div>
        <div class="sub">缓存读取 / (缓存读取 + 输入)</div>
      </div>
      <div class="stat-tile">
        <div class="label">缓存节省(等效)</div>
        <div class="value">${hasPriced ? (partial ? "≥ " : "") + usd(t.saved_usd || 0) : "无定价"}</div>
        <div class="sub">按 输入价 − 缓存读取价 折算</div>
      </div>
      <div class="stat-tile">
        <div class="label">缓存写入 TTL · 1h / 5m 占比</div>
        <div class="value">${split}</div>
        <div class="sub">${splitSub}</div>
      </div>`;
  },

  modelTable(models, unpriced) {
    models = models.filter((m) => m.input_tokens + m.cache_read_tokens + m.cache_creation_tokens > 0);
    if (!models.length) return `<span class="empty">暂无数据</span>`;
    return `<table><thead><tr>
        <th>模型</th><th>命中率</th><th>缓存读取</th><th>缓存写入</th>
        <th>1h / 5m</th><th>输入</th><th>请求</th><th>节省</th>
      </tr></thead><tbody>` +
      models.map((m) => {
        const ttl = m.cache_1h_tokens + m.cache_5m_tokens > 0
          ? `${compact(m.cache_1h_tokens)} / ${compact(m.cache_5m_tokens)}` : "—";
        const saved = unpriced.has(m.model) ? "无定价" : usd(m.saved_usd || 0);
        return `<tr>
          <td>${esc(m.model || "(未知)")}</td>
          <td>${m.input_tokens + m.cache_read_tokens > 0 ? pct(m.hit_rate) : "暂无可计算输入"}</td>
          <td>${full(m.cache_read_tokens)}</td>
          <td>${full(m.cache_creation_tokens)}</td>
          <td>${ttl}</td>
          <td>${full(m.input_tokens)}</td>
          <td>${full(m.events)}</td>
          <td>${saved}</td>
        </tr>`;
      }).join("") + `</tbody></table>`;
  },

  dailyBars(daily) {
    daily = daily.filter((r) => r.input_tokens + r.cache_read_tokens > 0);
    if (!daily.length) return `<span class="empty">暂无数据,等待采集…</span>`;
    return daily.map((r) => `<div class="row">
        <div class="row-head">
          <span class="key">${esc(r.bucket)}</span>
          <span class="val">${pct(r.hit_rate)} <span class="extra">· 读 ${compact(r.cache_read_tokens)} / 入 ${compact(r.input_tokens)}</span></span>
        </div>
        <div class="track"><div class="fill" style="width:${(100 * r.hit_rate).toFixed(1)}%"></div></div>
      </div>`).join("");
  },
};
