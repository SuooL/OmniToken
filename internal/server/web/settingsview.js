"use strict";
// Settings view (F23/GAP-5): pricing overrides, display currency, device
// renaming. Everything saved here takes effect without restarting the server —
// costs are computed at query time, so the server hot-swaps its price table on
// save and the next refresh shows the new numbers.
//
// Renders into #view-settings. Writes go to PUT /api/v1/settings, which is
// token-protected; the token is kept in localStorage (this panel is meant for
// a LAN/tailnet, the token is the same one the agents use).

const SETTINGS_TOKEN_KEY = "omnitoken.token";

const SettingsView = {
  _rows: [],       // pricing overrides being edited: {model, in, out, cr, cw}
  _devices: [],    // [{key, tokens, last_seen}] from the breakdown API
  _labels: {},     // hostname -> display name
  _currency: { code: "USD", rate: 1 },
  _loaded: false,

  enter() {
    this.load();
  },

  leave() {
    // No polling: this page is an editor, refreshing under the cursor would
    // throw away half-typed input.
  },

  async load() {
    const root = document.getElementById("view-settings");
    root.innerHTML = `<section class="card"><span class="empty">加载中…</span></section>`;
    try {
      const [settings, devices] = await Promise.all([
        fetch("/api/v1/settings").then((r) => {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        }),
        fetch("/api/v1/breakdown?by=device&days=3650")
          .then((r) => (r.ok ? r.json() : { rows: [] }))
          .catch(() => ({ rows: [] })),
      ]);
      this.adopt(settings, devices);
      this._loaded = true;
      this.render();
      document.getElementById("refresh-note").textContent =
        "设置已载入 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
    } catch (e) {
      root.innerHTML = `<section class="card"><span class="empty">设置读取失败:${esc(e.message)}</span></section>`;
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
    const c = (settings && settings.currency) || {};
    this._currency = { code: c.code || "USD", rate: c.rate || 1 };
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
      this.pricingCard() + this.currencyCard() + this.deviceCard() + this.tokenCard();
    this.bind(root);
  },

  // ---- pricing overrides -------------------------------------------------

  pricingCard() {
    const rows = this._rows.map((r, i) => `<tr>
        <td><input class="form-input model" data-i="${i}" type="text" value="${esc(r.model)}"
                   placeholder="claude-opus-4" spellcheck="false"></td>
        <td><input class="form-input num" data-i="${i}" data-f="in" type="number" min="0" max="10000" step="0.01" value="${r.in}"></td>
        <td><input class="form-input num" data-i="${i}" data-f="out" type="number" min="0" max="10000" step="0.01" value="${r.out}"></td>
        <td><input class="form-input num" data-i="${i}" data-f="cr" type="number" min="0" max="10000" step="0.01" value="${r.cr}"></td>
        <td><input class="form-input num" data-i="${i}" data-f="cw" type="number" min="0" max="10000" step="0.01" value="${r.cw}"></td>
        <td><button class="ghost-btn" data-act="del-price" data-i="${i}">删除</button></td>
      </tr>`).join("");
    const body = this._rows.length
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

  // ---- display currency --------------------------------------------------

  currencyCard() {
    return `
      <section class="card" id="card-currency">
        <div class="card-head">
          <h2>显示币种</h2>
          <div class="head-tools"><button class="ghost-btn" data-act="save-currency">保存</button></div>
        </div>
        <div class="filter-row">
          <label>币种代码
            <input class="form-input" id="cur-code" type="text" maxlength="3" size="4"
                   value="${esc(this._currency.code)}" spellcheck="false"></label>
          <label>汇率(1 USD =)
            <input class="form-input" id="cur-rate" type="number" min="0" step="0.0001"
                   value="${this._currency.rate}"></label>
          <span class="subtle">例:1 USD = 7.2 CNY</span>
        </div>
        <p class="subtle">仅用于展示换算,<b>不联网获取汇率</b>;底层成本始终以美元存算,改动只影响显示。</p>
        <div class="save-note" data-note="currency">&nbsp;</div>
      </section>`;
  },

  // ---- device renaming ---------------------------------------------------

  deviceCard() {
    const devices = this._devices.slice().sort((a, b) => (b.total_tokens || 0) - (a.total_tokens || 0));
    const body = devices.length
      ? `<table><thead><tr>
          <th>设备(主机名)</th><th>显示名</th><th>累计 tokens</th><th>最后活跃</th>
        </tr></thead><tbody>` +
        devices.map((d) => `<tr>
          <td>${esc(d.key || "(未知)")}</td>
          <td><input class="form-input label" type="text" maxlength="64"
                     data-host="${esc(d.key)}" value="${esc(this._labels[d.key] || "")}"
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

  // ---- write token -------------------------------------------------------

  tokenCard() {
    const tok = localStorage.getItem(SETTINGS_TOKEN_KEY) || "";
    return `
      <section class="card" id="card-token">
        <div class="card-head">
          <h2>写入令牌</h2>
          <div class="head-tools"><button class="ghost-btn" data-act="save-token">记住</button></div>
        </div>
        <div class="filter-row">
          <input class="form-input" id="settings-token" type="password" size="36"
                 value="${esc(tok)}" placeholder="config.json 里的 token,未配置则留空">
        </div>
        <p class="subtle">保存设置属于写接口,需与 agent 相同的 bearer token。令牌只存在本浏览器的 localStorage,不会上报。</p>
        <div class="save-note" data-note="token">&nbsp;</div>
      </section>`;
  },

  // ---- interaction -------------------------------------------------------

  bind(root) {
    root.onclick = (ev) => {
      const btn = ev.target.closest("button[data-act]");
      if (!btn) return;
      const act = btn.dataset.act;
      if (act === "add-price") {
        this.syncPricingFromDOM();
        this._rows.push({ model: "", in: 0, out: 0, cr: 0, cw: 0 });
        this.render();
      } else if (act === "del-price") {
        this.syncPricingFromDOM();
        this._rows.splice(Number(btn.dataset.i), 1);
        this.render();
      } else if (act === "save-price") {
        this.savePricing();
      } else if (act === "save-currency") {
        this.saveCurrency();
      } else if (act === "save-devices") {
        this.saveDevices();
      } else if (act === "save-token") {
        const v = document.getElementById("settings-token").value.trim();
        if (v) localStorage.setItem(SETTINGS_TOKEN_KEY, v);
        else localStorage.removeItem(SETTINGS_TOKEN_KEY);
        this.note("token", true, v ? "令牌已记住(仅本浏览器)" : "令牌已清除");
      }
    };
  },

  // syncPricingFromDOM keeps edits alive across add/remove re-renders.
  syncPricingFromDOM() {
    const root = document.getElementById("view-settings");
    root.querySelectorAll("input.model").forEach((el) => {
      const r = this._rows[Number(el.dataset.i)];
      if (r) r.model = el.value;
    });
    root.querySelectorAll("input.num").forEach((el) => {
      const r = this._rows[Number(el.dataset.i)];
      if (r) r[el.dataset.f] = el.value === "" ? 0 : Number(el.value);
    });
  },

  async savePricing() {
    this.syncPricingFromDOM();
    const out = {};
    for (const r of this._rows) {
      const name = (r.model || "").trim();
      if (!name) return this.note("price", false, "有一行没有填模型名");
      const key = name.toLowerCase();
      if (out[key]) return this.note("price", false, `模型 ${name} 重复`);
      for (const [label, v] of [["输入", r.in], ["输出", r.out], ["缓存读取", r.cr], ["缓存写入", r.cw]]) {
        if (!isFinite(v) || v < 0 || v > 10000) {
          return this.note("price", false, `${name} 的${label}价格 ${v} 非法(0–10000 美元/百万 token)`);
        }
      }
      out[key] = {
        input_per_mtok: Number(r.in),
        output_per_mtok: Number(r.out),
        cache_read_per_mtok: Number(r.cr),
        cache_write_per_mtok: Number(r.cw),
      };
    }
    const ok = await this.put({ pricing_overrides: out }, "price");
    if (ok) this.note("price", true, "已保存并热重载定价表,成本已按新价重算");
  },

  async saveCurrency() {
    const code = document.getElementById("cur-code").value.trim().toUpperCase();
    const rate = Number(document.getElementById("cur-rate").value);
    if (!/^[A-Z]{3}$/.test(code)) return this.note("currency", false, "币种代码须为 3 个字母,如 USD / CNY");
    if (!isFinite(rate) || rate <= 0) return this.note("currency", false, "汇率须大于 0");
    const ok = await this.put({ currency: { code, rate } }, "currency");
    if (ok) {
      this._currency = { code, rate };
      this.note("currency", true, `已保存:1 USD = ${rate} ${code}`);
    }
  },

  async saveDevices() {
    const labels = {};
    let bad = null;
    document.querySelectorAll("#view-settings input.label").forEach((el) => {
      const v = el.value.trim();
      if (!v) return; // empty = no rename
      if ([...v].length > 64) bad = el.dataset.host;
      labels[el.dataset.host] = v;
    });
    if (bad) return this.note("devices", false, `设备 ${bad} 的显示名超过 64 字符`);
    const ok = await this.put({ device_labels: labels }, "devices");
    if (ok) {
      this._labels = labels;
      this.note("devices", true, `已保存 ${Object.keys(labels).length} 个显示名`);
    }
  },

  // put sends one section; the server leaves absent sections untouched.
  async put(body, noteKey) {
    this.note(noteKey, true, "保存中…");
    const headers = { "Content-Type": "application/json" };
    const tok = localStorage.getItem(SETTINGS_TOKEN_KEY);
    if (tok) headers["Authorization"] = "Bearer " + tok;
    try {
      const res = await fetch("/api/v1/settings", { method: "PUT", headers, body: JSON.stringify(body) });
      if (res.status === 401) {
        this.note(noteKey, false, "未授权:请在下方填写写入令牌后重试");
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
