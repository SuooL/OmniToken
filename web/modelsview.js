"use strict";
// 模型页(F22 / GAP-4):模型 × 工具来源堆叠柱状图 + 每日模型构成 + 明细表。
// 渲染进 #view-models。
//
// 配色纪律:只用既有的 --series-1..4,按固定顺序分配(来源图按 ORDER,
// 每日图按用量排名);第 5 个及以后一律并入「其他」走 --text-muted。
// 文字不着色,颜色只由图例和色块承担。

const ModelsView = {
  // 固定槽位:即使某来源当前无数据也占住它的色号,换时间窗颜色不会漂移。
  ORDER: ["claude-code", "codex", "proxy"],
  LABEL: { "claude-code": "Claude Code", codex: "Codex", proxy: "本地代理" },
  PALETTE: ["--series-1", "--series-2", "--series-3", "--series-4"],
  OTHER: "其他",
  DAYS: 30,
  _timer: null,

  enter() {
    this.load();
    this._timer = setInterval(() => this.load(), 30000);
  },

  leave() {
    clearInterval(this._timer);
  },

  async load() {
    try {
      // top=4:每日图最多 4 个模型 + 「其他」,正好用满调色板,不新造色相。
      this.render(await Api.get(`/api/v1/models?days=${this.DAYS}&top=4`));
      document.getElementById("refresh-note").textContent =
        "更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    } catch (e) {
      document.getElementById("refresh-note").textContent = "服务不可达,重试中…";
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

    root.innerHTML = `
      <section class="stat-row">${this.tiles(models, srcSeries, unpriced)}</section>
      <section class="card">
        <div class="card-head">
          <h2>模型 × 来源 · 近 ${d.days || this.DAYS} 天</h2>
          <div class="head-tools"><div class="legend">${this.legend(srcSeries)}</div></div>
        </div>
        <div class="chart">${this.sourceChart(models, srcSeries)}</div>
      </section>
      <section class="card">
        <div class="card-head">
          <h2>每日模型构成 · 前 ${d.top_n || 4} 个模型</h2>
          <div class="head-tools"><div class="legend">${this.legend(dailySeries)}</div></div>
        </div>
        <div class="chart" id="models-daily-chart">${this.dailyChart(daily, dailySeries, d.days || this.DAYS)}</div>
      </section>
      <section class="card">
        <h2>模型明细 · 按来源拆分</h2>
        <div class="data-table">${this.table(rows, unpriced)}</div>
      </section>`;

    this.attachTooltip(daily, dailySeries, d.days || this.DAYS);
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

  legend(series) {
    if (series.length < 2) return "";
    return series.map((s) =>
      `<span class="item"><span class="swatch" style="background:${s.color}"></span>${esc(s.label)}</span>`
    ).join("");
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
    if (!models.length) return `<p class="bars"><span class="empty">暂无数据,等待采集…</span></p>`;
    const shown = models.slice(0, 12);
    const W = 1060, labelW = 190, valueW = 150, rowH = 30, barH = 16;
    const plotW = W - labelW - valueW;
    const H = shown.length * rowH;
    const max = Math.max(...shown.map((m) => m.total), 1);

    let svg = `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="模型按工具来源堆叠柱状图">`;
    shown.forEach((m, i) => {
      const y = i * rowH + (rowH - barH) / 2;
      const base = y + barH - 4;
      svg += `<text class="strong" x="0" y="${base}" text-anchor="start">${esc(this.trunc(this.modelLabel(m.model)))}` +
        `<title>${esc(this.modelLabel(m.model))}</title></text>`;
      const visible = series.filter((s) => (m.by[s.key] || 0) > 0);
      let x = labelW;
      visible.forEach((s, si) => {
        const v = m.by[s.key];
        const w = (v / max) * plotW;
        // 段间留 2px:露出卡片表面色,不额外画分隔线。
        const ww = Math.max(w - (si === visible.length - 1 ? 0 : 2), 1);
        svg += `<rect x="${x.toFixed(1)}" y="${y}" width="${ww.toFixed(1)}" height="${barH}" rx="2" fill="${s.color}">` +
          `<title>${esc(s.label)} · ${full(v)} tokens</title></rect>`;
        x += w;
      });
      const cost = m.unpriced ? "无定价" : usd(m.cost);
      svg += `<text x="${W}" y="${base}" text-anchor="end">${compact(m.total)} · ${esc(cost)}</text>`;
    });
    svg += `</svg>`;
    if (models.length > shown.length) {
      svg += `<p class="subtle">另有 ${models.length - shown.length} 个模型,见下方明细表</p>`;
    }
    return svg;
  },

  trunc(s, n = 26) {
    return s.length > n ? s.slice(0, n - 1) + "…" : s;
  },

  // ---- 图二:每日模型构成 -------------------------------------------

  dailyChart(daily, series, days) {
    if (!daily.length) return `<p class="bars"><span class="empty">暂无数据,等待采集…</span></p>`;
    const buckets = this.fillDays(daily, series, days);
    const W = 1060, H = 240, padL = 46, padR = 8, padT = 10, padB = 22;
    const plotW = W - padL - padR, plotH = H - padT - padB;
    const yMax = this.niceCeil(Math.max(...buckets.map((b) => b.total), 1));
    const slot = plotW / buckets.length;
    const barW = Math.min(24, slot * 0.7);
    const y = (v) => padT + plotH * (1 - v / yMax);

    let svg = `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="每日模型构成堆叠柱状图">`;
    for (let i = 1; i <= 4; i++) {
      const gy = padT + (plotH * i) / 4;
      const val = yMax * (1 - i / 4);
      svg += `<line x1="${padL}" y1="${gy}" x2="${W - padR}" y2="${gy}" stroke="${cssVar("--grid")}" stroke-width="1"/>`;
      if (val > 0) svg += `<text x="${padL - 6}" y="${gy + 4}" text-anchor="end">${compact(val)}</text>`;
    }
    svg += `<text x="${padL - 6}" y="${padT + 4}" text-anchor="end">${compact(yMax)}</text>`;
    svg += `<line x1="${padL}" y1="${padT + plotH}" x2="${W - padR}" y2="${padT + plotH}" stroke="${cssVar("--baseline")}" stroke-width="1"/>`;

    const labelEvery = Math.ceil(buckets.length / 8);
    buckets.forEach((row, i) => {
      const cx = padL + slot * i + slot / 2;
      const x = cx - barW / 2;
      let cum = 0;
      series.forEach((s) => {
        const v = row.by[s.key] || 0;
        if (!v) return;
        const top = y(cum + v), hgt = y(cum) - y(cum + v);
        cum += v;
        if (hgt < 0.5) return;
        const isTop = cum >= row.total;
        const gap = isTop ? 0 : 2; // 段间 2px 表面色间隙
        svg += `<rect x="${x.toFixed(1)}" y="${(top + gap).toFixed(1)}" width="${barW.toFixed(1)}" height="${Math.max(hgt - gap, 0.5).toFixed(1)}" fill="${s.color}"/>`;
      });
      if (i % labelEvery === 0) {
        svg += `<text x="${cx}" y="${H - 6}" text-anchor="middle">${esc(row.bucket.slice(5))}</text>`;
      }
      svg += `<rect class="hit" data-i="${i}" x="${padL + slot * i}" y="${padT}" width="${slot}" height="${plotH}" fill="transparent"/>`;
    });
    return svg + `</svg>`;
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

  niceCeil(v) {
    const mag = Math.pow(10, Math.floor(Math.log10(v)));
    for (const m of [1, 2, 2.5, 4, 5, 8, 10]) {
      if (m * mag >= v) return m * mag;
    }
    return 10 * mag;
  },

  attachTooltip(daily, series, days) {
    const el = document.getElementById("models-daily-chart");
    if (!el) return;
    const buckets = this.fillDays(daily, series, days);
    const tip = document.getElementById("tooltip");
    el.querySelectorAll(".hit").forEach((hit) => {
      hit.addEventListener("mousemove", (ev) => {
        const row = buckets[+hit.dataset.i];
        tip.innerHTML = `<div class="t-date">${esc(row.bucket)}</div>` +
          series.filter((s) => row.by[s.key]).map((s) =>
            `<div class="t-row"><span class="k"><span class="swatch" style="background:${s.color}"></span>${esc(s.label)}</span><span class="v">${full(row.by[s.key])}</span></div>`
          ).join("") +
          `<div class="t-row"><span class="k">合计</span><span class="v">${full(row.total)}</span></div>`;
        tip.hidden = false;
        const pad = 14;
        let tx = ev.clientX + pad, ty = ev.clientY + pad;
        const r = tip.getBoundingClientRect();
        if (tx + r.width > innerWidth - 8) tx = ev.clientX - r.width - pad;
        if (ty + r.height > innerHeight - 8) ty = ev.clientY - r.height - pad;
        tip.style.left = tx + "px";
        tip.style.top = ty + "px";
      });
      hit.addEventListener("mouseleave", () => { tip.hidden = true; });
    });
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
