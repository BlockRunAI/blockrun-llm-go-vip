package vip

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	blockrun "github.com/BlockRunAI/blockrun-llm-go"
)

// testWalletKey is a throwaway, well-known Hardhat account #0 private key.
// It only signs locally in this test; no funds, no network.
const testWalletKey = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

// newRequirement builds a base64-encoded x402 payment requirement matching what
// the BlockRun gateway returns in the `payment-required` header on a 402.
func newRequirement(resourceURL string) string {
	req := blockrun.PaymentRequirement{
		X402Version: 2,
		Accepts: []blockrun.PaymentOption{{
			Scheme:            "exact",
			Network:           "base",
			Amount:            "1000",
			Asset:             blockrun.USDCBase,
			PayTo:             "0x70997970C51812dc3A010C7d01b50e0d17dc79C8",
			MaxTimeoutSeconds: 60,
		}},
		Resource: blockrun.ResourceInfo{URL: resourceURL, Description: "test"},
	}
	raw, _ := json.Marshal(req)
	return base64.StdEncoding.EncodeToString(raw)
}

func TestX402Middleware_SignsAndRetries(t *testing.T) {
	var calls int32
	var sawSignature bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		body, _ := io.ReadAll(r.Body)

		if r.Header.Get("PAYMENT-SIGNATURE") == "" {
			// First, unpaid attempt -> 402 with the requirement header.
			w.Header().Set("payment-required", newRequirement("http://"+r.Host+r.URL.Path))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}

		// Paid retry -> verbatim 200. The body must match the original request
		// so we can prove it was replayed unchanged.
		sawSignature = true
		if string(body) != `{"hello":"world"}` {
			t.Errorf("retry body mutated: got %q", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		_ = n
	}))
	defer srv.Close()

	priv, err := blockrun.GetPrivateKeyFromHex(testWalletKey)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}

	mw := x402Middleware(func(paymentHeader, requestURL string) (string, error) {
		return signPayment(priv, paymentHeader, requestURL)
	}, nil)
	next := func(req *http.Request) (*http.Response, error) {
		return http.DefaultClient.Do(req)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages",
		bytes.NewReader([]byte(`{"hello":"world"}`)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := mw(req, next)
	if err != nil {
		t.Fatalf("middleware error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 after payment, got %d", resp.StatusCode)
	}
	out, _ := io.ReadAll(resp.Body)
	if string(out) != `{"ok":true}` {
		t.Fatalf("unexpected passthrough body: %q", string(out))
	}
	if !sawSignature {
		t.Fatal("retry did not carry PAYMENT-SIGNATURE")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("want exactly 2 upstream calls (402 then paid), got %d", got)
	}
}

func TestX402Middleware_PassthroughOn200(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"native":"verbatim"}`))
	}))
	defer srv.Close()

	priv, _ := blockrun.GetPrivateKeyFromHex(testWalletKey)
	mw := x402Middleware(func(paymentHeader, requestURL string) (string, error) {
		return signPayment(priv, paymentHeader, requestURL)
	}, nil)
	next := func(req *http.Request) (*http.Response, error) { return http.DefaultClient.Do(req) }

	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte(`{}`)))
	resp, err := mw(req, next)
	if err != nil {
		t.Fatalf("middleware error: %v", err)
	}
	defer resp.Body.Close()

	out, _ := io.ReadAll(resp.Body)
	if string(out) != `{"native":"verbatim"}` {
		t.Fatalf("200 response was reshaped: %q", string(out))
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("want 1 call (no payment needed), got %d", got)
	}
}
