package store

import "testing"

const m = int64(60 * 1000) // one minute in ms

func TestBridgeSpans(t *testing.T) {
	idle := 5 * m
	// turn1: event at t=10min with 8min generation (long stream, > idle);
	// user thinks 3min (≤ idle, bridged); turn2: event at 15min, 2min duration.
	points := []evPoint{
		{ts: 10 * m, dur: 8 * m}, // spans [2,10]
		{ts: 15 * m, dur: 2 * m}, // spans [13,15]; gap 13-10=3min ≤ idle → bridge
	}
	spans := bridgeSpans(points, idle)
	if len(spans) != 1 || spans[0].start != 2*m || spans[0].end != 15*m {
		t.Fatalf("long generation + bridged think-gap wrong: %+v", spans)
	}
	// 20min break (> idle) stops the clock, restarts at next interval
	points = append(points, evPoint{ts: 36 * m, dur: 1 * m}) // [35,36], gap 20min
	spans = bridgeSpans(points, idle)
	if len(spans) != 2 || spans[1].start != 35*m {
		t.Fatalf("idle break must split spans: %+v", spans)
	}
}

func TestUnionAndSum(t *testing.T) {
	// two concurrent sessions on one repo: [0,60] and [30,90] minutes
	a := []span{{0, 60 * m}}
	b := []span{{30 * m, 90 * m}}
	all := append(append([]span{}, a...), b...)
	if got := unionMS(all); got != 90*m {
		t.Errorf("union = %dmin, want 90", got/m)
	}
	if got := sumMS(all); got != 120*m {
		t.Errorf("sum = %dmin, want 120", got/m)
	}
}

func TestZeroDurationPointsStillBridge(t *testing.T) {
	// legacy events without duration: consecutive points ≤ idle apart
	points := []evPoint{{ts: 0, dur: 0}, {ts: 2 * m, dur: 0}, {ts: 4 * m, dur: 0}}
	spans := bridgeSpans(points, 5*m)
	if len(spans) != 1 || spans[0].end-spans[0].start != 4*m {
		t.Fatalf("point bridging wrong: %+v", spans)
	}
}
