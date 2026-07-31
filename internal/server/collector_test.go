package server

import (
	"reflect"
	"testing"
	"time"
)

func TestSSHSchedulerUsesElapsedTimeWithoutEarlyPulls(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		until    time.Duration
		want     []time.Duration
	}{
		{
			name:     "15 second local interval",
			interval: 15 * time.Second,
			until:    2 * time.Minute,
			want:     []time.Duration{0, 60 * time.Second, 120 * time.Second},
		},
		{
			name:     "17 second local interval",
			interval: 17 * time.Second,
			until:    140 * time.Second,
			want:     []time.Duration{0, 68 * time.Second, 136 * time.Second},
		},
		{
			name:     "30 second local interval",
			interval: 30 * time.Second,
			until:    2 * time.Minute,
			want:     []time.Duration{0, 60 * time.Second, 120 * time.Second},
		},
		{
			name:     "75 second local interval",
			interval: 75 * time.Second,
			until:    150 * time.Second,
			want:     []time.Duration{0, 75 * time.Second, 150 * time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheduler := newSSHScheduler(func(time.Duration) time.Duration {
				return 0
			})
			start := time.Unix(1_000, 0)
			var got []time.Duration
			for elapsed := time.Duration(0); elapsed <= tt.until; elapsed += tt.interval {
				if scheduler.Due(start.Add(elapsed)) {
					got = append(got, elapsed)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SSH runs = %v, want %v", got, tt.want)
			}
			for i := 1; i < len(got); i++ {
				if gap := got[i] - got[i-1]; gap < time.Minute {
					t.Fatalf("SSH pull %d ran after %v, want no earlier than 1m", i, gap)
				}
			}
		})
	}
}

func TestSSHSchedulerClampsInjectedJitterAndNeverRunsBeforeNextDue(t *testing.T) {
	tests := []struct {
		name       string
		injected   time.Duration
		wantJitter time.Duration
	}{
		{name: "negative", injected: -time.Second, wantJitter: 0},
		{name: "zero", injected: 0, wantJitter: 0},
		{name: "inside bound", injected: 7 * time.Second, wantJitter: 7 * time.Second},
		{name: "above bound", injected: sshJitterMax + time.Second, wantJitter: sshJitterMax},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheduler := newSSHScheduler(func(time.Duration) time.Duration {
				return tt.injected
			})
			start := time.Unix(2_000, 0)
			if !scheduler.Due(start) {
				t.Fatal("first SSH pull was not immediate")
			}
			wantDue := start.Add(time.Minute + tt.wantJitter)
			if !scheduler.nextDue.Equal(wantDue) {
				t.Fatalf("next due = %v, want %v", scheduler.nextDue, wantDue)
			}
			if scheduler.Due(wantDue.Add(-time.Nanosecond)) {
				t.Fatal("SSH pull ran before next due")
			}
			if !scheduler.Due(wantDue) {
				t.Fatal("SSH pull did not run at next due")
			}
		})
	}
}

func TestSSHSchedulerJitterDoesNotSkipNextOverMinuteLocalTick(t *testing.T) {
	scheduler := newSSHScheduler(func(time.Duration) time.Duration {
		return sshJitterMax
	})
	start := time.Unix(3_000, 0)
	var got []time.Duration
	for elapsed := time.Duration(0); elapsed <= 150*time.Second; elapsed += 75 * time.Second {
		if scheduler.Due(start.Add(elapsed)) {
			got = append(got, elapsed)
		}
	}
	want := []time.Duration{0, 75 * time.Second, 150 * time.Second}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SSH runs = %v, want %v", got, want)
	}
}
