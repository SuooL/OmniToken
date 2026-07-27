package store

import (
	"testing"
	"time"
)

const hMS = int64(time.Hour / time.Millisecond)

func TestIdentifyBlocks(t *testing.T) {
	dur := 5 * hMS
	base := 100*hMS + 17*int64(time.Minute/time.Millisecond) // 100h17m → floors to 100h
	entries := []blockEntry{
		{ts: base, tokens: 10, out: 1},
		{ts: base + 2*hMS, tokens: 20, out: 2},   // same block
		{ts: base + 6*hMS, tokens: 30, out: 3},   // >5h since block start → new block
		{ts: base + 20*hMS, tokens: 40, out: 4},  // >5h idle gap → new block
	}
	now := base + 20*hMS + 30*int64(time.Minute/time.Millisecond)
	blocks := identifyBlocks(entries, dur, now)
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].StartMS != 100*hMS {
		t.Errorf("block start must floor to hour: %d", blocks[0].StartMS)
	}
	if blocks[0].Tokens != 30 || blocks[0].Events != 2 {
		t.Errorf("block0 agg wrong: %+v", blocks[0])
	}
	if blocks[0].Active || blocks[1].Active {
		t.Error("old blocks must not be active")
	}
	if !blocks[2].Active {
		t.Error("last block within window+recent must be active")
	}
	// second block starts at floor(base+6h) = 106h
	if blocks[1].StartMS != 106*hMS {
		t.Errorf("block1 start = %d, want %d", blocks[1].StartMS, 106*hMS)
	}
}

func TestIdentifyBlocksInactiveWhenExpired(t *testing.T) {
	dur := 5 * hMS
	entries := []blockEntry{{ts: 10 * hMS, tokens: 5}}
	blocks := identifyBlocks(entries, dur, 16*hMS) // now past window end
	if blocks[0].Active {
		t.Error("expired block must be inactive")
	}
}
