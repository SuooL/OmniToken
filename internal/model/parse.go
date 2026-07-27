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
}
