package web

import (
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
