package vip

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	blockrun "github.com/BlockRunAI/blockrun-llm-go"
)

// cleanEnv isolates a test from the developer's own wallet and keys.
func cleanEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{
		"BLOCKRUN_API_KEY", "BLOCKRUN_API_KEY_URL", "BLOCKRUN_API_BASE_URL",
		"BLOCKRUN_WALLET_KEY", "BASE_CHAIN_WALLET_KEY", "SOLANA_WALLET_KEY", "BLOCKRUN_CHAIN",
	} {
		t.Setenv(k, "")
	}
}

// The pre-0.8 documented call — vip.WithAPIKey("blockrun") — is a placeholder
// upstream key, not an account credential, and must keep the wallet rail.
func TestPlaceholderAPIKeyKeepsWalletRail(t *testing.T) {
	cleanEnv(t)
	cfg, key, err := resolveKey(WithAPIKey(apiKeySentinel), WithWalletKey(testWalletKey))
	if err != nil {
		t.Fatalf("resolveKey: %v", err)
	}
	if cfg.accountMode() {
		t.Error("a non-account key must not select account mode")
	}
	if key != testWalletKey {
		t.Errorf("key = %q, want the wallet key", key)
	}
	if cfg.apiURL != blockrun.DefaultAPIURL {
		t.Errorf("apiURL = %q, want the Base gateway %q", cfg.apiURL, blockrun.DefaultAPIURL)
	}
	if cfg.apiKey != apiKeySentinel {
		t.Errorf("apiKey = %q, want it forwarded upstream unchanged", cfg.apiKey)
	}
	// And an explicit non-account key does not consult the environment.
	t.Setenv("BLOCKRUN_API_KEY", "brk_live_environment_key")
	if cfg, _, err = resolveKey(WithAPIKey("custom"), WithWalletKey(testWalletKey)); err != nil || cfg.accountMode() {
		t.Errorf("explicit upstream key lost to the env: account=%v err=%v", cfg.accountMode(), err)
	}
}

// Any brk_ key the main SDK accepts must work here: the two must not disagree
// about what a credential is.
func TestAccountKeyPrefixMatchesUpstream(t *testing.T) {
	cleanEnv(t)
	for _, key := range []string{"brk_live_abcdefghijklmnop", "brk_test_abcdefghijklmnop"} {
		if !blockrun.IsAPIKey(key) {
			t.Fatalf("upstream rejects %q — fixture is wrong", key)
		}
		cfg, _, err := resolveKey(WithAPIKey(key))
		if err != nil {
			t.Errorf("resolveKey(%q): %v", key, err)
			continue
		}
		if !cfg.accountMode() || cfg.apiURL != "https://api.blockrun.ai" {
			t.Errorf("%q: account=%v url=%q", key, cfg.accountMode(), cfg.apiURL)
		}
	}
	// A truncated secret is still refused where it is set, not at request time.
	if _, _, err := resolveKey(WithAPIKey(blockrun.APIKeyPrefix)); err == nil {
		t.Error("a bare brk_ prefix must be rejected, not demoted to the wallet rail")
	}
	if _, _, err := resolveKey(WithAPIKey(" brk_live_abcdefghijklmnop ")); err == nil {
		t.Error("a key with surrounding whitespace must be rejected")
	}
}

// A caller with nothing configured must be told about every way to pay, not
// just the rail the chain inference happened to land on.
func TestNoCredentialErrorNamesEveryRail(t *testing.T) {
	cleanEnv(t)
	_, _, err := resolveKey()
	if err == nil {
		t.Fatal("expected an error with no credential configured")
	}
	for _, want := range []string{"BLOCKRUN_API_KEY", "BLOCKRUN_WALLET_KEY", "SOLANA_WALLET_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
	// An explicitly chosen chain still gets its own specific message.
	if _, _, err = resolveKey(WithChain("solana")); err == nil || !strings.Contains(err.Error(), "Solana") {
		t.Errorf("explicit chain error = %v, want the Solana-specific message", err)
	}
}

// BLOCKRUN_API_KEY_URL is the main SDK's name for the account gateway root, so
// setting only it must move this package too.
func TestAccountBaseEnvNames(t *testing.T) {
	cleanEnv(t)
	t.Setenv("BLOCKRUN_API_KEY_URL", "https://staging.example/v1")
	cfg, _, err := resolveKey(WithAPIKey("brk_live_abcdefghijklmnop"))
	if err != nil {
		t.Fatalf("resolveKey: %v", err)
	}
	if cfg.apiURL != "https://staging.example" {
		t.Errorf("apiURL = %q, want the BLOCKRUN_API_KEY_URL root", cfg.apiURL)
	}
	// The name this package shipped first still works.
	t.Setenv("BLOCKRUN_API_KEY_URL", "")
	t.Setenv("BLOCKRUN_API_BASE_URL", "https://legacy.example")
	if cfg, _, err = resolveKey(WithAPIKey("brk_live_abcdefghijklmnop")); err != nil || cfg.apiURL != "https://legacy.example" {
		t.Errorf("apiURL = %q err=%v, want the legacy env honoured", cfg.apiURL, err)
	}
	// An explicit option still beats both.
	t.Setenv("BLOCKRUN_API_KEY_URL", "https://staging.example")
	if cfg, _, err = resolveKey(WithAPIKey("brk_live_abcdefghijklmnop"), WithBaseURL("https://explicit.example")); err != nil || cfg.apiURL != "https://explicit.example" {
		t.Errorf("apiURL = %q err=%v, want WithBaseURL to win", cfg.apiURL, err)
	}
	if _, e := os.Stat(filepath.Join(os.Getenv("HOME"), ".blockrun", ".session")); !os.IsNotExist(e) {
		t.Error("account resolution created wallet key material")
	}
}
