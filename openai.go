package vip

import (
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// NewOpenAI returns the official openai-go client wired for BlockRun native
// passthrough: requests are routed to the gateway and paid per call via x402,
// while responses are parsed by the official SDK byte-for-byte (genuine
// chatcmpl id, system_fingerprint, usage token details, JSON mode, streaming).
//
// Use it exactly like the official SDK:
//
//	client, err := vip.NewOpenAI()
//	if err != nil { ... }
//	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
//	    Model: openai.ChatModelGPT4o,
//	    Messages: []openai.ChatCompletionMessageParamUnion{
//	        openai.UserMessage("hi"),
//	    },
//	})
func NewOpenAI(opts ...Option) (openai.Client, error) {
	cfg, sign, err := resolveSigner(opts...)
	if err != nil {
		return openai.Client{}, err
	}
	return openai.NewClient(
		// Default per-request timeout for reasoning models (200-300s+); set
		// first so a per-call option.WithRequestTimeout still wins. Override the
		// default via the BLOCKRUN_CHAT_TIMEOUT env var (integer seconds).
		option.WithRequestTimeout(defaultChatTimeout()),
		// OpenAI SDK appends "chat/completions" to the base URL, so the
		// gateway's /v1 prefix must be part of the base (Anthropic's SDK adds
		// /v1/messages itself, so its base stays /api).
		option.WithBaseURL(cfg.apiURL+"/v1"),
		option.WithAPIKey(cfg.apiKey),
		option.WithMiddleware(x402Middleware(sign, cfg.paymentRoutingHeaders())),
	), nil
}
