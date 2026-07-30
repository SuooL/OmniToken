"use strict";
// Live view: SSE-driven realtime state (contract: docs/API.md).

const Live = {
  es: null,
  data: null,
  timer: null,

  start() {
    if (this.es) return;
    const status = document.getElementById("live-status");
    this.es = Api.stream("/api/v1/stream");
    const onData = (ev) => {
      this.data = JSON.parse(ev.data);
      this.render();
      status.textContent = "实时连接中 · 更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    };
    this.es.addEventListener("snapshot", onData);
    this.es.addEventListener("live", onData);
    this.es.onerror = () => { status.textContent = "连接断开,自动重连中…"; };
    // Re-render periodically so relative times stay fresh between events.
    this.timer = setInterval(() => { if (this.data) this.render(); }, 10000);
  },

  stop() {
    if (this.es) { this.es.close(); this.es = null; }
    if (this.timer) { clearInterval(this.timer); this.timer = null; }
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
      ? `<b>${n}</b> 个会话在生成 · 近 10 分钟输出 <b>${compact(sp.output_tokens || 0)}</b>`
      : "近 10 分钟没有生成";

    this.renderLanes(sp);
  },

  // One row per concurrent stream, then the union. Overlap is the point: it
  // shows why three sessions at 50 tok/s is not 150 unless they took turns.
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
      el.innerHTML = `<p class="subtle">近 10 分钟没有会话在生成。开始使用 Claude,这里会实时出现。</p>`;
      document.getElementById("lane-note").textContent = "";
      return;
    }

    const rows = sessions.slice(0, 8).map((s, i) => `
      <div class="lane lane-${(i % 5) + 1}">
        <div class="who">${esc(s.repo ? repoLabel(s.repo).split("/").pop() : s.session_id.slice(0, 8))}
          <span class="sub">${esc(s.device)}</span></div>
        <div class="track">${blocks(s.spans)}</div>
        <div class="rate">${(s.tps || 0).toFixed(1)}<span class="u"> t/s</span></div>
      </div>`).join("");

    el.innerHTML = rows + `
      <div class="lane union">
        <div class="who">本机(并集)</div>
        <div class="track">${blocks(sp.spans)}</div>
        <div class="rate">${(sp.tps || 0).toFixed(1)}<span class="u"> t/s</span></div>
      </div>`;

    const busy = Math.round(((sp.active_ms || 0) / Math.max(1, span)) * 100);
    document.getElementById("lane-note").textContent =
      `窗口内 ${busy}% 的时间在生成`;
  },

  render() {
    const d = this.data;
    if (!d) return;

    this.renderQuotas(d.quotas || []);

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
          <span class="key"><span class="dot ${v.state}"></span>${esc(v.device)} <span class="extra">${this.stateLabel(v.state)}</span></span>
          <span class="val">${compact(v.today_tokens)} <span class="extra">今日</span></span>
        </div>
        <div class="row-head sub2"><span class="key">最后活动 ${relTime(v.last_ts)} · ${this.procLabel(v)}</span><span class="val extra">${full(v.today_events)} 请求</span></div>
      </div>`).join("") : `<span class="empty">暂无设备</span>`;

    const sesEl = document.getElementById("live-sessions");
    const ses = d.sessions || [];
    sesEl.innerHTML = ses.length ? `<table><thead><tr><th>设备</th><th>项目</th><th>模型</th><th>tokens</th><th>最后活动</th></tr></thead><tbody>` +
      ses.map((s) => `<tr>
        <td>${esc(s.device)}</td>
        <td title="${esc(s.cwd || "")}">${esc(this.trunc(repoLabel(s.repo, s.cwd), 36))}</td>
        <td>${esc(s.model)}</td>
        <td>${compact(s.tokens)}</td>
        <td>${relTime(s.last_ts)}</td>
      </tr>`).join("") + `</tbody></table>`
      : `<span class="empty">近 10 分钟无活跃会话</span>`;
  },

  // 5-hour window cards, one per billing channel (server: buildWindowCards).
  // Subscription windows use the provider's real reset boundary when known;
  // pay-per-use has no window, so it is a rolling look-back and says so.
  renderWindows(windows) {
    const el = document.getElementById("window-row");
    if (!windows.length) { el.innerHTML = ""; return; }
    el.innerHTML = windows.map((w) => {
      const spanMin = Math.max(1, Math.round((w.end_ms - w.start_ms) / 60000));
      const elapsed = `${Math.floor(spanMin / 60)}h${spanMin % 60}m`;
      // Bar length: quota % when authoritative, else consumption intensity
      // relative to a 5h reference so the bar still conveys "how hot".
      const pct = w.authoritative && w.used_percent
        ? Math.min(100, w.used_percent)
        : Math.min(100, 100 * w.tokens / 80e6);
      const badge = w.authoritative
        ? `<span class="chip authoritative">权威窗口</span>`
        : (w.placeholder ? `<span class="chip">占位 · 滚动 5h</span>` : `<span class="chip">滚动 5h</span>`);
      const bits = [];
      if (w.authoritative && w.used_percent) {
        const remain = Math.max(0, Math.round((w.resets_at - Date.now()) / 60000));
        bits.push(`配额 ${w.used_percent.toFixed(0)}%`);
        bits.push(`${Math.floor(remain / 60)}h${remain % 60}m 后重置`);
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
      return `
      <div class="stat-tile">
        <div class="label">${esc(w.label)} ${badge}</div>
        <div class="value">${w.tokens ? compact(w.tokens) : "0"}</div>
        <div class="sub">${bits.join(" · ")}</div>
        ${proj ? `<div class="sub proj${w.projected_percent >= 100 ? " over" : ""}">${esc(proj)}</div>` : ""}
        <div class="meter"><div class="meter-fill ${severity(pct)}" style="width:${pct.toFixed(1)}%"></div></div>
      </div>`;
    }).join("");
  },

  // Authoritative quota windows (ADR-0007): Anthropic's OAuth usage endpoint
  // and Codex's rate_limits. These are real numbers, not inferred — labelled
  // as such so they are never confused with the estimated block below.
  renderQuotas(quotas) {
    const el = document.getElementById("quota-row");
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
      el.innerHTML = `<span class="empty">${esc(reporters.map((r) => r.device).join("、"))} 上没有会话开着</span>`;
      return;
    }
    el.innerHTML = `<table><thead><tr><th>设备</th><th>工具</th><th>已开启</th><th>PID</th></tr></thead><tbody>` +
      sessions.map((s) => `<tr>
        <td>${esc(s.device)}</td>
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

  trunc(s, n) {
    s = String(s);
    return s.length > n ? "…" + s.slice(-(n - 1)) : s;
  },
};
