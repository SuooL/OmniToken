package collect

import (
	"testing"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/parser/claudecode"
	"github.com/suool/omnitoken/internal/parser/codex"
)

func probeOf(p ClaudeAuthProbe) func() ClaudeAuthProbe {
	return func() ClaudeAuthProbe { return p }
}

// The probe answers "how does this machine pay", which only applies once the
// event is known to have used the first-party endpoint (ADR-0018 §3).
func TestRefineProviderResolvesFirstPartyPayment(t *testing.T) {
	events := []model.Event{
		{Source: claudecode.Source, Provider: model.ProviderAnthropic},
	}
	RefineProvider(events, probeOf(ClaudeAuthProbe{Provider: model.ProviderAnthropicOAuth}))
	if events[0].Provider != model.ProviderAnthropicOAuth {
		t.Errorf("provider = %q, want %q", events[0].Provider, model.ProviderAnthropicOAuth)
	}
}

// The relay verdict is terminal: a machine that holds a subscription also sends
// traffic through relays, and the machine-level probe cannot see the difference.
func TestRefineProviderNeverPromotesRelay(t *testing.T) {
	for _, probed := range []string{model.ProviderAnthropicOAuth, model.ProviderAnthropicAPI} {
		events := []model.Event{{Source: claudecode.Source, Provider: model.ProviderRelay}}
		RefineProvider(events, probeOf(ClaudeAuthProbe{Provider: probed}))
		if events[0].Provider != model.ProviderRelay {
			t.Errorf("probe %q rewrote a relay event to %q", probed, events[0].Provider)
		}
	}
}

// ADR-0018 §4 safety valve: with the endpoint rerouted, "no Anthropic request
// id" no longer distinguishes a relay from a real Bedrock/Vertex deployment,
// so the machine must say unknown rather than guess wrong.
func TestRefineProviderEndpointOverrideDemotesRelayToUnknown(t *testing.T) {
	events := []model.Event{
		{Source: claudecode.Source, Provider: model.ProviderRelay},
		{Source: claudecode.Source, Provider: model.ProviderAnthropic},
	}
	RefineProvider(events, probeOf(ClaudeAuthProbe{EndpointOverride: true}))
	if events[0].Provider != model.ProviderUnknown {
		t.Errorf("relay under an endpoint override = %q, want %q", events[0].Provider, model.ProviderUnknown)
	}
	if events[1].Provider != model.ProviderAnthropic {
		t.Errorf("first-party event must be untouched, got %q", events[1].Provider)
	}
}

// The probe describes the local Claude Code install; nothing else may be
// relabelled by it.
func TestRefineProviderLeavesOtherSourcesAlone(t *testing.T) {
	events := []model.Event{
		{Source: codex.Source, Provider: model.ProviderOpenAI},
		{Source: "proxy", Provider: model.ProviderAnthropic},
	}
	RefineProvider(events, probeOf(ClaudeAuthProbe{Provider: model.ProviderAnthropicOAuth}))
	if events[0].Provider != model.ProviderOpenAI || events[1].Provider != model.ProviderAnthropic {
		t.Errorf("non claude-code events were rewritten: %+v", events)
	}
}

func TestRefineProviderNoEvidenceChangesNothing(t *testing.T) {
	events := []model.Event{
		{Source: claudecode.Source, Provider: model.ProviderAnthropic},
		{Source: claudecode.Source, Provider: model.ProviderRelay},
	}
	RefineProvider(events, probeOf(ClaudeAuthProbe{}))
	RefineProvider(events, nil)
	if events[0].Provider != model.ProviderAnthropic || events[1].Provider != model.ProviderRelay {
		t.Errorf("events changed without evidence: %+v", events)
	}
}

// Refinement is applied on every scan of the same file, so it has to be
// idempotent (ADR-0018 §5.3).
func TestRefineProviderIsIdempotent(t *testing.T) {
	events := []model.Event{{Source: claudecode.Source, Provider: model.ProviderAnthropic}}
	probe := probeOf(ClaudeAuthProbe{Provider: model.ProviderAnthropicOAuth})
	RefineProvider(events, probe)
	first := events[0].Provider
	for i := 0; i < 3; i++ {
		RefineProvider(events, probe)
		if events[0].Provider != first {
			t.Fatalf("run %d changed %q to %q", i, first, events[0].Provider)
		}
	}
}

// RefineProvider only ever writes the provider column (ADR-0018 §5.1).
func TestRefineProviderTouchesNoCountColumn(t *testing.T) {
	before := model.Event{
		EventID: "cc:msg:req", TS: 1234, Device: "d", Source: claudecode.Source,
		Model: "claude-opus-4-8", Provider: model.ProviderAnthropic,
		InputTokens: 11, OutputTokens: 22, CacheReadTokens: 33, CacheCreationTokens: 44,
		Cache1hTokens: 5, Cache5mTokens: 6, DurationMS: 7, GenMS: 8, TTFTMS: 9,
	}
	events := []model.Event{before}
	RefineProvider(events, probeOf(ClaudeAuthProbe{Provider: model.ProviderAnthropicOAuth}))
	after := events[0]
	after.Provider = before.Provider
	if after != before {
		t.Errorf("a column other than provider changed:\n got %+v\nwant %+v", after, before)
	}
}
