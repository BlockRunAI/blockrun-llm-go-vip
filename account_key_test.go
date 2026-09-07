package vip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	blockrun "github.com/BlockRunAI/blockrun-llm-go"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go"
)

// testAccountKey is a syntactically valid BlockRun account API key. It is never
// sent anywhere real — every test below either resolves config locally or talks
// to an httptest server.
const testAccountKey = "brk_live_0000000000000000000000000000000000000000"

func TestResolveConfig_AccountKeyDefaults(t *testing.T) {
	cfg, err := resolveConfig(false, WithAPIKey(testAccountKey))
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if !cfg.usesAccountKey() {
		t.Fatal("usesAccountKey() should be true for a brk_ key")
	}
	if cfg.apiURL != DefaultAccountAPIURL {
		t.Errorf("apiURL = %q, want the account gateway %q", cfg.apiURL, DefaultAccountAPIURL)
	}
	if cfg.apiKey != testAccountKey {
		t.Errorf("apiKey = %q, want the account key verbatim", cfg.apiKey)
	}
}

// An explicit base URL still wins in account mode (staging, self-hosted proxy).
func TestResolveConfig_AccountKeyExplicitBaseURL(t *testing.T) {
	cfg, err := resolveConfig(false, WithAPIKey(testAccountKey), WithBaseURL("https://staging.example/api"))
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if cfg.apiURL != "https://staging.example/api" {
		t.Errorf("apiURL = %q, want the explicit override", cfg.apiURL)
	}
}

// A non-brk_ WithAPIKey is only an upstream-key override: the wallet path and
// its gateway must be exactly as before.
func TestResolveConfig_PlainAPIKeyKeepsWalletPath(t *testing.T) {
	cfg, sign, err := resolveSigner(WithAPIKey("custom-upstream"), WithWalletKey(testWalletKey))
	if err != nil {
		t.Fatalf("resolveSigner: %v", err)
	}
	if sign == nil {
		t.Fatal("wallet path must still resolve a signer")
	}
	if cfg.usesAccountKey() {
		t.Error("a non-brk_ key must not select account mode")
	}
	if cfg.apiURL != blockrun.DefaultAPIURL {
		t.Errorf("apiURL = %q, want %q", cfg.apiURL, blockrun.DefaultAPIURL)
	}
}

// Account mode signs nothing and must not need a wallet at all.
func TestResolveSigner_AccountKeyHasNoSigner(t *testing.T) {
	cfg, sign, err := resolveSigner(WithAPIKey(testAccountKey))
	if err != nil {
		t.Fatalf("resolveSigner: %v", err)
	}
	if sign != nil {
		t.Error("account mode must resolve a nil signer (nothing settles on-chain)")
	}
	if cfg.apiURL != DefaultAccountAPIURL {
		t.Errorf("apiURL = %q, want %q", cfg.apiURL, DefaultAccountAPIURL)
	}
}

// BLOCKRUN_API_KEY selects account mode for the chat constructors...
func TestResolveSigner_AccountKeyFromEnv(t *testing.T) {
	t.Setenv("BLOCKRUN_API_KEY", testAccountKey)
	cfg, sign, err := resolveSigner()
	if err != nil {
		t.Fatalf("resolveSigner: %v", err)
	}
	if sign != nil {
		t.Error("env account key must resolve a nil signer")
	}
	if cfg.apiKey != testAccountKey || cfg.apiURL != DefaultAccountAPIURL {
		t.Errorf("cfg = {apiKey:%q apiURL:%q}, want the env key on the account gateway", cfg.apiKey, cfg.apiURL)
	}
}

// ...but an explicit option still beats the environment.
func TestResolveSigner_ExplicitKeyBeatsEnv(t *testing.T) {
	t.Setenv("BLOCKRUN_API_KEY", testAccountKey)
	cfg, sign, err := resolveSigner(WithAPIKey("custom-upstream"), WithWalletKey(testWalletKey))
	if err != nil {
		t.Fatalf("resolveSigner: %v", err)
	}
	if sign == nil || cfg.apiKey != "custom-upstream" {
		t.Errorf("explicit key lost to the env: apiKey=%q sign=%v", cfg.apiKey, sign != nil)
	}
}

// The wallet-only clients must ignore an environment key they were never meant
// to use — a key exported for a chat client cannot break a media call.
func TestResolveKey_IgnoresEnvAccountKey(t *testing.T) {
	t.Setenv("BLOCKRUN_API_KEY", testAccountKey)
	cfg, key, err := resolveKey(WithWalletKey(testWalletKey))
	if err != nil {
		t.Fatalf("resolveKey: %v", err)
	}
	if key != testWalletKey {
		t.Errorf("key = %q, want the wallet key", key)
	}
	if cfg.apiURL != blockrun.DefaultAPIURL {
		t.Errorf("apiURL = %q, want the wallet gateway %q", cfg.apiURL, blockrun.DefaultAPIURL)
	}
}

// An account key handed to a wallet-only client fails loudly, at construction,
// instead of 402-ing against a gateway that cannot bill it.
func TestResolveKey_RejectsExplicitAccountKey(t *testing.T) {
	_, _, err := resolveKey(WithAPIKey(testAccountKey))
	if err == nil {
		t.Fatal("expected an error: media clients settle on-chain")
	}
	if !strings.Contains(err.Error(), "wallet") {
		t.Errorf("error %q should say a wallet is needed", err)
	}
	if _, mediaErr := NewImage(WithAPIKey(testAccountKey)); mediaErr == nil {
		t.Error("NewImage should reject an account key")
	}
}

func TestResolveConfig_AccountKeyConflicts(t *testing.T) {
	if _, err := resolveConfig(false, WithAPIKey(testAccountKey), WithChain("solana")); err == nil {
		t.Error("WithChain + account key should error")
	}
	if _, err := resolveConfig(false, WithAPIKey(testAccountKey), WithWalletKey(testWalletKey)); err == nil {
		t.Error("WithWalletKey + account key should error")
	}
}

// End-to-end through the official SDKs: account mode must send the key, must
// NOT send a payment signature, and must not install the x402 middleware.
func TestAccountModeRequests(t *testing.T) {
	type capture struct {
		path, auth, xAPIKey, payment string
	}
	var got capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = capture{
			path:    r.URL.Path,
			auth:    r.Header.Get("Authorization"),
			xAPIKey: r.Header.Get("x-api-key"),
			payment: r.Header.Get("PAYMENT-SIGNATURE"),
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/messages") {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4.5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	ac, err := NewAnthropic(WithAPIKey(testAccountKey), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	msg, err := ac.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-haiku-4.5"),
		MaxTokens: 16,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})
	if err != nil {
		t.Fatalf("Messages.New: %v", err)
	}
	if msg.ID != "msg_1" {
		t.Errorf("message id = %q, want msg_1", msg.ID)
	}
	if got.path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", got.path)
	}
	if got.xAPIKey != testAccountKey {
		t.Errorf("x-api-key = %q, want the account key", got.xAPIKey)
	}
	if got.payment != "" {
		t.Errorf("PAYMENT-SIGNATURE = %q, want none in account mode", got.payment)
	}

	oc, err := NewOpenAI(WithAPIKey(testAccountKey), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	resp, err := oc.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    openai.ChatModel("gpt-4o-mini"),
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Chat.Completions.New: %v", err)
	}
	if resp.ID != "chatcmpl-1" {
		t.Errorf("completion id = %q, want chatcmpl-1", resp.ID)
	}
	if got.path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", got.path)
	}
	if got.auth != "Bearer "+testAccountKey {
		t.Errorf("Authorization = %q, want a bearer account key", got.auth)
	}
	if got.payment != "" {
		t.Errorf("PAYMENT-SIGNATURE = %q, want none in account mode", got.payment)
	}
}

// The middleware itself is inert without a signer, so a nil signer can never
// panic if one is ever installed anyway.
func TestX402Middleware_NilSignerPassesThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PAYMENT-SIGNATURE") != "" {
			t.Error("nil signer must not produce a payment header")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	mw := x402Middleware(nil, map[string]string{"x-ignored": "1"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"a":1}`))
	resp, err := mw(req, http.DefaultClient.Do)
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
