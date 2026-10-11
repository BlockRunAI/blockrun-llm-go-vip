package vip

import (
	"crypto/ed25519"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mr-tron/base58"
)

// Solana poll-ownership proof (gateway src/lib/poll-ownership.ts).
//
// On Solana a signed x402 payment is a transaction pinned to a recent
// blockhash (~60-90s of life), so proving "I am the wallet that submitted this
// job" with a payment meant signing a fresh one on EVERY poll of a
// multi-minute video. The gateway instead accepts a detached ed25519 signature,
// by the submitting wallet, over a short text naming the job:
//
//	blockrun-poll:v1:<kind>:<job id>:<unix seconds>
//
// sent as x-poll-wallet / x-poll-timestamp / x-poll-signature (all base58
// except the decimal timestamp). It is not a transaction: nothing a
// facilitator can settle, so it never charges. The gateway accepts a
// timestamp within ±300s of its clock, so it is signed fresh per poll.

const (
	pollHeaderWallet    = "x-poll-wallet"
	pollHeaderTimestamp = "x-poll-timestamp"
	pollHeaderSignature = "x-poll-signature"

	pollKindVideo = "video"
)

// pollAuthMessage is the exact UTF-8 text the gateway verifies.
func pollAuthMessage(kind, jobID string, timestampS int64) string {
	return "blockrun-poll:v1:" + kind + ":" + jobID + ":" + strconv.FormatInt(timestampS, 10)
}

// solanaPollKey decodes the bs58 Solana wallet key the SDK already holds
// (64-byte keypair or 32-byte seed — the same two shapes blockrun-llm-go's
// signer accepts) into an ed25519 signing key.
func solanaPollKey(bs58Key string) (ed25519.PrivateKey, error) {
	decoded, err := base58.Decode(strings.TrimSpace(bs58Key))
	if err != nil {
		return nil, fmt.Errorf("vip: invalid base58 Solana key: %w", err)
	}
	switch len(decoded) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(decoded), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(decoded), nil
	default:
		return nil, fmt.Errorf("vip: invalid Solana private key: expected 32 or 64 bytes, got %d", len(decoded))
	}
}

// pollAuthHeaders signs the ownership proof for one poll of jobID at now.
func pollAuthHeaders(priv ed25519.PrivateKey, kind, jobID string, now time.Time) map[string]string {
	ts := now.Unix()
	sig := ed25519.Sign(priv, []byte(pollAuthMessage(kind, jobID, ts)))
	return map[string]string{
		pollHeaderWallet:    base58.Encode(priv.Public().(ed25519.PublicKey)),
		pollHeaderTimestamp: strconv.FormatInt(ts, 10),
		pollHeaderSignature: base58.Encode(sig),
	}
}

// pollJobID is the job id the gateway verifies the signature against: the
// poll_url's last path segment, percent-decoded (the route reads it with
// decodeURIComponent). The POST's `id` is the same value; it is the fallback
// when the poll URL cannot be parsed.
func pollJobID(pollURL, fallback string) string {
	u, err := url.Parse(pollURL)
	if err != nil {
		return fallback
	}
	p := strings.TrimRight(u.EscapedPath(), "/")
	i := strings.LastIndex(p, "/")
	if i < 0 || i == len(p)-1 {
		return fallback
	}
	seg, err := url.PathUnescape(p[i+1:])
	if err != nil || seg == "" {
		return fallback
	}
	return seg
}
