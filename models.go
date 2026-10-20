package vip

import "strings"

// Model describes a model published by BlockRun's public catalog.
//
// Prices are in USD, copied from the gateway's GET /v1/models response. Token
// prices are per one million tokens; the media fields use the unit named by
// their field. Per-call transaction fees and surcharges are not carried. Treat this as a useful SDK snapshot and
// use the gateway's GET /v1/models response when an application needs the
// live catalog.
type Model struct {
	ID            string
	Name          string
	Provider      string
	Categories    []string
	ContextWindow int
	MaxOutput     int

	// BillingMode mirrors the gateway's billing_mode: "paid", "free", or the
	// media unit that applies ("per_image", "per_second", "per_character",
	// "per_track", "per_generation"). Use it rather than a zero Pricing to
	// detect the free tier — an unpriced media model is not free.
	BillingMode string

	Pricing ModelPricing
}

// NativeID returns the bare provider-side id — "claude-opus-5" rather than
// "anthropic/claude-opus-5" — which is what the upstream Anthropic and OpenAI
// SDK docs name. The gateway's chat routes resolve both forms, so either can be
// passed to the clients from NewAnthropic and NewOpenAI; the media helpers take
// the namespaced ID.
func (m Model) NativeID() string {
	if _, rest, ok := strings.Cut(m.ID, "/"); ok {
		return rest
	}
	return m.ID
}

// ModelPricing holds the supported billing units for a model. A zero value
// means that unit does not apply to the model.
type ModelPricing struct {
	InputPerMillion  float64
	OutputPerMillion float64
	PerImage         float64
	PerSecond        float64
	PerThousandChars float64
	PerTrack         float64
	PerGeneration    float64

	// Request limits published alongside the price. MaxInputChars applies to
	// speech models; the duration fields apply to video and sound effects.
	MaxInputChars          int
	DefaultDurationSeconds int
	MaxDurationSeconds     int
}

// Models returns a defensive copy of the BlockRun model catalog snapshot.
// It includes chat, image, video, music, speech, and sound-effect models.
func Models() []Model {
	models := make([]Model, len(modelCatalog))
	copy(models, modelCatalog)
	for i := range models {
		models[i].Categories = append([]string(nil), models[i].Categories...)
	}
	return models
}

// FindModel returns the catalog entry for id. It accepts either the namespaced
// catalog id ("anthropic/claude-opus-5") or the bare native id the passthrough
// clients take ("claude-opus-5"); the namespaced form is matched first. Bare
// ids are unique across the catalog.
func FindModel(id string) (Model, bool) {
	for _, model := range modelCatalog {
		if model.ID == id {
			return model.clone(), true
		}
	}
	for _, model := range modelCatalog {
		if model.NativeID() == id {
			return model.clone(), true
		}
	}
	return Model{}, false
}

func (m Model) clone() Model {
	m.Categories = append([]string(nil), m.Categories...)
	return m
}

// Catalog snapshot from https://blockrun.ai/api/v1/models on 2026-10-20, in
// the gateway's order. It carries every listed model; the gateway's per-model
// "available" flag is a live health signal (free models flip it as NVIDIA load
// sheds) and is deliberately not part of the snapshot. Refresh this list
// together with the LiteLLM catalog when the gateway adds models. The gateway
// continues to accept any supported model id verbatim.
var modelCatalog = []Model{
	{ID: "openai/gpt-6-astra", Name: "GPT-6 Astra", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 10, OutputPerMillion: 50}},
	{ID: "openai/gpt-6.1-sol", Name: "GPT-6.1 Sol", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 10}},
	{ID: "openai/gpt-6-sol", Name: "GPT-6 Sol", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 10}},
	{ID: "openai/gpt-6-luna", Name: "GPT-6 Luna", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.1, OutputPerMillion: 0.5}},
	{ID: "openai/gpt-5.6-sol", Name: "GPT-5.6 Sol", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 4, OutputPerMillion: 20}},
	{ID: "openai/gpt-5.6-terra", Name: "GPT-5.6 Terra", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 12}},
	{ID: "openai/gpt-5.6-luna", Name: "GPT-5.6 Luna", Provider: "openai", Categories: []string{"chat", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.2, OutputPerMillion: 1.2}},
	{ID: "openai/gpt-5.6-sol-pro", Name: "GPT-5.6 Sol Pro", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 4, OutputPerMillion: 20}},
	{ID: "openai/gpt-5.6-terra-pro", Name: "GPT-5.6 Terra Pro", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 12}},
	{ID: "openai/gpt-5.6-luna-pro", Name: "GPT-5.6 Luna Pro", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.2, OutputPerMillion: 1.2}},
	{ID: "openai/gpt-5.5", Name: "GPT-5.5", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 5, OutputPerMillion: 30}},
	{ID: "openai/gpt-5.5-pro", Name: "GPT-5.5 Pro", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 30, OutputPerMillion: 180}},
	{ID: "openai/chat-latest", Name: "ChatGPT Instant (GPT-5.5)", Provider: "openai", Categories: []string{"chat", "vision"}, ContextWindow: 128000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 5, OutputPerMillion: 30}},
	{ID: "openai/gpt-5.4", Name: "GPT-5.4", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2.5, OutputPerMillion: 15}},
	{ID: "openai/gpt-5.4-pro", Name: "GPT-5.4 Pro", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 30, OutputPerMillion: 180}},
	{ID: "openai/gpt-5.1", Name: "GPT-5.1", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 400000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.25, OutputPerMillion: 10}},
	{ID: "openai/gpt-5.2", Name: "GPT-5.2", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 400000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.75, OutputPerMillion: 14}},
	{ID: "openai/gpt-5.4-mini", Name: "GPT-5.4 Mini", Provider: "openai", Categories: []string{"chat", "coding", "vision"}, ContextWindow: 400000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.75, OutputPerMillion: 4.5}},
	{ID: "openai/gpt-5-mini", Name: "GPT-5 Mini", Provider: "openai", Categories: []string{"chat", "coding"}, ContextWindow: 200000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.25, OutputPerMillion: 2}},
	{ID: "openai/gpt-5.4-nano", Name: "GPT-5.4 Nano", Provider: "openai", Categories: []string{"chat"}, ContextWindow: 1050000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.2, OutputPerMillion: 1.25}},
	{ID: "openai/gpt-5.2-pro", Name: "GPT-5.2 Pro", Provider: "openai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 400000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 21, OutputPerMillion: 168}},
	{ID: "openai/gpt-5.3-codex", Name: "GPT-5.3 Codex", Provider: "openai", Categories: []string{"coding", "reasoning", "chat"}, ContextWindow: 400000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.75, OutputPerMillion: 14}},
	{ID: "openai/gpt-4.1", Name: "GPT-4.1", Provider: "openai", Categories: []string{"chat", "coding", "vision"}, ContextWindow: 128000, MaxOutput: 32768, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 8}},
	{ID: "openai/gpt-4.1-mini", Name: "GPT-4.1 Mini", Provider: "openai", Categories: []string{"chat", "coding"}, ContextWindow: 128000, MaxOutput: 32768, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.4, OutputPerMillion: 1.6}},
	{ID: "openai/gpt-4.1-nano", Name: "GPT-4.1 Nano", Provider: "openai", Categories: []string{"chat"}, ContextWindow: 128000, MaxOutput: 32768, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.1, OutputPerMillion: 0.4}},
	{ID: "openai/gpt-4o", Name: "GPT-4o", Provider: "openai", Categories: []string{"chat", "coding", "vision"}, ContextWindow: 128000, MaxOutput: 16384, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2.5, OutputPerMillion: 10}},
	{ID: "openai/gpt-4o-mini", Name: "GPT-4o Mini", Provider: "openai", Categories: []string{"chat", "coding"}, ContextWindow: 128000, MaxOutput: 16384, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.15, OutputPerMillion: 0.6}},
	{ID: "openai/o1", Name: "o1", Provider: "openai", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 200000, MaxOutput: 100000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 15, OutputPerMillion: 60}},
	{ID: "openai/o3", Name: "o3", Provider: "openai", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 200000, MaxOutput: 100000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 8}},
	{ID: "openai/o3-mini", Name: "o3-mini", Provider: "openai", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 128000, MaxOutput: 100000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.1, OutputPerMillion: 4.4}},
	{ID: "openai/o4-mini", Name: "o4-mini", Provider: "openai", Categories: []string{"reasoning", "coding"}, ContextWindow: 128000, MaxOutput: 100000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.1, OutputPerMillion: 4.4}},
	{ID: "anthropic/claude-haiku-4.5", Name: "Claude Haiku 4.5", Provider: "anthropic", Categories: []string{"chat", "coding", "vision"}, ContextWindow: 200000, MaxOutput: 64000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1, OutputPerMillion: 5}},
	{ID: "anthropic/claude-sonnet-5.5", Name: "Claude Sonnet 5.5", Provider: "anthropic", Categories: []string{"chat", "coding", "reasoning", "vision"}, ContextWindow: 1000000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 10}},
	{ID: "anthropic/claude-sonnet-5", Name: "Claude Sonnet 5", Provider: "anthropic", Categories: []string{"chat", "coding", "reasoning", "vision"}, ContextWindow: 1000000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 10}},
	{ID: "anthropic/claude-sonnet-4.6", Name: "Claude Sonnet 4.6", Provider: "anthropic", Categories: []string{"chat", "coding", "reasoning", "vision"}, ContextWindow: 1000000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 3, OutputPerMillion: 15}},
	{ID: "anthropic/claude-sonnet-4.5", Name: "Claude Sonnet 4.5", Provider: "anthropic", Categories: []string{"chat", "coding", "reasoning", "vision"}, ContextWindow: 200000, MaxOutput: 64000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 3, OutputPerMillion: 15}},
	{ID: "anthropic/claude-opus-4.5", Name: "Claude Opus 4.5", Provider: "anthropic", Categories: []string{"chat", "coding", "reasoning", "vision"}, ContextWindow: 200000, MaxOutput: 64000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 5, OutputPerMillion: 25}},
	{ID: "anthropic/claude-opus-4.7", Name: "Claude Opus 4.7", Provider: "anthropic", Categories: []string{"chat", "coding", "reasoning", "vision"}, ContextWindow: 1000000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 5, OutputPerMillion: 25}},
	{ID: "anthropic/claude-fable-5.1", Name: "Claude Fable 5.1", Provider: "anthropic", Categories: []string{"chat", "coding", "reasoning", "vision"}, ContextWindow: 1000000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 10, OutputPerMillion: 50}},
	{ID: "anthropic/claude-fable-5", Name: "Claude Fable 5", Provider: "anthropic", Categories: []string{"chat", "coding", "reasoning", "vision"}, ContextWindow: 1000000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 10, OutputPerMillion: 50}},
	{ID: "anthropic/claude-opus-4.8", Name: "Claude Opus 4.8", Provider: "anthropic", Categories: []string{"chat", "coding", "reasoning", "vision"}, ContextWindow: 1000000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 5, OutputPerMillion: 25}},
	{ID: "anthropic/claude-opus-5", Name: "Claude Opus 5", Provider: "anthropic", Categories: []string{"chat", "coding", "reasoning", "vision"}, ContextWindow: 1000000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 5, OutputPerMillion: 25}},
	{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5", Provider: "anthropic", Categories: []string{"chat", "coding", "reasoning", "vision"}, ContextWindow: 1000000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 4, OutputPerMillion: 20}},
	{ID: "google/gemini-4-argon", Name: "Gemini 4 Argon", Provider: "google", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 10}},
	{ID: "google/gemini-3.1-pro", Name: "Gemini 3.1 Pro", Provider: "google", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 12}},
	{ID: "google/gemini-3-flash-preview", Name: "Gemini 3 Flash Preview", Provider: "google", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.5, OutputPerMillion: 3}},
	{ID: "google/gemini-3.8-flash", Name: "Gemini 3.8 Flash", Provider: "google", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.75, OutputPerMillion: 3.75}},
	{ID: "google/gemini-3.6-flash", Name: "Gemini 3.6 Flash", Provider: "google", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.75, OutputPerMillion: 3.75}},
	{ID: "google/gemini-3.5-flash", Name: "Gemini 3.5 Flash", Provider: "google", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.5, OutputPerMillion: 9}},
	{ID: "google/gemini-2.5-pro", Name: "Gemini 2.5 Pro", Provider: "google", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.25, OutputPerMillion: 10}},
	{ID: "google/gemini-2.5-flash", Name: "Gemini 2.5 Flash", Provider: "google", Categories: []string{"chat", "coding", "vision"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.3, OutputPerMillion: 2.5}},
	{ID: "google/gemini-3.5-flash-lite", Name: "Gemini 3.5 Flash Lite", Provider: "google", Categories: []string{"chat", "reasoning"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.3, OutputPerMillion: 2.5}},
	{ID: "google/gemini-3.1-flash-lite", Name: "Gemini 3.1 Flash Lite", Provider: "google", Categories: []string{"chat", "reasoning"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.25, OutputPerMillion: 1.5}},
	{ID: "google/gemini-2.5-flash-lite", Name: "Gemini 2.5 Flash Lite", Provider: "google", Categories: []string{"chat"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.1, OutputPerMillion: 0.4}},
	{ID: "deepseek/deepseek-v4-flash-vision-exp", Name: "DeepSeek V4 Flash Vision", Provider: "deepseek", Categories: []string{"chat", "reasoning", "vision"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.3, OutputPerMillion: 1.2}},
	{ID: "deepseek/deepseek-v4-pro", Name: "DeepSeek V4 Pro", Provider: "deepseek", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.32, OutputPerMillion: 3.96}},
	{ID: "deepseek/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", Provider: "deepseek", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.14, OutputPerMillion: 0.28}},
	{ID: "deepseek/deepseek-chat", Name: "DeepSeek Chat (V4.1 Flash)", Provider: "deepseek", Categories: []string{"chat", "coding"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.14, OutputPerMillion: 0.28}},
	{ID: "deepseek/deepseek-reasoner", Name: "DeepSeek Reasoner (V4.1 Flash)", Provider: "deepseek", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.14, OutputPerMillion: 0.28}},
	{ID: "moonshot/kimi-k3", Name: "Kimi K3", Provider: "moonshot", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 3, OutputPerMillion: 15}},
	{ID: "zai/glm-5.3", Name: "GLM-5.3", Provider: "zai", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 1000000, MaxOutput: 131072, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.4, OutputPerMillion: 4.4}},
	{ID: "zai/glm-5.3-flash", Name: "GLM-5.3 Flash", Provider: "zai", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1000000, MaxOutput: 131072, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.15, OutputPerMillion: 0.5}},
	{ID: "zai/glm-5.2", Name: "GLM-5.2", Provider: "zai", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 1000000, MaxOutput: 131072, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.4, OutputPerMillion: 4.4}},
	{ID: "zai/glm-5.1", Name: "GLM-5.1", Provider: "zai", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 200000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.4, OutputPerMillion: 4.4}},
	{ID: "zai/glm-5", Name: "GLM-5", Provider: "zai", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 200000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1, OutputPerMillion: 3.2}},
	{ID: "zai/glm-5-turbo", Name: "GLM-5 Turbo", Provider: "zai", Categories: []string{"chat", "reasoning"}, ContextWindow: 200000, MaxOutput: 128000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.2, OutputPerMillion: 4}},
	{ID: "xai/grok-4.3", Name: "Grok 4.3", Provider: "xai", Categories: []string{"reasoning", "coding", "vision", "chat"}, ContextWindow: 1000000, MaxOutput: 16384, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.25, OutputPerMillion: 2.5}},
	{ID: "xai/grok-build-0.1", Name: "Grok Build 0.1", Provider: "xai", Categories: []string{"coding", "chat"}, ContextWindow: 256000, MaxOutput: 16384, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1, OutputPerMillion: 2}},
	{ID: "xai/grok-4.7", Name: "Grok 4.7", Provider: "xai", Categories: []string{"reasoning", "coding", "vision", "chat", "search"}, ContextWindow: 500000, MaxOutput: 16384, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 6}},
	{ID: "xai/grok-4.6", Name: "Grok 4.6", Provider: "xai", Categories: []string{"reasoning", "coding", "vision", "chat", "search"}, ContextWindow: 500000, MaxOutput: 16384, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 6}},
	{ID: "xai/grok-4.5", Name: "Grok 4.5", Provider: "xai", Categories: []string{"reasoning", "coding", "vision", "chat", "search"}, ContextWindow: 500000, MaxOutput: 16384, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 6}},
	{ID: "minimax/minimax-m2.7", Name: "MiniMax M2.7", Provider: "minimax", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 204800, MaxOutput: 16384, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.3, OutputPerMillion: 1.2}},
	{ID: "minimax/minimax-m3", Name: "MiniMax M3", Provider: "minimax", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 1048576, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.3, OutputPerMillion: 1.2}},
	{ID: "qwen/qwen3.8-max", Name: "Qwen3.8 Max", Provider: "qwen", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 1000000, MaxOutput: 131072, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 2, OutputPerMillion: 6}},
	{ID: "qwen/qwen3.7-max", Name: "Qwen3.7 Max", Provider: "qwen", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 1000000, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.475, OutputPerMillion: 4.425}},
	{ID: "qwen/qwen3.7-plus", Name: "Qwen3.7 Plus", Provider: "qwen", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 1000000, MaxOutput: 131072, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.32, OutputPerMillion: 1.28}},
	{ID: "qwen/qwen3.7-flash", Name: "Qwen3.7 Flash", Provider: "qwen", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 1000000, MaxOutput: 65536, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.03, OutputPerMillion: 0.13}},
	{ID: "qwen/qwen3.8-flash", Name: "Qwen3.8 Flash", Provider: "qwen", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 1000000, MaxOutput: 131072, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.15, OutputPerMillion: 0.47}},
	{ID: "tencent/hy4-preview", Name: "Tencent Hy4 Preview", Provider: "tencent", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 1048576, MaxOutput: 64000, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.834, OutputPerMillion: 2.501}},
	{ID: "xiaomi/mimo-v2.5", Name: "MiMo V2.5", Provider: "xiaomi", Categories: []string{"chat", "reasoning", "vision"}, ContextWindow: 1048576, MaxOutput: 131072, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.14, OutputPerMillion: 0.28}},
	{ID: "xiaomi/mimo-v2.5-pro", Name: "Xiaomi MiMo-V2.5 Pro", Provider: "xiaomi", Categories: []string{"chat", "reasoning"}, ContextWindow: 1048576, MaxOutput: 131072, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.435, OutputPerMillion: 0.87}},
	{ID: "nvidia/nemotron-3-nano-omni-30b-a3b-reasoning", Name: "Nemotron 3 Nano Omni (Free)", Provider: "nvidia", Categories: []string{"chat", "reasoning", "vision"}, ContextWindow: 256000, MaxOutput: 16384, BillingMode: "free"},
	{ID: "nvidia/nemotron-3.5-lightning", Name: "Nemotron 3.5 Lightning (Free)", Provider: "nvidia", Categories: []string{"chat", "reasoning"}, ContextWindow: 1000000, MaxOutput: 16384, BillingMode: "free"},
	{ID: "nvidia/llama-3.2-11b-vision", Name: "Llama 3.2 11B Vision (Free)", Provider: "nvidia", Categories: []string{"chat", "vision"}, ContextWindow: 128000, MaxOutput: 16384, BillingMode: "free"},
	{ID: "nvidia/nemotron-3-ultra-550b", Name: "Nemotron 3 Ultra 550B (Free)", Provider: "nvidia", Categories: []string{"chat", "reasoning"}, ContextWindow: 1000000, MaxOutput: 16384, BillingMode: "free"},
	{ID: "cohere/north-mini-code", Name: "Cohere North Mini Code (Free)", Provider: "cohere", Categories: []string{"chat", "coding"}, ContextWindow: 256000, MaxOutput: 16384, BillingMode: "free"},
	{ID: "poolside/laguna-xs-2.1", Name: "Poolside Laguna XS 2.1 (Free)", Provider: "nvidia", Categories: []string{"chat", "coding"}, ContextWindow: 131072, MaxOutput: 16384, BillingMode: "free"},
	{ID: "poolside/laguna-s-2.1", Name: "Laguna S 2.1", Provider: "poolside", Categories: []string{"chat", "coding"}, ContextWindow: 1048576, MaxOutput: 131072, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 0.09, OutputPerMillion: 0.18}},
	{ID: "mistral/mistral-large-4", Name: "Mistral Large 4", Provider: "mistral", Categories: []string{"chat", "reasoning", "coding", "vision"}, ContextWindow: 524288, MaxOutput: 262144, BillingMode: "paid", Pricing: ModelPricing{InputPerMillion: 1.36, OutputPerMillion: 4.18}},
	{ID: "nvidia/muse-glimmer-30b", Name: "Muse Glimmer 30B (Free)", Provider: "nvidia", Categories: []string{"chat", "reasoning", "coding"}, ContextWindow: 131072, MaxOutput: 16384, BillingMode: "free"},
	{ID: "openai/gpt-image-1", Name: "GPT Image 1", Provider: "openai", Categories: []string{"image"}, BillingMode: "per_image", Pricing: ModelPricing{PerImage: 0.021}},
	{ID: "openai/gpt-image-2", Name: "ChatGPT Images 2.0", Provider: "openai", Categories: []string{"image"}, BillingMode: "per_image", Pricing: ModelPricing{PerImage: 0.063}},
	{ID: "openai/gpt-image-2.5-flare", Name: "GPT Image 2.5 Flare", Provider: "openai", Categories: []string{"image"}, BillingMode: "per_image", Pricing: ModelPricing{PerImage: 0.294}},
	{ID: "openai/gpt-image-2.5-sunburst", Name: "GPT Image 2.5 Sunburst", Provider: "openai", Categories: []string{"image"}, BillingMode: "per_image", Pricing: ModelPricing{PerImage: 0.294}},
	{ID: "google/nano-banana", Name: "Nano Banana", Provider: "google", Categories: []string{"image"}, BillingMode: "per_image", Pricing: ModelPricing{PerImage: 0.0525}},
	{ID: "google/nano-banana-2", Name: "Nano Banana 2", Provider: "google", Categories: []string{"image"}, BillingMode: "per_image", Pricing: ModelPricing{PerImage: 0.0945}},
	{ID: "google/nano-banana-pro", Name: "Nano Banana Pro", Provider: "google", Categories: []string{"image"}, BillingMode: "per_image", Pricing: ModelPricing{PerImage: 0.105}},
	{ID: "xai/grok-imagine-image", Name: "Grok Imagine", Provider: "xai", Categories: []string{"image"}, BillingMode: "per_image", Pricing: ModelPricing{PerImage: 0.021}},
	{ID: "xai/grok-imagine-image-2.0", Name: "Grok Imagine 2.0", Provider: "xai", Categories: []string{"image"}, BillingMode: "per_image", Pricing: ModelPricing{PerImage: 0.042}},
	{ID: "xai/grok-imagine-image-pro", Name: "Grok Imagine Pro", Provider: "xai", Categories: []string{"image"}, BillingMode: "per_image", Pricing: ModelPricing{PerImage: 0.0525}},
	{ID: "bytedance/seedream-5-pro", Name: "Seedream 5.0 Pro", Provider: "bytedance", Categories: []string{"image"}, BillingMode: "per_image", Pricing: ModelPricing{PerImage: 0.04725}},
	{ID: "zai/cogview-4", Name: "CogView-4", Provider: "zai", Categories: []string{"image"}, BillingMode: "per_image", Pricing: ModelPricing{PerImage: 0.01575}},
	{ID: "minimax/music-2.5+", Name: "MiniMax Music 2.5+", Provider: "minimax", Categories: []string{"music"}, BillingMode: "per_track", Pricing: ModelPricing{PerTrack: 0.1575}},
	{ID: "elevenlabs/flash-v2.5", Name: "ElevenLabs Flash v2.5", Provider: "elevenlabs", Categories: []string{"speech", "tts"}, BillingMode: "per_character", Pricing: ModelPricing{PerThousandChars: 0.0525, MaxInputChars: 40000}},
	{ID: "elevenlabs/turbo-v2.5", Name: "ElevenLabs Turbo v2.5", Provider: "elevenlabs", Categories: []string{"speech", "tts"}, BillingMode: "per_character", Pricing: ModelPricing{PerThousandChars: 0.0525, MaxInputChars: 40000}},
	{ID: "elevenlabs/multilingual-v2", Name: "ElevenLabs Multilingual v2", Provider: "elevenlabs", Categories: []string{"speech", "tts"}, BillingMode: "per_character", Pricing: ModelPricing{PerThousandChars: 0.105, MaxInputChars: 10000}},
	{ID: "elevenlabs/v3", Name: "ElevenLabs v3", Provider: "elevenlabs", Categories: []string{"speech", "tts"}, BillingMode: "per_character", Pricing: ModelPricing{PerThousandChars: 0.105, MaxInputChars: 5000}},
	{ID: "bytedance/seed-audio-1.0", Name: "Seed Audio 1.0", Provider: "bytedance", Categories: []string{"speech", "tts"}, BillingMode: "per_character", Pricing: ModelPricing{PerThousandChars: 0.315, MaxInputChars: 3000}},
	{ID: "elevenlabs/sound-effects", Name: "ElevenLabs Sound Effects", Provider: "elevenlabs", Categories: []string{"sound_effect"}, BillingMode: "per_generation", Pricing: ModelPricing{PerGeneration: 0.0525, MaxDurationSeconds: 22}},
	{ID: "xai/grok-imagine-video", Name: "Grok Imagine Video", Provider: "xai", Categories: []string{"video"}, BillingMode: "per_second", Pricing: ModelPricing{PerSecond: 0.05, DefaultDurationSeconds: 8, MaxDurationSeconds: 15}},
	{ID: "xai/grok-imagine-video-1.5", Name: "Grok Imagine Video 1.5", Provider: "xai", Categories: []string{"video"}, BillingMode: "per_second", Pricing: ModelPricing{PerSecond: 0.08, DefaultDurationSeconds: 8, MaxDurationSeconds: 15}},
	{ID: "bytedance/seedance-1.5-pro", Name: "Seedance 1.5 Pro", Provider: "bytedance", Categories: []string{"video"}, BillingMode: "per_second", Pricing: ModelPricing{PerSecond: 0.070783146, DefaultDurationSeconds: 5, MaxDurationSeconds: 12}},
	{ID: "bytedance/seedance-2.0-fast", Name: "Seedance 2.0 Fast", Provider: "bytedance", Categories: []string{"video"}, BillingMode: "per_second", Pricing: ModelPricing{PerSecond: 0.165160674, DefaultDurationSeconds: 5, MaxDurationSeconds: 15}},
	{ID: "bytedance/seedance-2.0-mini", Name: "Seedance 2.0 Mini", Provider: "bytedance", Categories: []string{"video"}, BillingMode: "per_second", Pricing: ModelPricing{PerSecond: 0.07971075, DefaultDurationSeconds: 5, MaxDurationSeconds: 15}},
	{ID: "bytedance/seedance-2.0", Name: "Seedance 2.0 Pro", Provider: "bytedance", Categories: []string{"video"}, BillingMode: "per_second", Pricing: ModelPricing{PerSecond: 0.22709592675, DefaultDurationSeconds: 5, MaxDurationSeconds: 15}},
	{ID: "bytedance/seedance-2.5", Name: "Seedance 2.5", Provider: "bytedance", Categories: []string{"video"}, BillingMode: "per_second", Pricing: ModelPricing{PerSecond: 0.31557485925, DefaultDurationSeconds: 5, MaxDurationSeconds: 30}},
	{ID: "azure/sora-2", Name: "Sora 2", Provider: "azure", Categories: []string{"video"}, BillingMode: "per_second", Pricing: ModelPricing{PerSecond: 0.105, DefaultDurationSeconds: 4, MaxDurationSeconds: 12}},
}
