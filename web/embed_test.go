package web

import (
	"strings"
	"testing"
)

func TestReportsUseAuthenticatedDownloadAPI(t *testing.T) {
	reports, err := FS.ReadFile("reports.js")
	if err != nil {
		t.Fatalf("read embedded reports.js: %v", err)
	}

	source := string(reports)
	if !strings.Contains(source, "downloadAPI(") {
		t.Fatal("report exports must call downloadAPI so bearer authentication reaches the download request")
	}
	if strings.Contains(source, "href = Api.url(this.apiPath") || strings.Contains(source, ".href = Api.url(this.apiPath") {
		t.Fatal("report exports must not assign direct /api/v1/reports anchors because anchors cannot attach bearer authentication")
	}
}
