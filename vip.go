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

// DefaultAccountAPIURL is the BlockRun account gateway — the endpoint that
// authenticates with an account API key (brk_live_…) and bills the account's
// prepaid credit instead of settling USDC per call. It serves the chat routes
// only; media and search stay on the wallet gateway.
const DefaultAccountAPIURL = "https://api.blockrun.ai"

// accountKeyPrefix marks a BlockRun account API key ("brk_live_…"). Any other
// value passed to WithAPIKey is treated as a plain upstream-key override and
// leaves the x402 wallet path untouched.
const accountKeyPrefix = "brk_"

// isAccountKey reports whether key is a BlockRun account API key.
func isAccountKey(key string) bool {
	return strings.HasPrefix(key, accountKeyPrefix)
}

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
	privHex      string // optional explicit wallet key (Base hex or Solana bs58); empty means auto-load
	chain        string // "base" (default) or "solana"
	chainSet     bool   // true when WithChain was passed explicitly
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

// WithChain selects the payment chain: "base" (default, USDC on Base via EIP-712)
// or "solana" (USDC on Solana via sol.blockrun.ai and the x402 SVM exact scheme).
// It also switches the default gateway base URL to match the chain.
func WithChain(chain string) Option {
	return func(c *config) {
		c.chain = chain
		c.chainSet = true
	}
}

// WithSolanaRPCURL overrides the Solana JSON-RPC endpoint used while signing (to
// fetch the recent blockhash and mint info). Defaults to BlockRun's free proxy
// (SOLANA_RPC_URL env, then https://sol.blockrun.ai/api/v1/solana/rpc).
func WithSolanaRPCURL(url string) Option {
	return func(c *config) { c.solanaRPCURL = url }
}

// WithAPIKey sets the API key sent upstream.
//
// A BlockRun account API key ("brk_live_…") switches the client into account
// mode: requests go to DefaultAccountAPIURL, the account's prepaid credit pays,
// and no wallet is loaded or signed with. BLOCKRUN_API_KEY selects the same
// mode for NewAnthropic / NewOpenAI when no key is passed explicitly.
//
// Any other value is just a placeholder override for the upstream key and
// changes nothing about payment — the x402 wallet path still authorizes the
// call. Rarely needed there.
func WithAPIKey(key string) Option {
	return func(c *config) { c.apiKey = key }
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

// usesAccountKey reports whether the resolved config pays from BlockRun account
// credit (API key) rather than by signing an x402 payment from a wallet.
func (c config) usesAccountKey() bool { return isAccountKey(c.apiKey) }

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

// resolveConfig applies the options and resolves the payment mode and gateway
// URL. It never touches the wallet — the caller decides whether one is needed.
//
// Two modes exist. The default is x402: a wallet signs a USDC payment per call
// against blockrun.ai (Base) or sol.blockrun.ai (Solana). The second is account
// mode, selected by a BlockRun account API key (brk_live_…): the account's
// prepaid credit pays, requests go to DefaultAccountAPIURL, and no wallet is
// involved at all.
//
// acceptEnvKey allows BLOCKRUN_API_KEY to select account mode. The chat
// constructors pass true; the wallet-only clients (media, video, search) pass
// false, so a key exported in the environment for an unrelated client cannot
// silently break a media call that has always paid from a wallet.
func resolveConfig(acceptEnvKey bool, opts ...Option) (config, error) {
	cfg := config{
		apiKey: apiKeySentinel,
		chain:  chainBase,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if acceptEnvKey && !cfg.usesAccountKey() && (cfg.apiKey == "" || cfg.apiKey == apiKeySentinel) {
		if env := strings.TrimSpace(os.Getenv("BLOCKRUN_API_KEY")); isAccountKey(env) {
			cfg.apiKey = env
		}
	}
	if cfg.apiKey == "" {
		cfg.apiKey = apiKeySentinel
	}

	if cfg.usesAccountKey() {
		// These options describe an on-chain payment this mode never makes.
		// Failing loudly beats silently ignoring a caller who believes they
		// are paying from the wallet they named.
		if cfg.chainSet {
			return cfg, fmt.Errorf("vip: WithChain is meaningless with a BlockRun account API key (%s…) — account credit pays and nothing settles on-chain; drop one of the two", accountKeyPrefix)
		}
		if cfg.privHex != "" {
			return cfg, fmt.Errorf("vip: WithWalletKey is meaningless with a BlockRun account API key (%s…) — account credit pays and the wallet is never signed with; drop one of the two", accountKeyPrefix)
		}
		if cfg.apiURL == "" {
			cfg.apiURL = DefaultAccountAPIURL
		}
		return cfg, nil
	}

	if cfg.chain == "" {
		cfg.chain = chainBase
	}
	if cfg.chain != chainBase && cfg.chain != chainSolana {
		return cfg, fmt.Errorf("vip: unknown chain %q (want %q or %q)", cfg.chain, chainBase, chainSolana)
	}
	if cfg.apiURL == "" {
		if cfg.isSolana() {
			cfg.apiURL = blockrun.DefaultSolanaAPIURL
		} else {
			cfg.apiURL = blockrun.DefaultAPIURL
		}
	}
	return cfg, nil
}

// loadWalletKey resolves the x402 wallet key for the resolved chain (Base hex
// or Solana bs58) and fills in the Solana-only routing fields.
func loadWalletKey(cfg config) (config, string, error) {
	if cfg.isSolana() {
		key := cfg.privHex
		if key == "" {
			var err error
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

	key := cfg.privHex
	if key == "" {
		var err error
		key, err = blockrun.LoadWallet()
		if err != nil {
			return cfg, "", fmt.Errorf("vip: no wallet key (set BLOCKRUN_WALLET_KEY or ~/.blockrun/.session, or pass WithWalletKey): %w", err)
		}
	}
	return cfg, key, nil
}

// resolveKey applies options and resolves the wallet key for the selected chain
// (Base hex or Solana bs58). The media clients reuse blockrun-llm-go's clients,
// which take the key directly — they always settle on-chain, so an account API
// key is rejected here rather than sent to a gateway that would 402 it.
func resolveKey(opts ...Option) (cfg config, key string, err error) {
	cfg, err = resolveConfig(false, opts...)
	if err != nil {
		return cfg, "", err
	}
	if cfg.usesAccountKey() {
		return cfg, "", fmt.Errorf("vip: a BlockRun account API key (%s…) pays for the chat routes only — image, video, speech, music, RealFace and search settle on-chain, so this client needs a wallet; drop WithAPIKey here", accountKeyPrefix)
	}
	return loadWalletKey(cfg)
}

// resolveSigner applies options and returns a chain-aware x402 payment signer
// for the passthrough middleware and native Video client. Base signs EIP-712
// (secp256k1); Solana signs the SVM exact scheme (ed25519).
//
// In account mode (BlockRun API key) the returned signer is nil: the account's
// credit pays, so there is no payment to sign and no wallet to load.
func resolveSigner(opts ...Option) (cfg config, sign paymentSigner, err error) {
	cfg, err = resolveConfig(true, opts...)
	if err != nil {
		return cfg, nil, err
	}
	if cfg.usesAccountKey() {
		return cfg, nil, nil
	}

	cfg, key, err := loadWalletKey(cfg)
	if err != nil {
		return cfg, nil, err
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
