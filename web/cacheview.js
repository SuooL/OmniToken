"use strict";
// Cache view (F16): hit rate, dollars saved by cache reads, TTL write split.
// Renders into #view-cache; single-hue visuals reuse .bars / .data-table.

function pct(r) {
  if (!isFinite(r) || r <= 0) return "0%";
  if (r >= 0.995) return "100%";
  return (r * 100).toFixed(1) + "%";
}

const CacheView = {
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
      this.render(await Api.get("/api/v1/cache?days=30"));
      document.getElementById("refresh-note").textContent =
        "更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    } catch (e) {
      document.getElementById("refresh-note").textContent = "服务不可达,重试中…";
    }
  },

  render(d) {
    const root = document.getElementById("view-cache");
    const t = d.totals || {};
    const unpriced = new Set(d.unpriced || []);
    root.innerHTML = `
      <section class="stat-row">${this.tiles(t)}</section>
      <section class="card">
        <h2>按模型 · 近 ${d.days || 30} 天</h2>
        <div class="data-table">${this.modelTable(d.models || [], unpriced)}</div>
      </section>
      <section class="card">
        <h2>每日命中率 · 缓存读取 / (缓存读取 + 输入)</h2>
        <div class="bars">${this.dailyBars(d.daily || [])}</div>
      </section>`;
  },

  tiles(t) {
    const writes = (t.cache_1h_tokens || 0) + (t.cache_5m_tokens || 0);
    const split = writes > 0
      ? `${pct(t.cache_1h_tokens / writes)} / ${pct(t.cache_5m_tokens / writes)}`
      : "—";
    const splitSub = writes > 0
      ? `1h ${compact(t.cache_1h_tokens)} · 5m ${compact(t.cache_5m_tokens)}`
      : "日志无 TTL 分量";
    return `
      <div class="stat-tile">
        <div class="label">总命中率(近 30 天)</div>
        <div class="value">${pct(t.hit_rate || 0)}</div>
        <div class="sub">缓存读取 / (缓存读取 + 输入)</div>
      </div>
      <div class="stat-tile">
        <div class="label">缓存节省(等效)</div>
        <div class="value">${usd(t.saved_usd || 0)}</div>
        <div class="sub">按 输入价 − 缓存读取价 折算</div>
      </div>
      <div class="stat-tile">
        <div class="label">缓存写入 1h / 5m 占比</div>
        <div class="value">${split}</div>
        <div class="sub">${splitSub}</div>
      </div>`;
  },

  modelTable(models, unpriced) {
    models = models.filter((m) => m.input_tokens + m.cache_read_tokens + m.cache_creation_tokens > 0);
    if (!models.length) return `<span class="empty">暂无数据</span>`;
    return `<table><thead><tr>
        <th>模型</th><th>命中率</th><th>缓存读取</th><th>缓存写入</th>
        <th>1h / 5m</th><th>输入</th><th>请求</th><th>节省</th>
      </tr></thead><tbody>` +
      models.map((m) => {
        const ttl = m.cache_1h_tokens + m.cache_5m_tokens > 0
          ? `${compact(m.cache_1h_tokens)} / ${compact(m.cache_5m_tokens)}` : "—";
        const saved = unpriced.has(m.model) ? "无定价" : usd(m.saved_usd || 0);
        return `<tr>
          <td>${esc(m.model || "(未知)")}</td>
          <td>${pct(m.hit_rate)}</td>
          <td>${full(m.cache_read_tokens)}</td>
          <td>${full(m.cache_creation_tokens)}</td>
          <td>${ttl}</td>
          <td>${full(m.input_tokens)}</td>
          <td>${full(m.events)}</td>
          <td>${saved}</td>
        </tr>`;
      }).join("") + `</tbody></table>`;
  },

  dailyBars(daily) {
    daily = daily.filter((r) => r.input_tokens + r.cache_read_tokens > 0);
    if (!daily.length) return `<span class="empty">暂无数据,等待采集…</span>`;
    return daily.map((r) => `<div class="row">
        <div class="row-head">
          <span class="key">${esc(r.bucket)}</span>
          <span class="val">${pct(r.hit_rate)} <span class="extra">· 读 ${compact(r.cache_read_tokens)} / 入 ${compact(r.input_tokens)}</span></span>
        </div>
        <div class="track"><div class="fill" style="width:${(100 * r.hit_rate).toFixed(1)}%"></div></div>
      </div>`).join("");
  },
};
