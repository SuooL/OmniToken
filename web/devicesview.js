"use strict";
// Devices view (F21 / GAP-3): cross-device comparison, per-device stacked
// daily trend, last-report time. Renders into #view-devices.
//
// At most four devices get a series colour; everything else collapses into a
// single muted "其他" band. Minting extra hues would imply distinctions the
// four-colour palette can't keep legible in both themes — the detail table
// below carries the per-device numbers instead.
const DEVICE_SERIES_VARS = ["--series-1", "--series-2", "--series-3", "--series-4"];
const DEVICE_SERIES_MAX = 4;

const DevicesView = {
  _timer: null,

  enter() {
    this.load();
    this._timer = setInterval(() => this.load(), 30000);
  },

  leave() {
    clearInterval(this._timer);
    this._timer = null;
  },

  async load() {
    try {
      this.render(await Api.get("/api/v1/devices?days=30"));
      document.getElementById("refresh-note").textContent =
        "更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    } catch (e) {
      document.getElementById("refresh-note").textContent = "服务不可达,重试中…";
    }
  },

  render(d) {
    const root = document.getElementById("view-devices");
    const days = d.days || 30;
    const summary = (d.summary || []).filter((r) => r.total_tokens > 0);
    const series = this.series(summary);
    const matrix = this.matrix(d.daily || [], series, days);
    root.innerHTML = `
      <section class="stat-row">${this.tiles(summary, days)}</section>
      <section class="card">
        <div class="card-head">
          <h2>每日用量 · 按设备堆叠 · 近 ${days} 天</h2>
          <div class="legend">${this.legend(series)}</div>
        </div>
        <div id="devices-chart" class="chart">${this.chart(matrix, series)}</div>
      </section>
      <section class="card">
        <h2>设备明细 · 近 ${days} 天</h2>
        <div class="data-table">${this.table(summary)}</div>
      </section>`;
    this.attachTooltip(document.getElementById("devices-chart"), matrix, series);
  },

  label(device) {
    return device || "(未知设备)";
  },

  // series maps the summary (already sorted by tokens desc) onto colour slots.
  series(summary) {
    const out = summary.slice(0, DEVICE_SERIES_MAX).map((r, i) => ({
      key: r.device,
      label: this.label(r.device),
      varName: DEVICE_SERIES_VARS[i],
    }));
    const rest = summary.slice(DEVICE_SERIES_MAX);
    if (rest.length) {
      out.push({ key: null, other: true, label: `其他 (${rest.length})`, varName: "--text-muted" });
    }
    return out;
  },

  legend(series) {
    if (series.length < 2) return "";
    return series.map((s) =>
      `<span class="item"><span class="swatch" style="background:${cssVar(s.varName)}"></span>${esc(s.label)}</span>`
    ).join("");
  },

  // matrix folds the sparse (device, day) rows into one dense row per day
  // with one slot per series, so days with no traffic still occupy an x slot.
  matrix(daily, series, n) {
    const slotOf = new Map();
    series.forEach((s, i) => { if (!s.other) slotOf.set(s.key, i); });
    const otherSlot = series.findIndex((s) => s.other);
    const byBucket = new Map();
    for (const key of this.dayKeys(n)) {
      byBucket.set(key, { bucket: key, values: series.map(() => 0), events: 0, total: 0 });
    }
    for (const r of daily) {
      const row = byBucket.get(r.bucket);
      if (!row) continue;
      const slot = slotOf.has(r.device) ? slotOf.get(r.device) : otherSlot;
      if (slot < 0) continue;
      row.values[slot] += r.total_tokens;
      row.events += r.events;
      row.total += r.total_tokens;
    }
    return [...byBucket.values()];
  },

  dayKeys(n) {
    const out = [];
    const end = new Date();
    for (let i = n - 1; i >= 0; i--) {
      const d = new Date(end.getFullYear(), end.getMonth(), end.getDate() - i);
      out.push(d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0"));
    }
    return out;
  },

  tiles(summary, days) {
    const top = summary[0];
    const total = summary.reduce((a, r) => a + r.total_tokens, 0);
    const share = top && total > 0 ? Math.round((100 * top.total_tokens) / total) + "% 占比" : "—";
    return `
      <div class="stat-tile">
        <div class="label">设备数</div>
        <div class="value">${summary.length}</div>
        <div class="sub">近 ${days} 天有上报</div>
      </div>
      <div class="stat-tile">
        <div class="label">最活跃设备</div>
        <div class="value">${top ? esc(this.label(top.device)) : "—"}</div>
        <div class="sub">${top ? `${compact(top.total_tokens)} tokens · ${share}` : "暂无数据"}</div>
      </div>`;
  },

  chart(rows, series) {
    if (!rows.some((r) => r.total > 0)) {
      return `<p class="bars"><span class="empty">暂无数据,等待采集…</span></p>`;
    }
    const W = 1060, H = 240, padL = 46, padR = 8, padT = 10, padB = 22;
    const plotW = W - padL - padR, plotH = H - padT - padB;
    const yMax = this.niceCeil(Math.max(...rows.map((r) => r.total), 1));
    const slot = plotW / rows.length;
    const barW = Math.min(24, slot * 0.7);
    const y = (v) => padT + plotH * (1 - v / yMax);
    const colors = series.map((s) => cssVar(s.varName));

    let svg = `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="每日 token 用量按设备堆叠柱状图">`;
    for (let i = 1; i <= 4; i++) {
      const gy = padT + (plotH * i) / 4;
      const val = yMax * (1 - i / 4);
      svg += `<line x1="${padL}" y1="${gy}" x2="${W - padR}" y2="${gy}" stroke="${cssVar("--grid")}" stroke-width="1"/>`;
      if (val > 0) svg += `<text x="${padL - 6}" y="${gy + 4}" text-anchor="end">${compact(val)}</text>`;
    }
    svg += `<text x="${padL - 6}" y="${padT + 4}" text-anchor="end">${compact(yMax)}</text>`;
    svg += `<line x1="${padL}" y1="${padT + plotH}" x2="${W - padR}" y2="${padT + plotH}" stroke="${cssVar("--baseline")}" stroke-width="1"/>`;

    const labelEvery = Math.ceil(rows.length / 8);
    rows.forEach((row, i) => {
      const cx = padL + slot * i + slot / 2;
      const x = cx - barW / 2;
      // Only non-empty slots become segments, so the 2px gap always lands
      // between two visible bands instead of doubling up on a skipped one.
      const parts = row.values.map((v, si) => ({ v, si })).filter((p) => p.v > 0);
      let cum = 0;
      parts.forEach((p, k) => {
        const y0 = y(cum), y1 = y(cum + p.v);
        cum += p.v;
        const isTop = k === parts.length - 1;
        const gapTop = isTop ? 0 : 2; // surface shows through between segments
        const h = Math.max(y0 - y1 - gapTop, 0.5);
        const top = y1 + gapTop;
        svg += isTop
          ? `<path d="${this.roundedTop(x, top, barW, h, Math.min(4, h / 2))}" fill="${colors[p.si]}"/>`
          : `<rect x="${x}" y="${top}" width="${barW}" height="${h}" fill="${colors[p.si]}"/>`;
      });
      if (i % labelEvery === 0) {
        svg += `<text x="${cx}" y="${H - 6}" text-anchor="middle">${row.bucket.slice(5)}</text>`;
      }
      svg += `<rect class="hit" data-i="${i}" x="${padL + slot * i}" y="${padT}" width="${slot}" height="${plotH}" fill="transparent"/>`;
    });
    return svg + `</svg>`;
  },

  roundedTop(x, y, w, h, r) {
    return `M${x},${y + h} L${x},${y + r} Q${x},${y} ${x + r},${y} L${x + w - r},${y} Q${x + w},${y} ${x + w},${y + r} L${x + w},${y + h} Z`;
  },

  niceCeil(v) {
    const mag = Math.pow(10, Math.floor(Math.log10(v)));
    for (const m of [1, 2, 2.5, 4, 5, 8, 10]) {
      if (m * mag >= v) return m * mag;
    }
    return 10 * mag;
  },

  attachTooltip(el, rows, series) {
    const tip = document.getElementById("tooltip");
    if (!el || !tip) return;
    el.querySelectorAll(".hit").forEach((hit) => {
      hit.addEventListener("mousemove", (ev) => {
        const row = rows[+hit.dataset.i];
        const lines = series
          .map((s, i) => ({ s, v: row.values[i] }))
          .filter((e) => e.v > 0)
          .map((e) => `<div class="t-row"><span class="k"><span class="swatch" style="background:${cssVar(e.s.varName)}"></span>${esc(e.s.label)}</span><span class="v">${full(e.v)}</span></div>`)
          .join("");
        tip.innerHTML = `<div class="t-date">${row.bucket}</div>` +
          (lines || `<div class="t-row"><span class="k">无用量</span></div>`) +
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

  // Cost is absent (not zero) when nothing the device ran had a price —
  // "无定价" and "$0" are different facts (ADR-0005).
  table(summary) {
    if (!summary.length) return `<span class="empty">暂无数据</span>`;
    return `<table><thead><tr>
        <th>设备</th><th>今日</th><th>区间 tokens</th><th>成本</th>
        <th>最后活动</th><th>项目</th><th>主力模型</th>
      </tr></thead><tbody>` +
      summary.map((r) => {
        let cost = "无定价";
        if (r.cost_usd != null) {
          cost = r.cost_partial
            ? `<span title="部分模型无定价,为下限">≥ ${usd(r.cost_usd)}</span>`
            : usd(r.cost_usd);
        }
        const model = r.top_model
          ? `${esc(r.top_model)} <span class="dev-share">${compact(r.top_model_tokens)}</span>`
          : "—";
        return `<tr>
          <td title="${esc(this.label(r.device))}">${esc(this.label(r.device))}</td>
          <td>${compact(r.today_tokens || 0)}</td>
          <td>${full(r.total_tokens)}</td>
          <td>${cost}</td>
          <td>${r.last_ts ? relTime(r.last_ts) : "—"}</td>
          <td>${full(r.repos)}</td>
          <td>${model}</td>
        </tr>`;
      }).join("") +
      `</tbody></table>`;
  },
};
