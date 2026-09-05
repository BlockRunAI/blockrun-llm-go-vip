package vip

import (
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// NewAnthropic returns the official anthropic-sdk-go client wired for BlockRun
// native passthrough: requests are routed to the gateway's /v1/messages and
// paid per call via x402, while responses are parsed by the official SDK
// byte-for-byte (real thinking-block signatures, native content blocks, cache
// usage, signature_delta streaming).
//
// Use it exactly like the official SDK:
//
//	client, err := vip.NewAnthropic()
//	if err != nil { ... }
//	msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
//	    Model:     "claude-opus-5", // current flagship; any id is forwarded verbatim,
//	                                // never substituted (also: claude-sonnet-5,
//	                                // claude-haiku-4.5). Opus 5 uses adaptive thinking.
//	    MaxTokens: 1024,
//	    Messages:  []anthropic.MessageParam{
//	        anthropic.NewUserMessage(anthropic.NewTextBlock("What is 23*47?")),
//	    },
//	})
func NewAnthropic(opts ...Option) (anthropic.Client, error) {
	cfg, sign, err := resolveSigner(opts...)
	if err != nil {
		return anthropic.Client{}, err
	}
	if cfg.accountMode() {
		return anthropic.NewClient(option.WithRequestTimeout(defaultChatTimeout()), option.WithBaseURL(cfg.apiURL), option.WithAPIKey(apiKeySentinel), option.WithHTTPClient(cfg.accountHTTPClient(0))), nil
	}
	return anthropic.NewClient(
		// Default per-request timeout for reasoning models (200-300s+); set
		// first so a per-call option.WithRequestTimeout still wins. Override the
		// default via the BLOCKRUN_CHAT_TIMEOUT env var (integer seconds).
		option.WithRequestTimeout(defaultChatTimeout()),
		option.WithBaseURL(cfg.apiURL),
		option.WithAPIKey(cfg.apiKey),
		option.WithMiddleware(x402Middleware(sign, cfg.paymentRoutingHeaders())),
	), nil
}
