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
  speed: "各模型的吐字速度",
  overview: "累计与趋势",
  reports: "按日/周/月/会话聚合,可导出",
  details: "事件级下钻",
  devices: "各设备用量对比",
  models: "各模型分布",
  cache: "命中率与节省",
  settings: "定价覆盖与偏好",
};

addEventListener("hashchange", route);

matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
  if (currentView === "overview" && Overview.lastData) Overview.render(Overview.lastData);
});

route();
