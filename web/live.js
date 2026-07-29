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
    document.getElementById("speed-tps").textContent =
      tps > 0 ? tps.toFixed(1) + " tok/s" : "—";
    document.getElementById("speed-sub").textContent = tps > 0
      ? `输出 ${compact(sp.output_tokens || 0)} · 生成中 ${((sp.active_ms || 0) / 1000).toFixed(0)}s`
      : "近 10 分钟没有生成";

    const rows = sp.sessions || [];
    document.getElementById("live-speed").innerHTML = rows.length ? rows.map((s) => `
      <div class="row">
        <div class="row-head">
          <span class="key">${esc(s.repo || s.session_id.slice(0, 8))}
            <span class="extra">${esc(s.device)}${s.model ? " · " + esc(s.model) : ""}</span></span>
          <span class="val">${(s.tps || 0).toFixed(1)} <span class="extra">tok/s</span></span>
        </div>
        <div class="extra">输出 ${compact(s.output_tokens)} · 生成中 ${((s.active_ms || 0) / 1000).toFixed(0)}s</div>
      </div>`).join("")
      : `<p class="subtle">近 10 分钟没有会话在生成。</p>`;
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

    const devEl = document.getElementById("live-devices");
    const devs = d.devices || [];
    devEl.innerHTML = devs.length ? devs.map((v) => `
      <div class="row dev-row">
        <div class="row-head">
          <span class="key"><span class="dot ${v.state}"></span>${esc(v.device)} <span class="extra">${this.stateLabel(v.state)}</span></span>
          <span class="val">${compact(v.today_tokens)} <span class="extra">今日</span></span>
        </div>
        <div class="row-head sub2"><span class="key">最后活动 ${relTime(v.last_ts)}</span><span class="val extra">${full(v.today_events)} 请求</span></div>
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

  // Consumption intensity → meter severity. The percentage or token figure is
  // always printed beside the bar, so colour only reinforces what the text says.
  intensityClass(pct) {
    if (pct >= 90) return " critical";
    if (pct >= 75) return " serious";
    if (pct >= 50) return " warn";
    return "";
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
        <div class="sub scope" title="${esc(w.note || "")}">${esc(w.scope)}</div>
        <div class="meter"><div class="meter-fill${this.intensityClass(pct)}" style="width:${pct.toFixed(1)}%"></div></div>
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
      const remain = q.remaining_minutes;
      const reset = remain > 0
        ? `${Math.floor(remain / 60)}h${remain % 60}m 后重置`
        : "即将重置";
      const scope = q.scope.startsWith("seven_day:") ? q.scope.slice(10) : "";
      return `
      <div class="stat-tile">
        <div class="label">${esc(q.source)} · ${esc(q.window_label)}${scope ? " · " + esc(scope) : ""}
          <span class="chip authoritative">权威</span></div>
        <div class="value">${pct.toFixed(0)}<span class="unit">%</span></div>
        <div class="sub">${reset} · 观测 ${relTime(q.observed_at)}</div>
        <div class="meter"><div class="meter-fill${this.intensityClass(pct)}" style="width:${pct}%"></div></div>
      </div>`;
    }).join("");
  },

  stateLabel(s) {
    return { active: "活跃", idle: "空闲", stale: "离线" }[s] || s;
  },

  trunc(s, n) {
    s = String(s);
    return s.length > n ? "…" + s.slice(-(n - 1)) : s;
  },
};
