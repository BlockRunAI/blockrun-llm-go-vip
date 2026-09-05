package vip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	blockrun "github.com/BlockRunAI/blockrun-llm-go"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const accountTestKey = "brk_live_account_test"

func accountEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BLOCKRUN_API_KEY", accountTestKey)
	t.Setenv("BLOCKRUN_API_BASE_URL", "")
	t.Setenv("BLOCKRUN_WALLET_KEY", "")
	t.Setenv("BASE_CHAIN_WALLET_KEY", "")
	t.Setenv("SOLANA_WALLET_KEY", "")
	t.Setenv("BLOCKRUN_CHAIN", "")
}
func TestAccountAllConstructors(t *testing.T) {
	accountEnv(t)
	checks := []func() error{
		func() error { _, e := NewOpenAI(); return e }, func() error { _, e := NewAnthropic(); return e }, func() error { _, e := NewVideo(); return e }, func() error { _, e := NewImage(); return e }, func() error { _, e := NewSpeech(); return e }, func() error { _, e := NewMusic(); return e }, func() error { _, e := NewVoice(); return e }, func() error { _, e := NewPhone(); return e }, func() error { _, e := NewSearch(); return e }, func() error { _, e := NewExa(); return e }, func() error { _, e := NewRealFace(); return e }, func() error { _, e := NewPortrait(); return e }}
	for i, f := range checks {
		if e := f(); e != nil {
			t.Fatalf("constructor %d: %v", i, e)
		}
	}
	if _, e := os.Stat(filepath.Join(os.Getenv("HOME"), ".blockrun")); !os.IsNotExist(e) {
		t.Fatal("account constructors created wallet state")
	}
	cfg, sign, e := resolveSigner()
	if e != nil || sign != nil || !cfg.accountMode() {
		t.Fatal("account created signer")
	}
}
func TestAccountNativeChatAndStreaming(t *testing.T) {
	accountEnv(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+accountTestKey || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Payment-Signature") != "" {
			t.Error("wrong account headers")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"chat-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "messages") {
			fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"OK"}],"model":"m","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		fmt.Fprint(w, `{"id":"chat-1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
	}))
	defer s.Close()
	c, e := NewOpenAI(WithBaseURL(s.URL + "/v1/"))
	if e != nil {
		t.Fatal(e)
	}
	p := openai.ChatCompletionNewParams{Model: "m", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")}}
	r, e := c.Chat.Completions.New(context.Background(), p)
	if e != nil || r.Choices[0].Message.Content != "OK" {
		t.Fatalf("chat: %v", e)
	}
	stream := c.Chat.Completions.NewStreaming(context.Background(), p)
	defer stream.Close()
	text := ""
	for stream.Next() {
		for _, c := range stream.Current().Choices {
			text += c.Delta.Content
		}
	}
	if stream.Err() != nil || text != "OK" {
		t.Fatalf("SSE: %v %s", stream.Err(), text)
	}
	a, e := NewAnthropic(WithBaseURL(s.URL))
	if e != nil {
		t.Fatal(e)
	}
	msg, e := a.Messages.New(context.Background(), anthropic.MessageNewParams{Model: "m", MaxTokens: 8, Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))}})
	if e != nil || len(msg.Content) == 0 {
		t.Fatalf("anthropic: %v", e)
	}
}
func TestAccountVideoQuotaNoPaymentRetry(t *testing.T) {
	accountEnv(t)
	for _, status := range []int{401, 402, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "12")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"error":"%s"}`, accountTestKey)
			}))
			defer s.Close()
			v, e := NewVideo(WithBaseURL(s.URL))
			if e != nil {
				t.Fatal(e)
			}
			_, e = v.Submit(context.Background(), "cat", nil)
			var apiErr *blockrun.APIError
			if !errors.As(e, &apiErr) || apiErr.StatusCode != status || apiErr.RetryAfter != "12" || strings.Contains(e.Error(), accountTestKey) || calls.Load() != 1 {
				t.Fatalf("quota: %v calls %d", e, calls.Load())
			}
		})
	}
}
func TestAccountVideoPollingAndForeignOrigin(t *testing.T) {
	accountEnv(t)
	var posts, gets atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+accountTestKey {
			t.Error("missing auth")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			posts.Add(1)
			w.WriteHeader(202)
			fmt.Fprint(w, `{"id":"job","status":"queued","poll_url":"/api/v1/videos/generations/job?sig=test"}`)
			return
		}
		gets.Add(1)
		if r.URL.Path != "/v1/videos/generations/job" || r.URL.Query().Get("sig") != "test" {
			t.Error("poll URL changed")
		}
		fmt.Fprint(w, `{"status":"completed","data":[{"url":"https://cdn.example/video"}]}`)
	}))
	defer s.Close()
	v, e := NewVideo(WithBaseURL(s.URL))
	if e != nil {
		t.Fatal(e)
	}
	job, e := v.Submit(context.Background(), "cat", nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = v.Poll(context.Background(), job); e != nil {
		t.Fatal(e)
	}
	if !job.Done() || posts.Load() != 1 || gets.Load() != 1 {
		t.Fatal("unexpected polling")
	}
	job.Status = "queued"
	job.PollURL = "https://other.example/job"
	if e = v.Poll(context.Background(), job); e == nil {
		t.Fatal("foreign poll allowed")
	}
}
func TestAccountKeyPrecedenceAndChain(t *testing.T) {
	accountEnv(t)
	if _, _, e := resolveKey(WithAPIKey(accountTestKey), WithWalletKey(testWalletKey)); e == nil {
		t.Fatal("ambiguous auth allowed")
	}
	if _, _, e := resolveKey(WithAPIKey("")); e == nil {
		t.Fatal("empty key allowed")
	}
	c, _, e := resolveKey(WithWalletKey(testWalletKey))
	if e != nil || c.accountMode() || c.chain != "base" {
		t.Fatal("explicit wallet precedence")
	}
	if preferredChain("") != "solana" {
		t.Fatal("new user not Solana")
	}
	t.Setenv("BLOCKRUN_WALLET_KEY", testWalletKey)
	if preferredChain("") != "base" {
		t.Fatal("existing Base lost")
	}
	dir := filepath.Join(os.Getenv("HOME"), ".blockrun")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "payment-chain"), []byte("solana"), 0600)
	if preferredChain("") != "solana" {
		t.Fatal("saved chain lost")
	}
}

func TestAccountNative402AndRedirect(t *testing.T) {
	accountEnv(t)
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(402)
		fmt.Fprintf(w, `{"error":{"message":"credits exhausted %s","type":"insufficient_credits"}}`, accountTestKey)
	}))
	defer s.Close()
	c, err := NewOpenAI(WithBaseURL(s.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{Model: "m", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")}})
	var native *openai.Error
	if !errors.As(err, &native) || native.StatusCode != 402 || calls.Load() != 1 || strings.Contains(err.Error(), accountTestKey) {
		t.Fatalf("native 402: %v", err)
	}
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	cfg, _, err := resolveKey(WithBaseURL(redirect.URL))
	if err != nil {
		t.Fatal(err)
	}
	response, err := cfg.accountHTTPClient(0).Get(redirect.URL + "/v1/models")
	if response != nil {
		response.Body.Close()
	}
	if err == nil || targetCalls.Load() != 0 {
		t.Fatal("account redirect forwarded")
	}
}
