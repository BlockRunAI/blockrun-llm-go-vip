package vip

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	blockrun "github.com/BlockRunAI/blockrun-llm-go"
)

// The facilitator routing headers must ride BOTH legs of the x402 negotiation:
// the gateway derives the 402's feePayer from them, and the signed transaction
// only settles through the facilitator that issued that feePayer — a retry
// without them would be verified against the wrong facilitator and rejected.
func TestX402Middleware_FacilitatorHeadersOnBothLegs(t *testing.T) {
	var mu sync.Mutex
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		if r.Header.Get("PAYMENT-SIGNATURE") == "" {
			w.Header().Set("payment-required", newRequirement("http://x/y"))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	priv, err := blockrun.GetPrivateKeyFromHex(testWalletKey)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	headers := map[string]string{
		"x-blockrun-facilitator": "figment",
		"x-payer-wallet":         "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin",
	}
	mw := x402Middleware(func(paymentHeader, requestURL string) (string, error) {
		return signPayment(priv, paymentHeader, requestURL)
	}, headers)
	next := func(req *http.Request) (*http.Response, error) { return http.DefaultClient.Do(req) }

	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte(`{}`)))
	resp, err := mw(req, next)
	if err != nil {
		t.Fatalf("middleware error: %v", err)
	}
	defer resp.Body.Close()

	if len(seen) != 2 {
		t.Fatalf("want 2 upstream calls (402 then paid), got %d", len(seen))
	}
	for i, h := range seen {
		for k, v := range headers {
			if got := h.Get(k); got != v {
				t.Errorf("leg %d: header %s = %q, want %q", i+1, k, got, v)
			}
		}
	}
}

func TestPaymentRoutingHeaders_DefaultFigmentOnSolana(t *testing.T) {
	t.Setenv("BLOCKRUN_FACILITATOR", "")
	cfg, _, err := resolveKey(WithChain("solana"), WithWalletKey(testSolanaKey))
	if err != nil {
		t.Fatalf("resolveKey: %v", err)
	}
	h := cfg.paymentRoutingHeaders()
	if h == nil {
		t.Fatal("solana VIP clients should send facilitator routing headers by default")
	}
	if h["x-blockrun-facilitator"] != "figment" {
		t.Errorf("facilitator = %q, want figment", h["x-blockrun-facilitator"])
	}
	wantAddr, err := blockrun.GetSolanaPublicKey(testSolanaKey)
	if err != nil {
		t.Fatalf("derive address: %v", err)
	}
	if h["x-payer-wallet"] != wantAddr {
		t.Errorf("x-payer-wallet = %q, want %q", h["x-payer-wallet"], wantAddr)
	}
}

func TestPaymentRoutingHeaders_OptOutAndBase(t *testing.T) {
	// Explicit opt-out → wire identical to older releases.
	cfg, _, err := resolveKey(WithChain("solana"), WithWalletKey(testSolanaKey), WithFacilitator("payai"))
	if err != nil {
		t.Fatalf("resolveKey: %v", err)
	}
	if h := cfg.paymentRoutingHeaders(); h != nil {
		t.Errorf("payai opt-out should send no routing headers, got %v", h)
	}

	// Env override behaves like the option when none is passed.
	t.Setenv("BLOCKRUN_FACILITATOR", "payai")
	cfg, _, err = resolveKey(WithChain("solana"), WithWalletKey(testSolanaKey))
	if err != nil {
		t.Fatalf("resolveKey: %v", err)
	}
	if h := cfg.paymentRoutingHeaders(); h != nil {
		t.Errorf("BLOCKRUN_FACILITATOR=payai should send no routing headers, got %v", h)
	}

	// Base chain never sends them.
	t.Setenv("BLOCKRUN_FACILITATOR", "")
	cfg, _, err = resolveKey(WithWalletKey(testWalletKey))
	if err != nil {
		t.Fatalf("resolveKey: %v", err)
	}
	if h := cfg.paymentRoutingHeaders(); h != nil {
		t.Errorf("base chain should send no routing headers, got %v", h)
	}
}
