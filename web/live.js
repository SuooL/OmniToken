"use strict";
// Live view: SSE-driven realtime state (contract: docs/API.md).

const Live = {
  es: null,
  data: null,
  timer: null,
  telemetryTimer: null,
  telemetryData: null,
  snapshotReceivedAt: 0,

  start() {
    if (this.es) return;
    const status = document.getElementById("live-status");
    this.es = Api.stream("/api/v1/stream");
    const onData = (ev) => {
      this.data = JSON.parse(ev.data);
      this.snapshotReceivedAt = performance.now();
      this.render();
      status.textContent = "实时连接中 · 更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    };
    this.es.addEventListener("snapshot", onData);
    this.es.addEventListener("live", onData);
    this.es.onerror = () => { status.textContent = "连接断开,自动重连中…"; };
    // Re-render periodically so relative times stay fresh between events.
    this.timer = setInterval(() => { if (this.data) this.render(); }, 10000);
    this.loadTelemetry();
    this.telemetryTimer = setInterval(() => this.loadTelemetry(), 30000);
  },

  stop() {
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
    host.innerHTML = `<article class="chart-card">
      <div class="card-head">
        <h2>生成速度来源<span class="eyebrow">近一小时 · 来源贡献</span></h2>
        <div class="coverage-note" data-role="measured-coverage">${
          telemetryCoverageLabel(speed)
        }${result.stale ? " · 显示上次成功数据" : ""}</div>
      </div>
      <div id="live-source-chart" class="chart source-lanes" data-chart="live-source-lanes"></div>
      ${(speed.unmeasured_sources || []).map((source) =>
        `<div class="unavailable-lane">${esc(sourceLabelA2(source))} 速度 unavailable / not measured</div>`
      ).join("")}
    </article>`;
    const buckets = speed.series || [];
    const sourceKeys = [...new Set([
      ...(speed.measured_sources || []),
      ...buckets.flatMap((bucket) => (bucket.sources || []).map(speedSourceKey)),
    ])];
    const el = document.getElementById("live-source-chart");
    if (!buckets.length || !sourceKeys.length) {
      el.innerHTML = `<p class="empty">近一小时没有可测生成区间。</p>`;
      return;
    }
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
        type: "value", gridIndex: index, min: 0, name: sourceLabelA2(key),
        nameTextStyle: { color: ChartRegistry.sourceColor(key) },
        splitLine: { lineStyle: { color: cssVar("--grid") } },
        axisLabel: { color: cssVar("--text-muted") },
      })),
      series: sourceKeys.map((key, index) => ({
        name: sourceLabelA2(key), type: "line", xAxisIndex: index, yAxisIndex: index,
        symbol: "none", smooth: false,
        lineStyle: { color: ChartRegistry.sourceColor(key), width: 2 },
        areaStyle: { color: mixWithSurface(ChartRegistry.sourceColor(key), .12) },
        data: buckets.map((bucket) => {
          const row = (bucket.sources || []).find((candidate) => speedSourceKey(candidate) === key);
          return row ? row.contribution_tps || 0 : 0;
        }),
      })),
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

    const rows = sessions.slice(0, 8).map((s, i) => `
      <div class="lane lane-${(i % 5) + 1}">
        <div class="who">${esc(s.repo ? repoLabel(s.repo).split("/").pop() : s.session_id.slice(0, 8))}
          <span class="sub">${esc(this.deviceLabel(s.device))} · 会话自身速度 ${Number(s.tps || 0).toFixed(1)} tok/s</span></div>
        <div class="track">${blocks(s.spans)}</div>
        <div class="rate">${Number(s.contribution_tps ?? s.tps ?? 0).toFixed(1)}<span class="u"> 贡献 t/s</span></div>
      </div>`).join("");
    const hidden = Math.max(0, sessions.length - 8);
    const more = hidden
      ? `<p class="subtle lane-more">另有 ${hidden} 个会话</p>`
      : "";

    el.innerHTML = rows + more + `
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

  // Quota window cards (server: buildWindowCards): per subscription source, the
  // 5-hour window and — when the provider reports one — the weekly window, plus
  // one rolling card per metered billing channel.
  //
  // Subscription windows use the provider's real reset boundary when known; the
  // metered channels have no window at all, so they are a rolling look-back and
  // say so — and they get no bar (ADR-0018 §7).
  renderWindows(windows) {
    const el = document.getElementById("window-row");
    if (!windows.length) { el.innerHTML = ""; return; }
    el.innerHTML = windows.map((w) => {
      const spanMin = Math.max(1, Math.round((w.end_ms - w.start_ms) / 60000));
      const elapsed = `${Math.floor(spanMin / 60)}h${spanMin % 60}m`;
      // A bar on a metered channel would be a category error, not a missing
      // feature: `api` / `relay` / `unknown` are not a percentage of anything,
      // because there is no allowance for them to be a fraction of. Drawing one
      // anyway — as this did, scaling tokens against an invented 80M reference
      // — is how relay spend came to look like it was eating a quota.
      const metered = w.kind !== "subscription";
      const pct = w.authoritative && w.used_percent
        ? Math.min(100, w.used_percent)
        : Math.min(100, 100 * w.tokens / 80e6);
      const badge = w.authoritative
        ? `<span class="chip authoritative">权威窗口</span>`
        : (metered ? `<span class="chip">按量计费 · 无窗口</span>`
          : (w.placeholder ? `<span class="chip">占位 · 滚动 5h</span>` : `<span class="chip">滚动 5h</span>`));
      const bits = [];
      if (w.authoritative && w.used_percent) {
        const remain = Math.max(0, Math.round((w.resets_at - Date.now()) / 60000));
        bits.push(`配额 ${w.used_percent.toFixed(0)}%`);
        bits.push(`${Math.floor(remain / 60)}h${remain % 60}m 后重置`);
        // How fresh the percentage is. It used to live on a separate tile above
        // this one; that tile said nothing this card does not, so it moved here
        // rather than being dropped — a quota reading without its age invites
        // reading a stale number as current.
        if (w.observed_at) bits.push(`观测 ${relTime(w.observed_at)}`);
      } else {
        bits.push(`近 ${elapsed}`);
      }
      if (w.cost_usd) bits.push(usd(w.cost_usd));
      bits.push(`${full(w.events)} 请求`);
      // Burn-rate projection (F11): only shown for windows with a real end.
      const proj = w.projected_tokens
        ? `${compact(w.rate_per_minute)}/min · 按此速率窗口结束约 ${compact(w.projected_tokens)}` +
          (w.projected_percent ? `(≈${Math.min(999, w.projected_percent).toFixed(0)}%)` : "")
        : "";
      // What is left in the window, learned from past windows (ADR-0025).
      // The card leads with the remainder because that is the question being
      // asked; the total is the smaller half of the same line. The `推断` chip
      // is what keeps it separable from the authoritative percentage above it —
      // the rule is that an inferred number is always marked as one.
      const room = w.remaining_tokens
        ? `<div class="sub">还能用 约 ${compact(w.remaining_tokens)} ` +
          `<span class="chip">推断</span>` +
          `<span class="extra"> · 窗口约 ${compact(w.capacity_tokens)}</span></div>`
        : "";
      return `
      <div class="stat-tile">
        <div class="label">${esc(w.label)} ${badge}</div>
        <div class="value">${w.tokens ? compact(w.tokens) : "0"}</div>
        <div class="sub">${bits.join(" · ")}</div>
        ${room}
        ${proj ? `<div class="sub proj${w.projected_percent >= 100 ? " over" : ""}">${esc(proj)}</div>` : ""}
        ${w.note ? `<div class="sub">${esc(w.note)}</div>` : ""}
        ${metered ? "" : `<div class="meter"><div class="meter-fill ${severity(pct)}" style="width:${pct.toFixed(1)}%"></div></div>`}
      </div>`;
    }).join("");
  },

  // Authoritative quota readings (ADR-0011 for Claude, Codex's rate_limits) that
  // no window card already states. These are real numbers, not inferred —
  // labelled as such so they are never confused with the estimated block below.
  //
  // A window card carries the same percentage and reset as the reading it
  // absorbed, and adds tokens, cost, projection and remaining allowance on top,
  // so drawing that reading again here is the identical number twice — which is
  // what this row used to do for every scope. What is left is what no card
  // shows: an account reports several scopes for one window (`seven_day` beside
  // per-model `seven_day:opus`) and only the tightest becomes a card.
  //
  // The server labels rather than filters: `quotas[]` arrives whole because the
  // menubar walks that same list for its 75%/90% alerts.
  renderQuotas(quotas, windows) {
    const el = document.getElementById("quota-row");
    const key = (source, minutes, scope) => `${source}|${minutes}|${scope || ""}`;
    const absorbed = new Set((windows || [])
      .filter((w) => w.authoritative && w.source)
      .map((w) => key(w.source, w.window_minutes, w.scope)));
    quotas = quotas.filter((q) => !absorbed.has(key(q.source, q.window_minutes, q.scope)));
    if (!quotas.length) { el.innerHTML = ""; return; }
    const order = { five_hour: 0, seven_day: 1 };
    const sorted = [...quotas].sort((a, b) =>
      (order[a.scope] ?? 9) - (order[b.scope] ?? 9) || a.window_minutes - b.window_minutes);
    el.innerHTML = sorted.map((q) => {
      const pct = Math.max(0, Math.min(100, q.used_percent));
      const reset = untilReset(q.remaining_minutes);
      const scope = q.scope.startsWith("seven_day:") ? q.scope.slice(10) : "";
      return `
      <div class="stat-tile">
        <div class="label">${esc(sourceLabel(q.source))} · ${esc(q.window_label)}${scope ? " · " + esc(scope) : ""}
          <span class="chip authoritative">权威</span></div>
        <div class="value">${pct.toFixed(0)}<span class="unit">%</span></div>
        <div class="sub">${reset} · 观测 ${relTime(q.observed_at)}</div>
        <div class="meter"><div class="meter-fill ${severity(pct)}" style="width:${pct}%"></div></div>
      </div>`;
    }).join("");
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
      el.innerHTML = `<span class="empty">没有机器上报进程状态。只有装了 agent 的机器能看到,SSH 拉取的机器看不到。</span>`;
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
        <td>${esc(sourceLabel(s.source))}</td>
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
