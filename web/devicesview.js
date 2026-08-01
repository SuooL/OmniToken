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

function devicesIsEmpty(data) {
  return !(data.summary || []).some((row) =>
    (row.total_tokens || 0) > 0 || row.identity_status === "registered");
}

const DevicesView = {
  _timer: null,
  lastData: null,
  _loadGeneration: 0,

  enter() {
    this.load();
    this._timer = setInterval(() => this.load(), 30000);
  },

  leave() {
    clearInterval(this._timer);
    this._timer = null;
    this._loadGeneration += 1;
  },

  async load() {
    const loadID = ++this._loadGeneration;
    const root = document.getElementById("view-devices");
    if (!this.lastData) {
      renderState(root, { kind: "loading", title: "正在加载设备数据" });
    }
    try {
      const [data, live] = await Promise.all([
        Api.get("/api/v1/devices?days=30"),
        Api.get("/api/v1/live").catch(() => ({ speed: {} })),
      ]);
      data.live = live;
      if (!isCurrentGeneration(this._loadGeneration, loadID)) return;
      this.render(data);
      this.lastData = data;
      if (devicesIsEmpty(data)) {
        renderState(root, {
          kind: "empty", title: "暂无设备用量",
          detail: "近 30 天尚无可比较的设备 token 数据。",
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
    const summary = d.summary || [];
    const usageSummary = summary.filter((r) => r.total_tokens > 0);
    const series = this.series(usageSummary);
    const matrix = this.matrix(d.daily || [], series, days);
    // Built once: an ECharts instance cannot survive its container being
    // rewritten on every poll.
    if (!root.dataset.built) {
      root.innerHTML = `
        <section class="stat-row" id="devices-tiles"></section>
        <section class="chart-card">
          <div class="card-head"><h2>当前设备吞吐贡献</h2><span class="subtle">共享分母</span></div>
          <div id="devices-throughput-chart" style="height:260px" data-chart="device-throughput-trend"></div>
        </section>
        <section class="chart-card">
          <div class="card-head"><h2 id="devices-chart-title">每日用量 · 按设备堆叠</h2></div>
          <div id="devices-chart" style="height:300px"></div>
        </section>
        <section class="instrument-card">
          <h2 id="devices-table-title">设备明细</h2>
          <div class="data-table data-table-shell" id="devices-table"></div>
        </section>`;
      root.dataset.built = "1";
    }
    document.getElementById("devices-chart-title").textContent =
      `每日用量 · 按设备堆叠 · 近 ${days} 天`;
    document.getElementById("devices-table-title").textContent = `设备明细 · 近 ${days} 天`;
    document.getElementById("devices-tiles").innerHTML = this.tiles(summary, days);
    document.getElementById("devices-table").innerHTML = this.table(summary);
    // The throughput rows come from the live payload, which is keyed by
    // identity alone; the summary beside them is the only place the resolved
    // name arrives, so it supplies the axis labels.
    this.throughputChart(((((d.live || {}).speed) || {}).devices) || [], summary);
    this.chart(matrix, series);
  },

  throughputChart(rows, summary = []) {
    const el = document.getElementById("devices-throughput-chart");
    const names = new Map(summary
      .filter((r) => r.display_name)
      .map((r) => [r.device, r.display_name]));
    rows = [...rows].filter((row) => row.contribution_tps > 0)
      .sort((a, b) => b.contribution_tps - a.contribution_tps);
    if (!rows.length) {
      el.innerHTML = `<p class="empty">当前没有可测设备吞吐。</p>`;
      return;
    }
    ChartRegistry.set(el, {
      titleText: "当前设备吞吐贡献",
      grid: {left: 12, right: 48, top: 8, bottom: 8, containLabel: true},
      xAxis: {type: "value", min: 0, splitLine: {lineStyle: {color: cssVar("--grid")}}, axisLabel: {color: cssVar("--text-muted")}},
      yAxis: {type: "category", inverse: true, data: rows.map((row) => {
        const identity = row.key || row.device;
        return names.get(identity) || identity;
      }), axisLabel: {color: cssVar("--text-secondary")}},
      series: [{
        type: "bar", barMaxWidth: 18, data: rows.map((row) => row.contribution_tps),
        itemStyle: {color: cssVar("--status-healthy"), borderRadius: [0, 4, 4, 0]},
        label: {show: true, position: "right", color: cssVar("--text-secondary"), formatter: ({value}) => `${Number(value).toFixed(1)} tok/s`},
      }],
    });
  },

  label(device) {
    if (device && typeof device === "object") {
      return device.display_name || device.device || "(未知设备)";
    }
    return device || "(未知设备)";
  },

  // series maps the summary (already sorted by tokens desc) onto colour slots.
  series(summary) {
    const out = summary.slice(0, DEVICE_SERIES_MAX).map((r, i) => ({
      key: r.device,
      label: this.label(r),
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
        <div class="value">${top ? esc(this.label(top)) : "—"}</div>
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
      aria: { enabled: true },
      animation: !matchMedia("(prefers-reduced-motion: reduce)").matches,
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
        <th>设备</th><th>连接</th><th>待发送</th><th>今日</th><th>区间 tokens</th><th>成本</th>
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
        const connection = r.identity_status === "registered"
          ? this.connectionLabel(r.connection_state)
          : "旧版设备";
        const lastSeen = r.identity_status === "registered" ? r.last_seen_at : r.last_ts;
        const backlog = r.identity_status === "registered"
          ? (r.queued_batches
            ? `${full(r.queued_batches)} 批 · ${compact(r.queued_bytes || 0)}B`
            : "已清空")
          : "—";
        return `<tr>
          <td title="${esc(r.device || "")}">
            ${esc(this.label(r))}
            ${r.display_name && r.device_id ? `<span class="dev-share">${esc(r.device_id.slice(0, 8))}</span>` : ""}
          </td>
          <td><span class="dot ${r.connection_state || "stale"}"></span>${connection}</td>
          <td>${backlog}</td>
          <td>${compact(r.today_tokens || 0)}</td>
          <td>${full(r.total_tokens)}</td>
          <td>${cost}</td>
          <td>${lastSeen ? relTime(lastSeen) : "从未连接"}</td>
          <td>${full(r.repos)}</td>
          <td>${model}</td>
        </tr>`;
      }).join("") +
      `</tbody></table>`;
  },

  connectionLabel(state) {
    return { online: "在线", stale: "延迟", offline: "离线", unknown: "未知" }[state] || "未知";
  },
};
