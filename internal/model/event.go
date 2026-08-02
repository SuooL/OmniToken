package model

// Event is the unified usage event across all sources (claude-code, codex, proxy).
// EventID is globally unique and used for idempotent ingestion.
type Event struct {
	EventID string `json:"event_id"`
	// DedupKey identifies the generation rather than the log line that recorded
	// it (ADR-0020). EventID answers "have I seen this line before"; that is
	// enough everywhere a copy of a request keeps its identifier — claude-code
	// repeats message.id, the proxy has no copies — but Codex forks copy a
	// parent thread's whole history into a new rollout, rewriting the rollout id
	// and every timestamp, which is exactly what EventID is built from. The key
	// is an ADDITIONAL uniqueness constraint, never a replacement: empty means
	// "no second opinion", which is the behaviour every source had before.
	DedupKey            string `json:"dedup_key,omitempty"`
	TS                  int64  `json:"ts"` // unix milliseconds
	Device              string `json:"device"`
	Source              string `json:"source"` // claude-code | codex | proxy
	Model               string `json:"model"`
	Provider            string `json:"provider"` // anthropic | anthropic-api | anthropic-oauth | bedrock | vertex | relay
	AccountLabel        string `json:"account_label,omitempty"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	Cache1hTokens       int64  `json:"cache_1h_tokens,omitempty"`
	Cache5mTokens       int64  `json:"cache_5m_tokens,omitempty"`
	DurationMS          int64  `json:"duration_ms,omitempty"`
	// GenMS spans [request sent → response recorded] (ADR-0009): the denominator
	// for generation speed. Distinct from DurationMS, which spans the gap to the
	// previous log line of any kind and is the work-time interval (ADR-0006) —
	// that one includes the user thinking and tools running.
	GenMS      int64  `json:"gen_ms,omitempty"`
	TTFTMS     int64  `json:"ttft_ms,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	CWD        string `json:"cwd,omitempty"`
	GitBranch  string `json:"git_branch,omitempty"`
	Repo       string `json:"repo,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
}

func (e *Event) TotalTokens() int64 {
	return e.InputTokens + e.OutputTokens + e.CacheReadTokens + e.CacheCreationTokens
}
