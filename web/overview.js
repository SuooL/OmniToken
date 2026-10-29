"use strict";

function overviewIsEmpty(payload) {
  return !payload || !payload.telemetry ||
    !(payload.telemetry.today || {}).total_tokens;
}

const Overview = {
  lastData: null,
  _loadGeneration: 0,
  _rendered: false,

  invalidate() {
    this._loadGeneration += 1;
    TelemetryCache.invalidate("today");
  },

  async load() {
    const loadID = ++this._loadGeneration;
    const root = document.getElementById("view-overview");
    if (!this.lastData) renderState(root, { kind: "loading", title: "正在加载遥测总览" });
    try {
      const [summary, liveResult, telemetryResult] = await Promise.all([
        Api.get("/api/v1/overview?days=30"),
        LiveSnapshotCache.load().then((data) => ({data, error: null})).catch((error) => ({
          data: error && (error.status === 401 || error.status === 403) ? null : LiveSnapshotCache.data,
          error,
        })),
        TelemetryCache.load("today", { force: true }),
      ]);
      if (!isCurrentGeneration(this._loadGeneration, loadID)) return;
      const payload = { summary, live: liveResult.data, liveStale: Boolean(liveResult.error), telemetry: telemetryResult.data, telemetryResult };
      this.render(payload);
      this.lastData = payload;
      renderState(root, telemetryResult.stale || payload.liveStale
        ? { kind: "stale", title: "部分数据可能已过期", detail: "今日用量与实时配额独立更新；缺失或过期的读数已单独标明。" }
        : overviewIsEmpty(payload)
        ? { kind: "empty", title: "今天还没有遥测数据", detail: "开始一次模型会话后，这里会展示速度与构成。" }
        : { kind: "ready", title: "" });
      document.getElementById("refresh-note").textContent =
        telemetryResult.stale ? `遥测数据已保留 · ${relTime(telemetryResult.receivedAt)}` : payload.liveStale ? "今日用量已更新 · 配额更新失败" : "刚刚更新";
    } catch (error) {
      if (!isCurrentGeneration(this._loadGeneration, loadID)) return;
      const issue = classifyAPIError(error);
      if (issue.kind === "unauthorized") {
        root.replaceChildren();
        this.lastData = null;
      }
      renderState(root, {
        kind: this.lastData ? "stale" : issue.kind,
        title: this.lastData ? "遥测总览可能已过期" : issue.title,
        detail: this.lastData ? "保留上次成功图表；刷新成功前不将缺失数据显示为零。" : issue.detail,
        action: { label: "重试", run: () => this.load() },
      });
      document.getElementById("refresh-note").textContent = this.lastData ? "显示上次成功数据" : "更新失败";
    }
  },

  buildShell(root) {
    if (root.querySelector("[data-overview-shell]")) return;
    root.innerHTML = `<div data-overview-shell>
      <section class="tool-section" aria-label="今日用量">
        <div class="card-head"><h2>今日累计</h2><span id="overview-today-period" class="subtle"></span></div>
        <div id="overview-today-total" class="metric-value" data-role="today-total">—</div>
        <div id="overview-source-composition" data-role="today-source-composition"></div>
        <div class="data-table-shell"><table><thead><tr><th>来源</th><th>今日 tokens</th><th>昨日同时段</th><th>变化</th></tr></thead><tbody id="overview-source-rows"></tbody></table></div>
      </section>
      <section class="tool-section"><h2>官方配额</h2><div id="overview-quotas"></div></section>
      <section class="tool-section" aria-labelledby="overview-trend-title"><div class="card-head"><h2 id="overview-trend-title">历史用量趋势</h2><span class="subtle">近 30 天 · 每日 tokens</span></div><div id="overview-token-trend" class="chart"></div></section>
      <section class="tool-section" data-role="fleet-coverage"><a href="#devices">设备与采集状态</a><span id="overview-fleet" class="subtle"></span></section>
      ${ToolUI.analysis("overview-usage-analysis", "用量分析 · 通道、模型与历史活动", `
        <div id="overview-channels"></div>
        <section class="chart-card"><h2 id="today-model-title">今日模型构成</h2><div id="overview-model-composition" class="chart model-composition" data-chart="today-model-composition"></div></section>
        <section class="chart-card" aria-labelledby="overview-heatmap-title"><h2 id="overview-heatmap-title">每日用量热力图 · 最近 365 天</h2><div id="overview-heatmap" class="chart heatmap-shell"></div></section>
        <section class="instrument-card" data-role="device-throughput"><h2>设备用量 · 近 30 天</h2><div id="overview-devices" class="bars"></div></section>`)}
      ${ToolUI.analysis("overview-speed-analysis", "速度诊断 · 当日测量与近 10 分钟贡献", `
        <section class="chart-card" aria-labelledby="overview-speed-title"><div class="card-head"><h2 id="overview-speed-title">当日来源速度贡献</h2><strong id="overview-current-speed"></strong><span class="coverage-note" data-role="measured-coverage"></span></div><div id="overview-source-lanes" class="chart source-lanes" data-chart="source-lanes"></div><div id="overview-unmeasured"></div></section>
        <section class="instrument-card" data-role="current-contributors"><h2>近 10m 贡献者</h2><div id="overview-contributors" class="data-table-shell"></div></section>
        <section class="instrument-card" data-role="model-throughput"><h2>近 10m 模型吞吐</h2><div id="overview-model-throughput" class="bars"></div></section>`)}
    </div>`;
    root.querySelector("#overview-source-rows").addEventListener("click", (event) => {
      const clicked = event.target.closest("[data-source-detail]");
      if (!clicked) return;
      const trigger = clicked.matches("button") ? clicked : clicked.querySelector("button[data-source-detail]");
      if (!trigger) return;
      const row = (this.sourceRows || []).find((item) => item.source === trigger.dataset.sourceDetail);
      if (!row) return;
      DetailDrawer.open({key: `overview-source:${row.source}`, title: SourceUI.label(row.source),
        subtitle: "今日模型用量 · 按事件来源归属", html: this.sourceDetail(row), trigger});
    });
    Heatmap.load(document.getElementById("overview-heatmap"), 365);
  },

  render(payload) {
    const root = document.getElementById("view-overview");
    this.buildShell(root);
    const telemetry = payload.telemetry || {};
    const today = telemetry.today || {};
    const summary = payload.summary || {};
    const live = payload.live;
    const liveSpeed = live && live.speed || {};
    this.sourceRows = telemetrySourceRows(telemetry);
    document.getElementById("overview-today-total").textContent = today.total_tokens == null ? "—" : compact(today.total_tokens);
    document.getElementById("overview-today-period").textContent = `零点至今 · ${telemetry.timezone || "服务端时区"} · ${(today.models || []).length} 个模型`;
    ToolUI.updateHTML(document.getElementById("overview-source-composition"), this.sourceComposition(this.sourceRows, today.total_tokens));
    ToolUI.updateHTML(document.getElementById("overview-source-rows"), this.sourceRows.map((row) => `<tr data-source-detail="${esc(row.source)}" data-source="${esc(row.source)}">
      <td><button type="button" class="tool-row-action" data-focus-key="overview-source:${esc(row.source)}" data-source-detail="${esc(row.source)}" aria-label="查看 ${esc(SourceUI.label(row.source))} 今日模型">${SourceUI.brand(row.source)}</button></td>
      <td>${compact(row.tokens ?? 0)}</td><td>${compact(row.previous_tokens ?? 0)}</td><td>${row.change_percent == null ? "—" : `${row.change_percent > 0 ? "+" : ""}${row.change_percent.toFixed(1)}%`}</td></tr>`).join(""));
    this.sourceRows.forEach((row) => DetailDrawer.update(`overview-source:${row.source}`, this.sourceDetail(row)));
    ToolUI.updateHTML(document.getElementById("overview-quotas"), live ? `${payload.liveStale ? '<p class="subtle">配额更新失败 · 显示上次成功读数</p>' : ""}${QuotaUI.render(live.windows || [], live.quotas || [])}` : `<p class="subtle">实时配额不可用</p>`);
    const devices = live && live.devices || [];
    const backlog = devices.reduce((sum, row) => sum + (row.queued_batches || 0), 0);
    document.getElementById("overview-fleet").textContent = !live ? " · 实时状态不可用" : ` · ${payload.liveStale ? "上次成功状态 · " : ""}${devices.length} 台设备 · ${backlog ? `${full(backlog)} 个待上报批次` : "无上报积压"}`;
    this.renderTrend(summary.daily || []);
    ToolUI.updateHTML(document.getElementById("overview-channels"), this.channelSection(summary.by_channel || []));
    this.renderSpeed(telemetry, liveSpeed);
    this.renderModels(today.models || []);
    this.renderContributors(liveSpeed.sessions || []);
    this.renderBars("overview-devices", summary.by_device || [], (row) => row.display_name || row.key || row.device, "total_tokens");
    this.renderBars("overview-model-throughput", liveSpeed.models || [], speedSourceKey, "contribution_tps");
  },

  sourceDetail(row) {
    const models = row.models || [];
    return `<p>${SourceUI.brand(row.source)} · 今日 ${full(row.tokens ?? 0)} tokens</p>` +
      (models.length ? `<div class="data-table-shell"><table><thead><tr><th>模型</th><th>tokens</th><th>来源内占比</th></tr></thead><tbody>${models.map((model) => `<tr><td>${esc(model.model)}</td><td>${full(model.tokens)}</td><td>${row.tokens > 0 ? (100 * model.tokens / row.tokens).toFixed(1) + "%" : "—"}</td></tr>`).join("")}</tbody></table></div>` : `<p class="empty">今天暂无模型用量。</p>`);
  },

  renderTrend(rows) {
    const el = document.getElementById("overview-token-trend");
    if (!rows.length) {
      ChartRegistry.disposeWithin?.(el);
      el.innerHTML = `<p class="empty">此范围暂无历史用量。</p>`;
      ChartRegistry.renderFallback?.(el, {caption: "历史每日用量 · 暂无数据", columns: [], rows: []});
      return;
    }
    el.querySelectorAll?.(".empty").forEach((node) => node.remove());
    ChartRegistry.set(el, {
      titleText: "近 30 天每日 token 用量",
      grid: {left: 14, right: 20, top: 20, bottom: 24, containLabel: true},
      xAxis: {type: "category", data: rows.map((row) => row.bucket)},
      yAxis: {type: "value", min: 0, axisLabel: {formatter: compact}},
      series: [{name: "tokens", type: "bar", data: rows.map((row) => row.total_tokens), itemStyle: {color: cssVar("--source-aggregate")}}],
    }, {caption: "历史每日用量", columns: [{key: "bucket", label: "日期"}, {key: "total_tokens", label: "tokens", format: full}], rows});
  },

  metricCard(role, label, value, detail) {
    return `<article class="metric-card" data-role="${role}">
      <div class="metric-label">${label}</div>
      <div class="metric-value">${compact(value)}</div>
      <div class="subtle">${esc(detail)}</div>
    </article>`;
  },

  sourceCard(source, label, row) {
    const change = row.change_percent == null ? ""
      : `<div class="subtle">${row.change_percent >= 0 ? "↑" : "↓"} ${Math.abs(row.change_percent).toFixed(1)}% · 昨日同时段 ${compact(row.previous_tokens || 0)}</div>`;
    return `<article class="metric-card" data-source="${source}">
      <div class="metric-label">${label}</div>
      <div class="metric-value">${compact(row.tokens || 0)}</div>
      ${change}
    </article>`;
  },

  // Billing channels (F9 / ADR-0018). Three real channels plus `unknown`.
  //
  // Why this is its own section rather than a slice of the token total: only
  // the subscription channel is bound by a quota window, so a total that mixes
  // it with relay traffic makes the quota bar next to it unreadable — you
  // cannot tell which of the two numbers is wrong. Split, both are usable.
  //
  // `unknown` gets a column and a stated share, never a fold into a neighbour
  // and never a proportional split across the three. It is what the panel says
  // when the evidence is gone (the source log was rotated away) or was never
  // conclusive — and the honest size of that gap is exactly what a reader needs
  // in order to know how far to trust the other three.
  channelSection(rows) {
    const total = rows.reduce((sum, row) => sum + (row.total_tokens || 0), 0);
    const unknown = rows.find((row) => row.channel === "unknown") || {};
    const coverage = total
      ? `已分类 ${(100 * (total - (unknown.total_tokens || 0)) / total).toFixed(1)}%`
      : "暂无数据";
    return `
      <section class="chart-card" aria-labelledby="overview-channel-title">
        <div class="card-head">
          <h2 id="overview-channel-title">计费通道构成<span class="eyebrow">近 30 天 · 只有「订阅」受配额窗口约束</span>${infoTip(
            "未知通道既不并入订阅,也不按比例摊分到其它通道 —— <b>证据不足时不猜</b>(ADR-0018)。")}</h2>
          <span class="subtle">${esc(coverage)}</span>
        </div>
        ${total ? `<div class="composition-strip" aria-label="计费通道构成">` + rows.map((row) =>
          `<span title="${esc(row.label)} ${compact(row.total_tokens || 0)}" ` +
          `style="width:${Math.max(0, 100 * (row.total_tokens || 0) / total)}%;` +
          `background:var(--channel-${row.channel})"></span>`).join("") + `</div>` : ""}
        <div class="data-table-shell">
          <table>
            <thead><tr><th>通道</th><th>tokens</th><th>占比</th><th>请求</th></tr></thead>
            <tbody>${rows.map((row) => `<tr>
              <td><span class="channel-dot" style="background:var(--channel-${row.channel})"></span>${esc(row.label || row.channel)}</td>
              <td>${compact(row.total_tokens || 0)}</td>
              <td>${total ? (100 * (row.total_tokens || 0) / total).toFixed(1) : "0.0"}%</td>
              <td>${full(row.events || 0)}</td>
            </tr>`).join("")}</tbody>
          </table>
        </div>
      </section>`;
  },

  sourceComposition(rows, total) {
    if (!total) return `<div class="composition-strip" aria-label="暂无来源用量"></div>`;
    return `<div class="composition-strip" aria-label="当日来源构成">` + rows.map((row) =>
      `<span title="${esc(SourceUI.label(speedSourceKey(row)))} ${compact(row.tokens || 0)}" ` +
      `style="width:${Math.max(0, 100 * (row.tokens || 0) / total)}%;background:${ChartRegistry.sourceGradientCSS(speedSourceKey(row), "90deg")}"></span>`
    ).join("") + `</div>`;
  },

  renderSpeed(snapshot, liveSpeed = {}) {
    const speed = telemetrySpeed(snapshot);
    const buckets = speed.series || [];
    const sourceKeys = [];
    buckets.forEach((bucket) => (bucket.sources || []).forEach((row) => {
      const key = speedSourceKey(row);
      if (!sourceKeys.includes(key)) sourceKeys.push(key);
    }));
    (speed.measured_sources || []).forEach((key) => {
      if (!sourceKeys.includes(key)) sourceKeys.push(key);
    });
    const liveSources = liveSpeed.sources || [];
    const visibleSum = liveSources.reduce((sum, row) => sum + (row.contribution_tps || 0), 0);
    const aggregate = currentAggregateTPS(liveSpeed);
    document.getElementById("overview-current-speed").textContent =
      aggregate == null ? "近 10m — tok/s" : `近 10m ${aggregate.toFixed(1)} tok/s`;
    document.querySelector("#view-overview [data-role='measured-coverage']").textContent =
      `${telemetryCoverageLabel(speed)} · ` +
      `可见贡献合计 ${visibleSum.toFixed(1)} tok/s`;
    document.getElementById("overview-unmeasured").innerHTML =
      (speed.unmeasured_sources || []).map((source) =>
        `<div class="unavailable-lane">${esc(SourceUI.label(source))} 速度 unavailable · 用量仍计入今日总数</div>`
      ).join("");

    const el = document.getElementById("overview-source-lanes");
    if (!buckets.length || !sourceKeys.length) {
      ChartRegistry.disposeWithin?.(el);
      el.innerHTML = `<p class="empty">成功查询，但此范围暂无可测生成区间。</p>`;
      ChartRegistry.renderFallback?.(el, {caption: "来源速度逐桶数据 · 暂无可测区间", columns: [], rows: []});
      return;
    }
    el.querySelectorAll?.(".empty").forEach((node) => node.remove());
    const times = buckets.map((bucket) => new Date(bucket.start_ms).toLocaleTimeString("zh-CN", {hour: "2-digit", minute: "2-digit", timeZone: snapshot.timezone && snapshot.timezone !== "Local" ? snapshot.timezone : undefined}));
    const grids = sourceKeys.map((_, index) => ({
      left: 58, right: 18, top: 16 + index * (220 / sourceKeys.length),
      height: Math.max(38, 170 / sourceKeys.length),
    }));
    ChartRegistry.set(el, {
      titleText: "当日来源速度贡献",
      grid: grids,
      xAxis: sourceKeys.map((_, index) => ({
        type: "category", gridIndex: index, data: times,
        axisLine: { lineStyle: { color: cssVar("--baseline") } },
        axisTick: { show: false }, axisLabel: { show: index === sourceKeys.length - 1, color: cssVar("--text-muted") },
      })),
      yAxis: sourceKeys.map((key, index) => ({
        type: "value", gridIndex: index, name: SourceUI.label(key),
        nameTextStyle: { color: ChartRegistry.sourceColor(key), align: "right" },
        min: 0, splitNumber: 2, splitLine: { lineStyle: { color: cssVar("--grid") } },
        axisLabel: { color: cssVar("--text-muted"), formatter: (v) => compact(v) },
      })),
      series: sourceKeys.map((key, index) => ({
        name: SourceUI.label(key), type: "bar", xAxisIndex: index, yAxisIndex: index,
        itemStyle: { color: ChartRegistry.sourceGradient(key), borderRadius: [3, 3, 0, 0] },
        data: buckets.map((bucket) => {
          const row = (bucket.sources || []).find((candidate) => speedSourceKey(candidate) === key);
          return row ? row.contribution_tps ?? null : null;
        }),
      })),
    }, {
      caption: "来源速度逐桶数据",
      columns: [
        { key: "time", label: "时间" },
        ...sourceKeys.map((key) => ({ key, label: SourceUI.label(key), format: (v) => v == null ? "—" : Number(v).toFixed(2) })),
        { key: "aggregate", label: "合计", format: (v) => v == null ? "—" : Number(v).toFixed(2) },
      ],
      rows: buckets.map((bucket, index) => {
        const row = { time: times[index], aggregate: bucket.aggregate_tps || 0 };
        sourceKeys.forEach((key) => {
          row[key] = ((bucket.sources || []).find((candidate) => speedSourceKey(candidate) === key) || {}).contribution_tps ?? null;
        });
        return row;
      }),
    });
  },

  renderModels(models) {
    const el = document.getElementById("overview-model-composition");
    if (!models.length) {
      ChartRegistry.disposeWithin?.(el);
      el.innerHTML = `<p class="empty">今天暂无模型用量。</p>`;
      ChartRegistry.renderFallback?.(el, {caption: "今日完整模型列表 · 暂无数据", columns: [], rows: []});
      return;
    }
    el.querySelectorAll?.(".empty").forEach((node) => node.remove());
    el.style.height = `${Math.min(560, Math.max(300, models.length * 30 + 54))}px`;
    const dataZoom = models.length > 14 ? [
      { type: "inside", yAxisIndex: 0, startValue: 0, endValue: 13 },
      { type: "slider", yAxisIndex: 0, right: 2, width: 10, startValue: 0, endValue: 13 },
    ] : [];
    ChartRegistry.set(el, {
      titleText: "今日所有模型 token 构成",
      grid: { left: 12, right: models.length > 14 ? 42 : 24, top: 8, bottom: 14, containLabel: true },
      dataZoom,
      xAxis: { type: "value", splitLine: { lineStyle: { color: cssVar("--grid") } }, axisLabel: { color: cssVar("--text-muted"), formatter: compact } },
      yAxis: { type: "category", inverse: true, data: models.map((row) => row.model), axisLabel: { color: cssVar("--text-secondary"), width: 170, overflow: "truncate" } },
      series: [{
        type: "bar", data: models.map((row) => row.tokens), barMaxWidth: 18,
        itemStyle: { color: cssVar("--source-aggregate"), borderRadius: [0, 5, 5, 0] },
        label: { show: true, position: "right", color: cssVar("--text-secondary"), formatter: ({value}) => compact(value) },
      }],
    }, {
      caption: "今日完整模型列表",
      columns: [
        { key: "model", label: "模型" },
        { key: "tokens", label: "tokens", format: full },
        { key: "share", label: "占比", format: (v) => `${(100 * (v || 0)).toFixed(1)}%` },
      ],
      rows: models,
    });
  },

  // What to call a session: where it is running, and on which machine.
  //
  // Those are the two things a reader recognises. The identifier is 36
  // characters of hex that identifies nothing to a human, and printed whole it
  // took the row's whole width — pushing the contribution column, the only
  // reason this card exists, past the card's edge.
  //
  // The id comes back only when it has work to do. Two sessions in the same
  // repo on the same machine would otherwise render as two identical rows, so
  // those rows — and only those — get eight characters appended. The full id
  // stays in the title either way.
  sessionLabels(rows) {
    const base = rows.map((row) => {
      const repo = row.repo ? repoLabel(row.repo).split("/").pop() : "";
      return [repo, row.device].filter(Boolean).join(" ");
    });
    const collisions = new Set(base.filter((label, i) => base.indexOf(label) !== i));
    return base.map((label, i) => {
      const id = rows[i].session_id || rows[i].key || "";
      if (!label) return id ? id.slice(0, 8) : "—";
      return collisions.has(label) && id ? `${label} ${id.slice(0, 8)}` : label;
    });
  },

  renderContributors(rows) {
    const el = document.getElementById("overview-contributors");
    if (!rows.length) {
      el.innerHTML = `<p class="empty">近 10m 没有可测贡献者。</p>`;
      return;
    }
    const labels = this.sessionLabels(rows);
    el.innerHTML = `<table><thead><tr><th>会话</th><th>模型</th><th>贡献 tok/s</th></tr></thead><tbody>` +
      rows.map((row, i) => `<tr><td title="${esc(row.session_id || row.key || "")}">${esc(labels[i])}</td>` +
        `<td>${esc(row.model || "—")}</td><td>${Number(row.contribution_tps || 0).toFixed(1)}</td></tr>`).join("") +
      `</tbody></table>`;
  },

  renderBars(id, rows, label, valueKey) {
    const el = document.getElementById(id);
    rows = (rows || []).filter((row) => Number(row[valueKey]) > 0).slice(0, 10);
    if (!rows.length) {
      el.innerHTML = `<span class="empty">暂无数据</span>`;
      return;
    }
    const max = Math.max(...rows.map((row) => Number(row[valueKey])));
    el.innerHTML = rows.map((row) => `<div class="row"><div class="row-head">` +
      `<span class="key">${esc(typeof label === "function" ? label(row) : row[label])}</span>` +
      `<span class="val">${compact(row[valueKey])}</span></div>` +
      `<div class="track"><div class="fill" style="width:${100 * row[valueKey] / max}%"></div></div></div>`).join("");
  },
};
