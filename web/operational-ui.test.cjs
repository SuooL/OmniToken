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
        style: {},
        addEventListener() {},
      });
    return nodes.get(id);
  };
  const context = vm.createContext({
    console,
    document: { getElementById: node },
    esc: (v) => String(v ?? "").replaceAll("<", "&lt;"),
    compact: String,
    full: String,
    SourceUI: { label: (v) => v, brand: (v) => `<b>${v}</b>` },
    ToolUI: {
      updateHTML: (el, html) => {
        el.innerHTML = html;
      },
    },
    cssVar: () => "#888",
    ...extra,
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, file), "utf8"), context);
  return { context, node, run: (code) => vm.runInContext(code, context) };
}
test("historical trend uses actual daily token buckets and exposes companion rows", () => {
  let chart;
  const { run, node } = load("overview.js", {
    ChartRegistry: {
      set: (...args) => {
        chart = args;
      },
    },
  });
  run(
    'Overview.renderTrend([{bucket:"2026-10-27",total_tokens:0},{bucket:"2026-10-28",total_tokens:450}])',
  );
  assert.deepEqual(Array.from(chart[1].series[0].data), [0, 450]);
  assert.deepEqual(Array.from(chart[1].xAxis.data), [
    "2026-10-27",
    "2026-10-28",
  ]);
  assert.equal(chart[2].rows.length, 2);
  run("Overview.renderTrend([])");
  assert.match(node("overview-token-trend").innerHTML, /暂无/);
});
test("source drawer uses explicit source model association even for cross-provider names", () => {
  const { run } = load("overview.js");
  const html = run(
    'Overview.sourceDetail({source:"codex",tokens:30,models:[{model:"claude-test",tokens:30,share:1}]})',
  );
  assert.match(html, /claude-test/);
  assert.match(html, /30/);
  assert.match(
    run('Overview.sourceDetail({source:"dsh",tokens:0,models:[]})'),
    /暂无/,
  );
});
test("live delegates official zero, missing, over-limit and remaining zero unchanged to shared quota UI", () => {
  let received;
  const { run, node } = load("live.js", {
    QuotaUI: {
      render: (windows, quotas) => {
        received = { windows, quotas };
        return "shared quota";
      },
    },
  });
  run(
    "Live.renderQuotas([{used_percent:0},{used_percent:null},{used_percent:105}], [{remaining_tokens:0}])",
  );
  assert.equal(received.quotas[0].used_percent, 0);
  assert.equal(received.quotas[1].used_percent, null);
  assert.equal(received.quotas[2].used_percent, 105);
  assert.equal(received.windows[0].remaining_tokens, 0);
  assert.match(node("quota-row").innerHTML, /shared quota/);
});
test("live timeline retains every concurrent session", () => {
  const { run, node } = load("live.js", { repoLabel: String });
  run(
    'Live.renderLanes({window_start_ms:0,window_end_ms:1000,sessions:Array.from({length:12},(_,i)=>({session_id:"s"+i,device:"dev",spans:[[0,500]],tps:1,contribution_tps:1})),spans:[[0,500]],tps:12})',
  );
  assert.match(node("lanes").innerHTML, /s11/);
  assert.doesNotMatch(node("lanes").innerHTML, /另有/);
});
test("overview builds one shell; polling has dedicated data update slots", () => {
  const source = fs.readFileSync(path.join(__dirname, "overview.js"), "utf8");
  assert.match(source, /buildShell\(/);
  assert.match(source, /ToolUI\.updateHTML/);
  assert.match(source, /LiveSnapshotCache\.load\(/);
  assert.match(source, /overview-token-trend/);
});
test("overview marks a failed live refresh stale while preserving explicit last-good quota", async () => {
  let state;
  let rendered;
  const cached = { windows: [{ used_percent: 90 }] };
  const { run, node } = load("overview.js", {
    Api: { get: async () => ({ daily: [] }) },
    LiveSnapshotCache: {
      data: cached,
      load: async () => {
        throw new Error("offline");
      },
    },
    TelemetryCache: {
      load: async () => ({ data: { today: { total_tokens: 10 } } }),
    },
    isCurrentGeneration: (a, b) => a === b,
    renderState: (_el, value) => {
      state = value;
    },
    relTime: () => "刚才",
  });
  run(
    "Overview.render = (payload) => { globalThis.renderedPayload = payload; }",
  );
  await run("Overview.load()");
  rendered = run("renderedPayload");
  assert.equal(rendered.live, cached);
  assert.equal(rendered.liveStale, true);
  assert.equal(state.kind, "stale");
  assert.match(node("refresh-note").textContent, /部分|配额|过期/);
});
test("source refresh uses a stable focus key and whole-row click target", () => {
  const source = fs.readFileSync(path.join(__dirname, "overview.js"), "utf8");
  assert.match(source, /data-focus-key=/);
  assert.match(source, /<tr data-source-detail=/);
});
test("SSE snapshots seed shared live cache and disconnect marks retained data stale", () => {
  const listeners = {};
  const stream = {
    addEventListener: (name, callback) => {
      listeners[name] = callback;
    },
  };
  let adopted;
  const { run } = load("live.js", {
    Api: { stream: () => stream },
    LiveSnapshotCache: {
      adopt: (data) => {
        adopted = data;
      },
    },
    performance: { now: () => 100 },
    setInterval: () => 1,
  });
  run("Live.render=()=>{}; Live.loadTelemetry=()=>{}; Live.start()");
  listeners.snapshot({ data: '{"windows":[]}' });
  assert.equal(adopted.windows.length, 0);
  assert.equal(run("Live.connectionStale"), false);
  stream.onerror();
  assert.equal(run("Live.connectionStale"), true);
});
test("missing source bucket remains a chart gap rather than measured zero", () => {
  let chart;
  const { run, node } = load("overview.js", {
    telemetrySpeed: (snapshot) => snapshot.speed,
    telemetryCoverageLabel: () => "部分可测",
    currentAggregateTPS: () => null,
    speedSourceKey: (row) => row.source,
    ChartRegistry: {
      set: (...args) => {
        chart = args;
      },
      sourceColor: () => "#888",
      sourceGradient: () => "#888",
    },
  });
  // Only the coverage query needs a selector; chart hosts are returned by id.
  run('document.querySelector=()=>document.getElementById("coverage")');
  run(
    'Overview.renderSpeed({speed:{measured_sources:["codex"],series:[{start_ms:0,sources:[]},{start_ms:60000,sources:[{source:"codex",contribution_tps:0}]}]}})',
  );
  assert.equal(chart[1].series[0].data[0], null);
  assert.equal(chart[1].series[0].data[1], 0);
  assert.equal(chart[2].columns[1].format(null), "—");
});
test("empty trend disposes its existing canvas and clears old companion data", () => {
  let disposed = false;
  let fallback;
  const { run } = load("overview.js", {
    ChartRegistry: {
      disposeWithin: () => {
        disposed = true;
      },
      renderFallback: (_el, data) => {
        fallback = data;
      },
    },
  });
  run("Overview.renderTrend([])");
  assert.equal(disposed, true);
  assert.equal(fallback.rows.length, 0);
});
