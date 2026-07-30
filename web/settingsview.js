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

const SettingsView = {
  _rows: [],       // pricing overrides being edited: {model, in, out, cr, cw}
  _devices: [],    // [{key, tokens, last_seen}] from the breakdown API
  _labels: {},     // hostname -> display name
  _loaded: false,
  _draft: { pricing: null, devices: null, readToken: null, adminToken: null },
  _revision: { pricing: 0, devices: 0, tokens: 0 },
  _saving: { pricing: false, devices: false },

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
      this.pricingCard() + this.deviceCard() + this.tokenCard();
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
      <section class="card" id="card-pricing">
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
      <section class="card" id="card-devices">
        <div class="card-head">
          <h2>设备重命名</h2>
          <div class="head-tools"><button class="ghost-btn" data-act="save-devices">保存</button></div>
        </div>
        <p class="subtle">给主机名起一个好认的显示名(≤64 字符)。清空输入框即取消重命名;主机名本身是事件的归属键,不会被改写。</p>
        <div class="data-table">${body}</div>
        <div class="save-note" data-note="devices">&nbsp;</div>
      </section>`;
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
      <section class="card" id="card-token">
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
        <div class="save-note" data-note="token">&nbsp;</div>
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
      }
    };
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
