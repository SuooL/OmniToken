package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// The live views are the last two places that showed a raw routing id.
//
// Every aggregate view folds (see model.CanonicalModel), but these two carry a
// model taken from one representative event rather than from a GROUP BY, and
// the fold was never applied to it. The result on a real panel: the device list
// said `mypc`, and the session row right below it said
// `anthropic.claude-opus-4-8` while every other page called that model
// `claude-opus-4-8`.
func TestLiveViewsFoldRoutingVariantsInSessionRows(t *testing.T) {
	st := speedStore(t)
	now := time.Now()

	events := []model.Event{
		{
			EventID: "bedrock", TS: now.Add(-5 * time.Second).UnixMilli(),
			Device: "mypc", Source: "claude-code", Model: "anthropic.claude-opus-4-8",
			SessionID: "s1", OutputTokens: 500, GenMS: 5_000,
		},
		{
			EventID: "pinned", TS: now.Add(-4 * time.Second).UnixMilli(),
			Device: "mac", Source: "claude-code", Model: "claude-haiku-4-5-20251001",
			SessionID: "s2", OutputTokens: 400, GenMS: 4_000,
		},
	}
	if _, err := st.InsertEvents(events, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	sessions, err := st.ActiveSessions(now.Add(-60 * time.Second))
	if err != nil {
		t.Fatalf("ActiveSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	for _, s := range sessions {
		if s.Model == "anthropic.claude-opus-4-8" || s.Model == "claude-haiku-4-5-20251001" {
			t.Errorf("ActiveSessions returned an unfolded model id: %q", s.Model)
		}
	}

	speed, err := st.LiveSpeedSince(now.Add(-60*time.Second), now, "")
	if err != nil {
		t.Fatalf("LiveSpeedSince: %v", err)
	}
	if len(speed.Sessions) == 0 {
		t.Fatal("LiveSpeedSince returned no sessions")
	}
	for _, s := range speed.Sessions {
		if s.Model == "anthropic.claude-opus-4-8" || s.Model == "claude-haiku-4-5-20251001" {
			t.Errorf("LiveSpeedSince returned an unfolded model id: %q", s.Model)
		}
	}
}

// Folding the display name must not merge two sessions into one row: the
// session is identified by its id, not by what it happens to be running.
func TestFoldingModelNamesKeepsSessionsDistinct(t *testing.T) {
	st := speedStore(t)
	now := time.Now()

	events := []model.Event{
		{
			EventID: "a", TS: now.Add(-5 * time.Second).UnixMilli(),
			Device: "mac", Source: "claude-code", Model: "anthropic.claude-opus-4-8",
			SessionID: "s1", OutputTokens: 100, GenMS: 1_000,
		},
		{
			EventID: "b", TS: now.Add(-5 * time.Second).UnixMilli(),
			Device: "mac", Source: "claude-code", Model: "claude-opus-4-8",
			SessionID: "s2", OutputTokens: 100, GenMS: 1_000,
		},
	}
	if _, err := st.InsertEvents(events, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	sessions, err := st.ActiveSessions(now.Add(-60 * time.Second))
	if err != nil {
		t.Fatalf("ActiveSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d session rows, want 2 — folding must not merge sessions", len(sessions))
	}
}
