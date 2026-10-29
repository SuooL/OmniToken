const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs"),
  vm = require("node:vm");
function load() {
  const context = vm.createContext({
    console,
    Date,
    URLSearchParams,
    setTimeout,
    clearTimeout,
    document: { addEventListener() {} },
    cssVar: (key) => key,
  });
  for (const file of ["format-core.js", "tool-ui.js"])
    vm.runInContext(fs.readFileSync(`${__dirname}/${file}`, "utf8"), context);
  return context;
}
test("official zero has a real zero meter and zero allowance is explicit", () => {
  const c = load();
  const html = vm.runInContext(
    'QuotaUI.render([{source:"claude-code",kind:"subscription",window_minutes:300,authoritative:true,used_percent:0,remaining_tokens:0,capacity_tokens:100,tokens:16000000}],[])',
    c,
  );
  assert.match(html, /aria-valuenow="0"/);
  assert.match(html, /0%/);
  assert.match(html, /data-remaining="0"/);
  assert.ok(!html.includes("20.0%"));
});
test("missing official reading is not a synthetic quota, and metered traffic has no meter", () => {
  const c = load();
  const html = vm.runInContext(
    'QuotaUI.render([{source:"codex",kind:"subscription",placeholder:true,tokens:8000000},{kind:"api",label:"API",tokens:200000}],[])',
    c,
  );
  assert.ok(!html.includes('role="progressbar"'));
  assert.match(html, /暂无/);
  assert.match(html, /按量计费/);
});
test("both windows independently expose real percentages with capped red overshoot", () => {
  const c = load();
  const html = vm.runInContext(
    'QuotaUI.render([{source:"codex",kind:"subscription",window_minutes:300,authoritative:true,used_percent:2,remaining_tokens:100,capacity_tokens:1000},{source:"codex",kind:"subscription",window_minutes:10080,authoritative:true,used_percent:123,remaining_tokens:500}],[])',
    c,
  );
  assert.match(html, /aria-valuenow="100"[^>]*aria-valuetext="123%/);
  assert.match(html, /data-risk="critical"/);
  assert.equal((html.match(/data-remaining="0"/g) || []).length, 2);
});
test("scoped quota readings retain their identity and source payload cannot inject markup", () => {
  const c = load();
  const html = vm.runInContext(
    'QuotaUI.render([], [{source:"claude-code",window_minutes:10080,used_percent:80,scope:"seven_day"},{source:"claude-code",window_minutes:10080,used_percent:95,scope:"seven_day:opus"}])',
    c,
  );
  assert.match(html, /95%/);
  assert.match(html, /opus/);
  assert.match(
    vm.runInContext('SourceUI.brand("<script>")', c),
    /&lt;script&gt;/,
  );
});
test("one live cache request serves concurrent consumers and auth failures clear old data", async () => {
  const c = load();
  vm.runInContext(
    'let calls=0; let fail=false; const Api={get:async()=>{calls++;if(fail){const e=new Error("denied");e.status=401;throw e;}return {generated_at:100};}};',
    c,
  );
  await vm.runInContext(
    "Promise.all([LiveSnapshotCache.load(),LiveSnapshotCache.load()])",
    c,
  );
  assert.equal(vm.runInContext("calls", c), 1);
  vm.runInContext("fail=true", c);
  await assert.rejects(
    vm.runInContext("LiveSnapshotCache.load({force:true})", c),
  );
  assert.equal(vm.runInContext("LiveSnapshotCache.data", c), null);
});
test("invalid explicit authoritative readings stay unknown; omitted Go zero remains zero", () => {
  const c = load();
  for (const value of ["null", "NaN", "-1"]) {
    const html = vm.runInContext(
      `QuotaUI.render([{source:"codex",kind:"subscription",window_minutes:300,authoritative:true,used_percent:${value}}],[])`,
      c,
    );
    assert.ok(!html.includes('role="progressbar"'), value);
  }
  const omitted = vm.runInContext(
    'QuotaUI.render([{source:"codex",kind:"subscription",window_minutes:300,authoritative:true}],[])',
    c,
  );
  assert.match(omitted, /aria-valuenow="0"/);
});
test("backend source-less placeholders retain provider unknown windows", () => {
  const c = load();
  const html = vm.runInContext(
    'QuotaUI.render([{key:"codex",kind:"subscription",window_minutes:300,placeholder:true}],[])',
    c,
  );
  assert.match(html, /Codex/);
  assert.match(html, /Week/);
  assert.ok(!html.includes('role="progressbar"'));
});
test("equal percentage on a different model scope never borrows its estimate", () => {
  const c = load();
  const html = vm.runInContext(
    'QuotaUI.render([{source:"claude-code",kind:"subscription",window_minutes:10080,scope:"seven_day",authoritative:true,used_percent:20,remaining_tokens:500}], [{source:"claude-code",window_minutes:10080,scope:"seven_day:opus",used_percent:20}])',
    c,
  );
  assert.match(html, /opus/);
  assert.ok(!html.includes('data-remaining="500"'));
});
test("new SSE snapshot cannot be overwritten by older HTTP completion", async () => {
  const c = load();
  vm.runInContext(
    "let resolve;const Api={get:()=>new Promise(r=>resolve=r)};const pending=LiveSnapshotCache.load();LiveSnapshotCache.adopt({generated_at:200});resolve({generated_at:100});",
    c,
  );
  const result = await vm.runInContext("pending", c);
  assert.equal(result.generated_at, 200);
  assert.equal(vm.runInContext("LiveSnapshotCache.data.generated_at", c), 200);
});
test("invalidated in-flight cache cannot deliver data across credentials", async () => {
  const c = load();
  vm.runInContext(
    "let resolve;const Api={get:()=>new Promise(r=>resolve=r)};const pending=LiveSnapshotCache.load();LiveSnapshotCache.invalidate();resolve({generated_at:100});",
    c,
  );
  await assert.rejects(vm.runInContext("pending", c));
  assert.equal(vm.runInContext("LiveSnapshotCache.data", c), null);
});
test("custom source names that match prototype properties keep their literal identity", () => {
  const c = load();
  assert.match(vm.runInContext('SourceUI.brand("__proto__")',c), /__proto__/);
  assert.equal(vm.runInContext('SourceUI.label("constructor")',c), 'constructor');
});
