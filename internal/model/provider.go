package model

// Billing channel taxonomy (ADR-0018).
//
// Two separate things live here and conflating them is what this file exists to
// prevent:
//
//   - `provider` is the EVIDENCE stored on the event: which endpoint answered,
//     and — where it could be established — how that endpoint is paid for. It is
//     the only column the reclassification ever writes.
//   - `channel` is the CLASSIFICATION the panel shows. It is derived from
//     `provider` at query time by the pure mapping below, exactly as costs are
//     derived at query time (ADR-0005). Changing the mapping therefore never
//     requires a rescan, and it never touches a count column.
//
// The previous occupant of this file, FingerprintProvider, guessed the channel
// from the model id alone. It was disproved on real data: 3,172 events were
// labelled `bedrock` on a machine with no Bedrock configuration at all, because
// a relay had adopted Bedrock-style model names — and relays equally use bare
// `claude-*` names, so the reverse failed too. A naming habit is not evidence of
// an endpoint; anyone can imitate one. Model ids no longer classify anything.
const (
	// ProviderAnthropicOAuth: the first-party Anthropic endpoint, billed
	// against a Claude subscription (probe-confirmed OAuth credentials).
	ProviderAnthropicOAuth = "anthropic-oauth"
	// ProviderAnthropicAPI: the first-party Anthropic endpoint, billed
	// per token against an API key.
	ProviderAnthropicAPI = "anthropic-api"
	// ProviderAnthropic: the first-party endpoint is established (the response
	// carried an Anthropic request id) but how it is paid for is not. OAuth and
	// API keys hit the same endpoint and get the same request id back, so this
	// label can only ever be resolved by probing the machine.
	ProviderAnthropic = "anthropic"
	// ProviderOpenAIChatGPT: Codex on a ChatGPT plan.
	ProviderOpenAIChatGPT = "openai-chatgpt"
	// ProviderOpenAIAPI: OpenAI billed per token against an API key.
	ProviderOpenAIAPI = "openai-api"
	// ProviderOpenAI: the first-party OpenAI endpoint with the payment method
	// undetermined — the Codex counterpart of ProviderAnthropic.
	ProviderOpenAI = "openai"
	// ProviderBedrock / ProviderVertex: first-party managed hosting. Nothing
	// currently produces these labels; they remain in the taxonomy because the
	// database still holds rows carrying them and because ADR-0018 §4 records
	// the intent to detect them properly from the CLAUDE_CODE_USE_* overrides.
	ProviderBedrock = "bedrock"
	ProviderVertex  = "vertex"
	// ProviderRelay: the response did not come from a first-party endpoint.
	// Note what this does and does not say — see BillingChannel.
	ProviderRelay = "relay"
	// ProviderUnknown: no evidence either way.
	ProviderUnknown = "unknown"
)

// Billing channels. These are the four columns the panel shows; `unknown` is
// one of them on purpose (ADR-0018 §5) — it is never folded into another class
// and never apportioned across them.
const (
	// ChannelSubscription is the only channel a subscription quota window
	// applies to. Everything the panel puts next to a quota bar must be
	// filtered to this channel, or the two numbers contradict each other.
	ChannelSubscription = "subscription"
	// ChannelAPI: first-party pay-per-token, including Bedrock/Vertex.
	ChannelAPI = "api"
	// ChannelRelay: a non-first-party endpoint.
	ChannelRelay = "relay"
	// ChannelUnknown: not enough evidence. Shown, with its share of the total.
	ChannelUnknown = "unknown"
)

// providerChannel is the whole mapping. A provider absent from this table is a
// relay's self-declared name (Codex `model_provider` is whatever the user typed
// in the config: `custom`, `sub2api`, `enjoy`, …), which is still evidence of
// exactly one thing — that it is not a first-party endpoint.
var providerChannel = map[string]string{
	ProviderAnthropicOAuth: ChannelSubscription,
	ProviderOpenAIChatGPT:  ChannelSubscription,
	ProviderAnthropicAPI:   ChannelAPI,
	ProviderOpenAIAPI:      ChannelAPI,
	ProviderBedrock:        ChannelAPI,
	ProviderVertex:         ChannelAPI,
	ProviderRelay:          ChannelRelay,
	ProviderAnthropic:      ChannelUnknown,
	ProviderOpenAI:         ChannelUnknown,
	ProviderUnknown:        ChannelUnknown,
	"":                     ChannelUnknown,
}

// BillingChannel maps a stored provider label onto the channel the panel shows.
//
// The honest name for ChannelRelay is "not a first-party endpoint" — no more,
// no less. A real Bedrock or Vertex deployment answers without an Anthropic
// request id too and would land here; that is a known gap, recorded in
// ADR-0018 §4 rather than papered over. What the classification never does is
// guess: an unrecognised first-party label stays ChannelUnknown.
func BillingChannel(provider string) string {
	if ch, ok := providerChannel[provider]; ok {
		return ch
	}
	// Codex's model_provider is a user-chosen name. Anything not first-party is
	// reached through someone else's endpoint, whatever it is called.
	return ChannelRelay
}

// Channels lists the four channels in display order: the one bound by a quota
// window first, then the two metered ones, then what we could not establish.
func Channels() []string {
	return []string{ChannelSubscription, ChannelAPI, ChannelRelay, ChannelUnknown}
}

// ChannelLabel is the Chinese display name for a channel.
func ChannelLabel(channel string) string {
	switch channel {
	case ChannelSubscription:
		return "订阅"
	case ChannelAPI:
		return "官方 API"
	case ChannelRelay:
		return "第三方中转"
	case ChannelUnknown:
		return "未知通道"
	}
	return ""
}

// Evidence strength of each provider label, used as the guard on the one
// sanctioned overwrite of the provider column (ADR-0018 §5). A reclassification
// may only ever raise the rank, which makes it idempotent and order-independent:
// replay the same evidence any number of times, in any order, and the column
// settles on the same value.
const (
	// Model-name fingerprints. Disproved, so they carry no evidence at all and
	// must be overwritable by anything — including by ProviderUnknown, which is
	// what a row gets when its source log is gone and the guess cannot be
	// re-derived (ADR-0018 §6: no evidence is not a licence to keep a bad one).
	rankFingerprint = 0
	// No evidence, but honestly labelled as such.
	rankNone = 1
	// Event-level: the endpoint was first-party. Says nothing about payment.
	rankEndpoint = 2
	// Machine- or session-level: how this endpoint is paid for. Both outcomes
	// share a rank, so neither probe result can overwrite the other — the same
	// shape as ADR-0015's "self does not overwrite self", and for the same
	// reason: re-running today's probe must not rewrite last month's billing.
	rankBilling = 3
	// Event-level and terminal: the response was not first-party. This outranks
	// the probe because the two answer different questions (ADR-0018 §3) — the
	// probe describes how the MACHINE pays, and a machine that holds a
	// subscription also sends traffic through relays. Letting the machine-level
	// answer swallow the event-level one is the very bug being fixed.
	rankNotFirstParty = 4
)

// RankedProviders lists every label whose rank is not the default. It exists so
// the store can mirror ProviderRank as one SQL expression instead of keeping a
// second copy of the ladder that could drift.
func RankedProviders() []string {
	return []string{
		ProviderBedrock, ProviderVertex,
		ProviderUnknown, "",
		ProviderAnthropic, ProviderOpenAI,
		ProviderAnthropicOAuth, ProviderAnthropicAPI,
		ProviderOpenAIChatGPT, ProviderOpenAIAPI,
		ProviderRelay,
	}
}

// DefaultProviderRank is the rank of any label not in RankedProviders — that
// is, a relay's self-declared name.
func DefaultProviderRank() int { return rankNotFirstParty }

// ProviderRank returns the evidence strength of a provider label.
func ProviderRank(provider string) int {
	switch provider {
	case ProviderRelay:
		return rankNotFirstParty
	case ProviderAnthropicOAuth, ProviderAnthropicAPI, ProviderOpenAIChatGPT, ProviderOpenAIAPI:
		return rankBilling
	case ProviderAnthropic, ProviderOpenAI:
		return rankEndpoint
	case ProviderBedrock, ProviderVertex:
		return rankFingerprint
	case ProviderUnknown, "":
		return rankNone
	}
	// A relay's declared name is a first-hand statement by the user that this
	// traffic goes somewhere else, so it is as strong as the relay verdict.
	return rankNotFirstParty
}
