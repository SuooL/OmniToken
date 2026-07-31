package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

const (
	mergeHostIdentity  = "JasonHudeMacBook-Pro.local"
	mergeNamedIdentity = "suool-mac"
)

func newMergeTestServer(t *testing.T) *Server {
	t.Helper()
	s := newLiveTestServer(t)
	s.cfg.AdminToken = "admin-secret"
	s.cfg.ReadToken = "read-secret"
	s.cfg.DeviceName = mergeNamedIdentity
	s.hostname = func() (string, error) { return mergeHostIdentity, nil }
	s.now = func() time.Time { return time.UnixMilli(1_753_920_036_000) }
	ts := time.UnixMilli(1_753_920_000_000)
	seedEventOn(t, s, "cc:host:req", mergeHostIdentity, ts)
	seedEventOn(t, s, "cc:named:req", mergeNamedIdentity, ts.Add(time.Second))
	quotas := []model.QuotaSnapshot{
		{Device: mergeHostIdentity, Source: "codex", LimitID: "primary", Scope: "primary",
			WindowMinutes: 300, UsedPercent: 11, ObservedAt: ts.UnixMilli()},
		{Device: mergeNamedIdentity, Source: "codex", LimitID: "primary", Scope: "primary",
			WindowMinutes: 300, UsedPercent: 22, ObservedAt: ts.UnixMilli()},
	}
	if _, err := s.store.InsertQuotas(quotas); err != nil {
		t.Fatalf("seed quotas: %v", err)
	}
	return s
}

func postMerge(t *testing.T, s *Server, path, token string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(payload)))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	return rec
}

func eventsUnder(t *testing.T, s *Server, device string) int64 {
	t.Helper()
	rows, err := s.store.DeviceSummary(time.UnixMilli(0), s.currentTime().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Device == device {
			return row.Events
		}
	}
	return 0
}

// The merge is a management action, not a query: the read credential that gets
// you the whole dashboard must not be enough to rewrite attribution (ADR-0019 §6.5).
func TestDeviceMergeEndpointsRequireTheAdminCredential(t *testing.T) {
	for _, path := range []string{"/api/v1/devices/merge/preview", "/api/v1/devices/merge"} {
		for _, tc := range []struct{ name, token string }{
			{"no credential", ""},
			{"read credential", "read-secret"},
		} {
			t.Run(path+" "+tc.name, func(t *testing.T) {
				s := newMergeTestServer(t)
				rec := postMerge(t, s, path, tc.token, map[string]string{
					"from": mergeHostIdentity, "to": mergeNamedIdentity, "confirm": mergeHostIdentity,
				})
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d body=%q, want 401", rec.Code, rec.Body.String())
				}
				if got := eventsUnder(t, s, mergeHostIdentity); got != 1 {
					t.Fatalf("an unauthorized call still moved rows: %d events left under the source", got)
				}
			})
		}
	}
}

// The numbers in the confirmation dialog come from the server, computed by the
// same code as the merge itself (ADR-0019 §6.2) — and looking changes nothing.
func TestDeviceMergePreviewReportsImpactWithoutTouchingAnything(t *testing.T) {
	s := newMergeTestServer(t)
	rec := postMerge(t, s, "/api/v1/devices/merge/preview", "admin-secret", map[string]string{
		"from": mergeHostIdentity, "to": mergeNamedIdentity,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q, want 200", rec.Code, rec.Body.String())
	}
	var resp deviceMergeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Plan.EventsMoved != 1 || resp.Plan.QuotaDropped != 1 || resp.Plan.QuotaMoved != 0 {
		t.Fatalf("preview = %+v, want 1 event moved and 1 colliding quota row dropped", resp.Plan)
	}
	if resp.Plan.From.TotalTokens == 0 || resp.Plan.To.TotalTokens == 0 {
		t.Fatal("preview must carry each identity's volume; the user picks by it")
	}
	if !resp.TargetIsLocal {
		t.Fatal("target is this machine's configured device name; the preview says so")
	}
	if resp.Applied {
		t.Fatal("a preview must never report itself as applied")
	}
	if got := eventsUnder(t, s, mergeHostIdentity); got != 1 {
		t.Fatalf("preview moved %d events", 1-got)
	}
}

// Merging into a name this machine does not write under would be undone by the
// next batch of events, so the preview says so out loud (ADR-0019 §6.7).
func TestDeviceMergePreviewWarnsWhenTheDirectionIsBackwards(t *testing.T) {
	s := newMergeTestServer(t)
	rec := postMerge(t, s, "/api/v1/devices/merge/preview", "admin-secret", map[string]string{
		"from": mergeNamedIdentity, "to": mergeHostIdentity,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q, want 200", rec.Code, rec.Body.String())
	}
	var resp deviceMergeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TargetIsLocal {
		t.Fatal("target is not the name this server writes under")
	}
	if len(resp.Warnings) == 0 {
		t.Fatal("a backwards merge must be flagged: the next events would land under the source again")
	}
}

// Confirmation is typed, not clicked (ADR-0019 §6.3).
func TestDeviceMergeRequiresTheSourceNameTypedOut(t *testing.T) {
	cases := []struct{ name, confirm string }{
		{"empty", ""},
		{"the target instead", mergeNamedIdentity},
		{"nearly right", "JasonHudeMacBook-Pro"},
		{"case-folded", "jasonhudemacbook-pro.local"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newMergeTestServer(t)
			rec := postMerge(t, s, "/api/v1/devices/merge", "admin-secret", map[string]string{
				"from": mergeHostIdentity, "to": mergeNamedIdentity, "confirm": tc.confirm,
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%q, want 400", rec.Code, rec.Body.String())
			}
			if got := eventsUnder(t, s, mergeHostIdentity); got != 1 {
				t.Fatalf("an unconfirmed merge still moved rows: %d events left under the source", got)
			}
		})
	}
}

func TestDeviceMergeThroughHTTPMovesRowsAndRecordsTheAudit(t *testing.T) {
	s := newMergeTestServer(t)
	rec := postMerge(t, s, "/api/v1/devices/merge", "admin-secret", map[string]string{
		"from": mergeHostIdentity, "to": mergeNamedIdentity, "confirm": mergeHostIdentity,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q, want 200", rec.Code, rec.Body.String())
	}
	var resp deviceMergeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Applied || resp.Plan.EventsMoved != 1 {
		t.Fatalf("response = %+v, want an applied merge of one event", resp)
	}
	if got := eventsUnder(t, s, mergeNamedIdentity); got != 2 {
		t.Fatalf("target holds %d events, want both", got)
	}
	history, err := s.store.DeviceMergeHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %+v, want one record", history)
	}
	if history[0].From != mergeHostIdentity || history[0].To != mergeNamedIdentity ||
		history[0].At != s.currentTime().UnixMilli() || history[0].Actor != "admin" {
		t.Fatalf("audit record = %+v", history[0])
	}
	if len(resp.History) != 1 {
		t.Fatalf("response history = %+v, want the panel to get the updated log back", resp.History)
	}
}

// A typo must fail loudly rather than report a successful merge of nothing
// (ADR-0019 §6.6).
func TestDeviceMergeRejectsUnknownAndIdenticalIdentities(t *testing.T) {
	cases := []struct {
		name     string
		from, to string
	}{
		{"unknown source", "typo-mac", mergeNamedIdentity},
		{"unknown target", mergeHostIdentity, "typo-mac"},
		{"same identity", mergeNamedIdentity, mergeNamedIdentity},
		{"empty source", "", mergeNamedIdentity},
	}
	for _, tc := range cases {
		for _, path := range []string{"/api/v1/devices/merge/preview", "/api/v1/devices/merge"} {
			t.Run(tc.name+" "+path, func(t *testing.T) {
				s := newMergeTestServer(t)
				rec := postMerge(t, s, path, "admin-secret", map[string]string{
					"from": tc.from, "to": tc.to, "confirm": tc.from,
				})
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d body=%q, want 400", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

// ADR-0019 §7.3: the hint rests on a fact — both of this machine's candidate
// names have self-reported — and never on "these two look similar".
func TestSettingsSurfaceMergeHistoryAndTheLocalDuplicateIdentity(t *testing.T) {
	s := newMergeTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	req.Header.Set("Authorization", "Bearer read-secret")
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q, want 200", rec.Code, rec.Body.String())
	}
	var resp settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.LocalIdentity.Device != mergeNamedIdentity || resp.LocalIdentity.Hostname != mergeHostIdentity {
		t.Fatalf("local identity = %+v", resp.LocalIdentity)
	}
	if resp.LocalIdentity.DuplicateIdentity != mergeHostIdentity {
		t.Fatalf("duplicate identity = %q, want the hostname that also self-reported",
			resp.LocalIdentity.DuplicateIdentity)
	}
	if resp.DeviceMerges == nil {
		t.Fatal("device_merges must always be present, so the panel can render an empty history")
	}

	// After the merge the hostname no longer holds any self-reported event, so
	// the hint has to go away — otherwise it nags forever.
	if rec := postMerge(t, s, "/api/v1/devices/merge", "admin-secret", map[string]string{
		"from": mergeHostIdentity, "to": mergeNamedIdentity, "confirm": mergeHostIdentity,
	}); rec.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	req.Header.Set("Authorization", "Bearer read-secret")
	s.routes().ServeHTTP(rec, req)
	resp = settingsResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.LocalIdentity.DuplicateIdentity != "" {
		t.Fatalf("hint survived the merge: %+v", resp.LocalIdentity)
	}
	if len(resp.DeviceMerges) != 1 {
		t.Fatalf("device_merges = %+v, want the merge that just happened", resp.DeviceMerges)
	}
}

// The rule that makes this overwrite safe is not a SQL guard — it is that only
// a human can start it (ADR-0019 §1). Collection, parsing and ingest therefore
// must not so much as name the function.
func TestNothingAutomaticCanStartAMerge(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		filepath.Join("internal", "store", "devicemerge.go"):  true,
		filepath.Join("internal", "server", "devicemerge.go"): true,
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(source), "MergeDeviceIdentity") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !allowed[rel] {
			t.Errorf("%s calls MergeDeviceIdentity; the merge may only be reached from the admin endpoint", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
