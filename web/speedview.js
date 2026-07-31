"use strict";
// Speed view (F15): how fast generation is running now, then how the models
// compare over the range.
//
// Every figure on this page divides by the union of generation intervals
// (ADR-0009) — the time something was actually producing tokens. That is a
// different question from the burn rate on the Live page, which divides by the
// whole window and therefore counts idle time. It also replaces what this page
// used to show: a mean of per-event ratios over duration_ms, which held human
// thinking and tool runs in the denominator and single 2ms events in the
// numerator, and was wrong in both directions at once.
//
// Three channels, never averaged together: the log-derived curve and model
// table, the proxy's measured numbers, and nothing at all for Codex.

function speedIsEmpty(data) {
  const groups = [data.models || [], data.exact || [], (data.series && data.series.buckets) || []];
  const output = groups.flat().reduce((sum, row) => sum + (row.output_tokens || 0), 0);
  return output + (((data.live || {}).output_tokens) || 0) === 0;
}

const SpeedView = {
  _timer: null,
  lastData: null,
  _loadGeneration: 0,

  enter() {
    this.load();
    // Faster than the other retrospective pages: the top half of this one is a
    // live curve, and a minute-resolution chart refreshed every 30s is stale
    // for half its own resolution.
    this._timer = setInterval(() => this.load(), 15000);
  },

  leave() {
    clearInterval(this._timer);
    this._loadGeneration += 1;
  },

  async load() {
    const loadID = ++this._loadGeneration;
    const root = document.getElementById("view-speed");
    if (!this.lastData) {
      renderState(root, { kind: "loading", title: "正在加载速度数据" });
    }
    try {
      const [data, telemetryResult] = await Promise.all([
        Api.get("/api/v1/speed?days=30"),
        TelemetryCache.load("1h", { force: true }),
      ]);
      data.telemetry = telemetryResult.data;
      if (!isCurrentGeneration(this._loadGeneration, loadID)) return;
      this.render(data);
      this.lastData = data;
      if (speedIsEmpty(data)) {
        renderState(root, {
          kind: "empty", title: "暂无速度数据",
          detail: "没有可计算的输出或生成区间;产生带时长的输出后这里会显示速度。",
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
        title: this.lastData ? "速度数据可能已过期" : issue.title,
        detail: issue.detail,
        action: { label: "重试", run: () => this.load() },
      });
      document.getElementById("refresh-note").textContent = this.lastData
        ? "刷新失败,正在显示上次数据" : issue.title;
    }
  },

  // --- formatting (kept as methods: format.js is shared, don't shadow it) ---

  tps(v) {
    if (!isFinite(v) || v <= 0) return "—";
    if (v >= 100) return v.toFixed(0);
    return v.toFixed(1);
  },

  ms(v) {
    if (!isFinite(v) || v <= 0) return "—";
    if (v >= 1000) return (v / 1000).toFixed(2) + "s";
    return Math.round(v) + "ms";
  },

  // Active time reads in whatever unit keeps it a small number: a model that
  // generated for 4 hours and one that generated for 20 seconds share a column.
  dur(msVal) {
    const s = Math.round(msVal / 1000);
    if (s <= 0) return "—";
    if (s < 60) return s + "s";
    if (s < 3600) return Math.round(s / 60) + "m";
    return (s / 3600).toFixed(1) + "h";
  },

  pct(v) {
    if (!isFinite(v) || v <= 0) return "0%";
    return (v * 100).toFixed(v >= 0.1 ? 0 : 1) + "%";
  },

  // --- rendering ---

  render(d) {
    const root = document.getElementById("view-speed");
    const models = d.models || [];
    const exact = d.exact || [];
    const series = d.series || {};
    const live = d.live || {};
    const days = d.days || 30;

    // Rebuilt only when the shape changes; the chart lives across polls so it
    // keeps its animation state instead of restarting every 15 seconds.
    if (!root.dataset.built) {
      root.innerHTML = `
        <section class="stat-row" id="speed-tiles"></section>
        <section class="chart-card">
          <div class="card-head">
            <div><div class="eyebrow">共享分母 contribution_tps · 非堆叠</div><h2>来源速度贡献</h2></div>
            <span class="coverage-note" data-role="measured-coverage"></span>
          </div>
          <div id="speed-source-lanes" class="chart source-lanes" data-chart="speed-source-lanes"></div>
          <div id="speed-unmeasured"></div>
          <p class="subtle">贡献速度使用共享分母，因此各来源可以相加得到 aggregate_tps；native_tps 是来源自身活跃时的速度，仅用于下钻，不能相加。</p>
        </section>
        <section class="card">
          <div class="card-head">
            <h2>生成速度 · 近 <span id="speed-window">60</span> 分钟</h2>
            <div class="head-tools"><span class="subtle" id="speed-curve-note"></span></div>
          </div>
          <div id="speed-curve" style="height:260px"></div>
          <p class="subtle" id="speed-curve-legend"></p>
        </section>
        <section class="card">
          <div class="card-head">
            <h2>按模型 · 近 <span id="speed-days">30</span> 天</h2>
            <span class="chip badge approx">并集口径 · 日志推算</span>
          </div>
          <p class="subtle" id="speed-model-note"></p>
          <div class="bars" id="speed-bars"></div>
          <div class="data-table" id="speed-model-table"></div>
        </section>
        <section class="card">
          <div class="card-head">
            <h2>本地代理(精确) · 近 <span id="speed-days-exact">30</span> 天</h2>
            <span class="chip badge exact">精确 · 代理实测</span>
          </div>
          <div id="speed-exact"></div>
        </section>`;
      root.dataset.built = "1";
    }
    document.getElementById("speed-days").textContent = days;
    document.getElementById("speed-days-exact").textContent = days;
    document.getElementById("speed-window").textContent = series.window_minutes || 60;

    this.renderTiles(live, series);
    this.renderSourceLanes(d.telemetry || {});
    this.renderCurve(series);
    this.renderModels(models);
    document.getElementById("speed-exact").innerHTML = this.exactBody(exact, d.has_exact);
  },

  renderSourceLanes(snapshot) {
    const speed = telemetrySpeed(snapshot);
    const buckets = speed.series || [];
    const keys = [...new Set([
      ...(speed.measured_sources || []),
      ...buckets.flatMap((bucket) => (bucket.sources || []).map(speedSourceKey)),
    ])];
    document.querySelector("#view-speed [data-role='measured-coverage']").textContent =
      `已测 ${(speed.measured_sources || []).map(sourceLabelA2).join(" · ") || "无"} · 未测 ${(speed.unmeasured_sources || []).map(sourceLabelA2).join(" · ") || "无"}`;
    document.getElementById("speed-unmeasured").innerHTML = (speed.unmeasured_sources || []).map((source) =>
      `<div class="unavailable-lane">${esc(sourceLabelA2(source))} 速度 unavailable；用量不受影响</div>`
    ).join("");
    const el = document.getElementById("speed-source-lanes");
    if (!buckets.length || !keys.length) {
      el.innerHTML = `<p class="empty">此范围暂无已测来源速度。</p>`;
      return;
    }
    const labels = buckets.map((bucket) => new Date(bucket.start_ms).toLocaleTimeString("zh-CN", {hour: "2-digit", minute: "2-digit"}));
    ChartRegistry.set(el, {
      titleText: "速度页来源贡献",
      grid: keys.map((_, i) => ({left: 58, right: 18, top: 12 + i * 92, height: 54})),
      xAxis: keys.map((_, i) => ({
        type: "category", gridIndex: i, data: labels,
        axisLabel: {show: i === keys.length - 1, color: cssVar("--text-muted")},
        axisLine: {lineStyle: {color: cssVar("--baseline")}},
      })),
      yAxis: keys.map((key, i) => ({
        type: "value", gridIndex: i, min: 0, name: sourceLabelA2(key),
        nameTextStyle: {color: ChartRegistry.sourceColor(key)},
        splitLine: {lineStyle: {color: cssVar("--grid")}},
        axisLabel: {color: cssVar("--text-muted")},
      })),
      series: keys.map((key, i) => ({
        name: sourceLabelA2(key), type: "bar", xAxisIndex: i, yAxisIndex: i,
        itemStyle: {color: ChartRegistry.sourceGradient(key), borderRadius: [3, 3, 0, 0]},
        data: buckets.map((bucket) => {
          const row = (bucket.sources || []).find((candidate) => speedSourceKey(candidate) === key);
          return row ? row.contribution_tps || 0 : 0;
        }),
      })),
    });
  },

  renderTiles(live, series) {
    const buckets = series.buckets || [];
    const active = buckets.filter((b) => b.active_ms > 0);
    const peak = active.reduce((m, b) => Math.max(m, b.tps), 0);
    const activeMS = buckets.reduce((s, b) => s + b.active_ms, 0);
    const spanMS = Math.max(1, (buckets.length || 1) * (series.bucket_ms || 60000));

    document.getElementById("speed-tiles").innerHTML = `
      <div class="stat-tile">
        <div class="label">近 10 分钟已测总吞吐</div>
        <div class="value">${this.tps(live.tps || 0)}<span class="unit"> tok/s</span></div>
        <div class="sub">${(live.sessions || []).length ? `${(live.sessions || []).length} 个近 10m 贡献会话` : "近 10m 没有生成"}</div>
      </div>
      <div class="stat-tile">
        <div class="label">近 ${series.window_minutes || 60} 分钟峰值</div>
        <div class="value">${this.tps(peak)}<span class="unit"> tok/s</span></div>
        <div class="sub">${active.length ? `${active.length} 分钟里有生成` : "整段窗口都空闲"}</div>
      </div>
      <div class="stat-tile">
        <div class="label">生成占比</div>
        <div class="value">${this.pct(activeMS / spanMS)}</div>
        <div class="sub">窗口内真正在产出 token 的时间</div>
      </div>`;
  },

  // The curve. Idle minutes are null, not zero: a gap says "nothing was
  // generating", while a zero would claim something ran and emitted nothing.
  renderCurve(series) {
    const el = document.getElementById("speed-curve");
    const buckets = series.buckets || [];
    const note = document.getElementById("speed-curve-note");
    if (!buckets.some((b) => b.active_ms > 0)) {
      el.innerHTML = `<p class="bars"><span class="empty">近 ${series.window_minutes || 60} 分钟没有生成。开始使用 Claude,曲线会实时出现。</span></p>`;
      note.textContent = "";
      document.getElementById("speed-curve-legend").textContent = "";
      return;
    }
    note.textContent = `每 ${Math.round((series.bucket_ms || 60000) / 1000)} 秒一个点`;
    document.getElementById("speed-curve-legend").textContent =
      "速度 = 该分钟产出的 output tokens ÷ 该分钟内真正在生成的时间。断开处表示没有生成,不是速度为 0。";

    const chart = echartsFor(el);
    const hue = cssVar("--series-1");
    chart.setOption({
      aria: { enabled: true },
      animation: !matchMedia("(prefers-reduced-motion: reduce)").matches,
      grid: { left: 8, right: 8, top: 16, bottom: 4, containLabel: true },
      tooltip: {
        trigger: "axis",
        axisPointer: { type: "line", lineStyle: { color: cssVar("--border-strong") } },
        ...tooltipStyle(),
        formatter: (ps) => {
          const p = ps[0];
          const b = buckets[p.dataIndex];
          if (!b || !b.active_ms) return `${p.axisValue}<br/>没有生成`;
          return `${p.axisValue}<br/><b>${this.tps(b.tps)}</b> tok/s<br/>` +
            `输出 ${compact(b.output_tokens)} · 生成 ${Math.round(b.active_ms / 1000)}s`;
        },
      },
      xAxis: {
        type: "category",
        data: buckets.map((b) => new Date(b.start_ms).toLocaleTimeString("zh-CN", { hour12: false, hour: "2-digit", minute: "2-digit" })),
        axisLine: { lineStyle: { color: cssVar("--baseline") } },
        axisTick: { show: false },
        // One label every ten minutes: sixty of them would collide.
        axisLabel: {
          color: cssVar("--text-muted"), fontSize: 10, fontFamily: chartFont(),
          interval: (i) => i % 10 === 0,
        },
      },
      yAxis: {
        type: "value",
        name: "tok/s",
        nameTextStyle: { color: cssVar("--text-muted"), fontSize: 10, fontFamily: chartFont() },
        splitLine: { lineStyle: { color: cssVar("--grid") } },
        axisLabel: { color: cssVar("--text-muted"), fontSize: 10, fontFamily: chartFont() },
      },
      series: [{
        name: "生成速度",
        type: "line",
        // Real measurements at one-minute resolution: smoothing would invent
        // intermediate values the data does not have.
        smooth: false,
        connectNulls: false,
        lineStyle: { width: 2, color: hue },
        itemStyle: { color: hue },
        symbol: "circle",
        symbolSize: 4,
        showSymbol: false,
        emphasis: { scale: 2.2 },
        areaStyle: { color: mixWithSurface(hue, 0.14) },
        data: buckets.map((b) => (b.active_ms > 0 ? Number(b.tps.toFixed(1)) : null)),
      }],
      animationDuration: 320,
      animationEasing: "cubicOut",
    }, true);
  },

  renderModels(rows) {
    const note = document.getElementById("speed-model-note");
    const withData = rows.filter((r) => r.tps > 0);
    note.innerHTML =
      "速度 = Σ输出 tokens ÷ 生成区间并集,按「一条会话流」取并集后再跨流相加(ADR-0009)。" +
      "这里<b>不给逐条中位数</b>:日志推出来的区间里含着等首 token 的时间,长回答无所谓," +
      "几个 token 的工具决策则几乎全是等待,逐条比值会得出没有任何一次响应跑出过的数。" +
      "要看单条分布与 TTFT,用下方的本地代理通道 —— 只有它把延迟和生成分开测。" +
      "<b>Codex 不在此列</b>:它的 rollout 文件会成批回放历史,70% 的记录时间戳是刷盘时刻,没有可用的生成区间。" +
      "覆盖率 = 有生成区间的事件占比;30 天前的日志已被 Claude Code 清理,那部分历史永远补不回来。";

    const bars = document.getElementById("speed-bars");
    if (!withData.length) {
      bars.innerHTML = `<span class="empty">暂无带生成区间的事件。新采集的事件会自动带上;历史需要跑一次 <code>omnitoken serve -rescan</code>。</span>`;
      document.getElementById("speed-model-table").innerHTML = "";
      return;
    }
    const sorted = [...withData].sort((a, b) => b.tps - a.tps);
    const max = sorted[0].tps;
    bars.innerHTML = sorted.map((r) => `<div class="row">
        <div class="row-head">
          <span class="key">${esc(r.model || "(未知)")}</span>
          <span class="val">${this.tps(r.tps)} tok/s <span class="extra">· 生成 ${this.dur(r.active_ms)} · ${full(r.samples)} 条</span></span>
        </div>
        <div class="track"><div class="fill" style="width:${(100 * r.tps / max).toFixed(1)}%"></div></div>
      </div>`).join("");

    document.getElementById("speed-model-table").innerHTML =
      `<table><thead><tr>
        <th>模型</th><th>tok/s</th><th>生成时长</th><th>输出 tokens</th>
        <th>响应数</th><th>会话流</th><th>覆盖率</th>
      </tr></thead><tbody>` +
      sorted.map((r) => `<tr>
        <td>${esc(r.model || "(未知)")}</td>
        <td>${this.tps(r.tps)}</td>
        <td>${this.dur(r.active_ms)}</td>
        <td>${compact(r.output_tokens)}</td>
        <td>${full(r.samples)}</td>
        <td>${full(r.streams)}</td>
        <td${r.coverage < 0.5 ? ' class="extra"' : ""}>${this.pct(r.coverage)}</td>
      </tr>`).join("") + `</tbody></table>`;
  },

  exactBody(rows, hasExact) {
    if (!hasExact || !rows.length) {
      return `<p class="subtle">暂无本地代理数据。配置 agent 的 <code>proxy_listen</code>(如 <code>127.0.0.1:8899</code>)并把工具的 base_url 指向代理后,即可获得精确的生成耗时与 TTFT(首 token 延迟);详见 docs/configuration.md。</p>`;
    }
    return `
      <p class="subtle">代理在请求两端打点,所以这里的耗时就是生成耗时,TTFT 是真实的首 token 延迟 —— 不需要上面那套区间推算。同样已排除 输出 &lt; 8 tokens 的事件。</p>
      <div class="data-table"><table><thead><tr>
        <th>模型</th><th>中位 tok/s</th><th>P90 tok/s</th><th>均值 tok/s</th>
        <th>中位 TTFT</th><th>均值 TTFT</th><th>样本数</th><th>输出 tokens</th>
      </tr></thead><tbody>` +
      rows.map((r) => `<tr>
          <td>${esc(r.model || "(未知)")}</td>
          <td>${this.tps(r.median_tps)}</td>
          <td>${this.tps(r.p90_tps)}</td>
          <td>${this.tps(r.avg_tps)}</td>
          <td>${this.ms(r.median_ttft_ms)}</td>
          <td>${this.ms(r.avg_ttft_ms)}</td>
          <td>${full(r.samples)}</td>
          <td>${compact(r.output_tokens)}</td>
        </tr>`).join("") + `</tbody></table></div>`;
  },
};
