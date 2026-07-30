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
		t.Fatal("Api.headers must merge the read bearer credential with normalized caller headers")
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
		"this._draft.readToken = null",
		"this._draft.adminToken = null",
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
		"Api.adminToken",
		"const persisted = Api.saveTokens(readValue, adminValue)",
	} {
		if !strings.Contains(source, contract) {
			t.Errorf("settings revision/token contract missing %q", contract)
		}
	}
	if strings.Contains(source, "localStorage.") {
		t.Error("Settings must use the Api token boundary instead of reading localStorage directly")
	}
}

func TestQuietInstrumentResponsiveAndAccessibleContracts(t *testing.T) {
	tokens := embeddedAsset(t, "tokens.css")
	for _, contract := range []string{
		"--font-body: -apple-system",
		"--font-display: ui-monospace",
	} {
		if !strings.Contains(tokens, contract) {
			t.Errorf("font-role tokens missing %q", contract)
		}
	}

	style := embeddedAsset(t, "style.css")
	for _, contract := range []string{
		":focus-visible",
		"@media (prefers-reduced-motion: reduce)",
		"#view-live .card",
		"#view-speed .card",
		"position: sticky",
		"overflow-x: auto",
		"scrollbar-width: none",
		"main:not(#view-live):not(#view-speed) .card",
	} {
		if !strings.Contains(style, contract) {
			t.Errorf("quiet instrument CSS contract missing %q", contract)
		}
	}

	app := embeddedAsset(t, "app.js")
	if !strings.Contains(app, `active.scrollIntoView({block: "nearest", inline: "center"})`) {
		t.Error("active mobile navigation item must be scrolled into view")
	}

	for _, name := range []string{
		"overview.js", "speedview.js", "devicesview.js", "modelsview.js",
	} {
		source := embeddedAsset(t, name)
		if !strings.Contains(source, `animation: !matchMedia("(prefers-reduced-motion: reduce)").matches`) {
			t.Errorf("%s must disable canvas animation for reduced motion", name)
		}
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

test('scoped tokens migrate missing admin key but preserve explicit empty admin', () => {
  const legacyValues = new Map([['omnitoken.token', 'read-secret']]);
  const legacyStorage = {getItem(key) { return legacyValues.has(key) ? legacyValues.get(key) : null; }};
  const legacy = load('api.js', {localStorage: legacyStorage});
  run(legacy, 'Api.loadToken()');
  assert.equal(run(legacy, 'Api.token'), 'read-secret');
  assert.equal(run(legacy, 'Api.adminToken'), 'read-secret');

  const explicitValues = new Map([
    ['omnitoken.token', 'read-secret'],
    ['omnitoken.admin_token', ''],
  ]);
  const explicitStorage = {getItem(key) { return explicitValues.has(key) ? explicitValues.get(key) : null; }};
  const explicit = load('api.js', {localStorage: explicitStorage});
  run(explicit, 'Api.loadToken()');
  assert.equal(run(explicit, 'Api.token'), 'read-secret');
  assert.equal(run(explicit, 'Api.adminToken'), '');
});

test('read credentials authorize headers and SSE while PUT uses only admin', async () => {
  const values = new Map([
    ['omnitoken.token', 'read-secret'],
    ['omnitoken.admin_token', 'admin-secret'],
  ]);
  let request = null;
  let streamURL = '';
  const context = load('api.js', {
    localStorage: {getItem(key) { return values.has(key) ? values.get(key) : null; }},
    fetch: async (url, init) => {
      request = {url, authorization: new Headers(init.headers).get('Authorization')};
      return {ok: true, status: 200, json: async () => ({})};
    },
    EventSource: class { constructor(url) { streamURL = url; } },
  });
  run(context, 'Api.loadToken()');
  assert.equal(run(context, 'Api.headers().get("Authorization")'), 'Bearer read-secret');
  await run(context, 'Api.get("/api/v1/settings")');
  assert.equal(request.authorization, 'Bearer read-secret');
  run(context, 'Api.stream("/api/v1/stream")');
  assert.match(streamURL, /access_token=read-secret/);
  await run(context, 'Api.put("/api/v1/settings", {x:1})');
  assert.equal(request.authorization, 'Bearer admin-secret');

  const explicitEmpty = load('api.js', {
    localStorage: {getItem(key) {
      if (key === 'omnitoken.token') return 'read-secret';
      if (key === 'omnitoken.admin_token') return '';
      return null;
    }},
    fetch: async (url, init) => {
      request = {url, authorization: new Headers(init.headers).get('Authorization')};
      return {ok: true, status: 200};
    },
  });
  run(explicitEmpty, 'Api.loadToken()');
  await run(explicitEmpty, 'Api.put("/api/v1/settings", {x:1})');
  assert.equal(request.authorization, null);
});

test('saving an explicit empty admin credential keeps the migration sentinel', () => {
  const values = new Map();
  const storage = {
    getItem(key) { return values.has(key) ? values.get(key) : null; },
    setItem(key, value) { values.set(key, value); },
    removeItem(key) { values.delete(key); },
  };
  const context = load('api.js', {localStorage: storage});
  assert.equal(run(context, 'Api.saveTokens("read-secret", "")'), true);
  assert.equal(values.get('omnitoken.token'), 'read-secret');
  assert.equal(values.has('omnitoken.admin_token'), true);
  assert.equal(values.get('omnitoken.admin_token'), '');
});

test('scoped tokens remain in memory when persistence fails', () => {
  const storage = {
    getItem() { throw new Error('blocked'); },
    setItem() { throw new Error('blocked'); },
    removeItem() { throw new Error('blocked'); },
  };
  const context = load('api.js', {localStorage: storage});
  assert.equal(run(context, 'Api.saveTokens("read-session", "admin-session")'), false);
  assert.equal(run(context, 'Api.token'), 'read-session');
  assert.equal(run(context, 'Api.adminToken'), 'admin-session');

  const settings = load('settingsview.js', {
    Api: {token: 'read-session', adminToken: 'admin-session'},
    esc: String,
  });
  const card = run(settings, 'SettingsView.tokenCard()');
  assert.match(card, /read-session/);
  assert.match(card, /admin-session/);
});

test('settings saves separate read and admin drafts and refreshes read auth', async () => {
  const calls = [];
  const context = load('settingsview.js', {
    Api: {
      token: 'old-read',
      adminToken: 'old-admin',
      saveTokens(readToken, adminToken) {
        calls.push({kind: 'save', readToken, adminToken});
        this.token = readToken;
        this.adminToken = adminToken;
        return true;
      },
    },
    esc: String,
    refreshAuthState: async () => { calls.push({kind: 'refresh'}); },
    reloadSettings: async () => { calls.push({kind: 'load'}); },
  });
  run(context, 'SettingsView.load = reloadSettings; SettingsView.note = function() { return false; };');
  run(context, 'SettingsView._draft.readToken = " new-read "; SettingsView._draft.adminToken = " new-admin ";');
  await run(context, 'SettingsView.saveTokens()');
  assert.deepEqual(calls, [
    {kind: 'save', readToken: 'new-read', adminToken: 'new-admin'},
    {kind: 'refresh'},
    {kind: 'load'},
  ]);
  assert.equal(run(context, 'SettingsView._draft.readToken'), null);
  assert.equal(run(context, 'SettingsView._draft.adminToken'), null);
});

test('pricing payload validates raw numeric drafts only at save time', () => {
  const context = load('settingsview.js', {Api: {token: '', adminToken: ''}});
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
