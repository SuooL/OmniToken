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
  renderEstimatedGroups(quotaGroups = []) {
    return (quotaGroups || [])
      .filter((g) => g && g.source)
      .map((g) => {
        const source = g.source;
        const label = g.label || SourceUI.label(source);
        const configured = Number(g.budget_configured_groups) || 0;
        const windows = [
          {
            key: "five_hour",
            label: "5h",
            rolling: "滚动 5h",
            tokens: g.five_hour_tokens || 0,
            usedPercent: g.five_hour_used_percent,
            remaining: g.five_hour_remaining_tokens,
            budget: g.five_hour_budget_tokens || 0,
          },
          {
            key: "weekly",
            label: "Week",
            rolling: "滚动 7d",
            tokens: g.weekly_tokens || 0,
            usedPercent: g.weekly_used_percent,
            remaining: g.weekly_remaining_tokens,
            budget: g.weekly_budget_tokens || 0,
          },
        ];
        const rowsHTML = windows
          .map((w) => {
            const hasBudget =
              w.budget > 0 && Number.isFinite(w.usedPercent) && w.usedPercent >= 0;
            const risk = hasBudget ? this.severity(w.usedPercent) : "estimate";
            const usedCell = hasBudget
              ? `<span class="quota-percent">约 ${w.usedPercent.toFixed(0)}%</span> <small>估算</small><div class="quota-track quota-track-estimated" role="progressbar" aria-label="${esc(label)} ${w.label} 滚动估算已用" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.min(w.usedPercent, 100)}" aria-valuetext="约 ${w.usedPercent.toFixed(0)}% · 已用 ${compact(w.tokens)} tokens"><div style="width:${Math.min(w.usedPercent, 100)}%"></div></div><span class="quota-observed">已用 ${compact(w.tokens)}</span>`
              : `<span class="quota-percent">${compact(w.tokens)}</span><span class="quota-observed">滚动累计 tokens</span>`;
            const remCell = Number.isFinite(w.remaining)
              ? `<td data-remaining="${w.remaining}">${compact(w.remaining)}</td>`
              : `<td><span class="quota-observed">未设上限</span></td>`;
            return `<tr data-risk="${risk}" data-quota-group="${esc(source)}" class="quota-data quota-estimate-row"><th scope="row">${w.label}</th><td class="quota-used">${usedCell}</td>${remCell}<td><span>${w.rolling}</span><span class="quota-observed">${configured > 0 ? `${configured}/3 组已设上限` : "点击看分组"}</span></td></tr>`;
          })
          .join("");
        return `<tbody class="quota-estimate-body"><tr class="quota-source quota-group-head"><th colspan="4" scope="rowgroup"><button type="button" class="tool-row-action quota-group-trigger" data-quota-group="${esc(source)}" data-focus-key="quota-group:${esc(source)}" aria-label="查看 ${esc(label)} 模型分组用量详情">${SourceUI.brand(source)}<span class="quota-estimate-badge">滚动估算</span><span class="quota-group-more">分组详情 ›</span></button></th></tr>${rowsHTML}</tbody>`;
      })
      .join("");
  },
  renderGroupDetail(group) {
    if (!group) return '<p class="empty">分组用量不可用</p>';
    const groups = Array.isArray(group.groups) ? group.groups : [];
    const summaryTile = (title, tokens, usedPct, remaining, budget) => {
      const budgetNote =
        budget > 0 && Number.isFinite(remaining)
          ? `<div class="subtle">参考上限 ${compact(budget)} · 预估余量 ${compact(remaining)}${Number.isFinite(usedPct) ? `（约 ${usedPct.toFixed(0)}%）` : ""}</div>`
          : `<div class="subtle">未设参考上限 · 仅统计滚动已用</div>`;
      return `<div class="quota-detail-tile"><div class="subtle">${title}</div><div class="quota-detail-fig">${compact(tokens || 0)} <span class="subtle">tokens</span></div>${budgetNote}</div>`;
    };
    const summaryHTML = `<div class="quota-detail-summary">${summaryTile("近 5h 滚动汇总", group.five_hour_tokens, group.five_hour_used_percent, group.five_hour_remaining_tokens, group.five_hour_budget_tokens)}${summaryTile("近 7d (Week) 汇总", group.weekly_tokens, group.weekly_used_percent, group.weekly_remaining_tokens, group.weekly_budget_tokens)}</div>`;
    const groupsHTML = groups
      .map((entry) => {
        const has5hBudget =
          entry.five_hour_budget_tokens > 0 &&
          Number.isFinite(entry.five_hour_used_percent);
        const has7dBudget =
          entry.weekly_budget_tokens > 0 &&
          Number.isFinite(entry.weekly_used_percent);
        const barPct = has5hBudget
          ? Math.min(100, Math.max(0, entry.five_hour_used_percent))
          : Number.isFinite(entry.five_hour_share_percent)
            ? Math.min(100, Math.max(0, entry.five_hour_share_percent))
            : Number.isFinite(entry.weekly_share_percent)
              ? Math.min(100, Math.max(0, entry.weekly_share_percent))
              : 0;
        const metaParts = [];
        if (has5hBudget) {
          metaParts.push(
            `5h 约 ${entry.five_hour_used_percent.toFixed(0)}%（余 ${compact(entry.five_hour_remaining_tokens ?? 0)} / 上限 ${compact(entry.five_hour_budget_tokens)}）`,
          );
        } else if (Number.isFinite(entry.five_hour_share_percent)) {
          metaParts.push(`5h 占比 ${entry.five_hour_share_percent.toFixed(1)}%`);
        }
        if (has7dBudget) {
          metaParts.push(
            `Week 约 ${entry.weekly_used_percent.toFixed(0)}%（余 ${compact(entry.weekly_remaining_tokens ?? 0)} / 上限 ${compact(entry.weekly_budget_tokens)}）`,
          );
        } else if (Number.isFinite(entry.weekly_share_percent)) {
          metaParts.push(`Week 占比 ${entry.weekly_share_percent.toFixed(1)}%`);
        }
        const models = Array.isArray(entry.models) ? entry.models : [];
        const modelsHTML = models.length
          ? `<div class="data-table-shell"><table><thead><tr><th>模型</th><th class="numeric">5h</th><th class="numeric">Week</th><th class="numeric">输入 / 输出 / 缓存 (7d)</th></tr></thead><tbody>${models
              .map(
                (m) =>
                  `<tr><td>${esc(m.model)}</td><td class="numeric">${compact(m.five_hour_tokens || 0)}</td><td class="numeric">${compact(m.weekly_tokens || 0)}</td><td class="numeric subtle">${compact(m.weekly_input_tokens || 0)} / ${compact(m.weekly_output_tokens || 0)} / ${compact((m.weekly_cache_read_tokens || 0) + (m.weekly_cache_creation_tokens || 0))}</td></tr>`,
              )
              .join("")}</tbody></table></div>`
          : `<p class="subtle">近 7 天暂无该分组模型用量</p>`;
        return `<section class="quota-detail-group" data-group-id="${esc(entry.id)}"><div class="quota-detail-group-head"><strong>${esc(entry.label)}</strong><span class="numeric">5h ${compact(entry.five_hour_tokens || 0)} · Week ${compact(entry.weekly_tokens || 0)}</span></div><div class="quota-track quota-track-estimated"><div style="width:${barPct}%"></div></div>${metaParts.length ? `<div class="subtle quota-detail-meta">${esc(metaParts.join(" · "))}</div>` : ""}${modelsHTML}</section>`;
      })
      .join("");
    const unclassified =
      group.unclassified_weekly_tokens > 0
        ? `<p class="subtle">另有未记录模型名的历史用量：5h ${compact(group.unclassified_five_hour_tokens || 0)} / Week ${compact(group.unclassified_weekly_tokens)} tokens（已计入总和，未归入三个官方分组）。</p>`
        : "";
    return `${summaryHTML}${groupsHTML}${unclassified}<p class="subtle quota-note">口径说明：基于本机 transcript 滚动窗口统计，非官方实时配额；可在「设置 → 定价」中为三个官方分组配置可选参考上限。</p>`;
  },
  bindGroupDetail(container, getGroups) {
    if (!container || container.dataset.quotaGroupBound === "1") return;
    container.dataset.quotaGroupBound = "1";
    container.addEventListener("click", (event) => {
      const target = event.target.closest("[data-quota-group]");
      if (!target) return;
      const source = target.dataset.quotaGroup;
      const list = typeof getGroups === "function" ? getGroups() : [];
      const group = (list || []).find((item) => item && item.source === source);
      if (!group || typeof DetailDrawer === "undefined") return;
      const trigger = target.matches("button")
        ? target
        : container.querySelector(`button[data-quota-group="${CSS.escape(source)}"]`) ||
          target;
      DetailDrawer.open({
        key: `quota-group:${source}`,
        title: `${group.label || SourceUI.label(source)} · 配额分组`,
        subtitle: "近 5h / 7d 滚动窗口估算 · 按官方模型池汇总",
        html: this.renderGroupDetail(group),
        trigger,
      });
    });
  },
  render(windows = [], quotas = [], quotaGroups = []) {
    const rows = this.normalize(windows || [], quotas || []);
    const sources = [...new Set(rows.map((row) => row.source))];
    const groups = sources
      .map(
        (source) =>
          `<tbody><tr class="quota-source"><th colspan="4" scope="rowgroup">${SourceUI.brand(source)}</th></tr>${rows
            .filter((r) => r.source === source)
            .map((row) => {
              const known = row.value !== null;
              const risk = known ? this.severity(row.value) : "missing";
              const text = known ? `${row.value.toFixed(0)}%` : "暂无";
              const hint = known ? this.hint(row.value) : "";
              const elapsed = this.elapsed(row);
              const timeLabel = elapsed == null ? "" : `时间已过 ${elapsed.toFixed(0)}%`;
              const timeMarker = elapsed == null ? "" : `<i class="quota-time-marker" style="left:${Number(elapsed.toFixed(2))}%" tabindex="0" role="img" title="${timeLabel}" aria-label="${timeLabel}"></i>`;
              const scope = /:/.test(row.scope)
                ? ` · ${esc(row.scope.split(":").slice(1).join(":"))}`
                : "";
              const observed = row.observed
                ? `观测 ${relTime(row.observed)}`
                : "观测时间未知";
              return `<tr data-risk="${risk}" class="quota-data"><th scope="row">${row.minutes === 300 ? "5h" : "Week"}${scope}</th><td class="quota-used"><span class="quota-percent">${text}</span>${hint ? ` <small>${hint}</small>` : ""}<div class="quota-track${known ? "" : " quota-unknown"}" ${known ? `role="progressbar" aria-label="${esc(SourceUI.label(source))} ${row.minutes === 300 ? "5h" : "Week"} 官方已用" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.min(row.value, 100)}" aria-valuetext="${row.value}% ${hint}${timeLabel ? ` · ${timeLabel}` : ""}"` : 'role="img" aria-label="官方配额暂无读数"'}><div style="width:${known ? Math.min(row.value, 100) : 0}%"></div>${timeMarker}</div></td><td ${row.remaining == null ? "" : `data-remaining="${row.remaining}"`}>${row.remaining == null ? "暂无估算" : compact(row.remaining)}</td><td><span>${this.countdown(row.reset)}</span><span class="quota-observed">${observed}</span></td></tr>`;
            })
            .join("")}</tbody>`,
      )
      .join("");
    const estimated = this.renderEstimatedGroups(quotaGroups);
    const hasTable = rows.length > 0 || estimated.length > 0;
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
    return `${hasTable ? `<div class="quota-table-shell"><table class="tool-quota-table"><caption class="sr-only">官方已用百分比、滚动估算用量、预估剩余 tokens 与重置倒计时</caption><thead><tr><th>窗口</th><th>官方已用 / 估算</th><th>预估余量</th><th>距重置</th></tr></thead>${groups}${estimated}</table></div><p class="subtle quota-note">余量单位 tokens · 预估 5h 可用量受周余量约束${estimated ? " · Antigravity 为滚动窗口估算" : ""}</p>` : '<p class="empty">官方配额暂无读数</p>'}${spend}${forecast}`;
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
