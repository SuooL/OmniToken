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
  const name = (location.hash || "#overview").slice(1);
  const next = Views[name] ? name : "overview";
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
}

addEventListener("hashchange", route);

// Re-render the visible view after the display currency changes, so saving on
// the settings page updates the other pages without a reload.
function refreshCurrentView() {
  if (!currentView) return;
  Views[currentView].leave();
  Views[currentView].enter();
}

matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
  if (currentView === "overview" && Overview.lastData) Overview.render(Overview.lastData);
});

// Load the display currency before the first paint: money() reads it, and
// rendering in USD only to swap to CNY a moment later would flash wrong
// numbers. A missing or failing settings call must still boot the UI, so the
// route happens in .finally rather than .then.
fetch("/api/v1/settings")
  .then((r) => (r.ok ? r.json() : null))
  .then((s) => {
    if (s && s.currency) Currency.set(s.currency.code, s.currency.rate);
  })
  .catch(() => {})
  .finally(route);
