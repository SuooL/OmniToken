"use strict";
// 模型页(F22 / GAP-4):模型 × 工具来源堆叠柱状图 + 每日模型构成 + 明细表。
// 渲染进 #view-models。
//
// 配色纪律:只用既有的 --series-1..4,按固定顺序分配(来源图按 ORDER,
// 每日图按用量排名);第 5 个及以后一律并入「其他」走 --text-muted。
// 文字不着色,颜色只由图例和色块承担。
//
// 图表走 ECharts(ADR-0010),与总览/实时/速度页同一套:共享 tooltip、hover
// 高亮、渐变与自适应都不值得再手写一遍 SVG。此前这两张图是手绘的,没有
// 十字准星,tooltip 是自己挂在透明 hit 区上的一套平行实现。

function modelsIsEmpty(data) {
  return !(data.by_source || []).some((row) => (row.total_tokens || 0) > 0);
}

const ModelsView = {
  // 固定槽位:即使某来源当前无数据也占住它的色号,换时间窗颜色不会漂移。
  ORDER: ["claude-code", "codex", "proxy"],
  LABEL: { "claude-code": "Claude", codex: "Codex", proxy: "本地代理" },
  PALETTE: ["--series-1", "--series-2", "--series-3", "--series-4"],
  OTHER: "其他",
  DAYS: 30,
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
    const root = document.getElementById("view-models");
    if (!this.lastData) {
      renderState(root, { kind: "loading", title: "正在加载模型数据" });
    }
    try {
      // top=4:每日图最多 4 个模型 + 「其他」,正好用满调色板,不新造色相。
      const data = await Api.get(`/api/v1/models?days=${this.DAYS}&top=4`);
      if (!isCurrentGeneration(this._loadGeneration, loadID)) return;
      this.render(data);
      this.lastData = data;
      if (modelsIsEmpty(data)) {
        renderState(root, {
          kind: "empty", title: "暂无模型用量",
          detail: "近 30 天尚无可展示的模型与来源数据。",
        });
      } else {
        renderState(root, { kind: "ready", title: "" });
      }
      document.getElementById("refresh-note").textContent =
        "更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    } catch (e) {
      if (!isCurrentGeneration(this._loadGeneration, loadID)) return;
      const issue = classifyAPIError(e);
      renderState(root, {
        kind: this.lastData ? "stale" : issue.kind,
        title: this.lastData ? "模型数据可能已过期" : issue.title,
        detail: issue.detail,
        action: { label: "重试", run: () => this.load() },
      });
      document.getElementById("refresh-note").textContent = this.lastData
        ? "刷新失败,正在显示上次数据" : issue.title;
    }
  },

  render(d) {
    const root = document.getElementById("view-models");
    const rows = (d.by_source || []).filter((r) => r.total_tokens > 0);
    const unpriced = new Set(d.unpriced || []);
    const srcSeries = this.sourceSeries(rows);
    const models = this.groupByModel(rows, srcSeries, unpriced);
    const daily = (d.daily || []).filter((r) => r.total_tokens > 0);
    const dailySeries = this.modelSeries(daily);

    // 结构只建一次:ECharts 实例挂在这些节点上,每轮轮询重建 innerHTML 会把
    // 画布连同它的动画状态一起丢掉。
    if (!root.dataset.built) {
      root.innerHTML = `
        <section class="stat-row" id="models-tiles"></section>
        <section class="card">
          <div class="card-head"><h2 id="models-source-title">模型 × 来源</h2></div>
          <div id="models-source-chart"></div>
          <p class="subtle" id="models-source-note"></p>
        </section>
        <section class="card">
          <div class="card-head"><h2 id="models-daily-title">每日模型构成</h2></div>
          <div id="models-daily-chart" style="height:300px"></div>
        </section>
        <section class="card">
          <h2>模型明细 · 按来源拆分</h2>
          <div class="data-table" id="models-table"></div>
        </section>`;
      root.dataset.built = "1";
    }
    document.getElementById("models-source-title").textContent =
      `模型 × 来源 · 近 ${d.days || this.DAYS} 天`;
    document.getElementById("models-daily-title").textContent =
      `每日模型构成 · 前 ${d.top_n || 4} 个模型`;
    document.getElementById("models-tiles").innerHTML = this.tiles(models, srcSeries, unpriced);
    document.getElementById("models-table").innerHTML = this.table(rows, unpriced);
    this.sourceChart(models, srcSeries);
    this.dailyChart(daily, dailySeries, d.days || this.DAYS);
  },

  // ---- 序列与配色 ---------------------------------------------------

  // sourceSeries:已知来源按固定顺序占 series-1..3,第一个未知来源补 series-4,
  // 再多的并入「其他」。返回按堆叠顺序排列的序列描述。
  sourceSeries(rows) {
    const present = [];
    rows.forEach((r) => {
      const k = r.source || "";
      if (!present.includes(k)) present.push(k);
    });
    const slot = new Map();
    this.ORDER.forEach((s, i) => slot.set(s, this.PALETTE[i]));
    let next = this.ORDER.length;
    present.forEach((s) => {
      if (slot.has(s)) return;
      slot.set(s, next < this.PALETTE.length ? this.PALETTE[next++] : null);
    });

    const series = [];
    this.ORDER.forEach((s) => {
      if (present.includes(s)) series.push({ key: s, label: this.sourceLabel(s), color: cssVar(slot.get(s)) });
    });
    present.forEach((s) => {
      if (this.ORDER.includes(s) || !slot.get(s)) return;
      series.push({ key: s, label: this.sourceLabel(s), color: cssVar(slot.get(s)) });
    });
    const folded = present.filter((s) => !this.ORDER.includes(s) && !slot.get(s));
    if (folded.length) {
      series.push({ key: this.OTHER, label: `${this.OTHER} (${folded.length})`, color: cssVar("--text-muted"), fold: folded });
    }
    return series;
  },

  // modelSeries:每日图的模型序列。后端已按 topN 归并出「其他」,这里只按
  // 区间用量排名分配色号,「其他」固定用 muted 并排在最后。
  modelSeries(daily) {
    const total = {};
    daily.forEach((r) => { total[r.model] = (total[r.model] || 0) + r.total_tokens; });
    const names = Object.keys(total).filter((m) => m !== this.OTHER)
      .sort((a, b) => total[b] - total[a] || (a < b ? -1 : 1));
    const series = names.slice(0, this.PALETTE.length).map((m, i) => ({
      key: m, label: this.modelLabel(m), color: cssVar(this.PALETTE[i]),
    }));
    const rest = names.slice(this.PALETTE.length);
    if (total[this.OTHER] || rest.length) {
      series.push({ key: this.OTHER, label: this.OTHER, color: cssVar("--text-muted"), fold: rest });
    }
    return series;
  },

  // seriesOf 把一行的 key 映射到它实际所属的序列(可能被折进「其他」)。
  seriesOf(series, key) {
    return series.find((s) => s.key === key) ||
      series.find((s) => s.fold && s.fold.includes(key)) || null;
  },

  sourceLabel(s) {
    return this.LABEL[s] || s || "(未知来源)";
  },

  modelLabel(m) {
    return m || "(未知模型)";
  },

  groupByModel(rows, series, unpriced) {
    const byModel = new Map();
    rows.forEach((r) => {
      let m = byModel.get(r.model);
      if (!m) {
        m = { model: r.model, total: 0, events: 0, cost: 0, unpriced: unpriced.has(r.model), by: {} };
        byModel.set(r.model, m);
      }
      m.total += r.total_tokens;
      m.events += r.events;
      if (r.cost_usd != null) m.cost += r.cost_usd;
      const s = this.seriesOf(series, r.source || "");
      const key = s ? s.key : this.OTHER;
      m.by[key] = (m.by[key] || 0) + r.total_tokens;
    });
    return [...byModel.values()].sort((a, b) => b.total - a.total);
  },

  // ---- 概览数字 -----------------------------------------------------

  tiles(models, series, unpriced) {
    const total = models.reduce((a, m) => a + m.total, 0);
    const events = models.reduce((a, m) => a + m.events, 0);
    const cost = models.reduce((a, m) => a + (m.unpriced ? 0 : m.cost), 0);
    const missing = models.filter((m) => m.unpriced).length;
    return `
      <div class="stat-tile">
        <div class="label">模型数(近 ${this.DAYS} 天)</div>
        <div class="value">${models.length}</div>
        <div class="sub">来源 ${series.length} 个</div>
      </div>
      <div class="stat-tile">
        <div class="label">合计 tokens</div>
        <div class="value">${compact(total)}</div>
        <div class="sub">请求 ${full(events)}</div>
      </div>
      <div class="stat-tile">
        <div class="label">合计成本</div>
        <div class="value">${usd(cost)}</div>
        <div class="sub">${missing ? `${missing} 个模型无定价,未计入` : "全部模型已定价"}</div>
      </div>`;
  },

  // ---- 图一:模型 × 来源横向堆叠 ------------------------------------

  sourceChart(models, series) {
    const el = document.getElementById("models-source-chart");
    const note = document.getElementById("models-source-note");
    if (!models.length) {
      el.style.height = "auto";
      el.innerHTML = `<p class="bars"><span class="empty">暂无数据,等待采集…</span></p>`;
      note.textContent = "";
      return;
    }
    const shown = models.slice(0, 12);
    note.textContent = models.length > shown.length
      ? `另有 ${models.length - shown.length} 个模型,见下方明细表` : "";
    // 行高固定,画布高度跟着行数走:12 个模型和 3 个模型不该占同样的空间。
    el.style.height = shown.length * 30 + 46 + "px";

    const names = shown.map((m) => this.modelLabel(m.model));
    const totals = shown.map((m) => m.total);
    const costs = shown.map((m) => (m.unpriced ? "无定价" : usd(m.cost)));

    echartsFor(el).setOption({
      grid: { left: 8, right: 90, top: 28, bottom: 0, containLabel: true },
      tooltip: {
        trigger: "axis", axisPointer: { type: "shadow" }, ...tooltipStyle(),
        valueFormatter: (v) => (v ? full(v) : "—"),
      },
      legend: {
        top: 0, left: 0, itemWidth: 9, itemHeight: 9, itemGap: 14,
        textStyle: { color: cssVar("--text-secondary"), fontSize: 11, fontFamily: chartFont() },
      },
      xAxis: { type: "value", show: false },
      yAxis: {
        type: "category",
        data: names,
        inverse: true, // 用量最大的排在最上面,与表格同序
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: {
          color: cssVar("--text-secondary"), fontSize: 11, fontFamily: chartFont(),
          width: 180, overflow: "truncate",
        },
      },
      series: series.map((sr, i) => ({
        name: sr.label,
        type: "bar",
        stack: "tokens",
        barMaxWidth: 16,
        itemStyle: {
          color: sr.color,
          borderRadius: i === series.length - 1 ? [0, 3, 3, 0] : 0,
        },
        emphasis: { focus: "series" },
        data: shown.map((m) => m.by[sr.key] || 0),
      })).concat([{
        // 值标签挂在一根零宽的透明段上。ECharts 的 label 属于某个 series,
        // 而「哪一段是这一行的最后一段」逐行不同,拿一根空段收尾才能让
        // 合计与成本稳定地落在条形右侧。
        name: "",
        type: "bar",
        stack: "tokens",
        silent: true,
        tooltip: { show: false },
        legendHoverLink: false,
        itemStyle: { color: "transparent" },
        label: {
          show: true, position: "right", color: cssVar("--text-muted"),
          fontSize: 11, fontFamily: chartFont(),
          formatter: (p) => `${compact(totals[p.dataIndex])} · ${costs[p.dataIndex]}`,
        },
        data: shown.map(() => 0),
      }]),
      animationDuration: 420,
      animationEasing: "cubicOut",
    }, true);
  },

  trunc(s, n = 26) {
    return s.length > n ? s.slice(0, n - 1) + "…" : s;
  },

  // ---- 图二:每日模型构成 -------------------------------------------

  dailyChart(daily, series, days) {
    const el = document.getElementById("models-daily-chart");
    if (!daily.length) {
      el.innerHTML = `<p class="bars"><span class="empty">暂无数据,等待采集…</span></p>`;
      return;
    }
    const buckets = this.fillDays(daily, series, days);
    echartsFor(el).setOption({
      grid: { left: 8, right: 8, top: 28, bottom: 4, containLabel: true },
      tooltip: {
        trigger: "axis", axisPointer: { type: "shadow" }, ...tooltipStyle(),
        valueFormatter: (v) => (v ? full(v) : "—"),
      },
      legend: {
        top: 0, left: 0, itemWidth: 9, itemHeight: 9, itemGap: 14,
        textStyle: { color: cssVar("--text-secondary"), fontSize: 11, fontFamily: chartFont() },
      },
      xAxis: {
        type: "category",
        data: buckets.map((b) => b.bucket.slice(5)),
        axisLine: { lineStyle: { color: cssVar("--baseline") } },
        axisTick: { show: false },
        axisLabel: { color: cssVar("--text-muted"), fontSize: 10, fontFamily: chartFont() },
      },
      yAxis: {
        type: "value",
        splitLine: { lineStyle: { color: cssVar("--grid") } },
        axisLabel: {
          color: cssVar("--text-muted"), fontSize: 10, fontFamily: chartFont(),
          formatter: (v) => compact(v),
        },
      },
      series: series.map((sr, i) => ({
        name: sr.label,
        type: "bar",
        stack: "tokens",
        barMaxWidth: 26,
        // 只有最顶上那段圆角,整叠柱子才读作一根柱子(与总览页一致)。
        itemStyle: {
          color: gradientOf(sr.color),
          borderRadius: i === series.length - 1 ? [4, 4, 0, 0] : 0,
        },
        emphasis: { focus: "series" },
        data: buckets.map((b) => b.by[sr.key] || 0),
      })),
      animationDuration: 420,
      animationEasing: "cubicOut",
    }, true);
  },

  // fillDays 补齐区间内无数据的日子,免得柱子被挤成假的连续序列。
  fillDays(daily, series, days) {
    const byBucket = {};
    daily.forEach((r) => {
      const b = (byBucket[r.bucket] ||= { bucket: r.bucket, total: 0, by: {} });
      const s = this.seriesOf(series, r.model);
      const key = s ? s.key : this.OTHER;
      b.by[key] = (b.by[key] || 0) + r.total_tokens;
      b.total += r.total_tokens;
    });
    const out = [];
    const end = new Date();
    for (let i = days - 1; i >= 0; i--) {
      const d = new Date(end.getFullYear(), end.getMonth(), end.getDate() - i);
      const key = d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
      out.push(byBucket[key] || { bucket: key, total: 0, by: {} });
    }
    return out;
  },

  // ---- 明细表 -------------------------------------------------------

  table(rows, unpriced) {
    if (!rows.length) return `<span class="empty">暂无数据</span>`;
    const grand = rows.reduce((a, r) => a + r.total_tokens, 0) || 1;
    return `<table><thead><tr>
        <th>模型</th><th>来源</th><th>输入</th><th>输出</th><th>缓存读取</th>
        <th>缓存写入</th><th>合计</th><th>成本</th><th>请求</th><th>占比</th>
      </tr></thead><tbody>` +
      rows.map((r) => {
        const label = this.sourceLabel(r.source);
        const cost = unpriced.has(r.model) ? "无定价" : usd(r.cost_usd || 0);
        const share = (100 * r.total_tokens / grand).toFixed(1) + "%";
        return `<tr>
          <td title="${esc(this.modelLabel(r.model))}">${esc(this.trunc(this.modelLabel(r.model), 34))}</td>
          <td>${esc(label)}</td>
          <td>${full(r.input_tokens)}</td>
          <td>${full(r.output_tokens)}</td>
          <td>${full(r.cache_read_tokens)}</td>
          <td>${full(r.cache_creation_tokens)}</td>
          <td>${full(r.total_tokens)}</td>
          <td>${cost}</td>
          <td>${full(r.events)}</td>
          <td>${share}</td>
        </tr>`;
      }).join("") + `</tbody></table>`;
  },
};
