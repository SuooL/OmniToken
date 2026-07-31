package collect

import (
	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/parser/claudecode"
)

// RefineProvider applies this machine's auth probe to freshly parsed Claude
// Code events (F9 / ADR-0018 §3). Only local collection may call it — the probe
// describes THIS machine, so SSH-pulled events keep whatever their own log said.
//
// Two rules, and the second is the one that matters:
//
//  1. An event that reached the first-party endpoint but whose payment method
//     the log cannot show (ProviderAnthropic) is resolved by the probe. OAuth
//     and API keys are indistinguishable in the log — same endpoint, same
//     request id — so this is the only thing that can split them.
//
//  2. A relay event is never promoted, no matter what the probe found. The
//     probe answers "how does this machine pay"; the log line answers "which
//     endpoint answered this request". Running a subscription and a relay from
//     one machine is the ordinary case — 25k first-party events alongside 5.2k
//     relay events on the machine this was built against — so letting a single
//     machine-level `anthropic-oauth` repaint the relay traffic as subscription
//     is the very error ADR-0018 was written to correct.
//
// The endpoint-override case runs the other way (ADR-0018 §4): once traffic is
// rerouted, a missing Anthropic request id no longer separates a relay from a
// real Bedrock/Vertex deployment, so the relay verdict is withdrawn rather than
// kept. Unknown is the honest answer; the panel shows it as its own column.
func RefineProvider(events []model.Event, probe func() ClaudeAuthProbe) {
	if probe == nil {
		return
	}
	p := probe()
	if p.Provider == "" && !p.EndpointOverride {
		return
	}
	for i := range events {
		if events[i].Source != claudecode.Source {
			continue
		}
		switch {
		case p.EndpointOverride && events[i].Provider == model.ProviderRelay:
			events[i].Provider = model.ProviderUnknown
		case p.Provider != "" && events[i].Provider == model.ProviderAnthropic:
			events[i].Provider = p.Provider
		}
	}
}
