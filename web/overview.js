"use strict";
// Overview view: stat tiles, daily stacked chart, breakdown bars.

const SERIES = [
  { key: "input_tokens",           label: "输入",     varName: "--series-1" },
  { key: "output_tokens",          label: "输出",     varName: "--series-2" },
  { key: "cache_read_tokens",      label: "缓存读取", varName: "--series-3" },
  { key: "cache_creation_tokens",  label: "缓存写入", varName: "--series-4" },
];

const Overview = {
  lastData: null,

  async load() {
    const root = document.getElementById("view-overview");
    if (!this.lastData) {
      renderState(root, { kind: "loading", title: "正在加载总览" });
    }
    try {
      this.lastData = await Api.get("/api/v1/overview?days=30");
      this.render(this.lastData);
      renderState(root, { kind: "ready", title: "" });
      document.getElementById("refresh-note").textContent =
        "更新于 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    } catch (e) {
      const issue = classifyAPIError(e);
      renderState(root, {
        kind: this.lastData ? "stale" : issue.kind,
        title: this.lastData ? "总览数据可能已过期" : issue.title,
        detail: issue.detail,
        action: { label: "重试", run: () => this.load() },
      });
      document.getElementById("refresh-note").textContent = this.lastData
        ? "刷新失败,正在显示上次数据" : issue.title;
    }
  },

  render(d) {
    this.renderStats(d);
    this.renderDaily(d.daily || []);
    this.renderTable(d.daily || []);
    const modelCosts = d.model_costs || {};
    const unpriced = new Set(d.unpriced_models || []);
    this.renderBars("by-model", d.by_model, (k) => k, (r) =>
      unpriced.has(r.key) ? "无定价" : (modelCosts[r.key] != null ? usd(modelCosts[r.key]) : ""));
    this.renderBars("by-device", d.by_device, (k) => k);
    const work = d.work_by_repo || {};
    this.renderBars("by-repo", d.by_repo, (k) => repoLabel(k), (r) => {
      const w = work[r.key];
      if (!w) return "";
      let s = hours(w.union_seconds);
      if (w.sum_seconds > w.union_seconds * 1.2) s += `(叠加 ${hours(w.sum_seconds)})`;
      return s;
    });
    this.renderChips(d);
    Heatmap.load(document.getElementById("heatmap"));
  },

  renderStats(d) {
    const tiles = [
      ["今日", d.today, "today"], ["本周", d.week, "week"], ["本月", d.month, "month"], ["累计", d.all_time, "all_time"],
    ];
    const costs = d.costs || {};
    document.getElementById("stat-row").innerHTML = tiles.map(([label, t, key]) => {
      const c = costs[key] || {};
      const parts = [];
      if (c.real_usd > 0) parts.push(`真实 ${usd(c.real_usd)}`);
      if (c.equivalent_usd > 0) parts.push(`等效 ${usd(c.equivalent_usd)}`);
      return `
      <div class="stat-tile">
        <div class="label">${label} tokens</div>
        <div class="value">${compact(t.total_tokens)}</div>
        <div class="sub">输出 ${compact(t.output_tokens)} · 请求 ${full(t.events)}</div>
        <div class="sub cost">${parts.join(" · ") || "&nbsp;"}</div>
      </div>`;
    }).join("");
  },

  renderDaily(daily) {
    const el = document.getElementById("daily-chart");
    document.getElementById("daily-legend").innerHTML = "";
    if (!daily.length) {
      el.innerHTML = `<p class="bars"><span class="empty">暂无数据,等待采集…</span></p>`;
      return;
    }
    // ECharts rather than the hand-rolled SVG this used to be (ADR-0010):
    // gradients, hover, and a shared tooltip are what the drawing code was
    // never going to be worth writing by hand.
    el.style.height = "300px";
    const chart = echartsFor(el);
    const days = this.fillDays(daily);
    chart.setOption({
      grid: { left: 8, right: 8, top: 28, bottom: 4, containLabel: true },
      tooltip: {
        trigger: "axis",
        axisPointer: { type: "shadow" },
        ...tooltipStyle(),
        valueFormatter: (v) => compact(v || 0),
      },
      legend: {
        top: 0, left: 0, itemWidth: 9, itemHeight: 9, itemGap: 14,
        textStyle: { color: cssVar("--text-secondary"), fontSize: 11, fontFamily: chartFont() },
      },
      xAxis: {
        type: "category",
        data: days.map((d) => d.bucket.slice(5)),
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
      series: SERIES.map((sname, i) => ({
        name: sname.label,
        type: "bar",
        stack: "tokens",
        barMaxWidth: 26,
        // Rounded only on the topmost segment, so the stack reads as one column.
        itemStyle: {
          color: gradientOf(cssVar(sname.varName)),
          borderRadius: i === SERIES.length - 1 ? [4, 4, 0, 0] : 0,
        },
        emphasis: { focus: "series" },
        data: days.map((d) => d[sname.key] || 0),
      })),
      animationDuration: 420,
      animationEasing: "cubicOut",
    }, true);
  },

  fillDays(daily) {
    const byDate = Object.fromEntries(daily.map((r) => [r.bucket, r]));
    const out = [];
    const end = new Date();
    for (let i = 29; i >= 0; i--) {
      const d = new Date(end.getFullYear(), end.getMonth(), end.getDate() - i);
      const key = d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
      out.push(byDate[key] || { bucket: key, events: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0, total_tokens: 0 });
    }
    return out;
  },

  roundedTop(x, y, w, h, r) {
    return `M${x},${y + h} L${x},${y + r} Q${x},${y} ${x + r},${y} L${x + w - r},${y} Q${x + w},${y} ${x + w},${y + r} L${x + w},${y + h} Z`;
  },

  niceCeil(v) {
    const mag = Math.pow(10, Math.floor(Math.log10(v)));
    for (const m of [1, 2, 2.5, 4, 5, 8, 10]) {
      if (m * mag >= v) return m * mag;
    }
    return 10 * mag;
  },

  attachTooltip(el, days) {
    const tip = document.getElementById("tooltip");
    el.querySelectorAll(".hit").forEach((hit) => {
      hit.addEventListener("mousemove", (ev) => {
        const row = days[+hit.dataset.i];
        tip.innerHTML = `<div class="t-date">${row.bucket}</div>` +
          SERIES.map((s) => `<div class="t-row"><span class="k"><span class="swatch" style="background:${cssVar(s.varName)}"></span>${s.label}</span><span class="v">${full(row[s.key])}</span></div>`).join("") +
          `<div class="t-row"><span class="k">合计</span><span class="v">${full(row.total_tokens)}</span></div>`;
        tip.hidden = false;
        const pad = 14;
        let tx = ev.clientX + pad, ty = ev.clientY + pad;
        const r = tip.getBoundingClientRect();
        if (tx + r.width > innerWidth - 8) tx = ev.clientX - r.width - pad;
        if (ty + r.height > innerHeight - 8) ty = ev.clientY - r.height - pad;
        tip.style.left = tx + "px";
        tip.style.top = ty + "px";
      });
      hit.addEventListener("mouseleave", () => { tip.hidden = true; });
    });
  },

  renderTable(daily) {
    const el = document.getElementById("daily-table");
    const rows = [...daily].reverse();
    el.innerHTML = `<table><thead><tr><th>日期</th><th>输入</th><th>输出</th><th>缓存读取</th><th>缓存写入</th><th>合计</th><th>请求</th></tr></thead><tbody>` +
      rows.map((r) => `<tr><td>${r.bucket}</td><td>${full(r.input_tokens)}</td><td>${full(r.output_tokens)}</td><td>${full(r.cache_read_tokens)}</td><td>${full(r.cache_creation_tokens)}</td><td>${full(r.total_tokens)}</td><td>${full(r.events)}</td></tr>`).join("") +
      `</tbody></table>`;
  },

  renderBars(id, rows, keyFmt, extraFmt) {
    const el = document.getElementById(id);
    rows = (rows || []).filter((r) => r.total_tokens > 0);
    if (!rows.length) { el.innerHTML = `<span class="empty">暂无数据</span>`; return; }
    const shown = rows.slice(0, 8);
    const rest = rows.slice(8);
    if (rest.length) {
      shown.push({
        key: `其他 (${rest.length})`, _raw: true,
        total_tokens: rest.reduce((a, r) => a + r.total_tokens, 0),
      });
    }
    const max = Math.max(...shown.map((r) => r.total_tokens));
    el.innerHTML = shown.map((r) => {
      const label = r._raw ? r.key : keyFmt(r.key);
      const extra = (!r._raw && extraFmt) ? extraFmt(r) : "";
      return `<div class="row">
        <div class="row-head"><span class="key" title="${esc(label)}">${esc(label)}</span><span class="val">${compact(r.total_tokens)}${extra ? ` <span class="extra">· ${esc(extra)}</span>` : ""}</span></div>
        <div class="track"><div class="fill" style="width:${(100 * r.total_tokens / max).toFixed(1)}%"></div></div>
      </div>`;
    }).join("");
  },

  renderChips(d) {
    const provider = (d.by_provider || []).map((r) => `<span class="chip">${esc(r.key || "unknown")} <b>${compact(r.total_tokens)}</b></span>`);
    const source = (d.by_source || []).map((r) => `<span class="chip">${esc(r.key)} <b>${compact(r.total_tokens)}</b></span>`);
    document.getElementById("chip-row").innerHTML = [...source, ...provider].join("");
  },
};

document.getElementById("table-toggle").addEventListener("click", (ev) => {
  const btn = ev.currentTarget;
  const on = btn.getAttribute("aria-pressed") !== "true";
  btn.setAttribute("aria-pressed", String(on));
  document.getElementById("daily-table").hidden = !on;
});
