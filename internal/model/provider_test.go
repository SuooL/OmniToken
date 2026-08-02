package model

import "testing"

func TestBillingChannel(t *testing.T) {
	cases := map[string]string{
		// Positive evidence about how the machine pays.
		ProviderAnthropicOAuth: ChannelSubscription,
		ProviderOpenAIChatGPT:  ChannelSubscription,
		ProviderAnthropicAPI:   ChannelAPI,
		ProviderOpenAIAPI:      ChannelAPI,
		// First-party managed hosting: pay-per-token, no subscription window.
		ProviderBedrock: ChannelAPI,
		ProviderVertex:  ChannelAPI,
		// Event-level evidence that the endpoint was not first-party.
		ProviderRelay: ChannelRelay,
		// First-party endpoint confirmed, payment method not established.
		ProviderAnthropic: ChannelUnknown,
		ProviderOpenAI:    ChannelUnknown,
		ProviderUnknown:   ChannelUnknown,
		"":                ChannelUnknown,
		// A relay's own declared name is still just "not first-party".
		"sub2api": ChannelRelay,
		"enjoy":   ChannelRelay,
		"custom":  ChannelRelay,
	}
	for provider, want := range cases {
		if got := BillingChannel(provider); got != want {
			t.Errorf("BillingChannel(%q) = %q, want %q", provider, got, want)
		}
	}
}

// Guards ADR-0018 §5: unknown is never folded into another class, so no mapping
// may silently resolve a missing provider into a billed channel.
func TestBillingChannelNeverGuessesFromModelName(t *testing.T) {
	for _, p := range []string{"anthropic.claude-opus-4-8", "claude-opus-4-6", "gpt-5"} {
		if got := BillingChannel(p); got != ChannelRelay && got != ChannelUnknown {
			t.Errorf("BillingChannel(%q) = %q: model-shaped strings must not reach a billed channel", p, got)
		}
	}
}

func TestBillingChannelIsPure(t *testing.T) {
	for i := 0; i < 3; i++ {
		if got := BillingChannel(ProviderRelay); got != ChannelRelay {
			t.Fatalf("call %d: %q", i, got)
		}
	}
}

func TestChannelLabelCoversEveryChannel(t *testing.T) {
	for _, ch := range Channels() {
		if ChannelLabel(ch) == "" {
			t.Errorf("channel %q has no display label", ch)
		}
	}
	if len(Channels()) != 4 {
		t.Fatalf("want exactly 4 channels, got %v", Channels())
	}
}

// ProviderRank encodes ADR-0018 §5's overwrite rules. The reclassification must
// never demote positive evidence, and event-level evidence outranks the
// machine-level probe (§3: the relay verdict is terminal).
func TestProviderRankOrdering(t *testing.T) {
	if ProviderRank(ProviderRelay) <= ProviderRank(ProviderAnthropicOAuth) {
		t.Error("event-level relay must outrank the machine-level probe (ADR-0018 §3)")
	}
	if ProviderRank(ProviderAnthropicOAuth) <= ProviderRank(ProviderAnthropic) {
		t.Error("probe-confirmed billing must outrank the undetermined first-party label")
	}
	if ProviderRank(ProviderAnthropic) <= ProviderRank(ProviderUnknown) {
		t.Error("first-party-confirmed must outrank no evidence at all")
	}
	if ProviderRank(ProviderAnthropicOAuth) != ProviderRank(ProviderAnthropicAPI) {
		t.Error("two probe outcomes are the same strength; neither may overwrite the other")
	}
	// Model-name fingerprints carry no evidence at all (ADR-0018 §16 rationale):
	// they must be overwritable by anything, including by "unknown".
	for _, legacy := range []string{ProviderBedrock, ProviderVertex} {
		if ProviderRank(legacy) != rankFingerprint {
			t.Errorf("%q must rank below every evidence-backed label", legacy)
		}
	}
}
