"use strict";
// Settings view (F23/GAP-5): pricing overrides and device renaming.
// Everything saved here takes effect without restarting the server —
// costs are computed at query time, so the server hot-swaps its price table on
// save and the next refresh shows the new numbers.
//
// Renders into #view-settings. Read and admin credentials are independently
// scoped and kept behind Api's storage boundary; neither is sent as settings
// payload data.
function buildPricingPayload(rows) {
  const value = {};
  for (const row of rows) {
    const name = String(row.model || "").trim();
    if (!name) return { ok: false, error: "有一行没有填模型名" };
    const key = name.toLowerCase();
    if (value[key]) return { ok: false, error: `模型 ${name} 重复` };

    const prices = {};
    for (const [field, label] of [["in", "输入"], ["out", "输出"], ["cr", "缓存读取"], ["cw", "缓存写入"]]) {
      const raw = row[field];
      if (raw == null || String(raw).trim() === "") {
        return { ok: false, error: `${name} 的${label}价格不能为空` };
      }
      const number = Number(raw);
      if (!isFinite(number) || number < 0 || number > 10000) {
        return { ok: false, error: `${name} 的${label}价格 ${raw} 非法(0–10000 美元/百万 token)` };
      }
      prices[field] = number;
    }
    value[key] = {
      input_per_mtok: prices.in,
      output_per_mtok: prices.out,
      cache_read_per_mtok: prices.cr,
      cache_write_per_mtok: prices.cw,
    };
  }
  return { ok: true, value };
}

// mergeSubmission is the client-side half of ADR-0019's confirmation rules,
// kept pure so it can be tested without a DOM. It refuses everything the server
// refuses, in the same terms, so the button is disabled before a doomed request
// is ever sent — and the confirmation is compared byte for byte, exactly as the
// server does it. Anything looser here would train the user to expect the
// forgiving version and then surprise them with a 400.
function mergeSubmission(merge) {
  const from = String((merge && merge.from) || "");
  const to = String((merge && merge.to) || "");
  if (!from || !to) return { ok: false, error: "请先选择要合并的两个设备" };
  if (from === to) return { ok: false, error: "两侧不能是同一个设备" };
  if (!merge.plan) return { ok: false, error: "请先预览影响,确认要改写的行数" };
  if (String(merge.confirm || "") !== from) {
    return { ok: false, error: `请原样输入被合并设备的完整名称:${from}` };
  }
  return { ok: true, value: { from, to, confirm: from } };
}

const SettingsView = {
  _rows: [],       // pricing overrides being edited: {model, in, out, cr, cw}
  _devices: [],    // [{key, tokens, last_seen}] from the breakdown API
  _labels: {},     // hostname -> display name
  _merges: [],     // audit log of past identity merges (read-only, ADR-0019)
  _local: {},      // {device, hostname, duplicate_identity} for this machine
  _loaded: false,
  _draft: { pricing: null, devices: null, readToken: null, adminToken: null },
  _revision: { pricing: 0, devices: 0, tokens: 0 },
  _saving: { pricing: false, devices: false },
  // The merge tool's own state. `plan` is whatever the server last previewed;
  // it is thrown away the moment either side changes, so a confirmation can
  // never be typed against numbers computed for a different pair.
  _merge: { open: false, from: "", to: "", plan: null, warnings: [], confirm: "", busy: false, error: "" },

  enter() {
    this.load();
  },

  leave() {
    // No polling: this page is an editor, refreshing under the cursor would
    // throw away half-typed input.
  },

  async load() {
    const root = document.getElementById("view-settings");
    if (!this._loaded) root.innerHTML = "";
    renderState(root, { kind: "loading", title: "正在加载设置" });
    try {
      const [settings, devices] = await Promise.all([
        Api.get("/api/v1/settings"),
        // A device list failure must not block editing pricing or the token.
        Api.get("/api/v1/breakdown?by=device&days=3650").catch(() => ({ rows: [] })),
      ]);
      this.adopt(settings, devices);
      this._loaded = true;
      this.render();
      renderState(root, { kind: "ready", title: "" });
      document.getElementById("refresh-note").textContent =
        "设置已载入 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    } catch (e) {
      // The token card must survive this. A server that authenticates reads
      // (ADR-0016) 401s the settings fetch until a token is stored — and the
      // box for storing it lives on this page, so bailing out entirely left the
      // user with a banner telling them to come here and nothing to type into.
      const issue = classifyAPIError(e);
      if (!this._loaded) {
        root.innerHTML = this.tokenCard();
        this.bind(root);
      }
      renderState(root, {
        kind: this._loaded ? "stale" : issue.kind,
        title: this._loaded ? "设置数据可能已过期" : issue.title,
        detail: issue.detail,
        action: { label: "重试", run: () => this.load() },
      });
      document.getElementById("refresh-note").textContent = this._loaded
        ? "刷新失败,正在显示未保存草稿" : issue.title;
    }
  },

  adopt(settings, devices) {
    const po = (settings && settings.pricing_overrides) || {};
    this._rows = Object.keys(po).sort().map((m) => ({
      model: m,
      in: po[m].input_per_mtok || 0,
      out: po[m].output_per_mtok || 0,
      cr: po[m].cache_read_per_mtok || 0,
      cw: po[m].cache_write_per_mtok || 0,
    }));
    this._labels = (settings && settings.device_labels) || {};
    this._merges = (settings && settings.device_merges) || [];
    this._local = (settings && settings.local_identity) || {};
    this._devices = (devices && devices.rows) || [];
    // Devices that only exist as a saved label (retired machine) stay editable.
    const known = new Set(this._devices.map((d) => d.key));
    Object.keys(this._labels).forEach((h) => {
      if (!known.has(h)) this._devices.push({ key: h, total_tokens: 0, last_seen: 0 });
    });
  },

  render() {
    const root = document.getElementById("view-settings");
    root.innerHTML =
      this.tokenCard() + this.pricingCard() + this.deviceCard() + this.mergeCard() +
      this.preferencesCard() + this.dangerCard();
    this.bind(root);
  },

  // ---- pricing overrides -------------------------------------------------

  pricingCard() {
    const pricing = this._draft.pricing || this._rows;
    const rows = pricing.map((r, i) => `<tr>
        <td><input class="form-input model" data-i="${i}" type="text" value="${esc(r.model)}"
                   placeholder="claude-opus-4" spellcheck="false"></td>
        <td><input class="form-input num" data-i="${i}" data-f="in" type="number" min="0" max="10000" step="0.01" value="${esc(r.in)}"></td>
        <td><input class="form-input num" data-i="${i}" data-f="out" type="number" min="0" max="10000" step="0.01" value="${esc(r.out)}"></td>
        <td><input class="form-input num" data-i="${i}" data-f="cr" type="number" min="0" max="10000" step="0.01" value="${esc(r.cr)}"></td>
        <td><input class="form-input num" data-i="${i}" data-f="cw" type="number" min="0" max="10000" step="0.01" value="${esc(r.cw)}"></td>
        <td><button class="ghost-btn" data-act="del-price" data-i="${i}">删除</button></td>
      </tr>`).join("");
    const body = pricing.length
      ? `<table><thead><tr>
          <th>模型</th><th>输入</th><th>输出</th><th>缓存读取</th><th>缓存写入</th><th></th>
        </tr></thead><tbody>${rows}</tbody></table>`
      : `<span class="empty">暂无覆盖,使用内置 LiteLLM 价格表</span>`;
    return `
      <section class="instrument-card" id="card-pricing"
               data-settings-group="pricing" data-settings-scope="section">
        <div class="card-head">
          <h2>定价覆盖 · 美元 / 百万 token</h2>
          <div class="head-tools">
            <button class="ghost-btn" data-act="add-price">新增模型</button>
            <button class="ghost-btn" data-act="save-price">保存</button>
          </div>
        </div>
        <p class="subtle">覆盖内置价格表中的单个模型。成本为查询时计算,保存后历史成本立即按新价重算,<b>无需重启</b>。模型名不区分大小写;写在 config.json 里的覆盖不在此列表,但仍然生效(此处同名条目优先)。</p>
        <div class="data-table">${body}</div>
        <div class="save-note" data-note="price">&nbsp;</div>
      </section>`;
  },

  // ---- device renaming ---------------------------------------------------

  deviceCard() {
    const labels = this._draft.devices || this._labels;
    const devices = this._devices.slice().sort((a, b) => (b.total_tokens || 0) - (a.total_tokens || 0));
    const body = devices.length
      ? `<table><thead><tr>
          <th>设备(主机名)</th><th>显示名</th><th>累计 tokens</th><th>最后活跃</th>
        </tr></thead><tbody>` +
        devices.map((d) => `<tr>
          <td>${esc(d.key || "(未知)")}</td>
          <td><input class="form-input label" type="text" maxlength="64"
                     data-host="${esc(d.key)}" value="${esc(labels[d.key] || "")}"
                     placeholder="留空则用主机名"></td>
          <td>${compact(d.total_tokens || 0)}</td>
          <td>${d.last_seen ? relTime(d.last_seen) : "—"}</td>
        </tr>`).join("") + `</tbody></table>`
      : `<span class="empty">暂无设备数据</span>`;
    return `
      <section class="instrument-card" id="card-devices"
               data-settings-group="device-identities" data-settings-scope="section">
        <div class="card-head">
          <h2>设备重命名</h2>
          <div class="head-tools"><button class="ghost-btn" data-act="save-devices">保存</button></div>
        </div>
        <p class="subtle">给主机名起一个好认的显示名(≤64 字符)。清空输入框即取消重命名;主机名本身是事件的归属键,不会被改写。</p>
        <div class="data-table">${body}</div>
        <div class="save-note" data-note="devices">&nbsp;</div>
      </section>`;
  },

  // ---- device identity merge (ADR-0019) ----------------------------------
  //
  // Deliberately not part of the rename card above. Renaming changes a label
  // and is undone by clearing a box; merging rewrites which device the stored
  // rows belong to and cannot be undone at all. Two operations that different
  // must not look alike or sit in the same group of controls — so this one is
  // collapsed by default, marked as a danger surface, and asks for the source
  // device's full name to be typed out before it will run.

  mergeCard() {
    const devices = this._devices.slice()
      .sort((a, b) => (b.total_tokens || 0) - (a.total_tokens || 0))
      .map((d) => d.key)
      .filter((key) => key);
    const options = (selected) =>
      `<option value=""${selected ? "" : " selected"}>请选择…</option>` +
      devices.map((key) => `<option value="${esc(key)}"${key === selected ? " selected" : ""}>${
        esc(key)}${this._labels[key] ? ` · ${esc(this._labels[key])}` : ""}</option>`).join("");
    return `
      <section class="instrument-card settings-danger" id="card-device-merge"
               data-settings-group="device-merge" data-settings-scope="section">
        <div class="card-head">
          <h2>设备身份合并</h2>
          <span class="chip">不可撤销 · 管理凭据</span>
        </div>
        <p class="subtle">同一台机器出现两个身份时,把 source 名下已入库的行改写为 target。
          <b>只改归属,不改任何用量计数</b>:合并前后 token 与事件总数逐字相等。
          合并<b>无法撤销</b>,执行前请先备份数据库(单文件 SQLite,<code>cp ~/.omnitoken/omnitoken.db 备份路径</code> 即可)。
          按设备分组的历史图表会改变形状 —— 总量不变,分组变了。</p>
        ${this.mergeIdentityHint()}
        <details class="merge-panel"${this._merge.open ? " open" : ""}>
          <summary>展开合并工具</summary>
          <div class="filter-row merge-picker">
            <label>被合并(source)
              <select class="merge-side" data-side="from" aria-label="被合并的设备">${options(this._merge.from)}</select>
            </label>
            <label>合并到(target)
              <select class="merge-side" data-side="to" aria-label="合并到的设备">${options(this._merge.to)}</select>
            </label>
            <button class="ghost-btn" data-act="merge-preview">预览影响</button>
          </div>
          <div id="merge-interactive">${this.mergeInteractive()}</div>
        </details>
        ${this.mergeHistory()}
        <div class="save-note" data-note="merge">&nbsp;</div>
      </section>`;
  },

  // The hint is a fact, not a guess: the server saw self-reported events under
  // both this machine's configured name and its hostname (ADR-0019 §7.3).
  // Nothing is merged automatically — the button only fills the two selects.
  mergeIdentityHint() {
    const duplicate = this._local.duplicate_identity;
    if (!duplicate) return "";
    return `
      <div class="merge-hint" role="note">
        <span>本机同时以 <b>${esc(this._local.device || "")}</b> 与 <b>${esc(duplicate)}</b>
          自报过事件,很可能是同一台机器被记成了两个身份。</span>
        <button class="ghost-btn" data-act="merge-prefill">填入这两个身份</button>
      </div>`;
  },

  mergeInteractive() {
    const merge = this._merge;
    if (merge.error) {
      return `<p class="subtle merge-error">${esc(merge.error)}</p>`;
    }
    if (!merge.plan) {
      return `<p class="subtle">选择两侧设备后点击「预览影响」。预览数字由服务端计算,
        与真正执行的语句同源;只看不改。</p>`;
    }
    const plan = merge.plan;
    const stat = (side, label) => `<tr>
        <td>${label}<br><span class="sub2">${esc(side.device || "")}</span></td>
        <td>${full(side.events || 0)}</td>
        <td>${compact(side.total_tokens || 0)}</td>
        <td>${side.first_ts ? new Date(side.first_ts).toLocaleDateString("zh-CN") : "—"}</td>
        <td>${side.last_ts ? relTime(side.last_ts) : "—"}</td>
        <td>${full(side.quota_snapshots || 0)}</td>
      </tr>`;
    const warnings = (merge.warnings || []).map((w) => `<li>${esc(w)}</li>`).join("");
    return `
      <div class="data-table merge-impact">
        <table><thead><tr>
          <th>身份</th><th>事件</th><th>tokens</th><th>首次</th><th>最近</th><th>配额快照</th>
        </tr></thead><tbody>${stat(plan.from, "被合并")}${stat(plan.to, "合并到")}</tbody></table>
      </div>
      <p class="subtle">将改写 <b>${full(plan.events_moved || 0)}</b> 条事件的归属、
        迁移 <b>${full(plan.quota_moved || 0)}</b> 条配额快照并丢弃
        <b>${full(plan.quota_dropped || 0)}</b> 条同键重复观测,
        丢弃 <b>${full(plan.live_rows_dropped || 0)}</b> 行实时进程状态(下一轮上报会重建)。
        事件一条都不会减少。</p>
      ${warnings ? `<ul class="merge-warnings">${warnings}</ul>` : ""}
      <div class="filter-row merge-confirm-row">
        <label>输入 <code>${esc(merge.from)}</code> 以确认
          <input class="form-input" id="merge-confirm" type="text" spellcheck="false"
                 autocomplete="off" value="${esc(merge.confirm)}" placeholder="${esc(merge.from)}">
        </label>
        <button class="ghost-btn merge-run" data-act="merge-run"${
          mergeSubmission(merge).ok ? "" : " disabled"}>执行合并(不可撤销)</button>
      </div>`;
  },

  mergeHistory() {
    if (!this._merges.length) {
      return `<p class="subtle">合并历史:还没有执行过任何合并。</p>`;
    }
    const rows = this._merges.slice().reverse().map((m) => `<tr>
        <td>${esc(m.from || "")} → ${esc(m.to || "")}</td>
        <td>${m.at ? new Date(m.at).toLocaleString("zh-CN", { hour12: false }) : "—"}</td>
        <td>${full(m.events || 0)}</td>
        <td>${full(m.quota_snapshots || 0)} / ${full(m.quota_dropped || 0)}</td>
        <td>${esc(m.actor || "")}</td>
      </tr>`).join("");
    return `
      <div class="data-table merge-history">
        <table><thead><tr>
          <th>合并</th><th>时间</th><th>事件</th><th>配额 迁移/丢弃</th><th>发起</th>
        </tr></thead><tbody>${rows}</tbody></table>
      </div>
      <p class="subtle">合并历史只增不减 —— 合并不可撤销,这份记录是事后唯一的对照物。</p>`;
  },

  // ---- scoped credentials ------------------------------------------------

  tokenCard() {
    const readToken = this._draft.readToken == null
      ? Api.token
      : this._draft.readToken;
    const adminToken = this._draft.adminToken == null
      ? Api.adminToken
      : this._draft.adminToken;
    return `
      <section class="instrument-card" id="card-token"
               data-settings-group="connection-auth" data-settings-scope="section">
        <div class="card-head">
          <h2>访问凭据</h2>
          <div class="head-tools"><button class="ghost-btn" data-act="save-tokens">记住</button></div>
        </div>
        <div class="filter-row">
          <label>读取 token
            <input class="form-input" id="settings-read-token" type="password" size="36"
                   value="${esc(readToken)}" placeholder="config.json 里的 read_token">
          </label>
          <label>管理 token
            <input class="form-input" id="settings-admin-token" type="password" size="36"
                   value="${esc(adminToken)}" placeholder="config.json 里的 admin_token">
          </label>
        </div>
        <p class="subtle">读取 token 用于 GET 与实时流;管理 token 只用于保存设置。
          旧浏览器没有管理 token 记录时会临时沿用读取 token,保存后两者独立。
          凭据只存在本浏览器的 localStorage,不会作为设置内容上报。</p>
        <div class="credential-scope-map" role="img"
             aria-label="读取 token 仅授权查询和实时流；管理 token 授权设置写入和设备撤销">
          <div><strong>读取 token</strong><span aria-hidden="true">→</span><span>GET · SSE · 下载</span></div>
          <div><strong>管理 token</strong><span aria-hidden="true">→</span><span>PUT · 身份撤销</span></div>
        </div>
        <div class="save-note" data-note="token">&nbsp;</div>
      </section>`;
  },

  preferencesCard() {
    return `
      <section class="instrument-card" data-settings-group="preferences" data-settings-scope="section">
        <div class="card-head"><h2>显示偏好</h2><span class="subtle">跟随当前浏览器</span></div>
        <p class="subtle">主题跟随系统；减少动态效果由 <code>prefers-reduced-motion</code> 控制。
          这些偏好只影响当前浏览器，不会写入 Hub，也不会改变遥测计算。</p>
      </section>`;
  },

  dangerCard() {
    return `
      <section class="state-panel settings-danger" data-settings-danger="true">
        <div class="card-head"><h2>危险操作</h2><span class="chip">管理凭据</span></div>
        <p class="subtle">撤销设备身份会立即使其凭据失效，必须通过独立确认流程执行。
          定价删除和设备改名不属于身份撤销；每个上方“保存”按钮也只提交所在分组。</p>
      </section>`;
  },

  // ---- interaction -------------------------------------------------------

  bind(root) {
    root.oninput = (ev) => this.updateDraft(ev.target);
    root.onclick = (ev) => {
      const btn = ev.target.closest("button[data-act]");
      if (!btn) return;
      const act = btn.dataset.act;
      if (act === "add-price") {
        this.pricingDraft().push({ model: "", in: 0, out: 0, cr: 0, cw: 0 });
        this._revision.pricing += 1;
        this.render();
      } else if (act === "del-price") {
        this.pricingDraft().splice(Number(btn.dataset.i), 1);
        this._revision.pricing += 1;
        this.render();
      } else if (act === "save-price") {
        this.savePricing();
      } else if (act === "save-devices") {
        this.saveDevices();
      } else if (act === "save-tokens") {
        this.saveTokens();
      } else if (act === "merge-prefill") {
        // Fills the pickers and nothing else: the hint may not act on itself.
        this._merge.from = this._local.duplicate_identity || "";
        this._merge.to = this._local.device || "";
        this.invalidateMergePlan();
        this._merge.open = true;
        this.render();
      } else if (act === "merge-preview") {
        this.previewMerge();
      } else if (act === "merge-run") {
        this.runMerge();
      }
    };
    const panel = root.querySelector("#card-device-merge details");
    if (panel) panel.addEventListener("toggle", () => { this._merge.open = panel.open; });
  },

  pricingDraft() {
    if (!this._draft.pricing) {
      this._draft.pricing = this._rows.map((r) => ({ ...r }));
    }
    return this._draft.pricing;
  },

  devicesDraft() {
    if (!this._draft.devices) this._draft.devices = { ...this._labels };
    return this._draft.devices;
  },

  updateDraft(target) {
    if (target.matches("input.model, input.num")) {
      const row = this.pricingDraft()[Number(target.dataset.i)];
      if (!row) return;
      if (target.classList.contains("model")) row.model = target.value;
      else row[target.dataset.f] = target.value;
      this._revision.pricing += 1;
    } else if (target.matches("input.label")) {
      this.devicesDraft()[target.dataset.host] = target.value;
      this._revision.devices += 1;
    } else if (target.matches("select.merge-side")) {
      this._merge[target.dataset.side] = target.value;
      // A plan describes one specific pair. Changing either side must void it,
      // or the typed confirmation would be checked against stale numbers.
      this.invalidateMergePlan();
      this.refreshMergeInteractive();
    } else if (target.id === "merge-confirm") {
      // Only the run button's enabled state depends on this; re-rendering here
      // would move focus out of the box the user is typing in.
      this._merge.confirm = target.value;
      const run = document.querySelector("#view-settings .merge-run");
      if (run) run.disabled = !mergeSubmission(this._merge).ok;
    } else if (target.id === "settings-read-token") {
      this._draft.readToken = target.value;
      this._revision.tokens += 1;
    } else if (target.id === "settings-admin-token") {
      this._draft.adminToken = target.value;
      this._revision.tokens += 1;
    }
  },

  async savePricing() {
    if (this._saving.pricing) {
      return this.note("price", false, "保存进行中,请等待当前请求完成");
    }
    this._saving.pricing = true;
    try {
      const snapshot = (this._draft.pricing || this._rows).map((row) => ({ ...row }));
      const sentRevision = this._revision.pricing;
      const built = buildPricingPayload(snapshot);
      if (!built.ok) return this.note("price", false, built.error);
      const ok = await this.put({ pricing_overrides: built.value }, "price");
      if (ok) {
        if (canCommitRevision(this._revision.pricing, sentRevision)) {
          this._rows = snapshot;
          this._draft.pricing = null;
          this.note("price", true, "已保存并热重载定价表,成本已按新价重算");
        } else {
          this.note("price", true, "发送版本已保存;当前编辑仍未保存");
        }
      }
    } finally {
      this._saving.pricing = false;
    }
  },

  async saveDevices() {
    if (this._saving.devices) {
      return this.note("devices", false, "保存进行中,请等待当前请求完成");
    }
    this._saving.devices = true;
    try {
      const snapshot = { ...(this._draft.devices || this._labels) };
      const sentRevision = this._revision.devices;
      const labels = {};
      let bad = null;
      Object.entries(snapshot).forEach(([host, value]) => {
        const v = String(value).trim();
        if (!v) return; // empty = no rename
        if ([...v].length > 64) bad = host;
        labels[host] = v;
      });
      if (bad) return this.note("devices", false, `设备 ${bad} 的显示名超过 64 字符`);
      const ok = await this.put({ device_labels: labels }, "devices");
      if (ok) {
        if (canCommitRevision(this._revision.devices, sentRevision)) {
          this._labels = labels;
          this._draft.devices = null;
          this.note("devices", true, `已保存 ${Object.keys(labels).length} 个显示名`);
        } else {
          this.note("devices", true, "发送版本已保存;当前编辑仍未保存");
        }
      }
    } finally {
      this._saving.devices = false;
    }
  },

  invalidateMergePlan() {
    this._merge.plan = null;
    this._merge.warnings = [];
    this._merge.confirm = "";
    this._merge.error = "";
  },

  refreshMergeInteractive() {
    const host = document.getElementById("merge-interactive");
    if (host) host.innerHTML = this.mergeInteractive();
  },

  // previewMerge asks the server what the merge would do. The numbers shown are
  // never computed here: the user decides on figures produced by the same code
  // that will run the statements (ADR-0019 §6.2).
  async previewMerge() {
    const { from, to } = this._merge;
    if (!from || !to) return this.note("merge", false, "请先选择要合并的两个设备");
    if (from === to) return this.note("merge", false, "两侧不能是同一个设备");
    this.note("merge", true, "正在计算影响…");
    const res = await this.postMerge("/api/v1/devices/merge/preview", { from, to });
    if (!res.ok) {
      this.invalidateMergePlan();
      this._merge.error = res.error;
      this.refreshMergeInteractive();
      return this.note("merge", false, res.error);
    }
    this._merge.plan = res.body.plan;
    this._merge.warnings = res.body.warnings || [];
    this._merge.confirm = "";
    this._merge.error = "";
    this.refreshMergeInteractive();
    this.note("merge", true, "以上为服务端计算的影响;确认无误后输入被合并设备全名执行。");
  },

  async runMerge() {
    if (this._merge.busy) return this.note("merge", false, "合并进行中,请等待当前请求完成");
    const submission = mergeSubmission(this._merge);
    if (!submission.ok) return this.note("merge", false, submission.error);
    this._merge.busy = true;
    try {
      this.note("merge", true, "正在合并…");
      const res = await this.postMerge("/api/v1/devices/merge", submission.value);
      if (!res.ok) return this.note("merge", false, res.error);
      const applied = res.body.plan || {};
      const from = submission.value.from;
      this.invalidateMergePlan();
      this._merge.from = "";
      // Everything on the page (device list, history, hint) has just changed.
      await this.load();
      this.note("merge", true,
        `已把 ${from} 并入 ${submission.value.to}:改写 ${full(applied.events_moved || 0)} 条事件归属,` +
        `token 计数未变。此操作不可撤销,已记入合并历史。`);
    } finally {
      this._merge.busy = false;
    }
  },

  // postMerge normalizes the three outcomes the panel must tell apart: a
  // missing admin credential, a rejection the server explained in words, and a
  // transport failure.
  async postMerge(path, body) {
    try {
      const res = await Api.post(path, body);
      if (res.status === 401) {
        return { ok: false, error: "未授权:设备合并需要管理 token,请在访问凭据卡片填写后重试" };
      }
      if (!res.ok) {
        return { ok: false, error: (await res.text()).trim() || `HTTP ${res.status}` };
      }
      return { ok: true, body: await res.json() };
    } catch (e) {
      return { ok: false, error: "请求失败:" + e.message };
    }
  },

  async saveTokens() {
    const currentRead = this._draft.readToken == null
      ? Api.token
      : this._draft.readToken;
    const currentAdmin = this._draft.adminToken == null
      ? Api.adminToken
      : this._draft.adminToken;
    const readValue = currentRead.trim();
    const adminValue = currentAdmin.trim();
    const persisted = Api.saveTokens(readValue, adminValue);
    if (persisted) {
      this._draft.readToken = null;
      this._draft.adminToken = null;
    }
    await refreshAuthState();
    await this.load();
    this.note("token", persisted,
      persisted
        ? "读取与管理凭据已记住(仅本浏览器)"
        : "凭据当前会话有效,但浏览器拒绝持久化");
  },

  // put sends one section; the server leaves absent sections untouched.
  async put(body, noteKey) {
    this.note(noteKey, true, "保存中…");
    try {
      const res = await Api.put("/api/v1/settings", body);
      if (res.status === 401) {
        this.note(noteKey, false, "未授权:请在下方填写管理 token 后重试");
        return false;
      }
      if (!res.ok) {
        this.note(noteKey, false, "保存失败:" + (await res.text()).trim());
        return false;
      }
      return true;
    } catch (e) {
      this.note(noteKey, false, "保存失败:" + e.message);
      return false;
    }
  },

  note(key, ok, msg) {
    const el = document.querySelector(`#view-settings .save-note[data-note="${key}"]`);
    if (!el) return false;
    el.textContent = msg;
    el.className = "save-note " + (ok ? "ok" : "err");
    return false;
  },
};
