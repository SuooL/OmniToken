package model

// ParseResult is what every log parser returns for one incremental read
// (ADR-0004 offset protocol, ADR-0007 quota snapshots).
//
// Consumed counts only bytes belonging to complete lines: a trailing partial
// line stays unconsumed so the next scan re-reads it.
type ParseResult struct {
	Events   []Event
	Quotas   []QuotaSnapshot
	Consumed int64
	// TurnStartMS carries the in-flight turn's start across incremental reads
	// (ADR-0009). A scan sees only the bytes appended since last time, and the
	// "user" line that began a turn usually landed in an earlier chunk — without
	// carrying it, gen_ms would be 0 for nearly every live event, which is
	// exactly the case the metric exists for. Committed with the offset, never
	// ahead of it.
	TurnStartMS int64
}
