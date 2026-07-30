package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func embeddedAsset(t *testing.T, name string) string {
	t.Helper()
	b, err := FS.ReadFile(name)
	if err != nil {
		t.Fatalf("read embedded %s: %v", name, err)
	}
	return string(b)
}

func TestReportsUseAuthenticatedDownloadAPI(t *testing.T) {
	source := embeddedAsset(t, "reports.js")
	if !strings.Contains(source, "downloadAPI(") {
		t.Fatal("report exports must call downloadAPI so bearer authentication reaches the download request")
	}

	start := strings.Index(source, `id="reports-export"`)
	if start < 0 {
		t.Fatal("reports export controls are missing")
	}
	end := strings.Index(source[start:], "</div>")
	if end < 0 {
		t.Fatal("reports export controls are malformed")
	}
	exports := source[start : start+end]
	if strings.Count(exports, `<button`) != 2 || strings.Contains(exports, `<a`) || strings.Contains(exports, `href=`) {
		t.Fatalf("report exports must be two buttons with no direct API anchor; got %q", exports)
	}
}

func TestReportDownloadExposesAndClearsBusyState(t *testing.T) {
	source := embeddedAsset(t, "reports.js")
	for _, contract := range []string{
		"this.download(btn.dataset.format, btn)",
		"button.disabled = true",
		`button.setAttribute("aria-busy", "true")`,
		`status.textContent = "正在导出"`,
		"finally",
		"button.disabled = false",
		`button.removeAttribute("aria-busy")`,
	} {
		if !strings.Contains(source, contract) {
			t.Errorf("report download must include %q", contract)
		}
	}
	if !strings.Contains(source, "e instanceof APIError && e.status === 401") {
		t.Error("report download must render an explicit unauthorized state for APIError status 401")
	}
}

func TestAPIHeadersAcceptEveryHeadersInitForm(t *testing.T) {
	source := embeddedAsset(t, "api.js")
	if !strings.Contains(source, "new Headers(extra)") {
		t.Fatal("Api.headers must normalize Headers, tuple arrays, and plain objects through the standard Headers constructor")
	}
	if !strings.Contains(source, `h.set("Authorization", "Bearer " + this.token)`) {
		t.Fatal("Api.headers must merge bearer authentication with normalized caller headers")
	}
}

func TestSharedViewStateIsLiveClassifiedAndRetryable(t *testing.T) {
	source := embeddedAsset(t, "api.js")
	for _, contract := range []string{
		"function renderState(",
		`setAttribute("aria-live", "polite")`,
		"function classifyAPIError(",
		"error instanceof APIError && error.status === 401",
		`kind: "unauthorized"`,
		`button.addEventListener("click", action.run)`,
	} {
		if !strings.Contains(source, contract) {
			t.Errorf("shared state API must include %q", contract)
		}
	}
}

func TestAsyncViewsKeepOldDataAndRenderStaleFailures(t *testing.T) {
	for _, name := range []string{
		"overview.js",
		"speedview.js",
		"cacheview.js",
		"devicesview.js",
		"modelsview.js",
	} {
		t.Run(name, func(t *testing.T) {
			source := embeddedAsset(t, name)
			for _, contract := range []string{"lastData", "renderState(", "classifyAPIError(", `"stale"`} {
				if !strings.Contains(source, contract) {
					t.Errorf("%s must preserve trustworthy data and expose classified stale state; missing %q", name, contract)
				}
			}
		})
	}
}

func TestSettingsDraftUpdatesOnInputAndClearsAfterSave(t *testing.T) {
	source := embeddedAsset(t, "settingsview.js")
	for _, contract := range []string{
		"_draft:",
		"root.oninput = (ev) =>",
		"this.updateDraft(ev.target)",
		"this._draft.pricing = null",
		"this._draft.devices = null",
		"this._draft.token = null",
		"await refreshAuthState()",
	} {
		if !strings.Contains(source, contract) {
			t.Errorf("settings draft contract missing %q", contract)
		}
	}

	app := embeddedAsset(t, "app.js")
	if !strings.Contains(app, "async function refreshAuthState()") ||
		!strings.Contains(app, `document.querySelector(".auth-banner")?.remove()`) {
		t.Error("token save must refresh auth health and remove a stale auth banner immediately")
	}
}

func TestPollingViewsGuardEveryAsyncCompletionAndRenderEmpty(t *testing.T) {
	for _, name := range []string{
		"overview.js",
		"speedview.js",
		"cacheview.js",
		"devicesview.js",
		"modelsview.js",
	} {
		t.Run(name, func(t *testing.T) {
			source := embeddedAsset(t, name)
			for _, contract := range []string{
				"_loadGeneration:",
				"const loadID = ++this._loadGeneration",
				"this._loadGeneration += 1",
				`kind: "empty"`,
			} {
				if !strings.Contains(source, contract) {
					t.Errorf("%s missing %q", name, contract)
				}
			}
			if got := strings.Count(source, "isCurrentGeneration(this._loadGeneration, loadID)"); got < 2 {
				t.Errorf("%s guards %d async paths, want success and catch", name, got)
			}
		})
	}
}

func TestSettingsRevisionSnapshotsRawNumbersAndApiTokenBoundary(t *testing.T) {
	source := embeddedAsset(t, "settingsview.js")
	for _, contract := range []string{
		"_revision:",
		"_saving:",
		"const sentRevision = this._revision.pricing",
		"const sentRevision = this._revision.devices",
		"canCommitRevision(this._revision.pricing, sentRevision)",
		"canCommitRevision(this._revision.devices, sentRevision)",
		"row[target.dataset.f] = target.value",
		"buildPricingPayload(snapshot)",
		"Api.token",
		"const persisted = Api.saveToken(value)",
	} {
		if !strings.Contains(source, contract) {
			t.Errorf("settings revision/token contract missing %q", contract)
		}
	}
	if strings.Contains(source, "localStorage.") {
		t.Error("Settings must use the Api token boundary instead of reading localStorage directly")
	}
}

func TestStateDecisionHelpersInNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; embedded asset contracts remain covered")
	}

	dir := t.TempDir()
	for _, name := range []string{
		"api.js", "settingsview.js", "overview.js", "speedview.js",
		"cacheview.js", "devicesview.js", "modelsview.js",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(embeddedAsset(t, name)), 0o600); err != nil {
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
  const document = {getElementById() { return {addEventListener() {}}; }};
  const context = vm.createContext({console, Headers, URL, Blob, TypeError, document, ...extra});
  vm.runInContext(readFileSync(join(root, name), 'utf8'), context, {filename: name});
  return context;
}
function run(context, expression) { return vm.runInContext(expression, context); }
function json(context, expression) { return JSON.parse(run(context, 'JSON.stringify(' + expression + ')')); }

test('generation and revision decisions reject obsolete work', () => {
  const context = load('api.js', {localStorage: {getItem() { return ''; }}});
  assert.equal(run(context, 'isCurrentGeneration(4, 4)'), true);
  assert.equal(run(context, 'isCurrentGeneration(5, 4)'), false);
  assert.equal(run(context, 'canCommitRevision(7, 7)'), true);
  assert.equal(run(context, 'canCommitRevision(8, 7)'), false);
});

test('apiFetch wraps transport TypeError and classifier does not mislabel render bugs', async () => {
  const context = load('api.js', {
    localStorage: {getItem() { return ''; }},
    fetch: async () => { throw new TypeError('connection refused'); },
  });
  await assert.rejects(run(context, 'apiFetch("/api/test")'), (error) =>
    error.name === 'APIError' && error.status === 0);
  assert.notEqual(run(context, 'classifyAPIError(new TypeError("render bug")).title'), '服务不可达');
});

test('token remains in memory when persistence fails', () => {
  const storage = {
    getItem() { throw new Error('blocked'); },
    setItem() { throw new Error('blocked'); },
    removeItem() { throw new Error('blocked'); },
  };
  const context = load('api.js', {localStorage: storage});
  assert.equal(run(context, 'Api.saveToken("session-secret")'), false);
  assert.equal(run(context, 'Api.token'), 'session-secret');

  const settings = load('settingsview.js', {
    Api: {TOKEN_KEY: 'omnitoken.token', token: 'session-secret'},
    esc: String,
  });
  assert.match(run(settings, 'SettingsView.tokenCard()'), /session-secret/);
});

test('pricing payload validates raw numeric drafts only at save time', () => {
  const context = load('settingsview.js', {Api: {TOKEN_KEY: 'omnitoken.token', token: ''}});
  const valid = json(context, 'buildPricingPayload([{model:"m",in:"1.5",out:"2",cr:"0",cw:"3"}])');
  assert.equal(valid.ok, true);
  assert.equal(valid.value.m.input_per_mtok, 1.5);
  assert.equal(json(context, 'buildPricingPayload([{model:"m",in:"",out:"2",cr:"0",cw:"3"}])').ok, false);
  assert.equal(json(context, 'buildPricingPayload([{model:"m",in:"-",out:"2",cr:"0",cw:"3"}])').ok, false);
});

test('settings serializes pricing and device saves per section', async () => {
  const context = load('api.js', {localStorage: {getItem() { return ''; }}});
  vm.runInContext(readFileSync(join(root, 'settingsview.js'), 'utf8'), context, {filename: 'settingsview.js'});
  run(context, 'SettingsView._notes = []; SettingsView._sent = []; SettingsView._resolves = []; SettingsView.note = function(key, ok, message) { this._notes.push({key, ok, message}); return false; }; SettingsView.put = function(body, key) { this._sent.push({key, body: JSON.parse(JSON.stringify(body))}); return new Promise((resolve) => this._resolves.push(resolve)); };');

  run(context, 'SettingsView._draft.pricing = [{model:"m",in:"1",out:"2",cr:"0",cw:"0"}]; SettingsView._revision.pricing = 1;');
  const priceA = run(context, 'SettingsView.savePricing()');
  const priceDuplicate = run(context, 'SettingsView.savePricing()');
  assert.equal(run(context, 'SettingsView._sent.length'), 1);
  assert.equal(run(context, 'SettingsView._notes.some((n) => n.key === "price" && n.message.includes("保存进行中"))'), true);

  run(context, 'SettingsView._draft.pricing[0].in = "9"; SettingsView._revision.pricing += 1; SettingsView._resolves[0](true);');
  await priceA;
  await priceDuplicate;
  assert.equal(run(context, 'SettingsView._draft.pricing[0].in'), '9');

  const priceB = run(context, 'SettingsView.savePricing()');
  assert.equal(run(context, 'SettingsView._sent.length'), 2);
  assert.equal(run(context, 'SettingsView._sent[1].body.pricing_overrides.m.input_per_mtok'), 9);
  run(context, 'SettingsView._resolves[1](true)');
  await priceB;
  assert.equal(run(context, 'SettingsView._draft.pricing'), null);

  run(context, 'SettingsView._sent = []; SettingsView._resolves = []; SettingsView._notes = []; SettingsView._draft.devices = {host:"old"}; SettingsView._revision.devices = 1;');
  const devicesA = run(context, 'SettingsView.saveDevices()');
  const devicesDuplicate = run(context, 'SettingsView.saveDevices()');
  assert.equal(run(context, 'SettingsView._sent.length'), 1);
  assert.equal(run(context, 'SettingsView._notes.some((n) => n.key === "devices" && n.message.includes("保存进行中"))'), true);
  run(context, 'SettingsView._draft.devices.host = "new"; SettingsView._revision.devices += 1; SettingsView._resolves[0](true);');
  await devicesA;
  await devicesDuplicate;
  assert.equal(run(context, 'SettingsView._draft.devices.host'), 'new');

  const devicesB = run(context, 'SettingsView.saveDevices()');
  assert.equal(run(context, 'SettingsView._sent.length'), 2);
  assert.equal(run(context, 'SettingsView._sent[1].body.device_labels.host'), 'new');
  run(context, 'SettingsView._resolves[1](true)');
  await devicesB;
  assert.equal(run(context, 'SettingsView._draft.devices'), null);
});

test('route empty predicates reflect meaningful data', () => {
  const overview = load('overview.js');
  assert.equal(run(overview, 'overviewIsEmpty({today:{total_tokens:0},week:{total_tokens:0},month:{total_tokens:0},all_time:{total_tokens:0}})'), true);
  assert.equal(run(overview, 'overviewIsEmpty({all_time:{total_tokens:1}})'), false);

  const speed = load('speedview.js');
  assert.equal(run(speed, 'speedIsEmpty({models:[],exact:[],series:{buckets:[]},live:{output_tokens:0}})'), true);
  assert.equal(run(speed, 'speedIsEmpty({models:[{output_tokens:8}],series:{buckets:[]},live:{}})'), false);

  const cache = load('cacheview.js');
  assert.equal(run(cache, 'cacheIsEmpty({models:[]})'), true);
  assert.equal(run(cache, 'cacheIsEmpty({models:[{input_tokens:1}]})'), false);

  const devices = load('devicesview.js');
  assert.equal(run(devices, 'devicesIsEmpty({summary:[]})'), true);
  assert.equal(run(devices, 'devicesIsEmpty({summary:[{total_tokens:1}]})'), false);

  const models = load('modelsview.js');
  assert.equal(run(models, 'modelsIsEmpty({by_source:[]})'), true);
  assert.equal(run(models, 'modelsIsEmpty({by_source:[{total_tokens:1}]})'), false);
});
`
	path := filepath.Join(dir, "state-decisions.test.mjs")
	if err := os.WriteFile(path, []byte(companion), 0o600); err != nil {
		t.Fatalf("write Node companion: %v", err)
	}
	output, err := exec.Command(node, "--test", path).CombinedOutput()
	if err != nil {
		t.Fatalf("Node state-decision tests failed: %v\n%s", err, output)
	}
	t.Logf("Node state-decision tests passed:\n%s", output)
}
