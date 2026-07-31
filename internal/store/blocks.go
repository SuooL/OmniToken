package store

import (
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// Claude subscription 5-hour billing windows (F11). Algorithm mirrors
// ccusage blocks.rs identify_session_blocks: entries sorted by time; a block
// starts at the FIRST entry's timestamp floored to the hour (UTC); a new
// block opens when an entry is past the window OR the idle gap since the
// last entry exceeds the window. Aggregating events from all devices makes
// this the true account-level window, which a single-machine view cannot see.

const BlockDuration = 5 * time.Hour

type Block struct {
	StartMS   int64   `json:"start_ms"`
	EndMS     int64   `json:"end_ms"` // start + 5h
	LastMS    int64   `json:"last_ms"`
	Tokens    int64   `json:"tokens"`
	OutTokens int64   `json:"output_tokens"`
	Events    int64   `json:"events"`
	Active    bool    `json:"active"`
	CostUSD   float64 `json:"cost_usd,omitempty"` // filled by server layer
}

type blockEntry struct {
	ts, tokens, out int64
}

func floorToHourMS(ms int64) int64 {
	const hour = int64(time.Hour / time.Millisecond)
	return ms - ms%hour
}

// identifyBlocks folds chronological entries into billing blocks.
func identifyBlocks(entries []blockEntry, durMS, nowMS int64) []Block {
	var blocks []Block
	var cur *Block
	for _, e := range entries {
		if cur != nil && (e.ts-cur.StartMS > durMS || e.ts-cur.LastMS > durMS) {
			cur = nil
		}
		if cur == nil {
			start := floorToHourMS(e.ts)
			blocks = append(blocks, Block{StartMS: start, EndMS: start + durMS})
			cur = &blocks[len(blocks)-1]
		}
		cur.LastMS = e.ts
		cur.Tokens += e.tokens
		cur.OutTokens += e.out
		cur.Events++
	}
	if cur != nil && nowMS < cur.EndMS && nowMS-cur.LastMS <= durMS {
		cur.Active = true
	}
	return blocks
}

// Blocks returns recent Claude-subscription billing blocks. Only usage that a
// subscription window actually constrains belongs in one (ADR-0018 §7): the
// probe-confirmed `anthropic-oauth` channel and nothing else. `anthropic-api`
// is pay-per-use, relay traffic bills somewhere else entirely, and the
// undetermined `anthropic` label is exactly that — undetermined.
//
// The undetermined label used to be counted here on the theory that a
// subscription is the common case. On real data that theory put 5,201 relay
// events inside the 5h window: the block total then disagreed with the quota
// percentage sitting next to it, and nothing on the page said which one to
// believe. A window is a claim about a specific billing relationship, so only
// events proven to be in that relationship may be counted against it.
func (s *Store) Blocks(from time.Time, now time.Time) ([]Block, error) {
	rows, err := s.db.Query(
		`SELECT ts, input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens, output_tokens
		 FROM events
		 WHERE ts >= ? AND source = 'claude-code' AND provider = ?
		 ORDER BY ts`,
		from.UnixMilli(), model.ProviderAnthropicOAuth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []blockEntry
	for rows.Next() {
		var e blockEntry
		if err := rows.Scan(&e.ts, &e.tokens, &e.out); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return identifyBlocks(entries, BlockDuration.Milliseconds(), now.UnixMilli()), nil
}
