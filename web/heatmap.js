"use strict";
// Activity heatmap (F20/GAP-2): GitHub-style calendar for the overview page.
// Not a view — a component the overview calls: Heatmap.render(el, days).
//
// Colour encodes magnitude, so the ramp is a single hue light→dark (never a
// rainbow, never hue-per-weekday). Each mode gets its own steps against its own
// surface rather than an automatic flip: on the dark page the ramp runs dark→
// light so "more" still reads as "further from the background".
const HEAT_LIGHT = ["#cde2fb", "#9ec5f4", "#5598e7", "#2a78d6", "#184f95"];
const HEAT_DARK = ["#184f95", "#256abf", "#3987e5", "#86b6ef", "#cde2fb"];

const CELL = 11, GAP = 3, STEP = CELL + GAP, RADIUS = 2;

const Heatmap = {
  span: 365,      // trailing days the grid covers
  _days: null,    // last payload, for repaint on colour-scheme change
  _at: 0,

  // load fetches and renders, reusing a recent payload so the overview's 30s
  // poll (and dark-mode repaints) don't re-scan a year of events each time.
  async load(el, span) {
    if (span) this.span = span;
    if (this._days && Date.now() - this._at < 60000) {
      this.render(el, this._days);
      return;
    }
    try {
      const d = await Api.get("/api/v1/heatmap?days=" + this.span);
      this._days = d.days || [];
      this._at = Date.now();
      this.render(el, this._days);
    } catch (e) {
      if (!this._days) el.innerHTML = `<p class="bars"><span class="empty">热力图加载失败,重试中…</span></p>`;
    }
  },

  render(el, days, span) {
    if (!el) return;
    const total = span || this.span;
    const byDate = Object.fromEntries((days || []).map((d) => [d.bucket, d]));
    const colors = this.isDark() ? HEAT_DARK : HEAT_LIGHT;
    const empty = cssVar("--grid");
    const muted = cssVar("--text-muted");
    const level = this.scale(days || []);

    const today = new Date();
    today.setHours(0, 0, 0, 0);
    const start = new Date(today.getFullYear(), today.getMonth(), today.getDate() - (total - 1));
    // Columns are ISO weeks: back up to the Monday on or before the first day.
    const first = new Date(start);
    first.setDate(first.getDate() - ((first.getDay() + 6) % 7));
    const cells = Math.round((today - first) / 86400000) + 1;
    const cols = Math.ceil(cells / 7);

    const padL = 30, padT = 17, padR = 6, legendH = 26;
    const W = padL + cols * STEP - GAP + padR;
    const H = padT + 7 * STEP - GAP + legendH;

    let grid = "", months = "", lastMonth = -1, lastLabelCol = -99;
    const readableDays = [];
    let active = 0, sum = 0;
    for (let i = 0; i < cells; i++) {
      const d = new Date(first.getFullYear(), first.getMonth(), first.getDate() + i);
      const col = Math.floor(i / 7), row = i % 7;
      if (d < start) continue; // leading pad before the window starts
      const key = this.key(d);
      const rec = byDate[key];
      const tokens = rec ? rec.tokens : 0;
      const events = rec ? rec.events : 0;
      readableDays.push({ key, tokens, events });
      if (tokens > 0) { active++; sum += tokens; }
      const l = level(tokens);
      const x = padL + col * STEP, y = padT + row * STEP;
      grid += `<rect x="${x}" y="${y}" width="${CELL}" height="${CELL}" rx="${RADIUS}" fill="${l ? colors[l - 1] : empty}"/>`;
      // Hit target overlaps the gap so the pointer never falls between cells.
      grid += `<rect class="heat-hit" x="${x - GAP / 2}" y="${y - GAP / 2}" width="${STEP}" height="${STEP}" fill="transparent"` +
        ` data-d="${key}" data-t="${tokens}" data-e="${events}" data-c="${l ? colors[l - 1] : empty}"/>`;
      // Month label on the first in-window column of each month.
      if (row === 0) {
        const m = d.getMonth();
        if (m !== lastMonth && col - lastLabelCol >= 3 && col < cols - 1) {
          months += `<text x="${x}" y="${padT - 6}" fill="${muted}">${m + 1} 月</text>`;
          lastMonth = m;
          lastLabelCol = col;
        }
      }
    }

    // Weekday rails: Mon/Wed/Fri only, so the labels never crowd the grid.
    let rails = "";
    [[0, "一"], [2, "三"], [4, "五"]].forEach(([row, label]) => {
      rails += `<text x="${padL - 6}" y="${padT + row * STEP + CELL - 1}" text-anchor="end" fill="${muted}">${label}</text>`;
    });

    // Legend inside the SVG: the ramp is the only thing that carries magnitude,
    // so it always ships with the grid.
    const ly = padT + 7 * STEP - GAP + 15;
    let legend = `<text x="${padL}" y="${ly + 8}" fill="${muted}">少</text>`;
    colors.forEach((c, i) => {
      legend += `<rect x="${padL + 22 + i * STEP}" y="${ly}" width="${CELL}" height="${CELL}" rx="${RADIUS}" fill="${c}"/>`;
    });
    legend += `<text x="${padL + 26 + colors.length * STEP}" y="${ly + 8}" fill="${muted}">多</text>`;
    const summary = active
      ? `${active} 天有活动 · 合计 ${compact(sum)} tokens`
      : `近 ${total} 天暂无数据`;
    legend += `<text x="${W - padR}" y="${ly + 8}" text-anchor="end" fill="${muted}">${esc(summary)}</text>`;

    const readableTable = `<details class="heatmap-details">
      <summary>逐日数据表</summary>
      <div class="data-table"><table>
        <thead><tr><th scope="col">日期</th><th scope="col">tokens</th><th scope="col">请求</th></tr></thead>
        <tbody>${readableDays.slice().reverse().map((day) => `<tr>
          <th scope="row">${esc(day.key)}</th>
          <td>${full(day.tokens)}</td>
          <td>${full(day.events)}</td>
        </tr>`).join("")}</tbody>
      </table></div>
    </details>`;

    el.innerHTML =
      `<div data-chart="calendar-activity"><svg viewBox="0 0 ${W} ${H}" width="${W}" height="${H}" role="img"` +
      ` style="display:block;width:100%;height:auto"` +
      ` aria-label="近 ${total} 天活动日历热力图,${esc(summary)}">` +
      `${months}${rails}${grid}${legend}</svg></div>` + readableTable;
    this.attachTooltip(el);
  },

  // scale bins non-zero days into 5 steps by quantile, so a couple of outlier
  // days can't flatten a year of ordinary ones into the palest step. Falls back
  // to a linear split of the max when there aren't enough distinct values for
  // quantiles to be meaningful.
  scale(days) {
    const vals = days.map((d) => d.tokens).filter((v) => v > 0).sort((a, b) => a - b);
    if (!vals.length) return () => 0;
    const q = (p) => vals[Math.min(vals.length - 1, Math.floor(p * vals.length))];
    let th = [q(0.2), q(0.4), q(0.6), q(0.8)];
    if (new Set(th).size < 4) {
      const max = vals[vals.length - 1];
      th = [1, 2, 3, 4].map((i) => (max * i) / 5);
    }
    return (v) => {
      if (v <= 0) return 0;
      let l = 1;
      for (const t of th) if (v > t) l++;
      return Math.min(l, 5);
    };
  },

  key(d) {
    return d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
  },

  // A4 is dark-only. The chart ramp follows the product surface rather than
  // the host OS preference, otherwise a light OS selects a low-contrast ramp
  // against the fixed navy cards.
  isDark() {
    return true;
  },

  // One delegated listener rather than ~365 per-cell ones; reuses the shared
  // #tooltip element and .tooltip styles.
  attachTooltip(el) {
    const tip = document.getElementById("tooltip");
    if (!tip) return;
    el.onmousemove = (ev) => {
      const hit = ev.target.closest ? ev.target.closest(".heat-hit") : null;
      if (!hit) { tip.hidden = true; return; }
      const tokens = +hit.dataset.t, events = +hit.dataset.e;
      tip.innerHTML =
        `<div class="t-date">${hit.dataset.d}</div>` +
        `<div class="t-row"><span class="k"><span class="swatch" style="background:${hit.dataset.c}"></span>tokens</span><span class="v">${full(tokens)}</span></div>` +
        `<div class="t-row"><span class="k">请求</span><span class="v">${full(events)}</span></div>` +
        (tokens ? "" : `<div class="t-row"><span class="k">无活动</span><span class="v"></span></div>`);
      tip.hidden = false;
      const pad = 14;
      let tx = ev.clientX + pad, ty = ev.clientY + pad;
      const r = tip.getBoundingClientRect();
      if (tx + r.width > innerWidth - 8) tx = ev.clientX - r.width - pad;
      if (ty + r.height > innerHeight - 8) ty = ev.clientY - r.height - pad;
      tip.style.left = tx + "px";
      tip.style.top = ty + "px";
    };
    el.onmouseleave = () => { tip.hidden = true; };
  },
};
