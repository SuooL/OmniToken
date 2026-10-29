const test = require("node:test");
const assert = require("node:assert/strict");
const vm = require("node:vm");
const fs = require("node:fs");
const path = require("node:path");
function boot(file, name) {
  const nodes = new Map();
  const node = (id) => {
    if (!nodes.has(id))
      nodes.set(id, {
        innerHTML: "",
        value: "",
        textContent: "",
        dataset: {},
        hidden: false,
        insertAdjacentHTML(where, html) {
          this.innerHTML += html;
        },
        addEventListener() {},
        querySelector(selector) {
          return selector.startsWith("#reports-")
            ? node(selector.slice(1))
            : null;
        },
        querySelectorAll() {
          return [];
        },
        setAttribute() {},
        removeAttribute() {},
        firstElementChild: { outerHTML: '<option value="">全部</option>' },
      });
    return nodes.get(id);
  };
  const escape = (v) =>
    String(v ?? "").replace(
      /[&<>"']/g,
      (c) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[c],
    );
  const context = vm.createContext({
    URLSearchParams,
    console,
    Date,
    Map,
    Set,
    Number,
    document: { getElementById: node, querySelectorAll: () => [] },
    location: { hash: "#details" },
    Api: { token: "", adminToken: "" },
    esc: escape,
    full: String,
    compact: String,
    usd: (v) => "$" + v,
    repoLabel: (v) => v || "",
    relTime: () => "刚刚",
    cssVar: () => "",
    mixWithSurface: () => "",
    classifyAPIError: (error) => ({kind: error.status === 401 || error.status === 403 ? 'unauthorized' : 'unavailable', title: error.status === 403 ? '无权访问' : '未授权'}),
    isCurrentGeneration: (current, expected) => current === expected,
    renderState: (root, state) => {context.lastState = state;},
    ChartRegistry: { set() {}, disposeWithin() {} },
    SourceUI: {
      brand: (v) => "<brand>" + escape(v) + "</brand>",
      label: (v) => v,
    },
    ToolUI: {
      analysis: (id, title, html) =>
        `<details id="${id}"><summary>${title}</summary>${html}</details>`,
      updateHTML: (el, html) => {
        el.innerHTML = html;
      },
    },
    DetailDrawer: {
      open(v) {
        context.drawer = v;
      },
      update(key, html) {
        context.drawerUpdate = { key, html };
      },
      close() {context.drawerClosed = true;},
    },
  });
  vm.runInContext(
    fs.readFileSync(path.join(__dirname, file), "utf8") +
      `\nthis.subject = ${name};`,
    context,
  );
  return { subject: context.subject, context, node };
}
test("detail route restores every filter and pagination, clears omitted values", () => {
  const { subject: d } = boot("details.js", "Details");
  d.applyRoute(
    new URLSearchParams(
      "device=d&source=codex&model=m&repo=a%2Fb&session=s&days=30&offset=200",
    ),
    false,
  );
  assert.deepEqual(
    { ...d.filters },
    {
      device: "d",
      source: "codex",
      model: "m",
      repo: "a/b",
      session: "s",
      days: "30",
    },
  );
  assert.equal(d.offset, 200);
  assert.equal(
    d.routeHref(),
    "#details?device=d&source=codex&model=m&repo=a%2Fb&session=s&days=30&offset=200",
  );
  d.applyRoute(new URLSearchParams("offset=-1&days=nope"), false);
  assert.equal(d.filters.device, "");
  assert.equal(d.filters.session, "");
  assert.equal(d.filters.days, "7");
  assert.equal(d.offset, 0);
});
test("detail table comes before closed current-page analysis; selected route values survive absent options", async () => {
  const { subject: d, node, context } = boot("details.js", "Details");
  d.build();
  const html = node("view-details").innerHTML;
  assert.ok(html.indexOf('id="d-table"') < html.indexOf('id="d-distribution"'));
  assert.match(html, /<details[^>]*id="details-analysis"[^>]*>/);
  d.filters.device = "rare<device";
  context.Api.get = async () => ({ rows: [] });
  await d.loadOptions();
  assert.match(node("d-f-device").innerHTML, /rare&lt;device/);
});
test("event drawer escapes every metadata value and preserves unknown cost", () => {
  const { subject: d, context } = boot("details.js", "Details");
  const event = {
    event_id: "e<1",
    source: "codex",
    ts: 1,
    device: "<img>",
    model: "<script>",
    cwd: "<svg>",
    cost_usd: null,
    account_label: "<b>",
  };
  d.events = [event];
  d.openEvent("e<1", {});
  assert.equal(context.drawer.key, "event:e<1");
  assert.match(context.drawer.html, /&lt;svg&gt;/);
  assert.match(context.drawer.html, /&lt;b&gt;/);
  assert.doesNotMatch(context.drawer.html, /<svg>|<script>|<img>|\$0/);
  assert.match(d.rowHTML(event), /data-event-key="e&lt;1"/);
});
test("settings task navigation keeps drafts, guards and advanced actions scoped to devices", () => {
  const { subject: s, node } = boot("settingsview.js", "SettingsView");
  s._draft.readToken = "unsaved";
  s.render();
  const html = node("view-settings").innerHTML;
  for (const group of ["connection", "pricing", "devices", "appearance"])
    assert.match(html, new RegExp('data-settings-nav="' + group + '"'));
  assert.match(
    html,
    /data-settings-panel="devices"[\s\S]*<details[^>]*id="settings-device-advanced"/,
  );
  assert.doesNotMatch(html, /主题跟随系统/);
  s.selectGroup("pricing");
  assert.equal(s._draft.readToken, "unsaved");
  assert.match(s.mergeInteractive(), /预览影响/);
});
test("reports table precedes collapsed analysis and sessions link to event filter", () => {
  const { subject: r, node } = boot("reports.js", "Reports");
  r.renderShell();
  const html = node("view-reports").innerHTML;
  assert.ok(
    html.indexOf('id="reports-table"') < html.indexOf('id="reports-trend"'),
  );
  assert.match(html, /<details[^>]*id="reports-analysis"[^>]*>/);
  r.renderSessions([
    { session_id: "a&b", source: "codex", first_ts: 1, last_ts: 2 },
  ]);
  assert.match(
    node("reports-table").innerHTML,
    /href="#details\?session=a%26b&amp;days=30"/,
  );
});

test("detail changes create shareable history entries and preserve filters when session clears", () => {
  const { subject: d, context, node } = boot("details.js", "Details");
  d.applyRoute(
    new URLSearchParams("device=d&model=m&days=90&session=s&offset=100"),
    false,
  );
  d.navigate({ source: "codex", offset: 0 });
  assert.equal(
    context.location.hash,
    "#details?device=d&source=codex&model=m&session=s&days=90",
  );
  assert.equal(
    d.offset,
    100,
    "router applies new state after recording old history",
  );
  d.renderChips();
  assert.match(
    node("d-chips").innerHTML,
    /href="#details\?device=d&amp;model=m&amp;days=90"/,
  );
});
test("detail ignores out-of-order and route-leave responses; failed refresh retains last data", async () => {
  const { subject: d, context, node } = boot("details.js", "Details");
  const pending = [];
  context.Api.get = () => new Promise((resolve) => pending.push(resolve));
  let rendered = [];
  d.render = (events) => {
    rendered = events;
  };
  const first = d.load();
  const second = d.load();
  pending[1]({ total: 1, events: ["new"] });
  await second;
  pending[0]({ total: 2, events: ["old"] });
  await first;
  assert.deepEqual(rendered, ["new"]);
  assert.equal(d.total, 1);
  const third = d.load();
  d.leave();
  pending[2]({ total: 9, events: ["left"] });
  await third;
  assert.deepEqual(rendered, ["new"]);
  context.Api.get = async () => {
    throw Error("offline");
  };
  await d.load();
  assert.match(node("d-note").textContent, /保留/);
  assert.deepEqual(rendered, ["new"]);
});
test("settings merge still requires preview and byte-for-byte complete identity", () => {
  const { subject: s, context } = boot("settingsview.js", "SettingsView");
  const submit = (value) => {
    context.input = value;
    return vm.runInContext("mergeSubmission(input)", context);
  };
  assert.equal(
    submit({ from: "full-device", to: "other", confirm: "full-device" }).ok,
    false,
  );
  assert.equal(
    submit({
      from: "full-device",
      to: "other",
      plan: {},
      confirm: " full-device",
    }).ok,
    false,
  );
  assert.equal(
    submit({
      from: "full-device",
      to: "other",
      plan: {},
      confirm: "full-device",
    }).ok,
    true,
  );
  s._merge = {
    from: "full-device",
    to: "other",
    plan: { events_moved: 1 },
    confirm: "full-device",
  };
  s.invalidateMergePlan();
  assert.equal(s._merge.plan, null);
  assert.equal(s._merge.confirm, "");
});
test("report export uses selected range, blocks duplicate click, and exposes retryable failure", async () => {
  const { subject: r, context, node } = boot("reports.js", "Reports");
  let finish;
  let calls = 0;
  let requested;
  context.downloadAPI = (url) => {
    calls++;
    requested = url;
    return new Promise((resolve) => {
      finish = resolve;
    });
  };
  context.APIError = class APIError extends Error {};
  const button = { disabled: false, setAttribute() {}, removeAttribute() {} };
  r.days = 90;
  r.granularity = "session";
  const pending = r.download("csv", button);
  await r.download("csv", button);
  assert.equal(calls, 1);
  assert.equal(
    requested,
    "/api/v1/reports?granularity=session&days=90&format=csv",
  );
  assert.equal(node("reports-export-status").hidden, false);
  assert.equal(button.disabled, true);
  finish();
  await pending;
  assert.equal(button.disabled, false);
  context.downloadAPI = async () => {
    throw Error("offline");
  };
  await r.download("json", button);
  assert.match(node("reports-export-status").textContent, /导出失败:offline/);
  assert.equal(button.disabled, false);
});

for (const status of [401, 403]) {
  test(`detail ${status} clears protected rows, drawer and pending option names without destroying filter shell`, async () => {
    const { subject: d, context, node } = boot('details.js', 'Details');
    d.build(); d.built = true; d.filters.device = 'selected';
    d.deviceNames = {selected: 'protected-name'};
    d.total = 1; d.events = [{event_id:'secret'}]; d.selectedEvent = 'secret'; d.lastUpdated = '12:00';
    node('d-table').innerHTML = 'secret-row'; node('d-distribution').innerHTML = 'secret-chart';
    const shell = node('view-details').innerHTML;
    const pending = []; context.Api.get = () => new Promise(resolve => pending.push(resolve));
    const options = d.loadOptions();
    context.Api.get = async () => { throw Object.assign(Error('denied'), {status}); };
    await d.load();
    for (const resolve of pending) resolve({rows:[{key:'old-device',display_name:'old-private-name'}]});
    await options;
    assert.equal(d.events.length, 0); assert.equal(d.total, 0); assert.equal(d.lastUpdated, null);
    assert.equal(d.selectedEvent, null); assert.equal(context.drawerClosed, true);
    assert.equal(Object.keys(d.deviceNames).length, 0);
    assert.doesNotMatch(node('d-table').innerHTML, /secret-row/);
    assert.doesNotMatch(node('d-distribution').innerHTML, /secret-chart/);
    assert.doesNotMatch(node('d-f-device').innerHTML, /old-private-name|protected-name/);
    assert.match(node('d-note').textContent, /未授权|无权/);
    assert.equal(d.filters.device, 'selected'); assert.equal(d.built, true);
    assert.equal(node('view-details').innerHTML, shell);
  });
  test(`report ${status} clears protected data and pricing names, retains scope controls and retry`, async () => {
    const { subject: r, context, node } = boot('reports.js', 'Reports');
    r.renderShell(); r._rendered = true; r.lastData = {rows:['secret']}; r.granularity = 'session'; r.days = 90;
    const shell = node('view-reports').innerHTML;
    node('reports-table').innerHTML = 'private-row'; node('reports-trend').innerHTML = 'private-chart';
    node('reports-unpriced').textContent = 'private-model'; node('reports-unpriced').hidden = false;
    context.Api.get = async () => { throw Object.assign(Error('denied'), {status}); };
    await r.load();
    assert.equal(r.lastData, null); assert.equal(r._rendered, true);
    assert.doesNotMatch(node('reports-table').innerHTML, /private-row/);
    assert.doesNotMatch(node('reports-trend').innerHTML, /private-chart/);
    assert.equal(node('reports-unpriced').textContent, ''); assert.equal(node('reports-unpriced').hidden, true);
    assert.equal(context.lastState.kind, 'unauthorized');
    assert.equal(r.granularity, 'session'); assert.equal(r.days, 90);
    assert.equal(node('view-reports').innerHTML, shell);
    context.Api.get = async () => ({granularity:'session', rows:[]});
    await r.load(); assert.notEqual(r.lastData, null); assert.equal(context.lastState.kind, 'ready');
  });
}
test('report transport failure retains last-good rows and scope', async () => {
  const {subject:r, context, node} = boot('reports.js','Reports');
  r.renderShell(); r.lastData = {rows:['old']}; node('reports-table').innerHTML = 'old-row';
  context.Api.get = async () => {throw Error('offline');}; await r.load();
  assert.notEqual(r.lastData, null); assert.equal(node('reports-table').innerHTML, 'old-row');
  assert.equal(context.lastState.kind, 'stale');
});
