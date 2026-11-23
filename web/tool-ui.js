"use strict";

// Compact tool-surface primitives. All provider payloads remain escaped;
// these helpers format read-only observations and never change stored facts.
const SourceUI = {
  identities: {
    "claude-code": { label: "Claude", color: "--tool-claude", logo: "claude" },
    claude: { label: "Claude", color: "--tool-claude", logo: "claude" },
    codex: { label: "Codex", color: "--tool-codex", logo: "codex" },
    dsh: { label: "DeepSeek", color: "--tool-dsh", logo: "deepseek" },
    antigravity: { label: "Antigravity", color: "--tool-antigravity", logo: "antigravity" },
    gemini: { label: "Gemini", color: "--tool-gemini", logo: "gemini" },
  },
  identity(source) {
    return (
      (Object.hasOwn(this.identities, source) ? this.identities[source] : null) || {
        label: source == null || source === "" ? "未知来源" : String(source),
        color: "--source-api",
      }
    );
  },
  label(source) {
    return this.identity(source).label;
  },
  color(source) {
    return cssVar(this.identity(source).color);
  },
  brand(source) {
    const item = this.identity(source);
    return `<span class="source-brand" data-source="${esc(source)}">${item.logo ? `<img src="icons/${item.logo}.svg" width="18" height="18" alt="">` : '<span aria-hidden="true">◇</span>'}<span>${esc(item.label)}</span></span>`;
  },
};

const QuotaUI = {
  severity(value) {
    return value >= 90
      ? "critical"
      : value >= 80
        ? "serious"
        : value >= 70
          ? "warn"
          : "normal";
  },
  hint(value) {
    return value >= 100
      ? "已满"
      : value >= 90
        ? "紧张"
        : value >= 80
          ? "较高"
          : value >= 70
            ? "留意"
            : "";
  },
  minutes(row) {
    if (Number.isFinite(row.window_minutes)) return row.window_minutes;
    const key = `${row.scope || ""} ${row.key || ""}`;
    return /week|seven_day|10080/.test(key)
      ? 10080
      : /five_hour|primary|300|5h/.test(key)
        ? 300
        : 0;
  },
  // An authoritative window has a valid zero even when Go's omitempty omitted
  // used_percent. A placeholder window is never evidence of a zero reading.
  normalize(windows = [], quotas = []) {
    windows = windows.map((row) =>
      row.kind === "subscription" &&
      !row.source &&
      ["claude-code", "codex", "dsh", "antigravity", "gemini"].includes(row.key)
        ? { ...row, source: row.key }
        : row,
    );
    const rows = [];
    const groups = new Set();
    for (const row of windows)
      if (row.source && row.kind === "subscription") groups.add(row.source);
    for (const row of quotas) if (row.source) groups.add(row.source);
    for (const source of groups) {
      for (const minutes of [300, 10080]) {
        const candidates = quotas.filter(
          (q) =>
            q.source === source &&
            this.minutes(q) === minutes &&
            Number.isFinite(q.used_percent) &&
            q.used_percent >= 0,
        );
        const card = windows.find(
          (w) =>
            w.source === source &&
            w.kind === "subscription" &&
            this.minutes(w) === minutes &&
            w.authoritative,
        );
        const strictest = candidates.reduce(
          (best, row) =>
            !best || row.used_percent > best.used_percent ? row : best,
          null,
        );
        const chosen =
          strictest &&
          (!card || strictest.used_percent >= (card.used_percent || 0))
            ? strictest
            : card;
        const value = chosen
          ? Number.isFinite(chosen.used_percent) && chosen.used_percent >= 0
            ? chosen.used_percent
            : chosen.used_percent === undefined && chosen.authoritative
              ? 0
              : null
          : null;
        const matchingCard =
          card &&
          (chosen === card || (card.scope || "") === (chosen?.scope || ""))
            ? card
            : null;
        let remaining =
          matchingCard &&
          Number.isFinite(matchingCard.remaining_tokens) &&
          matchingCard.remaining_tokens >= 0
            ? matchingCard.remaining_tokens
            : null;
        // A missing field becomes zero only with a known capacity or exhausted
        // official reading; missing estimates otherwise stay unknown.
        if (
          matchingCard &&
          remaining == null &&
          Number.isFinite(matchingCard.capacity_tokens) &&
          matchingCard.capacity_tokens > 0 &&
          matchingCard.remaining_tokens === undefined
        )
          remaining = 0;
        if (value >= 100) remaining = 0;
        rows.push({
          source,
          minutes,
          value,
          remaining,
          scope: chosen?.scope || "",
          reset: chosen?.resets_at || matchingCard?.resets_at || null,
          observed: chosen?.observed_at || matchingCard?.observed_at || null,
          card: matchingCard,
        });
      }
      const five = rows.find((r) => r.source === source && r.minutes === 300);
      const week = rows.find((r) => r.source === source && r.minutes === 10080);
      if (week.remaining === 0 && five.value !== null) five.remaining = 0;
      else if (five.remaining != null && week.remaining != null)
        five.remaining = Math.min(five.remaining, week.remaining);
    }
    return rows;
  },
  countdown(deadline) {
    if (!Number.isFinite(deadline) || deadline <= 0) return "未知";
    const minutes = Math.ceil(Math.max(0, deadline - Date.now()) / 60000);
    return `${Math.floor(minutes / 60)}h${minutes % 60}m`;
  },
  elapsed(row, now = Date.now()) {
    if (row.value == null || !Number.isFinite(row.reset) || row.reset <= now || !(row.minutes > 0)) return null;
    return Math.max(0, Math.min(100, 100 * (1 - (row.reset - now) / (row.minutes * 60000))));
  },
  scopeLabel(scope) {
    const s = String(scope || "").trim();
    const map = {
      "gemini-5h": "Gemini",
      "gemini-weekly": "Gemini",
      "gemini-argon-wtu-weekly": "Gemini 4 Argon",
      "3p-5h": "Claude / 第三方",
      "3p-weekly": "Claude / 第三方",
      seven_day_opus: "opus",
      seven_day_sonnet: "sonnet",
    };
    if (map[s]) return map[s];
    if (/:/.test(s)) return s.split(":").slice(1).join(":");
    return "";
  },
  bucketsFor(source, quotas = []) {
    const order = {
      "gemini-5h": 1,
      "gemini-weekly": 2,
      "gemini-argon-wtu-weekly": 3,
      "3p-5h": 4,
      "3p-weekly": 5,
    };
    const byScope = new Map();
    for (const q of quotas || []) {
      if (!q || q.source !== source || !Number.isFinite(q.used_percent) || q.used_percent < 0) continue;
      const scope = q.scope || "";
      const minutes = this.minutes(q);
      const key = `${minutes}:${scope}`;
      const prev = byScope.get(key);
      if (!prev || q.used_percent > prev.used_percent) {
        byScope.set(key, {
          scope,
          minutes,
          value: q.used_percent,
          reset: q.resets_at || null,
          observed: q.observed_at || null,
        });
      }
    }
    return [...byScope.values()].sort((a, b) => {
      const ra = order[a.scope] || 99;
      const rb = order[b.scope] || 99;
      if (ra !== rb) return ra - rb;
      if (a.minutes !== b.minutes) return a.minutes - b.minutes;
      return String(a.scope).localeCompare(String(b.scope));
    });
  },
  hasDetailBuckets(source, quotas = []) {
    const buckets = this.bucketsFor(source, quotas);
    if (buckets.length > 2) return true;
    const perWindow = new Map();
    for (const b of buckets) {
      perWindow.set(b.minutes, (perWindow.get(b.minutes) || 0) + 1);
      if (perWindow.get(b.minutes) > 1) return true;
    }
    return source === "antigravity" && buckets.length > 0;
  },
  renderSourceDetail(source, quotas = []) {
    const buckets = this.bucketsFor(source, quotas);
    if (!buckets.length) return '<p class="empty">暂无官方配额分桶读数</p>';
    const body = buckets
      .map((row) => {
        const risk = this.severity(row.value);
        const text = `${row.value.toFixed(0)}%`;
        const hint = this.hint(row.value);
        const elapsed = this.elapsed(row);
        const timeLabel = elapsed == null ? "" : `时间已过 ${elapsed.toFixed(0)}%`;
        const timeMarker =
          elapsed == null
            ? ""
            : `<i class="quota-time-marker" style="left:${Number(elapsed.toFixed(2))}%" tabindex="0" role="img" title="${timeLabel}" aria-label="${timeLabel}"></i>`;
        const win = row.minutes === 300 ? "5h" : row.minutes === 10080 ? "Week" : `${row.minutes || "?"}m`;
        const pool = this.scopeLabel(row.scope) || row.scope || "默认";
        const observed = row.observed ? `观测 ${relTime(row.observed)}` : "观测时间未知";
        return `<tr data-risk="${risk}" class="quota-data"><th scope="row">${esc(pool)} · ${win}</th><td class="quota-used"><span class="quota-percent">${text}</span>${hint ? ` <small>${hint}</small>` : ""}<div class="quota-track" role="progressbar" aria-label="${esc(pool)} ${win} 官方已用" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.min(row.value, 100)}" aria-valuetext="${row.value}% ${hint}${timeLabel ? ` · ${timeLabel}` : ""}"><div style="width:${Math.min(row.value, 100)}%"></div>${timeMarker}</div></td><td><span>${this.countdown(row.reset)}</span><span class="quota-observed">${observed}</span></td></tr>`;
      })
      .join("");
    return `<div class="quota-table-shell"><table class="tool-quota-table"><thead><tr><th>配额池 / 窗口</th><th>官方已用</th><th>距重置</th></tr></thead><tbody>${body}</tbody></table></div><p class="subtle quota-note">来自官方 statusLine 实时上报；蓝色刻度表示当前窗口已过去的时间比例。</p>`;
  },
  bindSourceDetail(container, getQuotas) {
    if (!container || container.dataset.quotaDetailBound === "1") return;
    container.dataset.quotaDetailBound = "1";
    container.addEventListener("click", (event) => {
      const trigger = event.target.closest("[data-quota-source-detail]");
      if (!trigger || typeof DetailDrawer === "undefined") return;
      const source = trigger.dataset.quotaSourceDetail;
      const quotas = typeof getQuotas === "function" ? getQuotas() : [];
      DetailDrawer.open({
        key: `quota-source:${source}`,
        title: `${SourceUI.label(source)} · 官方配额分组`,
        subtitle: "各模型池官方已用百分比与重置倒计时",
        html: this.renderSourceDetail(source, quotas),
        trigger,
      });
    });
  },
  render(windows = [], quotas = []) {
    const rows = this.normalize(windows || [], quotas || []);
    const sources = [...new Set(rows.map((row) => row.source))];
    const groups = sources
      .map((source) => {
        const hasDetail = this.hasDetailBuckets(source, quotas);
        const head = hasDetail
          ? `<button type="button" class="tool-row-action quota-source-trigger" data-quota-source-detail="${esc(source)}" data-focus-key="quota-source:${esc(source)}" aria-label="查看 ${esc(SourceUI.label(source))} 各分组官方配额">${SourceUI.brand(source)}<span class="quota-group-more">分组详情 ›</span></button>`
          : SourceUI.brand(source);
        return `<tbody><tr class="quota-source"><th colspan="4" scope="rowgroup">${head}</th></tr>${rows
          .filter((r) => r.source === source)
          .map((row) => {
            const known = row.value !== null;
            const risk = known ? this.severity(row.value) : "missing";
            const text = known ? `${row.value.toFixed(0)}%` : "暂无";
            const hint = known ? this.hint(row.value) : "";
            const elapsed = this.elapsed(row);
            const timeLabel = elapsed == null ? "" : `时间已过 ${elapsed.toFixed(0)}%`;
            const timeMarker = elapsed == null ? "" : `<i class="quota-time-marker" style="left:${Number(elapsed.toFixed(2))}%" tabindex="0" role="img" title="${timeLabel}" aria-label="${timeLabel}"></i>`;
            const scopeName = this.scopeLabel(row.scope);
            const scope = scopeName ? ` · ${esc(scopeName)}` : "";
            const observed = row.observed
              ? `观测 ${relTime(row.observed)}`
              : "观测时间未知";
            return `<tr data-risk="${risk}" class="quota-data"><th scope="row">${row.minutes === 300 ? "5h" : "Week"}${scope}</th><td class="quota-used"><span class="quota-percent">${text}</span>${hint ? ` <small>${hint}</small>` : ""}<div class="quota-track${known ? "" : " quota-unknown"}" ${known ? `role="progressbar" aria-label="${esc(SourceUI.label(source))} ${row.minutes === 300 ? "5h" : "Week"} 官方已用" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.min(row.value, 100)}" aria-valuetext="${row.value}% ${hint}${timeLabel ? ` · ${timeLabel}` : ""}"` : 'role="img" aria-label="官方配额暂无读数"'}><div style="width:${known ? Math.min(row.value, 100) : 0}%"></div>${timeMarker}</div></td><td ${row.remaining == null ? "" : `data-remaining="${row.remaining}"`}>${row.remaining == null ? "暂无估算" : compact(row.remaining)}</td><td><span>${this.countdown(row.reset)}</span><span class="quota-observed">${observed}</span></td></tr>`;
          })
          .join("")}</tbody>`;
      })
      .join("");
    const metered = (windows || []).filter((w) => w.kind !== "subscription");
    const spend = metered.length
      ? `<details class="tool-analysis quota-spend" data-disclosure-key="quota-spend"><summary>按量计费 · 无配额窗口</summary><div class="tool-analysis-body">${metered.map((row) => `<div class="tool-kv"><span>${esc(row.label || "其他通道")}</span><span>${compact(row.tokens || 0)} tokens${Number.isFinite(row.cost_usd) ? ` · ${usd(row.cost_usd)}` : ""}</span></div>`).join("")}</div></details>`
      : "";
    const projections = rows.filter(
      (r) =>
        r.card &&
        Number.isFinite(r.card.projected_percent) &&
        r.card.projected_tokens > 0,
    );
    const forecast = projections.length
      ? `<details class="tool-analysis quota-projections" data-disclosure-key="quota-projection"><summary>窗口末用量估算</summary><div class="tool-analysis-body">${projections.map((row) => `<div class="tool-kv"><span>${SourceUI.brand(row.source)} ${row.minutes === 300 ? "5h" : "Week"}</span><span>约 ${row.card.projected_percent.toFixed(0)}% · ${compact(row.card.projected_tokens)} tokens</span></div>`).join("")}<p class="subtle">根据已观测速率外推，非官方读数。</p></div></details>`
      : "";
    return `${rows.length ? `<div class="quota-table-shell"><table class="tool-quota-table"><caption class="sr-only">官方已用百分比、预估剩余 tokens 与重置倒计时</caption><thead><tr><th>窗口</th><th>官方已用</th><th>预估余量</th><th>距重置</th></tr></thead>${groups}</table></div><p class="subtle quota-note">余量单位 tokens · 预估 5h 可用量受周余量约束</p>` : '<p class="empty">官方配额暂无读数</p>'}${spend}${forecast}`;
  },
};

const ToolUI = {
  analysis(id, title, html) {
    return `<details class="tool-analysis" id="${esc(id)}"><summary>${esc(title)}</summary><div class="tool-analysis-body">${html}</div></details>`;
  },
  updateHTML(el, html) {
    if (!el || el.innerHTML === html) return;
    const open = new Set(
      [...el.querySelectorAll("details")]
        .filter((node) => node.open)
        .map(
          (node) =>
            node.id || node.dataset.chartFallback || node.dataset.disclosureKey,
        )
        .filter(Boolean),
    );
    const focused = el.contains(document.activeElement)
      ? document.activeElement
      : null;
    let key =
      focused &&
      (focused.id
        ? `#${CSS.escape(focused.id)}`
        : focused.dataset.focusKey
          ? `[data-focus-key="${CSS.escape(focused.dataset.focusKey)}"]`
          : focused.dataset.deviceKey
            ? `[data-device-key="${CSS.escape(focused.dataset.deviceKey)}"]`
            : focused.dataset.eventKey
              ? `[data-event-key="${CSS.escape(focused.dataset.eventKey)}"]`
              : null);
    if (!key && focused?.tagName === "SUMMARY") {
      const parent = focused.closest("details");
      if (parent?.id) key = `#${CSS.escape(parent.id)} > summary`;
      else if (parent?.dataset.disclosureKey)
        key = `[data-disclosure-key="${CSS.escape(parent.dataset.disclosureKey)}"] > summary`;
      else if (parent?.dataset.chartFallback)
        key = `[data-chart-fallback="${CSS.escape(parent.dataset.chartFallback)}"] > summary`;
    }
    const scroll = el.scrollTop;
    el.innerHTML = html;
    el.querySelectorAll("details").forEach((node) => {
      const key =
        node.id || node.dataset.chartFallback || node.dataset.disclosureKey;
      if (key && open.has(key)) node.open = true;
    });
    el.scrollTop = scroll;
    if (key) el.querySelector(key)?.focus({ preventScroll: true });
  },
};

// Only GET snapshots; the live view still owns its one active SSE connection.
const LiveSnapshotCache = {
  data: null,
  receivedAt: 0,
  error: null,
  promise: null,
  generation: 0,
  adopt(data) {
    const currentTime = this.data?.generated_at ?? this.data?.generated_at_ms;
    const nextTime = data?.generated_at ?? data?.generated_at_ms;
    if (
      Number.isFinite(currentTime) &&
      Number.isFinite(nextTime) &&
      nextTime < currentTime
    )
      return this.data;
    this.data = data;
    this.receivedAt = Date.now();
    this.error = null;
    return data;
  },
  invalidate() {
    this.generation++;
    this.promise = null;
    this.data = null;
    this.receivedAt = 0;
  },
  load({ force = false } = {}) {
    if (this.promise) return this.promise;
    if (!force && this.data && Date.now() - this.receivedAt < 2000)
      return Promise.resolve(this.data);
    const generation = ++this.generation;
    this.promise = Api.get("/api/v1/live")
      .then((data) => {
        if (generation !== this.generation)
          throw new Error("读取凭据或查询已变更；旧响应已丢弃");
        return this.adopt(data);
      })
      .catch((error) => {
        if (generation === this.generation) {
          this.error = error;
          if (error?.status === 401 || error?.status === 403) {
            this.data = null;
            this.receivedAt = 0;
          }
        }
        throw error;
      })
      .finally(() => {
        if (generation === this.generation) this.promise = null;
      });
    return this.promise;
  },
};
