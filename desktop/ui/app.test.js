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

const { coverageLabel, onLive, renderAll } = require("./app.js");

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

const quotaReading = (html, window) => {
  const match = html.match(new RegExp(`<div class="quota-window" data-window="${window}">([\\s\\S]*?)</div>`));
  assert.ok(match, `missing ${window} reading`);
  return match[1];
};

test("5h and Week percentages independently step at 70, 80 and 90 percent", () => {
  for (const window of ["five_hour", "weekly"]) {
    for (const [percent, expected] of [
      [0, ""], [50, ""], [69.9, ""], [70, "warn"], [75, "warn"], [79.9, "warn"],
      [80, "serious"], [89.9, "serious"], [90, "critical"], [100, "critical"], [120, "critical"],
      [0, ""], [null, ""], [undefined, ""], [NaN, ""], [Infinity, ""],
    ]) {
      const html = showQuotas([{
        source: "claude-code", label: "Claude",
        five_hour_percent: 100, weekly_percent: 100,
        [`${window}_percent`]: percent,
      }]);
      assert.equal(quotaReading(html, window).match(/data-severity="([^"]*)"/)[1], expected,
        `${window} at ${percent}%`);
    }
  }
});

test("low five-hour usage cannot hide a near-full weekly quota", () => {
  const html = showQuotas([{
    source: "claude-code", label: "Claude", five_hour_percent: 2, weekly_percent: 96,
    five_hour_remaining_tokens: 132_000_000, weekly_remaining_tokens: 8_000_000,
  }]);
  assert.match(quotaReading(html, "five_hour"), /5h 已用/);
  assert.match(quotaReading(html, "five_hour"), /data-severity="">2%/);
  assert.match(quotaReading(html, "weekly"), /Week 已用/);
  assert.match(quotaReading(html, "weekly"), /data-severity="critical">96%/);
  assert.match(html, /预估 5h 剩余<\/span><b>8.0M tokens<\/b>/);
  assert.match(html, /预估周剩余<\/span><b>8.0M tokens<\/b>/);
  assert.ok(html.indexOf("quota-estimates") > html.indexOf('data-window="weekly"'));
});

test("a missing window stays visible as unavailable instead of borrowing another window", () => {
  const html = showQuotas([{
    source: "codex", label: "Codex", five_hour_percent: null, weekly_percent: 72,
    five_hour_remaining_tokens: null, weekly_remaining_tokens: 114_000_000,
  }]);
  assert.match(quotaReading(html, "five_hour"), />暂无<\/strong>/);
  assert.match(quotaReading(html, "weekly"), /data-severity="warn">72%/);
  assert.match(html, /预估 5h 剩余<\/span><b>暂无估算<\/b>/);
  assert.match(html, /预估周剩余<\/span><b>114M tokens<\/b>/);
});

test("missing or invalid estimates are not rendered as zero or reused from the old headline", () => {
  for (const invalid of [null, undefined, NaN, Infinity, -1]) {
    const html = showQuotas([{
      source: "claude-code", label: "Claude", five_hour_percent: 2, weekly_percent: 96,
      five_hour_remaining_tokens: invalid, weekly_remaining_tokens: invalid,
      used_tokens: 2_200_000, remaining_tokens: 132_000_000, projected_percent: 3,
    }]);
    assert.equal((html.match(/暂无估算/g) || []).length, 2);
    assert.ok(!html.includes("2.2M"));
    assert.ok(!html.includes("132M"));
    assert.ok(!html.includes("3%"));
  }
});

test("zero usage and zero remaining are real readings", () => {
  const html = showQuotas([{
    source: "claude-code", label: "Claude", five_hour_percent: 0, weekly_percent: 100,
    five_hour_remaining_tokens: 132_000_000, weekly_remaining_tokens: 0,
  }]);
  assert.match(quotaReading(html, "five_hour"), /data-severity="">0%/);
  assert.match(quotaReading(html, "weekly"), /data-severity="critical">100%/);
  assert.match(html, /预估周剩余<\/span><b>0 tokens<\/b>/);
});

test("an unavailable source never claims either window is untouched", () => {
  const html = showQuotas([{ source: "codex", label: "Codex" }]);
  assert.equal((html.match(/>暂无<\/strong>/g) || []).length, 2);
  assert.equal((html.match(/暂无估算/g) || []).length, 2);
  assert.ok(!html.includes("0%"));
});

test("no quota rows at all leaves the card empty rather than inventing sources", () => {
  assert.ok(showQuotas([]).includes("官方配额不可用"));
  assert.ok(showQuotas(undefined).includes("官方配额不可用"));
});


test("five-hour usable allowance is capped by weekly allowance, including exhaustion", () => {
  for (const [fiveHour, weekly, expected] of [[132_000_000, 8_000_000, "8.0M"], [5_000_000, 8_000_000, "5.0M"], [132_000_000, 0, "0"], [null, 0, "0"]]) {
    const html = showQuotas([{
      source: "claude-code", label: "Claude", five_hour_percent: 2, weekly_percent: 96,
      five_hour_remaining_tokens: fiveHour, weekly_remaining_tokens: weekly,
    }]);
    assert.ok(html.includes(`预估 5h 剩余</span><b>${expected} tokens</b>`));
  }
});

test("both countdowns use total hours and minutes and advance without a new snapshot", () => {
  const originalNow = Date.now;
  let now = 1_800_000_000_000;
  Date.now = () => now;
  try {
    const html = showQuotas([{
      source: "claude-code", label: "Claude", five_hour_percent: 2, weekly_percent: 96,
      five_hour_resets_at_ms: now + 91 * 60_000,
      weekly_resets_at_ms: now + (72 * 60 + 5) * 60_000,
    }]);
    assert.match(quotaReading(html, "five_hour"), /1h31min 后重置/);
    assert.match(quotaReading(html, "weekly"), /72h5min 后重置/);
    now += 60_000;
    renderAll();
    const updated = document.getElementById("quota-grid").innerHTML;
    assert.match(quotaReading(updated, "five_hour"), /1h30min 后重置/);
    assert.match(quotaReading(updated, "weekly"), /72h4min 后重置/);
  } finally { Date.now = originalNow; }
});

test("countdowns distinguish unknown reset times from an expired deadline", () => {
  const originalNow = Date.now;
  Date.now = () => 1_800_000_000_000;
  try {
    for (const [deadline, expected] of [[null, "重置时间暂无"], [0, "重置时间暂无"], [NaN, "重置时间暂无"], [1_800_000_000_000 - 1, "0h0min 后重置"], [1_800_000_000_000 + 31 * 60_000, "0h31min 后重置"]]) {
      const html = showQuotas([{
        source: "claude-code", label: "Claude", five_hour_percent: 2,
        five_hour_resets_at_ms: deadline,
      }]);
      assert.ok(quotaReading(html, "five_hour").includes(expected));
    }
  } finally { Date.now = originalNow; }
});
