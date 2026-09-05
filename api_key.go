package vip

import (
	"bytes"
	"fmt"
	blockrun "github.com/BlockRunAI/blockrun-llm-go"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (c config) accountMode() bool { return c.apiKeySet }

func accountBase(raw string) (string, error) {
	if raw == "" {
		raw = os.Getenv("BLOCKRUN_API_BASE_URL")
	}
	if raw == "" {
		raw = "https://api.blockrun.ai"
	}
	raw = strings.TrimSuffix(strings.TrimRight(raw, "/"), "/v1")
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("vip: invalid account API URL")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && local)) {
		return "", fmt.Errorf("vip: account API requires HTTPS (localhost HTTP allowed)")
	}
	return raw, nil
}

func preferredChain(key string) string {
	if key != "" {
		if strings.HasPrefix(key, "0x") || len(key) == 64 {
			return chainBase
		}
		if _, err := blockrun.GetSolanaPublicKey(key); err == nil {
			return chainSolana
		}
		return chainBase
	}
	if chain := os.Getenv("BLOCKRUN_CHAIN"); chain != "" {
		return chain
	}
	home, _ := os.UserHomeDir()
	for _, name := range []string{"payment-chain", ".chain"} {
		if b, e := os.ReadFile(filepath.Join(home, ".blockrun", name)); e == nil && strings.TrimSpace(string(b)) != "" {
			return strings.TrimSpace(string(b))
		}
	}
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(home, ".blockrun", name)); return err == nil }
	base := os.Getenv("BLOCKRUN_WALLET_KEY") != "" || os.Getenv("BASE_CHAIN_WALLET_KEY") != "" || exists(".session")
	sol := os.Getenv("SOLANA_WALLET_KEY") != "" || exists(".solana-session")
	if base && !sol {
		return chainBase
	}
	return chainSolana
}

type accountTransport struct {
	key  string
	base *url.URL
	next http.RoundTripper
}

func (t *accountTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != t.base.Scheme || !strings.EqualFold(req.URL.Host, t.base.Host) || req.URL.User != nil {
		return nil, fmt.Errorf("vip: refusing account credentials outside configured API origin")
	}
	clone := req.Clone(req.Context())
	if (t.base.Path == "" || t.base.Path == "/") && strings.HasPrefix(clone.URL.Path, "/api/v1/") {
		clone.URL.Path = strings.TrimPrefix(clone.URL.Path, "/api")
		clone.URL.RawPath = ""
	}
	for h := range clone.Header {
		if strings.Contains(strings.ToLower(h), "payment") || strings.EqualFold(h, "authorization") || strings.EqualFold(h, "x-api-key") {
			clone.Header.Del(h)
		}
	}
	clone.Header.Set("Authorization", "Bearer "+t.key)
	resp, err := t.next.RoundTrip(clone)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		body, e := io.ReadAll(resp.Body)
		resp.Body.Close()
		if e != nil {
			return nil, e
		}
		body = bytes.ReplaceAll(body, []byte(t.key), []byte("[REDACTED]"))
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Del("Content-Length")
	}
	return resp, nil
}
func (c config) accountHTTPClient(timeout time.Duration) *http.Client {
	u, _ := url.Parse(c.apiURL)
	return &http.Client{Timeout: timeout, Transport: &accountTransport{key: c.apiKey, base: u, next: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("vip: redirects disabled for account credentials")
	}}
}
