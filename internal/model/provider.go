package model

import "strings"

// FingerprintProvider classifies the API channel from the model ID format alone.
// This works on historical data too:
//   - Bedrock model IDs look like "us.anthropic.claude-...-v1:0" / "anthropic.claude-...-v2:1"
//   - Vertex model IDs look like "claude-sonnet-4@20250514"
//   - First-party Anthropic IDs are bare names like "claude-fable-5"
//   - Anything non-Claude reached through Claude Code means a relay/proxy endpoint.
//
// Distinguishing subscription (OAuth) vs API-key on the first-party endpoint needs
// runtime environment probing (M2); until then those stay as "anthropic".
func FingerprintProvider(modelID string) string {
	m := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case m == "":
		return "unknown"
	case strings.Contains(m, "anthropic.claude"):
		return "bedrock"
	case strings.HasPrefix(m, "claude") && strings.Contains(m, "@"):
		return "vertex"
	case strings.HasPrefix(m, "claude"):
		return "anthropic"
	default:
		return "relay"
	}
}
