package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func TestEventCountFilters(t *testing.T) {
	s, events, from, to := seedDetailStore(t)
	defer s.Close()

	cases := []struct {
		name string
		f    EventFilter
		want int64
	}{
		{"all", EventFilter{From: from, To: to}, 4},
		{"device", EventFilter{Device: "mac", From: from, To: to}, 2},
		{"source+provider", EventFilter{Source: "claude-code", Provider: "anthropic", From: from, To: to}, 2},
		{"model", EventFilter{Model: "gpt-5", From: from, To: to}, 2},
		{"repo", EventFilter{Repo: "github.com/a/b", From: from, To: to}, 2},
		{"session", EventFilter{SessionID: "s1", From: from, To: to}, 2},
		{"combo device+session", EventFilter{Device: "mac", SessionID: "s1", From: from, To: to}, 1},
		{"time window trims", EventFilter{From: time.UnixMilli(events[1].TS), To: to}, 3},
		{"no match", EventFilter{Device: "nope", From: from, To: to}, 0},
		{"zero times = unbounded", EventFilter{}, 4},
	}
	for _, c := range cases {
		n, err := s.EventCount(c.f)
		if err != nil {
			t.Fatalf("%s: count: %v", c.name, err)
		}
		if n != c.want {
			t.Errorf("%s: count = %d, want %d", c.name, n, c.want)
		}
		page, err := s.EventPage(c.f, 100, 0)
		if err != nil {
			t.Fatalf("%s: page: %v", c.name, err)
		}
		if int64(len(page)) != c.want {
			t.Errorf("%s: page len = %d, want %d", c.name, len(page), c.want)
		}
	}
}

func TestEventPageOrderAndPagination(t *testing.T) {
	s, events, from, to := seedDetailStore(t)
	defer s.Close()
	all := EventFilter{From: from, To: to}

	page, err := s.EventPage(all, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"e4", "e3", "e2", "e1"} // ts DESC
	for i, id := range wantOrder {
		if page[i].EventID != id {
			t.Fatalf("order[%d] = %s, want %s", i, page[i].EventID, id)
		}
	}
	// Full field round-trip: oldest row must equal what was inserted.
	if page[3] != events[0] {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", page[3], events[0])
	}

	p2, err := s.EventPage(all, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2) != 2 || p2[0].EventID != "e2" || p2[1].EventID != "e1" {
		t.Errorf("limit=2 offset=2 page wrong: %+v", p2)
	}
	p3, err := s.EventPage(all, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(p3) != 0 {
		t.Errorf("offset past end must be empty, got %d rows", len(p3))
	}
}

func seedDetailStore(t *testing.T) (*Store, []model.Event, time.Time, time.Time) {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	mk := func(id string, min int, dev, src, prov, mdl, repo, sess string) model.Event {
		return model.Event{
			EventID: id, TS: base.Add(time.Duration(min) * time.Minute).UnixMilli(),
			Device: dev, Source: src, Provider: prov, Model: mdl, Repo: repo, SessionID: sess,
			AccountLabel: "acct", InputTokens: 10, OutputTokens: 20,
			CacheReadTokens: 30, CacheCreationTokens: 40, Cache1hTokens: 5, Cache5mTokens: 6,
			DurationMS: 7, TTFTMS: 8, CWD: "/w/" + id, GitBranch: "main", AppVersion: "1.0",
		}
	}
	events := []model.Event{
		mk("e1", 0, "mac", "claude-code", "anthropic", "claude-opus-4", "github.com/a/b", "s1"),
		mk("e2", 1, "mac", "codex", "openai", "gpt-5", "github.com/a/b", "s2"),
		mk("e3", 2, "srv", "claude-code", "anthropic", "claude-sonnet-4", "local:/tmp/x", "s1"),
		mk("e4", 3, "srv", "codex", "openai", "gpt-5", "", "s3"),
	}
	if _, err := s.InsertEvents(events, 1); err != nil {
		t.Fatal(err)
	}
	return s, events, base.Add(-time.Hour), base.Add(time.Hour)
}
