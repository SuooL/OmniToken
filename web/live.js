"use strict";
// Live view: SSE-driven realtime state (contract: docs/API.md).

const Live = {
  es: null,
  data: null,
  timer: null,
  telemetryTimer: null,
  telemetryData: null,
  snapshotReceivedAt: 0,
  connectionStale: true,

  start() {
    if (this.es) return;
    const status = document.getElementById("live-status");
    this.es = Api.stream("/api/v1/stream");
    const onData = (ev) => {
      this.data = JSON.parse(ev.data);
      LiveSnapshotCache.adopt(this.data);
      this.connectionStale = false;
      this.snapshotReceivedAt = performance.now();
      this.render();
      status.textContent = "实时连接中 · 更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    };
    this.es.addEventListener("snapshot", onData);
    this.es.addEventListener("live", onData);
    this.es.onerror = () => {
      this.connectionStale = true;
      status.textContent = "连接断开 · 显示上次快照，自动重连中…";
      if (this.data) this.render();
    };
    // Re-render periodically so relative times stay fresh between events.
    this.timer = setInterval(() => { if (this.data) this.render(); }, 10000);
    this.loadTelemetry();
    this.telemetryTimer = setInterval(() => this.loadTelemetry(), 30000);
  },

  stop() {
    this.connectionStale = true;
    if (this.es) { this.es.close(); this.es = null; }
    if (this.timer) { clearInterval(this.timer); this.timer = null; }
    if (this.telemetryTimer) { clearInterval(this.telemetryTimer); this.telemetryTimer = null; }
    TelemetryCache.invalidate("1h");
  },

  async loadTelemetry() {
    try {
      const result = await TelemetryCache.load("1h", { force: true });
      this.telemetryData = result.data;
      this.renderTelemetry(result);
    } catch (error) {
      const cached = TelemetryCache.peek("1h");
      if (cached.data) this.renderTelemetry(cached);
      else {
        const host = document.getElementById("live-telemetry-card");
        if (host) {
          const issue = classifyAPIError(error);
          host.innerHTML = `<section class="state-panel"><strong>${esc(issue.title)}</strong><p class="subtle">${esc(issue.detail)}</p></section>`;
        }
      }
    }
  },

  renderTelemetry(result) {
    const host = document.getElementById("live-telemetry-card");
    if (!host || !result.data) return;
    const speed = telemetrySpeed(result.data);
    if (!document.getElementById("live-source-chart")) {
      host.innerHTML = `<article class="chart-card"><div class="card-head"><h2>生成速度来源 · 近一小时</h2><div id="live-coverage" class="coverage-note" data-role="measured-coverage"></div></div><div id="live-source-chart" class="chart source-lanes" data-chart="live-source-lanes"></div><div id="live-unmeasured"></div></article>`;
    }
    document.getElementById("live-coverage").textContent = telemetryCoverageLabel(speed) + (result.stale ? " · 显示上次成功数据" : "");
    ToolUI.updateHTML(document.getElementById("live-unmeasured"), (speed.unmeasured_sources || []).map((source) => `<div class="unavailable-lane">${esc(SourceUI.label(source))} 速度不可测 / not measured</div>`).join(""));
    const buckets = speed.series || [];
    const sourceKeys = [...new Set([
      ...(speed.measured_sources || []),
      ...buckets.flatMap((bucket) => (bucket.sources || []).map(speedSourceKey)),
    ])];
    const el = document.getElementById("live-source-chart");
    if (!buckets.length || !sourceKeys.length) {
      ChartRegistry.disposeWithin?.(el);
      el.innerHTML = `<p class="empty">近一小时没有可测生成区间。</p>`;
      ChartRegistry.renderFallback?.(el, {caption: "近一小时来源贡献 · 暂无可测区间", columns: [], rows: []});
      return;
    }
    el.querySelectorAll?.(".empty").forEach((node) => node.remove());
    const labels = buckets.map((bucket) => new Date(bucket.start_ms).toLocaleTimeString("zh-CN", {hour: "2-digit", minute: "2-digit"}));
    const grids = sourceKeys.map((_, index) => ({
      left: 58, right: 18, top: 14 + index * (220 / sourceKeys.length),
      height: Math.max(38, 170 / sourceKeys.length),
    }));
    ChartRegistry.set(el, {
      titleText: "实时页一小时来源贡献",
      grid: grids,
      xAxis: sourceKeys.map((_, index) => ({
        type: "category", gridIndex: index, data: labels,
        axisLabel: { show: index === sourceKeys.length - 1, color: cssVar("--text-muted") },
        axisLine: { lineStyle: { color: cssVar("--baseline") } },
      })),
      yAxis: sourceKeys.map((key, index) => ({
        type: "value", gridIndex: index, min: 0, name: SourceUI.label(key),
        nameTextStyle: { color: ChartRegistry.sourceColor(key) },
        splitLine: { lineStyle: { color: cssVar("--grid") } },
        axisLabel: { color: cssVar("--text-muted") },
      })),
      series: sourceKeys.map((key, index) => ({
        name: SourceUI.label(key), type: "line", xAxisIndex: index, yAxisIndex: index,
        symbol: "none", smooth: false,
        lineStyle: { color: ChartRegistry.sourceColor(key), width: 2 },
        areaStyle: { color: mixWithSurface(ChartRegistry.sourceColor(key), .12) },
        data: buckets.map((bucket) => {
          const row = (bucket.sources || []).find((candidate) => speedSourceKey(candidate) === key);
          return row ? row.contribution_tps ?? null : null;
        }),
      })),
    }, {
      caption: "近一小时来源贡献",
      columns: [{key: "time", label: "时间"}, ...sourceKeys.map((key) => ({key, label: SourceUI.label(key), format: (v) => v == null ? "—" : Number(v).toFixed(2)}))],
      rows: buckets.map((bucket, index) => {
        const row = {time: labels[index]};
        sourceKeys.forEach((key) => { row[key] = (bucket.sources || []).find((source) => speedSourceKey(source) === key)?.contribution_tps ?? null; });
        return row;
      }),
    });
  },

  // Generation speed (ADR-0009). Divided by the union of generation intervals,
  // so it says how fast the model emits — not how much of the window was busy.
  renderSpeed(sp) {
    const tps = sp.tps || 0;
    const now = document.getElementById("now-block");
    document.getElementById("speed-tps").textContent = tps > 0 ? tps.toFixed(1) : "—";
    now.classList.toggle("idle", !(tps > 0));

    const n = (sp.sessions || []).length;
    document.getElementById("speed-sub").innerHTML = tps > 0
      ? `<b>${n}</b> 个近 10m 贡献会话 · 输出 <b>${compact(sp.output_tokens || 0)}</b>`
      : "近 10 分钟没有生成";

    this.renderLanes(sp);
  },

  // One row per concurrent stream, then the union. Overlap is the point: three
  // concurrent 50 tok/s sessions are 150 aggregate, while three sequential
  // sessions remain 50 aggregate across their shared wall-clock timeline.
  renderLanes(sp) {
    const el = document.getElementById("lanes");
    const t0 = sp.window_start_ms, t1 = sp.window_end_ms;
    const span = Math.max(1, t1 - t0);
    const blocks = (spans) => (spans || []).map(([a, b]) => {
      const left = ((Math.max(a, t0) - t0) / span) * 100;
      const width = ((Math.min(b, t1) - Math.max(a, t0)) / span) * 100;
      return `<i class="blk" style="left:${left}%;width:${Math.max(width, 0.4)}%"></i>`;
    }).join("");

    const sessions = sp.sessions || [];
    if (!sessions.length) {
      el.innerHTML = `<p class="subtle">近 10 分钟没有已测贡献会话。开始使用 Claude,这里会实时出现。</p>`;
      document.getElementById("lane-note").textContent = "";
      return;
    }

    const rows = sessions.map((s, i) => `
      <div class="lane lane-${(i % 5) + 1}">
        <div class="who">${esc(s.repo ? repoLabel(s.repo).split("/").pop() : (s.session_id || "—").slice(0, 8))}
          <span class="sub">${esc(this.deviceLabel(s.device))} · 会话自身速度 ${Number(s.tps || 0).toFixed(1)} tok/s</span></div>
        <div class="track">${blocks(s.spans)}</div>
        <div class="rate">${Number(s.contribution_tps ?? s.tps ?? 0).toFixed(1)}<span class="u"> 贡献 t/s</span></div>
      </div>`).join("");
    el.innerHTML = rows + `
      <div class="lane union">
        <div class="who">全部设备(并集)</div>
        <div class="track">${blocks(sp.spans)}</div>
        <div class="rate">${(sp.tps || 0).toFixed(1)}<span class="u"> t/s</span></div>
      </div>`;

    const busy = Math.round(((sp.active_ms || 0) / Math.max(1, span)) * 100);
    document.getElementById("lane-note").textContent =
      `窗口内 ${busy}% 的时间在生成 · 总吞吐按全局活跃时间计算；各行贡献使用同一分母，因此可以相加。`;
  },

  // Identity → display name, rebuilt from every snapshot.
  //
  // `devices` is the only part of the payload the server resolves names for
  // (deviceNames), and every other list here — sessions, lanes, process rows,
  // reporters — is keyed by the same identity. Without this they print the raw
  // key, which for a v2 device is a 36-character UUID.
  deviceNames: {},

  deviceLabel(identity) {
    return this.deviceNames[identity] || identity;
  },

  render() {
    const d = this.data;
    if (!d) return;

    this.deviceNames = {};
    for (const v of d.devices || []) {
      if (v.device && v.display_name) this.deviceNames[v.device] = v.display_name;
    }

    this.renderQuotas(d.quotas || [], d.windows || []);

    const burn = d.burn || {};
    document.getElementById("burn-rate").textContent = compact(burn.per_minute || 0) + "/min";
    // Name what is counted: the figure excludes cache reads, and without
    // saying so the number invites comparison with the generation speed above.
    document.getElementById("burn-sub").textContent =
      `${burn.window_minutes} 分钟内新增 ${compact(burn.tokens || 0)} · 输出 ${compact(burn.output_tokens || 0)}`;

    this.renderSpeed(d.speed || {});
    this.renderWindows(d.windows || []);

    this.renderProcs(d.processes || {});

    const devEl = document.getElementById("live-devices");
    const devs = d.devices || [];
    devEl.innerHTML = devs.length ? devs.map((v) => `
      <div class="row dev-row">
        <div class="row-head">
          <span class="key"><span class="dot ${this.visualState(v)}"></span>${esc(v.display_name || v.device)} <span class="extra">${this.connectionLabel(v)}</span></span>
          <span class="val">${compact(v.today_tokens)} <span class="extra">今日</span></span>
        </div>
        <div class="row-head sub2"><span class="key">最后活动 ${this.deviceLastSeen(v)} · ${this.procLabel(v)}</span><span class="val extra">${full(v.today_events)} 请求</span></div>
      </div>`).join("") : `<span class="empty">暂无设备</span>`;

    const sesEl = document.getElementById("live-sessions");
    const ses = d.sessions || [];
    sesEl.innerHTML = ses.length ? `<table><thead><tr><th>设备</th><th>项目</th><th>模型</th><th>tokens</th><th>最后活动</th></tr></thead><tbody>` +
      ses.map((s) => `<tr>
        <td title="${esc(s.device || "")}">${esc(this.deviceLabel(s.device))}</td>
        <td title="${esc(s.cwd || "")}">${esc(this.trunc(repoLabel(s.repo, s.cwd), 36))}</td>
        <td>${esc(s.model)}</td>
        <td>${compact(s.tokens)}</td>
        <td>${relTime(s.last_ts)}</td>
      </tr>`).join("") + `</tbody></table>`
      : `<span class="empty">近 10 分钟无活跃会话</span>`;
  },

  // One renderer preserves official 0/null/overshoot and keeps inferred allowance separate.
  renderWindows() {
    const el = document.getElementById("window-row");
    if (el) el.innerHTML = "";
  },

  renderQuotas(quotas, windows) {
    ToolUI.updateHTML(document.getElementById("quota-row"), (this.connectionStale ? '<p class="subtle">连接中断 · 显示上次成功读数</p>' : "") + QuotaUI.render(windows || [], quotas || []));
  },

  // Ground truth from the machines' own process tables (ADR-0012), as opposed
  // to the events-inferred list beside it. A session shows up here from the
  // moment it opens until it is closed, whether or not it spent any tokens.
  renderProcs(procs) {
    const el = document.getElementById("live-procs");
    const note = document.getElementById("procs-note");
    const sessions = procs.sessions || [];
    const reporters = procs.reporters || [];

    // No reporter means no visibility, which is a different statement from
    // "nothing is open" and must never be rendered as one.
    if (!reporters.length) {
      note.textContent = "";
      el.innerHTML = `<span class="empty">会话状态不可观测：没有机器上报进程状态。只有装了 agent 的机器能看到,SSH 拉取的机器看不到。</span>`;
      return;
    }
    note.textContent = `${reporters.length} 台机器可见`;
    if (!sessions.length) {
      el.innerHTML = `<span class="empty">${esc(reporters.map((r) => this.deviceLabel(r.device)).join("、"))} 上没有会话开着</span>`;
      return;
    }
    el.innerHTML = `<table><thead><tr><th>设备</th><th>工具</th><th>已开启</th><th>PID</th></tr></thead><tbody>` +
      sessions.map((s) => `<tr>
        <td title="${esc(s.device || "")}">${esc(this.deviceLabel(s.device))}</td>
        <td>${SourceUI.brand(s.source)}</td>
        <td>${esc(since(s.started_at))}</td>
        <td class="extra">${s.pid}</td>
      </tr>`).join("") + `</tbody></table>`;
  },

  // Per-device process line. "无进程数据" is not "nothing open" — it is a
  // machine we cannot see into, and saying otherwise would be a lie about a
  // machine that may well have three sessions running.
  procLabel(v) {
    if (!v.has_procs) return `无进程数据`;
    return v.running ? `${v.running} 个会话开着` : `没有会话开着`;
  },

  stateLabel(s) {
    return { active: "活跃", idle: "空闲", stale: "离线" }[s] || s;
  },

  connectionLabel(v) {
    if (v.identity_status !== "registered") return this.stateLabel(v.state);
    return { online: "在线", stale: "延迟", offline: "离线" }[this.effectiveConnectionState(v)] || "未知";
  },

  effectiveConnectionState(v, elapsedMS = this.snapshotElapsedMS()) {
    if (v.identity_status !== "registered") return v.connection_state || "unknown";
    // The Hub's offline state is terminal for this snapshot (notably revoked
    // credentials). Client-side ageing may only degrade a state, never improve
    // one the authenticated Hub already classified.
    if (v.connection_state === "offline") return "offline";
    if (v.last_seen_age_ms == null) return v.connection_state || "offline";
    const age = Math.max(0, v.last_seen_age_ms + Math.max(0, elapsedMS));
    if (age <= 2 * 60 * 1000) return "online";
    if (age <= 10 * 60 * 1000) return "stale";
    return "offline";
  },

  snapshotElapsedMS() {
    if (!this.snapshotReceivedAt || typeof performance === "undefined") return 0;
    return Math.max(0, performance.now() - this.snapshotReceivedAt);
  },

  visualState(v) {
    if (v.identity_status !== "registered") return v.state;
    return { online: "active", stale: "idle", offline: "stale" }[this.effectiveConnectionState(v)] || "stale";
  },

  deviceLastSeen(v) {
    const timestamp = v.identity_status === "registered" ? v.last_seen_at : v.last_ts;
    return timestamp ? relTime(timestamp) : "从未连接";
  },

  trunc(s, n) {
    s = String(s);
    return s.length > n ? "…" + s.slice(-(n - 1)) : s;
  },
};
