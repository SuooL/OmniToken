"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");

function fakeNode() {
  return {
    className: "",
    dataset: {},
    hidden: false,
    innerHTML: "",
    placeholder: "",
    style: {},
    textContent: "",
    value: "",
    addEventListener() {},
    classList: { toggle() {} },
    focus() {},
    select() {},
  };
}

const nodes = new Map();
// A bridge that never answers, rather than no bridge at all: `boot()` then
// stalls on its first call and sets no timers, so the module loads with a clean
// slate. `__TAURI__: undefined` would instead park a permanent "IPC 不可用"
// error in the footer, which is precisely the slot these tests need to read.
const pending = () => new Promise(() => {});
global.window = { __TAURI__: { core: { invoke: pending }, event: { listen: pending } } };
global.document = {
  body: fakeNode(),
  addEventListener() {},
  getElementById(id) {
    if (!nodes.has(id)) nodes.set(id, fakeNode());
    return nodes.get(id);
  },
  querySelector() {
    return fakeNode();
  },
};
global.compact = (value) => String(value);
global.esc = (value) => String(value);

const { coverageLabel, onLive } = require("./app.js");

const text = (id) => document.getElementById(id).textContent;

test("partial source coverage is shown as measured over total events", () => {
  const label = coverageLabel({
    coverage: [
      { source: "api", measured_events: 3, total_events: 3 },
      { source: "claude-code", measured_events: 8, total_events: 10 },
      { source: "codex", measured_events: 0, total_events: 4 },
    ],
  });

  assert.equal(label, "覆盖 Other/API 3/3 · Claude 8/10 · Codex 0/4");
});

// While the stream is healthy the age is always "1s 前", so printing it beside
// 实时 says nothing and costs a line of the reader's attention every second.
test("a healthy connection states the channel and nothing else", () => {
  onLive({
    view: {
      connection: { kind: "live", generated_at_ms: Date.now() },
      activity: { kind: "idle", rate: 0, session_count: 0, contributing_devices: 0 },
    },
  });

  assert.equal(text("connection-state"), "实时");
  assert.equal(text("footer-age"), "");
});

// Degraded is the opposite case: how old the figures are is the whole question,
// so the footer must keep saying it.
test("a degraded connection still reports how old the figures are", () => {
  onLive({
    view: {
      connection: { kind: "offline", generated_at_ms: Date.now() - 42_000 },
      activity: { kind: "idle", rate: 0, session_count: 0, contributing_devices: 0 },
    },
  });

  assert.equal(text("connection-state"), "离线");
  assert.match(text("footer-age"), /^数据年龄 4[123]s$/);
});

// The speed figure is still a 10-minute union; the popover just stops reciting
// the window length in every label.
test("the contributing-session line does not recite the window length", () => {
  onLive({
    view: {
      connection: { kind: "live", generated_at_ms: Date.now() },
      activity: { kind: "active", rate: 68, session_count: 3, contributing_devices: 2 },
    },
  });

  assert.equal(text("active-sessions"), "3 个贡献会话");
  assert.equal(text("contributing-devices"), "2 台贡献设备");
});

test("no static popover copy names the speed window either", () => {
  const markup = require("node:fs").readFileSync(`${__dirname}/index.html`, "utf8");

  assert.ok(!markup.includes("近 10m"), "index.html still names the speed window");
});
