"use strict";
// Devices view (F21 / GAP-3): cross-device comparison, per-device stacked
// daily trend, last-report time. Renders into #view-devices.
//
// At most four devices get a series colour; everything else collapses into a
// single muted "其他" band. Minting extra hues would imply distinctions the
// four-colour palette can't keep legible in both themes — the detail table
// below carries the per-device numbers instead.
//
// The chart is ECharts (ADR-0010), same as every other page. It used to be
// hand-drawn SVG with its own tooltip bolted onto invisible hit rectangles —
// a second implementation of what the library already does, and the only
// reason this page behaved differently from the overview beside it.
const DEVICE_SERIES_VARS = ["--series-1", "--series-2", "--series-3", "--series-4"];
const DEVICE_SERIES_MAX = 4;

const DevicesView = {
  _timer: null,
  lastData: null,

  enter() {
    this.load();
    this._timer = setInterval(() => this.load(), 30000);
  },

  leave() {
    clearInterval(this._timer);
    this._timer = null;
  },

  async load() {
    const root = document.getElementById("view-devices");
    if (!this.lastData) {
      renderState(root, { kind: "loading", title: "正在加载设备数据" });
    }
    try {
      this.lastData = await Api.get("/api/v1/devices?days=30");
      this.render(this.lastData);
      renderState(root, { kind: "ready", title: "" });
      document.getElementById("refresh-note").textContent =
        "更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    } catch (e) {
      const issue = classifyAPIError(e);
      renderState(root, {
        kind: this.lastData ? "stale" : issue.kind,
        title: this.lastData ? "设备数据可能已过期" : issue.title,
        detail: issue.detail,
        action: { label: "重试", run: () => this.load() },
      });
      document.getElementById("refresh-note").textContent = this.lastData
        ? "刷新失败,正在显示上次数据" : issue.title;
    }
  },

  render(d) {
    const root = document.getElementById("view-devices");
    const days = d.days || 30;
    const summary = (d.summary || []).filter((r) => r.total_tokens > 0);
    const series = this.series(summary);
    const matrix = this.matrix(d.daily || [], series, days);
    // Built once: an ECharts instance cannot survive its container being
    // rewritten on every poll.
    if (!root.dataset.built) {
      root.innerHTML = `
        <section class="stat-row" id="devices-tiles"></section>
        <section class="card">
          <div class="card-head"><h2 id="devices-chart-title">每日用量 · 按设备堆叠</h2></div>
          <div id="devices-chart" style="height:300px"></div>
        </section>
        <section class="card">
          <h2 id="devices-table-title">设备明细</h2>
          <div class="data-table" id="devices-table"></div>
        </section>`;
      root.dataset.built = "1";
    }
    document.getElementById("devices-chart-title").textContent =
      `每日用量 · 按设备堆叠 · 近 ${days} 天`;
    document.getElementById("devices-table-title").textContent = `设备明细 · 近 ${days} 天`;
    document.getElementById("devices-tiles").innerHTML = this.tiles(summary, days);
    document.getElementById("devices-table").innerHTML = this.table(summary);
    this.chart(matrix, series);
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
    const el = document.getElementById("devices-chart");
    if (!rows.some((r) => r.total > 0)) {
      el.innerHTML = `<p class="bars"><span class="empty">暂无数据,等待采集…</span></p>`;
      return;
    }
    echartsFor(el).setOption({
      grid: { left: 8, right: 8, top: 28, bottom: 4, containLabel: true },
      tooltip: {
        trigger: "axis", axisPointer: { type: "shadow" }, ...tooltipStyle(),
        valueFormatter: (v) => (v ? full(v) : "—"),
      },
      legend: {
        top: 0, left: 0, itemWidth: 9, itemHeight: 9, itemGap: 14,
        // A single device needs no legend: the card title already says what
        // the bars are, and one swatch explains nothing.
        show: series.length > 1,
        textStyle: { color: cssVar("--text-secondary"), fontSize: 11, fontFamily: chartFont() },
      },
      xAxis: {
        type: "category",
        data: rows.map((r) => r.bucket.slice(5)),
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
      series: series.map((s, i) => ({
        name: s.label,
        type: "bar",
        stack: "tokens",
        barMaxWidth: 26,
        itemStyle: {
          color: gradientOf(cssVar(s.varName)),
          borderRadius: i === series.length - 1 ? [4, 4, 0, 0] : 0,
        },
        emphasis: { focus: "series" },
        data: rows.map((r) => r.values[i] || 0),
      })),
      animationDuration: 420,
      animationEasing: "cubicOut",
    }, true);
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
