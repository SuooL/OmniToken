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
