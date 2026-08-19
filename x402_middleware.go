package vip

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	blockrun "github.com/BlockRunAI/blockrun-llm-go"
)

// transportMiddleware is the shared underlying type of the Anthropic and OpenAI
// SDK option.Middleware aliases. Both are defined as
//
//	type Middleware = func(*http.Request, MiddlewareNext) (*http.Response, error)
//
// over the same MiddlewareNext alias, so a single implementation can be passed
// to option.WithMiddleware on either SDK without conversion.
type transportMiddleware = func(*http.Request, func(*http.Request) (*http.Response, error)) (*http.Response, error)

// paymentSigner signs an x402 payment requirement for the resolved chain and
// returns the base64 payment payload for the PAYMENT-SIGNATURE header.
type paymentSigner func(paymentHeader, requestURL string) (string, error)

// staleBlockhashRetryBackoffs bound the recovery loop while allowing a cached
// gateway challenge to roll over. A stale signed transaction must never be
// replayed: every recovery attempt below obtains a fresh 402 and re-signs it.
var staleBlockhashRetryBackoffs = []time.Duration{500 * time.Millisecond, 2 * time.Second}

// unavailableBackoffs bound recovery from a gateway whose payment VERIFIER is
// down (503 PAYMENT_VERIFICATION_UNAVAILABLE). Fallbacks only — the gateway
// sends a jittered Retry-After and that value wins when it is sane.
//
// Longer than the stale-blockhash backoffs on purpose. A stale blockhash is
// per-request and clears on the next block; a fail-closed screen is a global
// dependency outage that every caller hits in the same second, so retrying it
// fast just adds load to something already down. Two attempts, because past
// that the caller's own error handling should decide — an outage that outlives
// ~30s is not one an SDK should hide behind a long silent block.
var unavailableBackoffs = []time.Duration{2 * time.Second, 8 * time.Second}

// maxRetryAfterDelay caps a server-supplied Retry-After. The header is a hint
// from a host that is currently malfunctioning; honouring an arbitrary value
// would let it park a caller's request (and goroutine) for as long as it likes.
const maxRetryAfterDelay = 30 * time.Second

// x402Middleware builds the native-passthrough payment middleware.
//
// It performs the x402 negotiation transparently: the first request goes out
// unpaid, and on a 402 the requirement is parsed, a USDC authorization is signed
// locally (EIP-712 on Base, SVM exact scheme on Solana), and the original request
// is replayed verbatim with the PAYMENT-SIGNATURE header. Everything else flows
// through untouched, so the official SDK parses the upstream provider's response
// byte-for-byte.
//
// One exception to single-shot payment: if the paid leg comes back 402 with an
// explicit stale-blockhash rejection (see isStaleBlockhashResponse), the signed
// transaction is dead as signed and is discarded — never replayed. The loop then
// re-runs the whole negotiation from a fresh unpaid challenge, bounded by
// staleBlockhashRetryBackoffs. Every other 402 is terminal and passes through.
// A caller can therefore sign up to len(staleBlockhashRetryBackoffs)+1 distinct
// payments for one request, each against a server-supplied quote.
//
// The unpaid leg has one recovery of its own: if the gateway cannot build a
// challenge because its facilitator is unreachable (see
// isChallengeUnavailableResponse), the quote is retried on the same schedule as
// a verifier outage. Nothing is signed at that point, so it is the one recovery
// here with no double-charge exposure to reason about.
//
// SECURITY: the wallet key is used ONLY for local signing. The key never leaves
// the machine; only the signature is transmitted.
//
// extraHeaders (nil OK) are set on the outgoing request before the first send —
// the 402 retry is a Clone of that request, so they ride on both legs. Used for
// facilitator routing (x-blockrun-facilitator / x-payer-wallet), which must be
// identical on the challenge and the paid retry: the gateway derives the 402's
// feePayer from them, and the signed transaction only settles through the
// facilitator that issued that feePayer.
func x402Middleware(sign paymentSigner, extraHeaders map[string]string) transportMiddleware {
	return x402MiddlewareWithBackoffs(sign, extraHeaders, staleBlockhashRetryBackoffs)
}

// x402MiddlewareWithBackoffs exists so the retry state machine can be tested
// deterministically without sleeping. Production callers use x402Middleware.
// Verifier-outage recovery keeps its production schedule here; tests that need
// it fast use x402MiddlewareWithAllBackoffs.
func x402MiddlewareWithBackoffs(sign paymentSigner, extraHeaders map[string]string, staleBackoffs []time.Duration) transportMiddleware {
	return x402MiddlewareWithAllBackoffs(sign, extraHeaders, staleBackoffs, unavailableBackoffs)
}

// x402MiddlewareWithAllBackoffs is the full test seam: both recovery loops'
// schedules are injectable.
func x402MiddlewareWithAllBackoffs(
	sign paymentSigner,
	extraHeaders map[string]string,
	staleBackoffs []time.Duration,
	unavailableBackoffs []time.Duration,
) transportMiddleware {
	return func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
		for k, v := range extraHeaders {
			if req.Header.Get(k) == "" {
				req.Header.Set(k, v)
			}
		}

		// Buffer the body so the 402 retry can replay it verbatim.
		var body []byte
		if req.Body != nil {
			b, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, fmt.Errorf("x402: read request body: %w", err)
			}
			_ = req.Body.Close()
			body = b
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
		}

		newAttempt := func(signature string) *http.Request {
			attempt := req.Clone(req.Context())
			if body != nil {
				attempt.Body = io.NopCloser(bytes.NewReader(body))
				attempt.ContentLength = int64(len(body))
				attempt.GetBody = func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(body)), nil
				}
			}
			attempt.Header.Del("PAYMENT-SIGNATURE")
			if signature != "" {
				attempt.Header.Set("PAYMENT-SIGNATURE", signature)
			}
			return attempt
		}

		for staleRetries, unavailableRetries := 0, 0; ; {
			// Always begin a payment attempt without a signature. This obtains a
			// fresh challenge instead of replaying a transaction whose blockhash
			// the verifier has already rejected.
			resp, err := next(newAttempt(""))
			if err != nil {
				return resp, err
			}
			if resp.StatusCode != http.StatusPaymentRequired {
				// The gateway could not even QUOTE the call, because the
				// facilitator it must consult to build the challenge was
				// unreachable. Nothing has been signed at this point, so unlike
				// every other recovery in this loop, this one carries no
				// double-charge exposure at all.
				if isChallengeUnavailableResponse(resp) && unavailableRetries < len(unavailableBackoffs) {
					delay := retryAfterDelay(resp, unavailableBackoffs[unavailableRetries])
					_ = resp.Body.Close()
					if err := waitForX402Retry(req, delay); err != nil {
						return nil, err
					}
					unavailableRetries++
					continue
				}
				// Native passthrough: hand the upstream response back untouched.
				return resp, nil
			}

			paymentHeader := readPaymentRequirement(resp)
			_ = resp.Body.Close()
			if paymentHeader == "" {
				return resp, &blockrun.PaymentError{Message: "402 response but no payment requirements found"}
			}

			signature, err := sign(paymentHeader, req.URL.String())
			if err != nil {
				return resp, err
			}

			paidResp, err := next(newAttempt(signature))
			if err != nil {
				return paidResp, err
			}

			// The gateway could not RUN its verifier — its risk screen was down,
			// so the signed payment was never judged, let alone charged. Observed
			// in blockrun-sol production on 2026-08-13 02:00-03:59Z: ~600 requests
			// from one wallet, which settled 565 times in the hour before and 359
			// in the hour after. Without this the caller gets a bare 503 and the
			// gateway's Retry-After is read by nobody.
			if isVerificationUnavailableResponse(paidResp) && unavailableRetries < len(unavailableBackoffs) {
				delay := retryAfterDelay(paidResp, unavailableBackoffs[unavailableRetries])
				_ = paidResp.Body.Close()
				if err := waitForX402Retry(req, delay); err != nil {
					return nil, err
				}
				unavailableRetries++
				continue
			}

			if paidResp.StatusCode != http.StatusPaymentRequired ||
				!isStaleBlockhashResponse(paidResp) ||
				staleRetries >= len(staleBackoffs) {
				return paidResp, nil
			}

			_ = paidResp.Body.Close()
			if err := waitForX402Retry(req, staleBackoffs[staleRetries]); err != nil {
				return nil, err
			}
			staleRetries++
		}
	}
}

// maxStaleClassifyBytes caps how much of a rejected 402 is buffered to classify
// it. A payment rejection is a few hundred bytes; a larger body is not one, and
// reading it unbounded would let a gateway that already holds a signed payment
// exhaust client memory. Past the cap the response is treated as terminal.
const maxStaleClassifyBytes = 64 << 10

// isStaleBlockhashResponse recognizes only explicit, machine-readable stale
// blockhash failures. transaction_simulation_failed alone is intentionally not
// enough: it also covers terminal account, balance, and signature failures.
// The response body is restored so an unhandled 402 remains byte-for-byte
// available to the official SDK and caller.
//
// Three body shapes carry the signal, because the gateway speaks two dialects
// and changed one of them:
//
//	OpenAI-shaped routes   {"code":"PAYMENT_INVALID","reason":"expired_signature"}
//	Anthropic /v1/messages {"error":{"message":"Payment verification failed: expired_signature"}}
//	pre-2026-08 gateways   {"invalidMessage":"BlockhashNotFound"}
//
// `reason` is the current, documented client-facing vocabulary; the gateway
// stopped echoing the facilitator's verbatim invalidMessage (blockrun-sol
// c2a17bf) because it named internal simulation causes to anyone posting a
// bogus header. Matching only the old field would make this recovery inert.
// insufficient_funds must never match: no re-sign can fund a wallet.
func isStaleBlockhashResponse(resp *http.Response) bool {
	if resp == nil || resp.Body == nil {
		return false
	}
	original := resp.Body
	body, err := io.ReadAll(io.LimitReader(original, maxStaleClassifyBytes+1))
	// Restore the body: the bytes consumed here, then whatever remains unread.
	resp.Body = &prefixedBody{r: io.MultiReader(bytes.NewReader(body), original), c: original}
	if err != nil || len(body) > maxStaleClassifyBytes {
		return false
	}

	var failure struct {
		Code           string `json:"code"`
		Reason         string `json:"reason"`
		InvalidMessage string `json:"invalidMessage"`
		// RawMessage because `error` is an object on the Anthropic routes and a
		// plain string on the OpenAI ones — a typed field would fail the whole
		// decode on the other dialect.
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &failure) != nil {
		return false
	}
	var errText string
	var nested struct {
		Message string `json:"message"`
	}
	if len(failure.Error) > 0 {
		if json.Unmarshal(failure.Error, &errText) != nil {
			_ = json.Unmarshal(failure.Error, &nested)
		}
	}

	code := normalizePaymentSignal(failure.Code)
	reason := normalizePaymentSignal(failure.Reason)
	detail := normalizePaymentSignal(failure.InvalidMessage)
	errLabel := normalizePaymentSignal(errText)
	message := normalizePaymentSignal(nested.Message)

	// PHASE GATE. A settlement-phase rejection is never retried, whatever it
	// says. Settle has already broadcast a transaction; if that transaction
	// actually landed and only the confirmation was lost, re-signing pays a
	// second time for one request. Verify is the safe phase: it runs before any
	// broadcast, and the route returns 402 without ever reaching settle.
	//
	// The phase is not always in `code` — only /v1/chat/completions sets
	// SETTLEMENT_FAILED. The exa/audio/surf/phone/rpc/pm routes send a bare
	// {"error":"Payment settlement failed","reason":...}, so the error label
	// carries it there.
	if strings.Contains(code, "settlementfailed") ||
		strings.Contains(errLabel, "settlementfailed") ||
		strings.Contains(message, "settlementfailed") {
		return false
	}
	verifyPhase := code == "paymentinvalid" ||
		strings.Contains(errLabel, "verificationfailed") ||
		strings.Contains(message, "verificationfailed")

	// An explicit stale-blockhash signal names the cause outright and stands on
	// its own. expired_signature is a broader classification (it also covers a
	// plainly expired authorization), so it is honoured only on a verify-phase
	// body, where re-signing cannot double-charge.
	return code == "paymentblockhashstale" ||
		strings.Contains(detail, "blockhashnotfound") ||
		strings.Contains(detail, "blockheightexceeded") ||
		(verifyPhase && (reason == "expiredsignature" || strings.Contains(message, "expiredsignature")))
}

// isVerificationUnavailableResponse recognizes the one failure where the gateway
// never judged the payment at all: a required verification dependency (the
// facilitator's payer-risk screen) was down and it failed closed.
//
// Retrying is safe here for the same reason the stale-blockhash path is safe,
// and only for that reason: this is a VERIFY-phase answer, returned before any
// transaction is broadcast, so no retry can pay twice. That is also why a bare
// 503 is not enough. 503 is the generic "something upstream is unwell", and the
// routes settle OPTIMISTICALLY — a 503 raised after settlement has already been
// kicked off would, if retried, buy the same thing again. So this requires the
// explicit machine-readable marker the gateway emits for this case
// (blockrun-sol lib/payment-unavailable-response.ts) and nothing looser:
//
//	503 {"code":"PAYMENT_VERIFICATION_UNAVAILABLE","reason":"verification_unavailable"}
//
// Same body-restoring contract as isStaleBlockhashResponse: an unrecognized 503
// reaches the caller byte-for-byte.
func isVerificationUnavailableResponse(resp *http.Response) bool {
	if resp == nil || resp.Body == nil || resp.StatusCode != http.StatusServiceUnavailable {
		return false
	}
	original := resp.Body
	body, err := io.ReadAll(io.LimitReader(original, maxStaleClassifyBytes+1))
	resp.Body = &prefixedBody{r: io.MultiReader(bytes.NewReader(body), original), c: original}
	if err != nil || len(body) > maxStaleClassifyBytes {
		return false
	}

	var failure struct {
		Code   string `json:"code"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(body, &failure) != nil {
		return false
	}
	return normalizePaymentSignal(failure.Code) == "paymentverificationunavailable" ||
		normalizePaymentSignal(failure.Reason) == "verificationunavailable"
}

// isChallengeUnavailableResponse recognizes a gateway that could not produce a
// payment challenge because the facilitator it depends on was unreachable. It
// is consulted ONLY on the unpaid leg.
//
// It is deliberately wider than isVerificationUnavailableResponse, for a
// structural reason rather than a matter of taste. Everything that pins the
// paid-leg classifier to one exact marker is a double-charge argument: those
// routes settle optimistically, so a loose match there could replay a payment
// the caller already made. Before signing there is no authorization in flight,
// so no retry can buy anything twice. All that remains to weigh is whether a
// retry is useful, and against a facilitator outage it is.
//
// Two shapes qualify — the marker the gateway is supposed to send, and what
// production actually sent during a facilitator outage at 33 req/s on
// 2026-08-19 03:22:56-58Z, where it reported a failed dependency as its own
// internal error:
//
//	503 {"code":"PAYMENT_VERIFICATION_UNAVAILABLE","reason":"verification_unavailable"}
//	500 {"code":"INTERNAL_ERROR","debug":"Facilitator /supported returned 503"}
//
// Matching the second on `debug` is a bridge, not a contract: it is the only
// field naming the failing dependency, and it should be deleted once
// blockrun-sol maps a facilitator outage onto the 503 marker above. A 500
// WITHOUT that field stays terminal on purpose — retrying every 5xx would park
// a caller for ~10s on deterministic failures like an unknown model id.
func isChallengeUnavailableResponse(resp *http.Response) bool {
	if resp == nil || resp.Body == nil || resp.StatusCode < http.StatusInternalServerError {
		return false
	}
	if isVerificationUnavailableResponse(resp) {
		return true
	}
	original := resp.Body
	body, err := io.ReadAll(io.LimitReader(original, maxStaleClassifyBytes+1))
	resp.Body = &prefixedBody{r: io.MultiReader(bytes.NewReader(body), original), c: original}
	if err != nil || len(body) > maxStaleClassifyBytes {
		return false
	}

	var failure struct {
		Debug string `json:"debug"`
	}
	if json.Unmarshal(body, &failure) != nil {
		return false
	}
	return strings.Contains(normalizePaymentSignal(failure.Debug), "facilitator")
}

// retryAfterDelay reads the gateway's Retry-After, falling back to the caller's
// own backoff when it is absent, unparseable, or implausible.
//
// Delta-seconds only. The HTTP-date form is not honoured: it depends on the two
// clocks agreeing, and a skewed one turns a 5-second wait into hours or into
// nothing. The gateway sends seconds (jittered 5-15s), so the date form would be
// somebody else's proxy rewriting the header — a value worth ignoring.
//
// 0 is a valid answer meaning "immediately" and is NOT treated as absent; a
// negative or over-cap value falls back rather than being clamped, since it says
// the sender is not speaking the same protocol.
func retryAfterDelay(resp *http.Response, fallback time.Duration) time.Duration {
	if resp == nil {
		return fallback
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return fallback
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs < 0 {
		return fallback
	}
	d := time.Duration(secs) * time.Second
	if d > maxRetryAfterDelay {
		return fallback
	}
	return d
}

// prefixedBody re-presents a response body whose leading bytes were consumed
// for classification, closing the underlying body it wraps.
type prefixedBody struct {
	r io.Reader
	c io.Closer
}

func (b *prefixedBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *prefixedBody) Close() error               { return b.c.Close() }

func normalizePaymentSignal(value string) string {
	value = strings.ToLower(value)
	replacer := strings.NewReplacer("_", "", "-", "", " ", "", ":", "")
	return replacer.Replace(value)
}

func waitForX402Retry(req *http.Request, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-req.Context().Done():
		return req.Context().Err()
	}
}

// readPaymentRequirement extracts the base64 payment requirement, preferring the
// `payment-required` header and falling back to an x402 body, mirroring the
// blockrun-llm-go base client behaviour.
func readPaymentRequirement(resp *http.Response) string {
	if h := resp.Header.Get("payment-required"); h != "" {
		return h
	}
	var rb map[string]any
	if json.NewDecoder(resp.Body).Decode(&rb) == nil {
		if _, ok := rb["x402"]; ok {
			if jb, err := json.Marshal(rb); err == nil {
				return string(jb)
			}
		}
	}
	return ""
}

// signSolanaPayment parses the requirement and returns a signed x402 SVM
// exact-scheme payload (USDC on Solana). rpcURL is passed through to fetch the
// blockhash and mint info (empty → SOLANA_RPC_URL / BlockRun's free proxy).
func signSolanaPayment(bs58Key, rpcURL, paymentHeader, requestURL string) (string, error) {
	paymentReq, err := blockrun.ParsePaymentRequired(paymentHeader)
	if err != nil {
		return "", &blockrun.PaymentError{Message: fmt.Sprintf("failed to parse payment requirements: %v", err)}
	}
	option, err := blockrun.ExtractPaymentDetails(paymentReq)
	if err != nil {
		return "", &blockrun.PaymentError{Message: fmt.Sprintf("failed to extract payment details: %v", err)}
	}
	resourceURL := paymentReq.Resource.URL
	if resourceURL == "" {
		resourceURL = requestURL
	}
	return blockrun.CreateSolanaPaymentPayload(bs58Key, option, resourceURL, paymentReq.Resource.Description, paymentReq.Extensions, rpcURL)
}

// signPayment parses the requirement and returns a signed x402 v2 payload.
func signPayment(priv *ecdsa.PrivateKey, paymentHeader, requestURL string) (string, error) {
	paymentReq, err := blockrun.ParsePaymentRequired(paymentHeader)
	if err != nil {
		return "", &blockrun.PaymentError{Message: fmt.Sprintf("failed to parse payment requirements: %v", err)}
	}
	option, err := blockrun.ExtractPaymentDetails(paymentReq)
	if err != nil {
		return "", &blockrun.PaymentError{Message: fmt.Sprintf("failed to extract payment details: %v", err)}
	}

	resourceURL := paymentReq.Resource.URL
	if resourceURL == "" {
		resourceURL = requestURL
	}

	payload, err := blockrun.CreatePaymentPayload(
		priv,
		option.PayTo,
		option.Amount,
		option.Network,
		resourceURL,
		paymentReq.Resource.Description,
		option.MaxTimeoutSeconds,
		option.Extra,
		paymentReq.Extensions,
	)
	if err != nil {
		return "", &blockrun.PaymentError{Message: fmt.Sprintf("failed to create payment: %v", err)}
	}
	return payload, nil
}
