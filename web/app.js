"use strict";
// Boot + hash router: #overview (default) | #live.

const Views = {
  overview: {
    el: () => document.getElementById("view-overview"),
    enter() { Overview.load(); this._timer = setInterval(() => Overview.load(), 30000); },
    leave() { clearInterval(this._timer); },
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

function route() {
  // Lands on live: the panel's job is now what the machines are doing, with
  // the retrospective views a click away.
  const name = (location.hash || "#live").slice(1);
  const next = Views[name] ? name : "live";
  if (next === currentView) return;
  if (currentView) {
    Views[currentView].leave();
    Views[currentView].el().hidden = true;
  }
  currentView = next;
  Views[next].el().hidden = false;
  Views[next].enter();
  // The view was hidden until a moment ago; anything canvas-based needs to be
  // told its real size now that it has one.
  requestAnimationFrame(() => resizeChartsIn(Views[next].el()));
  document.querySelectorAll("#nav a").forEach((a) => {
    a.toggleAttribute("aria-current", a.dataset.view === next);
  });
  // The rail no longer carries the page name, so the head does.
  const link = document.querySelector(`#nav a[data-view="${next}"]`);
  document.getElementById("page-title").textContent = link ? link.textContent : "";
  document.getElementById("page-sub").textContent = PAGE_SUB[next] || "";
}

// One line each, saying what the page answers rather than what it contains.
const PAGE_SUB = {
  live: "这台机器现在在生成什么",
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
  try {
    const h = await (await fetch(Api.url("/api/v1/health"))).json();
    if (h.auth_required && !Api.token) {
      document.querySelector(".viz-root").insertAdjacentHTML("afterbegin",
        `<div class="auth-banner">这台服务端可被其它机器访问,读取需要令牌。
           请到 <a href="#settings">设置 → 访问令牌</a> 填写 <code>config.json</code> 里的 <code>token</code>。</div>`);
    }
  } catch (e) {
    // Server down or not ours; the views report that themselves.
  }
}

refreshAuthState();

route();
