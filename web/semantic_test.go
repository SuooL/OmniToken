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
			options := strings.Count(source, "setOption({")
			aria := strings.Count(source, "aria: { enabled: true }")
			if options == 0 || aria != options {
				t.Errorf("%s has %d setOption calls but %d enabled aria configs", name, options, aria)
			}
		})
	}
}

func TestSemanticBehaviorInNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; embedded integration contracts remain covered")
	}

	dir := t.TempDir()
	for _, name := range []string{"details.js", "live.js", "heatmap.js"} {
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
