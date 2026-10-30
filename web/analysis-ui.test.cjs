const test = require("node:test");
const assert = require("node:assert/strict");
const vm = require("node:vm");
const fs = require("node:fs");
const path = require("node:path");
function load(file, extra = {}) {
  const nodes = new Map();
  const node = (id) => {
    if (!nodes.has(id))
      nodes.set(id, {
        innerHTML: "",
        textContent: "",
        dataset: {},
        style: {},
        addEventListener() {},
        replaceChildren() {
          this.innerHTML = "";
        },
      });
    return nodes.get(id);
  };
  let option;
  const ctx = vm.createContext({
    document: { getElementById: node, querySelector: node },
    esc: String,
    compact: String,
    full: String,
    usd: (v) => "$" + v,
    relTime: String,
    cssVar: (v) => v,
    SourceUI: { label: (s) => s, color: (s) => "color:" + s, brand: (s) => s },
    ToolUI: {
      analysis: (id, title, html) =>
        `<details id="${id}"><summary>${title}</summary>${html}</details>`,
      updateHTML: (el, html) => {
        el.innerHTML = html;
      },
    },
    ChartRegistry: {
      set: (_, o) => {
        option = o;
      },
    },
    ...extra,
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, file), "utf8"), ctx);
  return { run: (s) => vm.runInContext(s, ctx), node, option: () => option };
}
test("source series shares canonical identity colors including unknown sources", () => {
  const { run } = load("modelsview.js");
  const series = run(
    'ModelsView.sourceSeries([{source:"codex"},{source:"claude-code"},{source:"new-tool"}])',
  );
  for (const row of series) assert.equal(row.color, "color:" + row.key);
});
test("scatter constrains log domain for equal and single token values", () => {
  const { run, option, node } = load("modelsview.js");
  for (const rows of [
    "[{total:100,cost:2},{total:100,cost:3}]",
    "[{total:100,cost:2}]",
  ]) {
    run(`ModelsView.costTokenChart(${rows})`);
    assert.ok(option().xAxis.min > 0 && option().xAxis.min < 100);
    assert.ok(
      option().xAxis.max > 100 && option().xAxis.max / option().xAxis.min < 10,
    );
  }
  run("ModelsView.costTokenChart([{total:100,cost:0,unpriced:true}])");
  assert.match(node("models-cost-token-chart").innerHTML, /暂无/);
});
test("cache creation-only input has no calculable hit rate or invented savings", () => {
  const { run } = load("cacheview.js");
  assert.match(
    run(
      "CacheView.tiles({hit_rate:0,saved_usd:0},{input_tokens:0,cache_read_tokens:0},false)",
    ),
    /暂无可计算输入/,
  );
  assert.match(
    run(
      "CacheView.tiles({hit_rate:0,saved_usd:0},{input_tokens:0,cache_read_tokens:0},false)",
    ),
    /无定价/,
  );
  assert.match(
    run(
      'CacheView.modelTable([{model:"x",input_tokens:0,cache_read_tokens:0,cache_creation_tokens:3}],new Set(["x"]))',
    ),
    /暂无可计算输入/,
  );
});
test("cache refresh preserves shell and chart containers", () => {
  const { run, node } = load("cacheview.js");
  run("CacheView.render({models:[]})");
  node("view-cache").innerHTML += "sentinel";
  run("CacheView.render({models:[]})");
  assert.match(node("view-cache").innerHTML, /sentinel/);
});
test("device detail separates heartbeat, token activity and unobservable sessions", () => {
  const { run } = load("devicesview.js");
  const html = run(
    'DevicesView.deviceDetail({device:"dev",identity_status:"registered",last_seen_at:12,last_ts:8,queued_batches:3,total_tokens:40},{daily:[{device:"dev",bucket:"2026-10-29",total_tokens:40}],live:{}})',
  );
  assert.match(html, /心跳/);
  assert.match(html, /会话状态不可观测/);
  assert.match(html, /2026-10-29/);
});
test("speed lanes share scale, retain missing measurements, and use canonical source colors", () => {
  const file = fs.readFileSync(path.join(__dirname, "speedview.js"), "utf8");
  const nodes = new Map();
  let option;
  const node = (id) => {
    if (!nodes.has(id)) nodes.set(id, { innerHTML: "", textContent: "" });
    return nodes.get(id);
  };
  const ctx = vm.createContext({
    document: { getElementById: node, querySelector: node },
    esc: String,
    cssVar: String,
    telemetrySpeed: (s) => s.speed,
    telemetryCoverageLabel: () => "",
    speedSourceKey: (r) => r.key,
    SourceUI: { label: String, color: (s) => "color:" + s },
    sourceLabelA2: String,
    ChartRegistry: {
      sourceColor: String,
      sourceGradient: String,
      set: (_, o) => {
        option = o;
      },
    },
  });
  vm.runInContext(file, ctx);
  vm.runInContext(
    'SpeedView.renderSourceLanes({speed:{measured_sources:["codex","claude-code"],series:[{start_ms:0,sources:[{key:"codex",contribution_tps:100}]},{start_ms:1000,sources:[{key:"claude-code",contribution_tps:10}]}]}})',
    ctx,
  );
  assert.equal(option.yAxis[0].max, option.yAxis[1].max);
  assert.ok(option.yAxis[0].max >= 100);
  assert.equal(option.series[0].data[1], null);
  assert.equal(option.series[0].itemStyle.color, "color:codex");
});
test("speed model rows name each timing basis, including DeepSeek pure generation", () => {
  const file = fs.readFileSync(path.join(__dirname, "speedview.js"), "utf8");
  const nodes = new Map();
  const node = (id) => {
    if (!nodes.has(id)) nodes.set(id, { innerHTML: "", textContent: "" });
    return nodes.get(id);
  };
  const ctx = vm.createContext({
    document: { getElementById: node, querySelector: node },
    esc: String,
    infoTip: String,
    compact: String,
    full: String,
  });
  vm.runInContext(file, ctx);
  const chip = (sources) => vm.runInContext(`SpeedView.channelChip({sources:${JSON.stringify(sources)}})`, ctx);
  assert.match(chip(["dsh"]), /DeepSeek · 纯生成/);
  assert.match(chip(["dsh"]), /title="[^"]*不含[^"]*重试/);
  // A model fed by dsh and another log source blends two interval bases.
  assert.match(chip(["claude-code", "dsh"]), /混合口径/);
  assert.match(chip(["claude-code"]), /日志推算/);
  assert.match(chip(["codex", "dsh"]), /Codex · 保守下界/);
  const row = (model, sources) =>
    `{model:"${model}",sources:${JSON.stringify(sources)},tps:100,active_ms:1000,samples:3,streams:1,coverage:1,output_tokens:100}`;
  vm.runInContext(`SpeedView.renderModels([${row("deepseek-flash", ["dsh"])}])`, ctx);
  assert.match(node("speed-model-info").innerHTML, /首个流式 chunk/);
  vm.runInContext(`SpeedView.renderModels([${row("claude-opus-4-8", ["claude-code"])}])`, ctx);
  assert.doesNotMatch(node("speed-model-info").innerHTML, /首个流式 chunk/);
});
for (const [file, view, route] of [
  ["devicesview.js", "DevicesView", "devices"],
  ["modelsview.js", "ModelsView", "models"],
  ["cacheview.js", "CacheView", "cache"],
  ["speedview.js", "SpeedView", "speed"],
]) {
  test(`${route} clears protected cached content on 401 and 403 and can recover`, async () => {
    for (const status of [401, 403]) {
      let error = { status };
      let closed = 0,
        disposed = 0,
        lastState;
      const { run, node } = load(file, {
        Api: {
          get: async () => {
            if (error) throw error;
            return { summary: [], models: [], by_source: [] };
          },
        },
        LiveSnapshotCache: { load: async () => ({ speed: {} }) },
        TelemetryCache: { load: async () => ({ data: {} }) },
        isCurrentGeneration: (a, b) => a === b,
        classifyAPIError: (e) => ({
          kind: e.status === 403 ? "forbidden" : "unauthorized",
          title: "auth",
          detail: "auth",
        }),
        renderState: (_, state) => {
          lastState = state;
        },
        ChartRegistry: { disposeWithin: () => disposed++ },
        DetailDrawer: { close: () => closed++ },
      });
      node("view-" + route).innerHTML = "protected payload";
      node("view-" + route).dataset.built = "1";
      node("#tool-detail-drawer .drawer-content").innerHTML =
        "protected detail";
      run(
        `${view}.lastData={secret:'private'}; ${view}._displayData={secret:'private'}; ${view}._selectedDevice='dev'; ${view}.render=function(d){this.rendered=d;};`,
      );
      await run(`${view}.load()`);
      assert.equal(run(`${view}.lastData`), null);
      assert.equal(node("view-" + route).innerHTML, "");
      assert.equal(node("#tool-detail-drawer .drawer-content").innerHTML, "");
      assert.equal(node("view-" + route).dataset.built, undefined);
      assert.equal(
        lastState.kind,
        status === 403 ? "forbidden" : "unauthorized",
      );
      assert.ok(closed > 0 && disposed > 0);
      error = null;
      await run(`${view}.load()`);
      assert.ok(run(`${view}.lastData`));
    }
  });
}
for (const [file, view] of [
  ["devicesview.js", "DevicesView"],
  ["modelsview.js", "ModelsView"],
]) {
  test(`${view} marks failed auxiliary live refresh stale and preserves previous measurement`, async () => {
    let state;
    const { run, node } = load(file, {
      Api: {
        get: async () => ({
          summary: [{ total_tokens: 5 }],
          by_source: [{ total_tokens: 5 }],
        }),
      },
      LiveSnapshotCache: {
        load: async () => {
          throw { status: 0 };
        },
      },
      isCurrentGeneration: (a, b) => a === b,
      renderState: (_, next) => {
        state = next;
      },
      classifyAPIError: () => ({ kind: "error", title: "network" }),
    });
    run(
      `${view}.lastData={live:{speed:{tps:99}},liveState:{updatedAt:12}};${view}.render=function(d){this.rendered=d;}`,
    );
    await run(`${view}.load()`);
    assert.equal(run(`${view}.lastData.live.speed.tps`), 99);
    assert.equal(run(`${view}.lastData.liveState.stale`), true);
    assert.equal(state.kind, "stale");
    assert.match(node("refresh-note").textContent, /实时.*失败|实时.*过期/);
  });
}
