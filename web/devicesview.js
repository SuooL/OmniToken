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
      const results = await Promise.allSettled([
        Api.get("/api/v1/devices?days=30"),
        LiveSnapshotCache.load().catch((error) => {
          if (error && (error.status === 401 || error.status === 403)) throw error;
          return { speed: {}, unavailable: true };
        }),
      ]);
      const failure = results.find((result) => result.status === "rejected" &&
        (result.reason?.status === 401 || result.reason?.status === 403)) ||
        results.find((result) => result.status === "rejected");
      if (failure) throw failure.reason;
      const [data, live] = results.map((result) => result.value);
      const previousLive = this.lastData && this.lastData.live && !this.lastData.live.unavailable
        ? this.lastData.live : null;
      data.live = live.unavailable ? previousLive || live : live;
      data.liveState = {
        stale: Boolean(live.unavailable), unavailable: Boolean(live.unavailable && !previousLive),
        updatedAt: live.unavailable ? (this.lastData && this.lastData.liveState || {}).updatedAt || null : Date.now(),
      };
      if (!isCurrentGeneration(this._loadGeneration, loadID)) return;
      this.render(data);
      this.lastData = data;
      if (data.liveState.stale) {
        renderState(root, {kind: "stale", title: "设备用量已更新；实时数据更新失败",
          detail: data.liveState.unavailable ? "实时吞吐与会话观测不可用。" : "实时吞吐与会话观测保留上次快照。"});
      } else if (devicesIsEmpty(data)) {
        renderState(root, {
          kind: "empty", title: "暂无设备用量",
          detail: "近 30 天尚无可比较的设备 token 数据。",
        });
      } else {
        renderState(root, { kind: "ready", title: "" });
      }
      document.getElementById("refresh-note").textContent =
        "设备用量更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false }) +
        (data.liveState.stale ? " · 实时数据更新失败" : "");
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
        title: this.lastData ? "设备数据可能已过期" : issue.title,
        detail: issue.detail,
        action: { label: "重试", run: () => this.load() },
      });
      document.getElementById("refresh-note").textContent = this.lastData
        ? "刷新失败,正在显示上次数据" : issue.title;
    }
  },

  liveStatus(d) {
    const state = d.liveState || {};
    if (state.unavailable) return "实时数据更新失败 · 当前不可用";
    const timestamp = state.updatedAt ? new Date(state.updatedAt).toLocaleTimeString("zh-CN", {hour12: false}) : "时间未知";
    return (state.stale ? "实时数据已过期 · 上次成功于 " : "实时快照更新于 ") + timestamp;
  },

  render(d) {
    this._displayData = d;
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
        <section class="instrument-card">
          <h2 id="devices-table-title">设备明细</h2>
          <p class="subtle">连接状态来自设备心跳；最近 token 活动与进程观测分别显示。点击设备查看详情。</p>
          <div class="data-table data-table-shell" id="devices-table"></div>
        </section>
        ${ToolUI.analysis("devices-throughput-analysis", "当前设备吞吐贡献 · 近 10 分钟", `
          <p class="subtle" id="devices-live-status"></p><div id="devices-throughput-chart" style="height:260px" data-chart="device-throughput-trend"></div>`)}
        ${ToolUI.analysis("devices-daily-analysis", "每日用量比较 · 近 30 天", `
          <h2 id="devices-chart-title">每日用量 · 按设备堆叠</h2><div id="devices-chart" style="height:300px"></div>`)}
      `;
      const table = document.getElementById("devices-table");
      table.addEventListener("click", (event) => {
        const row = event.target.closest("[data-device-key]");
        if (row) this.openDevice(row.dataset.deviceKey, row);
      });
      table.addEventListener("keydown", (event) => {
        const row = event.target.closest("[data-device-key]");
        if (row && (event.key === "Enter" || event.key === " ")) {
          event.preventDefault(); this.openDevice(row.dataset.deviceKey, row);
        }
      });
      root.dataset.built = "1";
    }
    document.getElementById("devices-chart-title").textContent =
      `每日用量 · 按设备堆叠 · 近 ${days} 天`;
    document.getElementById("devices-table-title").textContent = `设备明细 · 近 ${days} 天`;
    document.getElementById("devices-tiles").innerHTML = this.tiles(summary, days);
    ToolUI.updateHTML(document.getElementById("devices-table"), this.table(summary));
    if (this._selectedDevice) {
      const selected = summary.find((row) => row.device === this._selectedDevice);
      if (selected) DetailDrawer.update("device:" + selected.device, this.deviceDetail(selected, d));
    }
    document.getElementById("devices-live-status").textContent = this.liveStatus(d);
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
      ChartRegistry.disposeWithin?.(el);
      el.innerHTML = `<p class="empty">当前没有可测设备吞吐。</p>`;
      return;
    }
    if (el.querySelector?.(".empty")) el.replaceChildren();
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
    const registered = summary.filter((row) => row.identity_status === "registered");
    const online = registered.filter((row) => row.connection_state === "online").length;
    const stale = registered.filter((row) => row.connection_state !== "online").length;
    const backlog = registered.reduce((sum, row) => sum + (row.queued_batches || 0), 0);
    return `<div class="stat-tile"><div class="label">在线设备</div><div class="value">${online}</div><div class="sub">已注册 ${registered.length} · 全部 ${summary.length}</div></div>
      <div class="stat-tile"><div class="label">未及时上报</div><div class="value">${stale}</div><div class="sub">心跳延迟、离线或未知</div></div>
      <div class="stat-tile"><div class="label">待上报批次</div><div class="value">${full(backlog)}</div><div class="sub">设备最近一次心跳报告 · 用量近 ${days} 天</div></div>`;
  },

  openDevice(key, trigger) {
    const d = this._displayData;
    const row = (d.summary || []).find((candidate) => candidate.device === key);
    if (!row) return;
    this._selectedDevice = key;
    DetailDrawer.open({key: "device:" + key, title: this.label(row),
      subtitle: `近 ${d.days || 30} 天 · ${key}`, html: this.deviceDetail(row, d), trigger});
  },

  deviceDetail(row, d) {
    const live = d.live || {};
    const status = (live.devices || []).find((device) => device.device === row.device);
    const daily = (d.daily || []).filter((day) => day.device === row.device);
    const sessions = (live.sessions || []).filter((session) => session.device === row.device);
    const heartbeat = row.identity_status === "registered"
      ? (row.last_seen_at ? relTime(row.last_seen_at) : "从未连接") : "不适用";
    return `<p class="subtle">${this.liveStatus(d)}</p><dl class="detail-fields">
      <dt>最近心跳</dt><dd>${heartbeat}</dd>
      <dt>最近 token 活动</dt><dd>${row.last_ts ? relTime(row.last_ts) : "暂无"}</dd>
      <dt>待上报批次</dt><dd>${row.identity_status === "registered" ? full(row.queued_batches || 0) : "不可观测"}</dd>
      <dt>采集 / 上传时间</dt><dd>当前接口未提供独立时间</dd>
      <dt>进程观测</dt><dd>${status && status.has_procs ? `${full(status.running)} 个打开的会话` : "会话状态不可观测"}</dd>
      <dt>最近 10 分钟有用量的会话</dt><dd>${live.sessions ? full(sessions.length) : "不可观测"}</dd>
      <dt>区间 tokens</dt><dd>${full(row.total_tokens || 0)}</dd>
      <dt>主力模型</dt><dd>${esc(row.top_model || "暂无")} · ${compact(row.top_model_tokens || 0)} tokens</dd>
      </dl><p class="subtle">当前设备接口提供主力模型；未提供完整来源与模型拆分。</p>
      <h3>每日用量 · 近 ${d.days || 30} 天</h3>
      <div class="data-table"><table><thead><tr><th>日期</th><th>tokens</th></tr></thead><tbody>${daily.map((day) => `<tr><td>${esc(day.bucket)}</td><td>${full(day.total_tokens)}</td></tr>`).join("")}</tbody></table>${daily.length ? "" : '<p class="empty">暂无每日用量</p>'}</div>`;
  },

  chart(rows, series) {
    const el = document.getElementById("devices-chart");
    if (!rows.some((r) => r.total > 0)) {
      ChartRegistry.disposeWithin?.(el);
      el.innerHTML = `<p class="bars"><span class="empty">暂无数据,等待采集…</span></p>`;
      return;
    }
    if (el.querySelector?.(".empty")) el.replaceChildren();
    ChartRegistry.get(el).setOption({
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
        <th>设备</th><th>连接</th><th>最近心跳</th><th>待上报</th><th>今日</th><th>区间 tokens</th><th>成本</th>
        <th>最近 token 活动</th><th>会话观测</th><th>项目</th><th>主力模型</th>
      </tr></thead><tbody>` +
      summary.map((r) => {
        let cost = "无定价";
        if (r.cost_usd != null) {
          cost = r.cost_partial
            ? `<span title="部分模型无定价,为下限">≥ ${usd(r.cost_usd)}</span>`
            : usd(r.cost_usd);
        }
        const status = (((this._displayData || {}).live || {}).devices || []).find((device) => device.device === r.device);
        const sessionState = status && status.has_procs ? `${full(status.running)} 个打开` : "不可观测";
        const model = r.top_model
          ? `${esc(r.top_model)} <span class="dev-share">${compact(r.top_model_tokens)}</span>`
          : "—";
        // Three kinds of row, not two. The hub's own machine has no agent to
        // be online or offline and nothing to enrol, so calling it a legacy
        // device pointed at an upgrade that does not exist.
        const connection = r.identity_status === "registered"
          ? this.connectionLabel(r.connection_state)
          : r.identity_status === "local" ? "本机" : "旧版设备";

        const backlog = r.identity_status === "registered"
          ? (r.queued_batches
            ? `${full(r.queued_batches)} 批 · ${compact(r.queued_bytes || 0)}B`
            : "已清空")
          : "—";
        return `<tr class="clickable-row" tabindex="0" role="button" aria-label="查看设备 ${esc(this.label(r))}" data-device-key="${esc(r.device)}">
          <td title="${esc(r.device || "")}">
            ${esc(this.label(r))}
            ${r.display_name && r.device_id ? `<span class="dev-share">${esc(r.device_id.slice(0, 8))}</span>` : ""}
          </td>
          <td><span class="dot ${r.connection_state || "stale"}"></span>${connection}</td>
          <td>${r.identity_status === "registered" ? (r.last_seen_at ? relTime(r.last_seen_at) : "从未连接") : "—"}</td>
          <td>${backlog}</td>
          <td>${compact(r.today_tokens || 0)}</td>
          <td>${full(r.total_tokens)}</td>
          <td>${cost}</td>
          <td>${r.last_ts ? relTime(r.last_ts) : "暂无"}</td>
          <td>${sessionState}</td>
          <td>${full(r.repos)}</td>
          <td>${model}</td>
        </tr>`;
      }).join("") +
      `</tbody></table>`;
  },

  connectionLabel(state) {
    return { online: "在线", stale: "未及时上报", offline: "离线", unknown: "未知" }[state] || "未知";
  },
};
