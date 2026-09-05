// Package vip is a genuine native passthrough for Anthropic and OpenAI through
// the BlockRun gateway, paid per call in USDC (x402) on Base.
//
// Unlike a re-implemented client, the constructors here return the *official*
// anthropic-sdk-go and openai-go client types. They only swap the transport (to
// add x402 payment) and the base URL — the gateway returns the upstream
// provider's response verbatim, so the official SDK parses the real signals:
// Claude thinking-block signatures and native content blocks, GPT
// system_fingerprint and token-detail usage, native streaming, and so on. A
// relay detector sees a direct upstream call.
//
// Wallet keys are used ONLY for local EIP-712 signing and never leave the
// machine. Payment runs on Base (USDC).
package vip

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	blockrun "github.com/BlockRunAI/blockrun-llm-go"
)

// apiKeySentinel is sent as the upstream API key. The gateway authorizes by
// x402 payment, not by this value, but the official SDKs require a non-empty
// key, so we supply a placeholder rather than leak any real provider key.
const apiKeySentinel = "blockrun"

// DefaultChatTimeout is the default per-request timeout applied to the
// passthrough chat clients (NewOpenAI / NewAnthropic).
//
// Reasoning models (opus-4.8, deepseek-v4-pro) routinely think for 200-300s+,
// so the official SDKs' lower default cut off non-streaming calls. Override via
// the BLOCKRUN_CHAT_TIMEOUT env var (integer seconds). Mirrors blockrun-llm 1.4.7.
const DefaultChatTimeout = 600 * time.Second

// defaultChatTimeout returns the default chat request timeout. It reads the
// BLOCKRUN_CHAT_TIMEOUT environment variable (integer seconds) and falls back
// to DefaultChatTimeout (600s) when unset or invalid. A per-call or per-client
// override (option.WithRequestTimeout on the official SDK) still wins, since
// this is applied as the first option and later options take precedence.
func defaultChatTimeout() time.Duration {
	if v := os.Getenv("BLOCKRUN_CHAT_TIMEOUT"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return DefaultChatTimeout
}

// chainBase and chainSolana are the supported payment chains.
const (
	chainBase   = "base"
	chainSolana = "solana"
)

// config holds resolved client settings shared by the Anthropic and OpenAI
// constructors.
type config struct {
	apiURL       string
	apiKey       string
	apiKeySet    bool
	privHex      string // optional explicit wallet key (Base hex or Solana bs58); empty means auto-load
	chain        string // "base" (default) or "solana"
	solanaRPCURL string // optional Solana RPC override (blockhash + mint info)
	facilitator  string // x402 facilitator preference ("figment" default on Solana; "payai" opts out)
	solanaAddr   string // derived bs58 wallet address (Solana only), for x-payer-wallet
}

// Option customises a VIP client.
type Option func(*config)

// WithBaseURL overrides the BlockRun gateway base URL
// (default: https://blockrun.ai/api). The BLOCKRUN_API_URL env var is honoured
// by the underlying loader when no explicit URL is set.
func WithBaseURL(url string) Option {
	return func(c *config) { c.apiURL = url }
}

// WithWalletKey sets the wallet private key explicitly (Base hex, or bs58 when
// WithChain("solana") is set). When unset, the key is loaded per chain: Base from
// BLOCKRUN_WALLET_KEY / BASE_CHAIN_WALLET_KEY / ~/.blockrun/.session; Solana from
// SOLANA_WALLET_KEY / ~/.*/solana-wallet.json / ~/.blockrun/.solana-session.
func WithWalletKey(key string) Option {
	return func(c *config) { c.privHex = key }
}

// WithChain explicitly selects the payment chain: "base" (USDC via EIP-712)
// or "solana" (USDC on Solana via sol.blockrun.ai and the x402 SVM exact scheme).
// It also switches the default gateway base URL to match the chain.
func WithChain(chain string) Option {
	return func(c *config) { c.chain = chain }
}

// WithSolanaRPCURL overrides the Solana JSON-RPC endpoint used while signing (to
// fetch the recent blockhash and mint info). Defaults to BlockRun's free proxy
// (SOLANA_RPC_URL env, then https://sol.blockrun.ai/api/v1/solana/rpc).
func WithSolanaRPCURL(url string) Option {
	return func(c *config) { c.solanaRPCURL = url }
}

// WithAPIKey selects account billing without a wallet. Empty/malformed keys
// are rejected. When omitted, BLOCKRUN_API_KEY selects account mode.
func WithAPIKey(key string) Option {
	return func(c *config) { c.apiKey = key; c.apiKeySet = true }
}

// WithFacilitator sets the x402 facilitator preference sent to the gateway
// (Solana only). VIP clients default to "figment" — the gateway routes
// allowlisted enterprise wallets through the Figment facilitator and silently
// keeps everyone else on PayAI, so the default is safe for non-allowlisted
// wallets. Pass "payai" to opt out entirely (no routing headers sent). The
// BLOCKRUN_FACILITATOR env var overrides the default when no explicit option
// is given.
func WithFacilitator(name string) Option {
	return func(c *config) { c.facilitator = name }
}

// isSolana reports whether the resolved config pays on Solana.
func (c config) isSolana() bool { return c.chain == chainSolana }

// paymentRoutingHeaders returns the facilitator-routing headers attached to
// every gateway request (Solana + non-payai preference only; nil otherwise, so
// the wire is byte-identical to older releases for Base and opted-out clients).
// The gateway treats these as a ROUTING HINT: non-allowlisted wallets fall
// back to PayAI silently, and the money gate is the gateway's verify-time
// check against the facilitator-established payer, not these headers.
func (c config) paymentRoutingHeaders() map[string]string {
	if !c.isSolana() || c.facilitator == "" || c.facilitator == "payai" || c.solanaAddr == "" {
		return nil
	}
	return map[string]string{
		"x-blockrun-facilitator": c.facilitator,
		"x-payer-wallet":         c.solanaAddr,
	}
}

// resolveKey applies options and resolves the wallet key for the selected chain
// (Base hex or Solana bs58). The media clients reuse blockrun-llm-go's clients,
// which take the key directly.
func resolveKey(opts ...Option) (cfg config, key string, err error) {
	cfg = config{
		apiKey: apiKeySentinel,
		chain:  "",
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.apiKeySet && cfg.privHex != "" {
		return cfg, "", fmt.Errorf("vip: pass either WithAPIKey or WithWalletKey, not both")
	}
	if !cfg.apiKeySet && cfg.privHex == "" {
		if key, exists := os.LookupEnv("BLOCKRUN_API_KEY"); exists {
			cfg.apiKey = key
			cfg.apiKeySet = true
		}
	}
	if cfg.apiKeySet {
		if !strings.HasPrefix(cfg.apiKey, "brk_live_") || len(cfg.apiKey) <= 9 || strings.ContainsAny(cfg.apiKey, " \n\r\t") {
			return cfg, "", fmt.Errorf("vip: invalid BlockRun API key; create one at https://user.blockrun.ai/dashboard/keys")
		}
		cfg.apiURL, err = accountBase(cfg.apiURL)
		cfg.chain = "account"
		return cfg, "", err
	}
	if cfg.chain == "" {
		cfg.chain = preferredChain(cfg.privHex)
	}
	if cfg.chain != chainBase && cfg.chain != chainSolana {
		return cfg, "", fmt.Errorf("vip: unknown chain %q (want %q or %q)", cfg.chain, chainBase, chainSolana)
	}

	if cfg.isSolana() {
		if cfg.apiURL == "" {
			cfg.apiURL = blockrun.DefaultSolanaAPIURL
		}
		key = cfg.privHex
		if key == "" {
			key, err = blockrun.LoadSolanaWallet()
			if err != nil {
				return cfg, "", fmt.Errorf("vip: failed to load Solana wallet: %w", err)
			}
		}
		if key == "" {
			return cfg, "", fmt.Errorf("vip: no Solana wallet key (set SOLANA_WALLET_KEY or ~/.blockrun/.solana-session, or pass WithWalletKey)")
		}
		// Facilitator preference (Solana only): explicit option > env > the
		// "figment" default. The gateway whitelist decides what actually
		// happens, so defaulting on is a no-op for non-enterprise wallets.
		if cfg.facilitator == "" {
			cfg.facilitator = os.Getenv("BLOCKRUN_FACILITATOR")
		}
		if cfg.facilitator == "" {
			cfg.facilitator = "figment"
		}
		// The wallet address rides along as x-payer-wallet so the gateway can
		// whitelist-check BEFORE the 402 challenge (the challenge's feePayer
		// differs per facilitator, so the split must happen pre-payment).
		if addr, addrErr := blockrun.GetSolanaPublicKey(key); addrErr == nil {
			cfg.solanaAddr = addr
		}
		return cfg, key, nil
	}

	if cfg.apiURL == "" {
		cfg.apiURL = blockrun.DefaultAPIURL
	}
	key = cfg.privHex
	if key == "" {
		key, err = blockrun.LoadWallet()
		if err != nil {
			return cfg, "", fmt.Errorf("vip: no wallet key (set BLOCKRUN_WALLET_KEY or ~/.blockrun/.session, or pass WithWalletKey): %w", err)
		}
	}
	return cfg, key, nil
}

// resolveSigner applies options, resolves the wallet key, and returns a
// chain-aware x402 payment signer for the passthrough middleware and native
// Video client. Base signs EIP-712 (secp256k1); Solana signs the SVM exact scheme
// (ed25519).
func resolveSigner(opts ...Option) (cfg config, sign paymentSigner, err error) {
	cfg, key, err := resolveKey(opts...)
	if err != nil {
		return cfg, nil, err
	}

	if cfg.accountMode() {
		return cfg, nil, nil
	}
	if cfg.isSolana() {
		rpc := cfg.solanaRPCURL
		return cfg, func(paymentHeader, requestURL string) (string, error) {
			return signSolanaPayment(key, rpc, paymentHeader, requestURL)
		}, nil
	}

	priv, err := blockrun.GetPrivateKeyFromHex(key)
	if err != nil {
		return cfg, nil, fmt.Errorf("vip: invalid wallet key: %w", err)
	}
	return cfg, func(paymentHeader, requestURL string) (string, error) {
		return signPayment(priv, paymentHeader, requestURL)
	}, nil
}
