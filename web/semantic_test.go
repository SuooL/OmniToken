package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func semanticAsset(t *testing.T, name string) string {
	t.Helper()
	data, err := FS.ReadFile(name)
	if err != nil {
		t.Fatalf("read embedded %s: %v", name, err)
	}
	return string(data)
}

func TestSemanticAssetContracts(t *testing.T) {
	app := semanticAsset(t, "app.js")
	if strings.Contains(app, "这台机器现在在生成什么") {
		t.Error("fleet-wide Live subtitle must not claim this-machine scope")
	}
	if !strings.Contains(app, "function parseRouteHash(") ||
		strings.Count(app, "Details.applyRoute(parsed.params") != 2 {
		t.Error("router must restore Details session state on initial and same-view hash navigation")
	}

	details := semanticAsset(t, "details.js")
	for _, label := range []string{
		`aria-label="设备筛选"`,
		`aria-label="来源筛选"`,
		`aria-label="模型筛选"`,
		`aria-label="项目筛选"`,
		`aria-label="时间范围"`,
	} {
		if !strings.Contains(details, label) {
			t.Errorf("Details filter is missing %s", label)
		}
	}
	if !strings.Contains(details, `href="${detailsSessionHref(sid)}"`) {
		t.Error("session drill-down must render a real hash href")
	}

	for _, name := range []string{"overview.js", "speedview.js", "devicesview.js", "modelsview.js"} {
		t.Run(name+" ECharts aria", func(t *testing.T) {
			source := semanticAsset(t, name)
			if strings.Contains(source, "ChartRegistry.set(") {
				charts := semanticAsset(t, "charts.js")
				if !strings.Contains(charts, "aria: { enabled: true") {
					t.Errorf("%s delegates charts to ChartRegistry without its aria contract", name)
				}
				return
			}
			options := strings.Count(source, "setOption({")
			aria := strings.Count(source, "aria: { enabled: true }")
			if options == 0 || aria != options {
				t.Errorf("%s has %d setOption calls but %d enabled aria configs", name, options, aria)
			}
		})
	}
}

func TestTelemetryStudioOverviewAndLiveContracts(t *testing.T) {
	overview := semanticAsset(t, "overview.js")
	for _, contract := range []string{
		"TelemetryCache.load",
		`this.metricCard("today-total"`,
		`data-role="rolling-five-hour-total"`,
		`this.sourceCard("claude-code"`,
		`this.sourceCard("codex"`,
		`data-role="fleet-coverage"`,
		`data-chart="source-lanes"`,
		`data-role="current-contributors"`,
		`data-chart="today-model-composition"`,
		`data-role="device-throughput"`,
		`data-role="model-throughput"`,
		"unmeasured_sources",
	} {
		if !strings.Contains(overview, contract) {
			t.Errorf("Overview A2 contract missing %q", contract)
		}
	}
	if strings.Contains(overview, `data-chart="source-donut"`) {
		t.Error("Overview must not restore the redundant standalone source donut")
	}

	live := semanticAsset(t, "live.js")
	for _, contract := range []string{
		"TelemetryCache.load",
		"contribution_tps",
		"会话自身速度",
		"总吞吐按全局活跃时间计算；各行贡献使用同一分母，因此可以相加。",
		`data-chart="live-source-lanes"`,
		`data-role="measured-coverage"`,
	} {
		if !strings.Contains(live, contract) {
			t.Errorf("Live A2 contract missing %q", contract)
		}
	}
}

func TestTelemetryStudioAnalyticalPageContracts(t *testing.T) {
	contracts := map[string][]string{
		"speedview.js": {
			`data-chart="speed-source-lanes"`, `data-role="measured-coverage"`,
			"native_tps", "contribution_tps", "共享分母",
		},
		"modelsview.js": {
			`data-chart="model-distribution"`, `data-chart="model-cost-token"`,
			`data-role="active-model-contribution"`, `data-table-shell`,
		},
		"devicesview.js": {
			`data-chart="device-throughput-trend"`, "identity_status",
			"connection_state", "queued_batches", `data-table-shell`,
		},
		"cacheview.js": {
			`data-chart="cache-composition"`, `data-chart="cache-model-comparison"`,
			"saved_usd", "cache_creation", "cache_read", "TTL",
		},
		"heatmap.js": {
			`data-chart="calendar-activity"`, "heatmap-details", "逐日数据表",
		},
	}
	for name, required := range contracts {
		source := semanticAsset(t, name)
		for _, contract := range required {
			if !strings.Contains(source, contract) {
				t.Errorf("%s analytical contract missing %q", name, contract)
			}
		}
	}
}

func TestTelemetryStudioOperationalPageContracts(t *testing.T) {
	contracts := map[string][]string{
		"reports.js": {
			`data-chart="report-trend"`, `id="reports-export-status"`,
			"downloadAPI(", "_loadGeneration:", `"stale"`,
		},
		"details.js": {
			`data-role="filter-summary"`, `data-chart="event-distribution"`,
			"detailsSessionHref", "applyRoute", `data-table-shell`,
		},
		"settingsview.js": {
			`data-settings-group="connection-auth"`, `data-settings-group="pricing"`,
			`data-settings-group="device-identities"`, `data-settings-group="preferences"`,
			`data-settings-scope="section"`, `data-settings-danger="true"`,
			`class="credential-scope-map"`,
			"Api.token", "Api.adminToken", "_revision:",
		},
	}
	for name, required := range contracts {
		source := semanticAsset(t, name)
		for _, contract := range required {
			if !strings.Contains(source, contract) {
				t.Errorf("%s operational contract missing %q", name, contract)
			}
		}
	}
}

func TestSemanticBehaviorInNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; embedded integration contracts remain covered")
	}

	dir := t.TempDir()
	for _, name := range []string{"charts.js", "details.js", "live.js", "heatmap.js", "telemetry.js"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(semanticAsset(t, name)), 0o600); err != nil {
			t.Fatalf("write %s companion asset: %v", name, err)
		}
	}
	companion := `
import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {dirname, join} from 'node:path';

const root = dirname(fileURLToPath(import.meta.url));
function load(name, extra = {}) {
  const context = vm.createContext({console, URLSearchParams, ...extra});
  vm.runInContext(readFileSync(join(root, name), 'utf8'), context, {filename: name});
  return context;
}
function run(context, expression) { return vm.runInContext(expression, context); }

test('session rows expose a durable hash route and route state restores it', () => {
  const context = load('details.js', {
    esc: String, full: String, compact: String, relTime: () => '刚刚',
    repoLabel: (repo) => repo || '', usd: String,
  });
  const html = run(context, 'Details.rowHTML({ts:0,session_id:"session/a b",repo:"r"})');
  assert.match(html, /href="#details\?session=session%2Fa%20b"/);
  assert.match(html, /<a class="session-link"/);
  run(context, 'Details.applyRoute(new URLSearchParams("session=restored%2Fid"), false)');
  assert.equal(run(context, 'Details.filters.session'), 'restored/id');
});

test('live lanes reveal the count beyond the eight visible sessions', () => {
  const nodes = {lanes: {innerHTML: ''}, 'lane-note': {textContent: ''}};
  const context = load('live.js', {
    document: {getElementById: (id) => nodes[id]},
    esc: String, repoLabel: String,
  });
  context.nodes = nodes;
  run(context, 'Live.renderLanes({window_start_ms:0,window_end_ms:1000,active_ms:100,sessions:Array.from({length:10},(_,i)=>({session_id:"s"+i,device:"d",spans:[],tps:1})),spans:[],tps:1})');
  assert.match(nodes.lanes.innerHTML, /另有 2 个会话/);
  assert.equal((nodes.lanes.innerHTML.match(/class="lane lane-/g) || []).length, 8);
});

test('heatmap renders a keyboard-readable per-day table alongside the svg', () => {
  const heat = {innerHTML: ''};
  const context = load('heatmap.js', {
    document: {documentElement: {getAttribute: () => null}, getElementById: () => null},
    matchMedia: () => ({matches: false}),
    cssVar: () => '#ddd', compact: String, full: String, esc: String,
  });
  context.heat = heat;
  run(context, 'Heatmap.attachTooltip = () => {}; const now = new Date(); now.setHours(0,0,0,0); const yesterday = new Date(now); yesterday.setDate(now.getDate()-1); Heatmap.render(heat,[{bucket:Heatmap.key(yesterday),tokens:12,events:3},{bucket:Heatmap.key(now),tokens:5,events:1}],2)');
  assert.match(heat.innerHTML, /<details class="heatmap-details">/);
  assert.match(heat.innerHTML, /<summary>逐日数据表<\/summary>/);
  assert.match(heat.innerHTML, /<th scope="col">日期<\/th>/);
  assert.match(heat.innerHTML, />12<\/td>/);
  assert.match(heat.innerHTML, />3<\/td>/);
});

test('telemetry cache rejects obsolete completions and keeps last-good data stale', async () => {
  const pending = [];
  const context = load('telemetry.js', {
    Api: {get: (path) => new Promise((resolve, reject) => pending.push({path, resolve, reject}))},
    Date,
  });
  const older = run(context, 'TelemetryCache.load("5h", {force:true})');
  const newer = run(context, 'TelemetryCache.load("5h", {force:true})');
  assert.equal(pending[0].path, '/api/v1/telemetry?range=5h');
  pending[1].resolve({generated_at: 2, speed: {series: []}});
  await newer;
  pending[0].resolve({generated_at: 1, speed: {series: []}});
  await older;
  assert.equal(run(context, 'TelemetryCache.peek("5h").data.generated_at'), 2);

  const failed = run(context, 'TelemetryCache.load("5h", {force:true})');
  pending[2].reject(new Error('offline'));
  const stale = await failed;
  assert.equal(stale.data.generated_at, 2);
  assert.equal(stale.stale, true);

  const unauthorized = run(context, 'TelemetryCache.load("5h", {force:true})');
  const authError = new Error('unauthorized');
  authError.status = 401;
  pending[3].reject(authError);
  await assert.rejects(unauthorized, /unauthorized/);
  assert.equal(run(context, 'TelemetryCache.peek("5h").data'), null);
});

test('overview headline prefers the fresher live aggregate over the last historical bucket', () => {
  const context = load('telemetry.js');
  assert.equal(
    run(context, 'currentAggregateTPS({tps:80.3}, {aggregate_tps:60.2})'),
    80.3,
  );
  assert.equal(run(context, 'currentAggregateTPS({}, {aggregate_tps:60.2})'), null);
  assert.equal(run(context, 'currentAggregateTPS({}, {})'), null);
});

test('chart registry disposes detached canvases and observers before rendering again', () => {
  const context = load('charts.js', {
    matchMedia: () => ({matches: false}),
    cssVar: String, tooltipStyle: () => ({}), chartFont: () => 'sans',
  });
  const connected = {isConnected: true};
  const detached = {isConnected: false};
  const disposed = [];
  const disconnected = [];
  context.connected = connected;
  context.detached = detached;
  context.disposed = disposed;
  context.disconnected = disconnected;
  run(context, 'ChartRegistry._instances.set(connected,{dispose(){}}); ChartRegistry._instances.set(detached,{dispose(){disposed.push("chart")}}); ChartRegistry._observers.set(detached,{disconnect(){disconnected.push("observer")}})');
  run(context, 'ChartRegistry.prune()');
  assert.deepEqual(disposed, ['chart']);
  assert.deepEqual(disconnected, ['observer']);
  assert.equal(run(context, 'ChartRegistry._instances.has(detached)'), false);
  assert.equal(run(context, 'ChartRegistry._instances.has(connected)'), true);
});
`
	path := filepath.Join(dir, "semantic.test.mjs")
	if err := os.WriteFile(path, []byte(companion), 0o600); err != nil {
		t.Fatalf("write Node companion: %v", err)
	}
	output, err := exec.Command(node, "--test", path).CombinedOutput()
	if err != nil {
		t.Fatalf("Node semantic behavior tests failed: %v\n%s", err, output)
	}
	t.Logf("Node semantic behavior tests passed:\n%s", output)
}
