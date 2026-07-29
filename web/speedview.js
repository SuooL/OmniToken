"use strict";
// Speed view (F15): per-model tokens/sec distribution and TTFT.
//
// The two channels are rendered as separate sections and are never averaged
// together (requirements.md F15): log sources derive speed from the gap to the
// previous session event (ADR-0006) — approximate, TTFT unavailable — while the
// local proxy measures duration and TTFT directly. Every number therefore
// carries a visible 近似 / 精确 badge.
// Renders into #view-speed; single-hue bars reuse .bars / .data-table.

const SpeedView = {
  _timer: null,

  enter() {
    this.load();
    this._timer = setInterval(() => this.load(), 30000);
  },

  leave() {
    clearInterval(this._timer);
  },

  async load() {
    try {
      this.render(await Api.get("/api/v1/speed?days=30"));
      document.getElementById("refresh-note").textContent =
        "更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    } catch (e) {
      document.getElementById("refresh-note").textContent = "服务不可达,重试中…";
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

  // weightedMedian approximates the overall median from per-model medians,
  // weighting each by its sample count. Exact per-event quantiles live in the
  // store; this tile only needs the headline figure.
  weightedMedian(rows) {
    const xs = rows.filter((r) => r.samples > 0 && r.median_tps > 0)
      .sort((a, b) => a.median_tps - b.median_tps);
    const total = xs.reduce((s, r) => s + r.samples, 0);
    if (!total) return 0;
    let acc = 0;
    for (const r of xs) {
      acc += r.samples;
      if (acc * 2 >= total) return r.median_tps;
    }
    return xs[xs.length - 1].median_tps;
  },

  // fastest prefers models with enough samples to be meaningful; a single
  // lucky event should not be crowned the fastest model.
  fastest(rows) {
    let pool = rows.filter((r) => r.samples >= 5 && r.median_tps > 0);
    if (!pool.length) pool = rows.filter((r) => r.median_tps > 0);
    if (!pool.length) return null;
    return pool.reduce((a, b) => (b.median_tps > a.median_tps ? b : a));
  },

  // --- rendering ---

  render(d) {
    const root = document.getElementById("view-speed");
    const approx = d.approx || [];
    const exact = d.exact || [];
    const days = d.days || 30;
    root.innerHTML = `
      <section class="stat-row">${this.tiles(approx, exact)}</section>
      <section class="card">
        <div class="card-head">
          <h2>日志估算(近似) · Claude · 近 ${days} 天</h2>
          <span class="chip badge approx">近似 · 由日志间隔推算</span>
        </div>
        <p class="subtle">速度 = 输出 tokens ÷ 与会话内上一事件的间隔(ADR-0006)。该间隔含思考与工具等待,系统性低估真实生成速度,只适合模型之间横向比较,不能与下方代理数据混合平均。已排除 输出 &lt; 8 tokens 或时长未知的事件。<b>Codex 不在此列</b>:它的 token_count 日志在一轮结束后才写入(间隔中位仅 30ms),不反映生成耗时;要看 Codex 速度请启用下方本地代理。</p>
        <div class="data-table">${this.approxTable(approx)}</div>
        <h2 style="margin-top:14px">中位速度 · tok/s(近似)</h2>
        <div class="bars">${this.bars(approx)}</div>
      </section>
      <section class="card">
        <div class="card-head">
          <h2>本地代理(精确) · 近 ${days} 天</h2>
          <span class="chip badge exact">精确 · 代理实测</span>
        </div>
        ${this.exactBody(exact, d.has_exact)}
      </section>`;
  },

  tiles(approx, exact) {
    const overall = this.weightedMedian(approx);
    const top = this.fastest(approx);
    const proxySamples = exact.reduce((s, r) => s + r.samples, 0);
    const approxSamples = approx.reduce((s, r) => s + r.samples, 0);
    return `
      <div class="stat-tile">
        <div class="label">全局中位速度(近似)</div>
        <div class="value">${this.tps(overall)}<span class="unit"> tok/s</span></div>
        <div class="sub">日志来源 · ${full(approxSamples)} 个样本(近似,含等待)</div>
      </div>
      <div class="stat-tile">
        <div class="label">最快模型(近似)</div>
        <div class="value" style="font-size:18px">${top ? esc(top.model || "(未知)") : "—"}</div>
        <div class="sub">${top ? `中位 ${this.tps(top.median_tps)} tok/s · ${full(top.samples)} 样本` : "暂无数据"}</div>
      </div>
      <div class="stat-tile">
        <div class="label">代理样本数(精确)</div>
        <div class="value">${proxySamples ? full(proxySamples) : "未启用"}</div>
        <div class="sub">${proxySamples ? "实测生成耗时与 TTFT" : "无本地代理数据"}</div>
      </div>`;
  },

  approxTable(rows) {
    if (!rows.length) return `<p class="subtle">暂无数据,等待采集…</p>`;
    return `<table><thead><tr>
        <th>模型</th><th>中位 tok/s</th><th>P90 tok/s</th><th>均值 tok/s</th>
        <th>样本数</th><th>输出 tokens</th>
      </tr></thead><tbody>` +
      rows.map((r) => `<tr>
          <td>${esc(r.model || "(未知)")}</td>
          <td>${this.tps(r.median_tps)}</td>
          <td>${this.tps(r.p90_tps)}</td>
          <td>${this.tps(r.avg_tps)}</td>
          <td>${full(r.samples)}</td>
          <td>${compact(r.output_tokens)}</td>
        </tr>`).join("") + `</tbody></table>`;
  },

  bars(rows) {
    const xs = rows.filter((r) => r.median_tps > 0)
      .slice().sort((a, b) => b.median_tps - a.median_tps);
    if (!xs.length) return `<span class="empty">暂无数据,等待采集…</span>`;
    const max = xs[0].median_tps;
    return xs.map((r) => `<div class="row">
        <div class="row-head">
          <span class="key">${esc(r.model || "(未知)")}</span>
          <span class="val">${this.tps(r.median_tps)} tok/s <span class="extra">· P90 ${this.tps(r.p90_tps)} · ${full(r.samples)} 样本</span></span>
        </div>
        <div class="track"><div class="fill" style="width:${(100 * r.median_tps / max).toFixed(1)}%"></div></div>
      </div>`).join("");
  },

  exactBody(rows, hasExact) {
    if (!hasExact || !rows.length) {
      return `<p class="subtle">暂无本地代理数据。配置 agent 的 <code>proxy_listen</code>(如 <code>127.0.0.1:8899</code>)并把工具的 base_url 指向代理后,即可获得精确的生成耗时与 TTFT(首 token 延迟);详见 docs/configuration.md。</p>`;
    }
    return `
      <p class="subtle">生成耗时与 TTFT 由代理实测,速度可与真实体感对齐;TTFT 为首 token 延迟。同样已排除 输出 &lt; 8 tokens 的事件。</p>
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
