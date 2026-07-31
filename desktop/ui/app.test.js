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
// The real formatters, not stand-ins: how a percentage or a reset instant is
// written is defined once in web/format-core.js (ADR-0014), and a test that
// re-implements it would pass while the popover printed something else.
require("node:vm").runInThisContext(
  require("node:fs").readFileSync(`${__dirname}/format-core.js`, "utf8"),
);

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

const showQuotas = (quotas) => {
  onLive({ view: { connection: { kind: "live", generated_at_ms: Date.now() }, quotas } });
  return document.getElementById("quota-grid").innerHTML;
};

test("a five-hour card leads with it and still shows the projection and the week", () => {
  const html = showQuotas([{
    source: "claude-code",
    label: "Claude",
    basis: "five_hour",
    five_hour_percent: 24,
    projected_percent: 36.5,
    weekly_percent: 12,
    resets_in_minutes: 102,
  }]);

  assert.match(html, /<strong[^>]*>24%<\/strong>/);
  assert.ok(html.includes("官方 5h 用量"));
  assert.ok(html.includes("1h42m 后重置"));
  assert.ok(html.includes("预估 5h 37%"));
  assert.ok(html.includes("周 12%"));
});

// Codex has had no 5-hour window since its `primary` limit became weekly, so
// this is its permanent state and has to read as a stated fact, not a fault.
test("a weekly fallback names the window it fell back to", () => {
  const html = showQuotas([{
    source: "codex",
    label: "Codex",
    basis: "weekly",
    five_hour_percent: null,
    projected_percent: null,
    weekly_percent: 72,
    resets_in_minutes: 9575,
  }]);

  assert.match(html, /<strong[^>]*>72%<\/strong>/);
  assert.ok(html.includes("官方周用量"));
  assert.ok(html.includes("无官方 5h 数据"));
});

// Absent data is not zero usage: rendering "0%" here would tell the user they
// have a full quota when what we actually have is no reading.
test("a source with no official quota says so instead of showing zero", () => {
  const html = showQuotas([{
    source: "codex",
    label: "Codex",
    basis: "none",
    five_hour_percent: null,
    projected_percent: null,
    weekly_percent: null,
    resets_in_minutes: null,
  }]);

  assert.ok(html.includes("暂无"));
  assert.ok(!html.includes("0%"));
  assert.ok(!html.includes("后重置"));
});

// A quota already at 0% is a reading, and the card must not confuse it with the
// case above.
test("an untouched authoritative window is a reading, not a blank", () => {
  const html = showQuotas([{
    source: "claude-code",
    label: "Claude",
    basis: "five_hour",
    five_hour_percent: 0,
    projected_percent: null,
    weekly_percent: null,
    resets_in_minutes: 300,
  }]);

  assert.match(html, /<strong[^>]*>0%<\/strong>/);
  assert.ok(!html.includes("暂无"));
  // Nothing to project from yet, and the popover says that rather than 0%.
  assert.ok(html.includes("预估 5h —"));
});

test("no quota rows at all leaves the card empty rather than inventing sources", () => {
  assert.ok(showQuotas([]).includes("官方配额不可用"));
  assert.ok(showQuotas(undefined).includes("官方配额不可用"));
});
