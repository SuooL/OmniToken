"use strict";

function overviewIsEmpty(payload) {
  return !payload || !payload.telemetry ||
    (!(payload.telemetry.today || {}).total_tokens &&
     !(payload.telemetry.rolling_5h || {}).total_tokens);
}

const Overview = {
  lastData: null,
  _loadGeneration: 0,
  _rendered: false,

  invalidate() {
    this._loadGeneration += 1;
    TelemetryCache.invalidate("5h");
  },

  async load() {
    const loadID = ++this._loadGeneration;
    const root = document.getElementById("view-overview");
    if (!this.lastData) renderState(root, { kind: "loading", title: "正在加载遥测总览" });
    try {
      const [summary, live, telemetryResult] = await Promise.all([
        Api.get("/api/v1/overview?days=30"),
        Api.get("/api/v1/live").catch(() => null),
        TelemetryCache.load("5h", { force: true }),
      ]);
      if (!isCurrentGeneration(this._loadGeneration, loadID)) return;
      const payload = { summary, live, telemetry: telemetryResult.data, telemetryResult };
      this.render(payload);
      this.lastData = payload;
      renderState(root, overviewIsEmpty(payload)
        ? { kind: "empty", title: "今天还没有遥测数据", detail: "开始一次模型会话后，这里会展示速度与构成。" }
        : { kind: "ready", title: "" });
      document.getElementById("refresh-note").textContent =
        telemetryResult.stale ? `遥测数据已保留 · ${relTime(telemetryResult.receivedAt)}` : "刚刚更新";
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

  render(payload) {
    const root = document.getElementById("view-overview");
    const telemetry = payload.telemetry || {};
    const today = telemetry.today || {};
    const rolling = telemetry.rolling_5h || {};
    const sources = telemetrySourceRows(telemetry);
    const sourceMap = Object.fromEntries(sources.map((row) => [speedSourceKey(row), row]));
    const claude = sourceMap["claude-code"] || {};
    const codex = sourceMap.codex || {};
    const summary = payload.summary || {};
    const live = payload.live;
    const liveSpeed = live && live.speed || {};
    const devices = live && live.devices || [];
    const backlog = devices.reduce((sum, row) => sum + (row.queued_batches || 0), 0);

    root.innerHTML = `
      <section class="telemetry-summary" aria-label="关键遥测">
        ${this.metricCard("today-total", "今日累计", today.total_tokens || 0,
          `${(today.models || []).length} 个模型`)}
        <article class="metric-card" data-role="rolling-five-hour-total">
          <div class="metric-label">滚动五小时</div>
          <div class="metric-value">${compact(rolling.total_tokens || 0)}</div>
          <div class="subtle">实际 token 用量</div>
          ${this.sourceComposition(sources, rolling.total_tokens || 0)}
        </article>
        ${this.sourceCard("claude-code", "Claude · 五小时", claude)}
        ${this.sourceCard("codex", "Codex · 五小时", codex)}
        <article class="metric-card" data-role="fleet-coverage">
          <div class="metric-label">Fleet coverage</div>
          <div class="metric-value">${live ? devices.length : "—"}</div>
          <div class="subtle">${!live ? "实时状态不可用" : backlog ? `${full(backlog)} 个待上报批次` : "无上报积压"}</div>
        </article>
      </section>

      <section class="chart-card" aria-labelledby="overview-speed-title">
        <div class="card-head">
          <div>
            <div class="eyebrow">共享时间轴 · 独立零基线</div>
            <h2 id="overview-speed-title">五小时来源速度贡献</h2>
          </div>
          <div class="head-tools">
            <strong id="overview-current-speed">近 10m — tok/s</strong>
            <span class="coverage-note" data-role="measured-coverage"></span>
          </div>
        </div>
        <div id="overview-source-lanes" class="chart source-lanes" data-chart="source-lanes"></div>
        <div id="overview-unmeasured"></div>
      </section>

      <section class="grid2">
        <article class="instrument-card" data-role="current-contributors">
          <div class="card-head"><h2>近 10m 贡献者</h2><span class="subtle">共享分母 · 可加和</span></div>
          <div id="overview-contributors" class="data-table-shell"></div>
        </article>
        <article class="instrument-card" data-role="device-throughput">
          <div class="card-head"><h2>设备用量</h2><span class="subtle">近 30 天 tokens</span></div>
          <div id="overview-devices" class="bars"></div>
        </article>
      </section>

      ${this.channelSection(summary.by_channel || [])}

      <section class="chart-card" aria-labelledby="today-model-title">
        <div class="card-head">
          <div><div class="eyebrow">本地午夜至今 · 完整列表</div><h2 id="today-model-title">今日模型构成</h2></div>
          <span>${compact(today.total_tokens || 0)} tokens</span>
        </div>
        <div id="overview-model-composition" class="chart model-composition" data-chart="today-model-composition"></div>
      </section>

      <section class="chart-card" aria-labelledby="overview-heatmap-title">
        <div class="card-head">
          <div><div class="eyebrow">历史活动 · 最近 365 天</div><h2 id="overview-heatmap-title">每日用量热力图</h2></div>
          <span class="subtle">颜色表示当日 token 量</span>
        </div>
        <div id="overview-heatmap" class="chart heatmap-shell"></div>
      </section>

      <section class="instrument-card" data-role="model-throughput">
        <div class="card-head"><h2>近 10m 模型吞吐</h2><span class="subtle">共享分母贡献</span></div>
        <div id="overview-model-throughput" class="bars"></div>
      </section>`;

    this.renderSpeed(telemetry, liveSpeed);
    this.renderModels(today.models || []);
    Heatmap.load(document.getElementById("overview-heatmap"), 365);
    this.renderContributors(liveSpeed.sessions || []);
    this.renderBars(
      "overview-devices",
      summary.by_device || [],
      (row) => row.display_name || row.key || row.device,
      "total_tokens",
    );
    this.renderBars("overview-model-throughput", liveSpeed.models || [], speedSourceKey, "contribution_tps");
  },

  metricCard(role, label, value, detail) {
    return `<article class="metric-card" data-role="${role}">
      <div class="metric-label">${label}</div>
      <div class="metric-value">${compact(value)}</div>
      <div class="subtle">${esc(detail)}</div>
    </article>`;
  },

  sourceCard(source, label, row) {
    const change = row.change_percent == null ? "无可比基线"
      : `${row.change_percent >= 0 ? "↑" : "↓"} ${Math.abs(row.change_percent).toFixed(1)}%`;
    return `<article class="metric-card" data-source="${source}">
      <div class="metric-label">${label}</div>
      <div class="metric-value">${compact(row.tokens || 0)}</div>
      <div class="subtle">${change} · 前五小时 ${compact(row.previous_tokens || 0)}</div>
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
          <div>
            <div class="eyebrow">近 30 天 · 只有「订阅」受配额窗口约束</div>
            <h2 id="overview-channel-title">计费通道构成</h2>
          </div>
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
        <p class="subtle">未知通道既不并入订阅,也不按比例摊分到其它通道 —— 证据不足时不猜(ADR-0018)。</p>
      </section>`;
  },

  sourceComposition(rows, total) {
    if (!total) return `<div class="composition-strip" aria-label="暂无来源用量"></div>`;
    return `<div class="composition-strip" aria-label="五小时来源构成">` + rows.map((row) =>
      `<span title="${esc(sourceLabelA2(speedSourceKey(row)))} ${compact(row.tokens || 0)}" ` +
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
    const latest = buckets[buckets.length - 1] || {};
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
        `<div class="unavailable-lane">${esc(sourceLabelA2(source))} 速度 unavailable · 用量仍计入五小时和今日总数</div>`
      ).join("");

    const el = document.getElementById("overview-source-lanes");
    if (!buckets.length || !sourceKeys.length) {
      el.innerHTML = `<p class="empty">成功查询，但此范围暂无可测生成区间。</p>`;
      return;
    }
    const times = buckets.map((bucket) => new Date(bucket.start_ms).toLocaleTimeString("zh-CN", {hour: "2-digit", minute: "2-digit"}));
    const grids = sourceKeys.map((_, index) => ({
      left: 58, right: 18, top: 16 + index * (220 / sourceKeys.length),
      height: Math.max(38, 170 / sourceKeys.length),
    }));
    ChartRegistry.set(el, {
      titleText: "五小时来源速度贡献",
      grid: grids,
      xAxis: sourceKeys.map((_, index) => ({
        type: "category", gridIndex: index, data: times,
        axisLine: { lineStyle: { color: cssVar("--baseline") } },
        axisTick: { show: false }, axisLabel: { show: index === sourceKeys.length - 1, color: cssVar("--text-muted") },
      })),
      yAxis: sourceKeys.map((key, index) => ({
        type: "value", gridIndex: index, name: sourceLabelA2(key),
        nameTextStyle: { color: ChartRegistry.sourceColor(key), align: "right" },
        min: 0, splitNumber: 2, splitLine: { lineStyle: { color: cssVar("--grid") } },
        axisLabel: { color: cssVar("--text-muted"), formatter: (v) => compact(v) },
      })),
      series: sourceKeys.map((key, index) => ({
        name: sourceLabelA2(key), type: "bar", xAxisIndex: index, yAxisIndex: index,
        itemStyle: { color: ChartRegistry.sourceGradient(key), borderRadius: [3, 3, 0, 0] },
        data: buckets.map((bucket) => {
          const row = (bucket.sources || []).find((candidate) => speedSourceKey(candidate) === key);
          return row ? row.contribution_tps || 0 : 0;
        }),
      })),
    }, {
      caption: "来源速度逐桶数据",
      columns: [
        { key: "time", label: "时间" },
        ...sourceKeys.map((key) => ({ key, label: sourceLabelA2(key), format: (v) => Number(v || 0).toFixed(2) })),
        { key: "aggregate", label: "合计", format: (v) => Number(v || 0).toFixed(2) },
      ],
      rows: buckets.map((bucket, index) => {
        const row = { time: times[index], aggregate: bucket.aggregate_tps || 0 };
        sourceKeys.forEach((key) => {
          row[key] = ((bucket.sources || []).find((candidate) => speedSourceKey(candidate) === key) || {}).contribution_tps || 0;
        });
        return row;
      }),
    });
  },

  renderModels(models) {
    const el = document.getElementById("overview-model-composition");
    if (!models.length) {
      el.innerHTML = `<p class="empty">今天暂无模型用量。</p>`;
      return;
    }
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

  renderContributors(rows) {
    const el = document.getElementById("overview-contributors");
    if (!rows.length) {
      el.innerHTML = `<p class="empty">近 10m 没有可测贡献者。</p>`;
      return;
    }
    el.innerHTML = `<table><thead><tr><th>会话</th><th>来源</th><th>模型</th><th>贡献 tok/s</th></tr></thead><tbody>` +
      rows.map((row) => `<tr><td>${esc(row.session_id || row.key || "—")}</td><td>${esc(sourceLabelA2(row.source))}</td>` +
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
