"use strict";
// Boot + hash router: #overview (default) | #live.

const Views = {
  overview: {
    el: () => document.getElementById("view-overview"),
    enter() { Overview.load(); this._timer = setInterval(() => Overview.load(), 30000); },
    leave() { clearInterval(this._timer); Overview.invalidate(); },
  },
  live: {
    el: () => document.getElementById("view-live"),
    enter() { Live.start(); },
    leave() { Live.stop(); },
  },
  reports: {
    el: () => document.getElementById("view-reports"),
    enter() { Reports.enter(); },
    leave() { Reports.leave(); },
  },
  details: {
    el: () => document.getElementById("view-details"),
    enter() { Details.enter(); },
    leave() { Details.leave(); },
  },
  cache: {
    el: () => document.getElementById("view-cache"),
    enter() { CacheView.enter(); },
    leave() { CacheView.leave(); },
  },
  speed: {
    el: () => document.getElementById("view-speed"),
    enter() { SpeedView.enter(); },
    leave() { SpeedView.leave(); },
  },
  devices: {
    el: () => document.getElementById("view-devices"),
    enter() { DevicesView.enter(); },
    leave() { DevicesView.leave(); },
  },
  models: {
    el: () => document.getElementById("view-models"),
    enter() { ModelsView.enter(); },
    leave() { ModelsView.leave(); },
  },
  settings: {
    el: () => document.getElementById("view-settings"),
    enter() { SettingsView.enter(); },
    leave() { SettingsView.leave(); },
  },
};

let currentView = null;

function parseRouteHash(hash) {
  const raw = (hash || "#overview").replace(/^#/, "");
  const split = raw.indexOf("?");
  return {
    name: split < 0 ? raw : raw.slice(0, split),
    params: new URLSearchParams(split < 0 ? "" : raw.slice(split + 1)),
  };
}

function route() {
  // Overview is the product landing page; live operation remains one direct
  // navigation step away.
  const parsed = parseRouteHash(location.hash);
  const next = Views[parsed.name] ? parsed.name : "overview";
  if (next === currentView) {
    if (next === "details") Details.applyRoute(parsed.params);
    return;
  }
  if (currentView) {
    Views[currentView].leave();
    Views[currentView].el().hidden = true;
  }
  if (next === "details") Details.applyRoute(parsed.params, false);
  currentView = next;
  Views[next].el().hidden = false;
  Views[next].enter();
  // The view was hidden until a moment ago; anything canvas-based needs to be
  // told its real size now that it has one.
  requestAnimationFrame(() => {
    resizeChartsIn(Views[next].el());
    ChartRegistry.resizeWithin(Views[next].el());
  });
  document.querySelectorAll("#nav a").forEach((a) => {
    if (a.dataset.view === next) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  });
  const active = document.querySelector(`#nav a[data-view="${next}"]`);
  if (active && matchMedia("(max-width: 860px)").matches) {
    active.scrollIntoView({block: "nearest", inline: "center"});
  }
  // The rail no longer carries the page name, so the head does.
  const link = active;
  document.getElementById("page-title").textContent = link ? link.textContent : "";
  document.getElementById("page-sub").textContent = PAGE_SUB[next] || "";
  document.getElementById("main-content").focus({ preventScroll: true });
}

// One line each, saying what the page answers rather than what it contains.
const PAGE_SUB = {
  live: "全部设备现在在生成什么",
  speed: "现在多快,以及各模型的吐字速度",
  overview: "累计与趋势",
  reports: "按日/周/月/会话聚合,可导出",
  details: "事件级下钻",
  devices: "各设备用量对比",
  models: "各模型分布",
  cache: "命中率与节省",
  settings: "定价覆盖与偏好",
};

// Icons are injected rather than written into the markup: one source of paths,
// and the nav keeps working if a view is added without one.
document.querySelectorAll("#nav a[data-icon]").forEach((a) => {
  a.insertAdjacentHTML("afterbegin", icon(a.dataset.icon));
});

addEventListener("hashchange", route);

matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
  if (currentView === "overview" && Overview.lastData) Overview.render(Overview.lastData);
});

// Before the first request: a server reachable from other machines needs the
// token on reads too (ADR-0016), and every view fetches on enter.
Api.loadToken();

// A server that wants a token we do not have would otherwise show nine pages of
// identical 401s with no hint about where to fix it. /api/v1/health is
// unauthenticated precisely so this check is possible. Token saves call this
// again so an obsolete warning never survives after credentials change.
async function refreshAuthState() {
  document.querySelector(".auth-banner")?.remove();
  const health = document.getElementById("hub-health");
  try {
    const h = await (await fetch(Api.url("/api/v1/health"))).json();
    health.className = "hub-health healthy";
    health.innerHTML = `<span class="health-dot" aria-hidden="true"></span><span>Hub 正常</span>`;
    // Only warn when a read actually fails. A reverse proxy in front of the hub
    // can inject the token, so `auth_required && !Api.token` no longer implies a
    // 401 — probe an authenticated read and trust the result instead of the
    // heuristic. Without this, the omni-behind-Authelia setup nags for a token
    // the browser never needs.
    if (h.auth_required && !Api.token) {
      let denied = false;
      try {
        const probe = await fetch(Api.url("/api/v1/live"));
        denied = probe.status === 401 || probe.status === 403;
      } catch {
        denied = false;
      }
      if (denied) {
        document.querySelector(".viz-root").insertAdjacentHTML("afterbegin",
          `<div class="auth-banner">这台服务端可被其它机器访问,读取需要令牌。
             请到 <a href="#settings">设置 → 访问令牌</a> 填写 <code>config.json</code> 里的 <code>token</code>。</div>`);
      }
    }
  } catch (e) {
    health.className = "hub-health unavailable";
    health.innerHTML = `<span class="health-dot" aria-hidden="true"></span><span>Hub 不可达</span>`;
  }
}

refreshAuthState();

route();
