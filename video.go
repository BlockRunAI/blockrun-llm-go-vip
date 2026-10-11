package vip

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	blockrun "github.com/BlockRunAI/blockrun-llm-go"
)

const (
	videoEndpoint = "/v1/videos/generations"
	// The gateway now runs video generation asynchronously: POST returns 202
	// with a poll_url, and the client must GET it until the job is completed
	// (on Solana with a signed ownership proof, see poll_ownership.go; on Base
	// re-signing x402). These bound that poll loop.
	defaultVideoPollInterval = 12 * time.Second
	defaultVideoMaxWait      = 5 * time.Minute

	// maxVideoPaidPollRetries bounds consecutive Solana paid polls whose
	// outcome is unknown (lost in transit, or a 402 "settlement in progress")
	// before Poll reports the payment as in doubt.
	maxVideoPaidPollRetries = 4
	// maxVideoRetryAfter caps a poll's Retry-After (the gateway asks for 90s
	// at most, while a timed-out settle's claim is held).
	maxVideoRetryAfter = 120 * time.Second
)

// Video generates short videos through ByteDance Seedance (and Grok Imagine)
// via the BlockRun gateway, paid per call in USDC (x402) on Base.
//
// The gateway runs generation asynchronously. The native API mirrors that:
//   - Submit returns immediately with a VideoJob. The x402 payment header is
//     signed and verified here, but NO USDC moves yet — the gateway settles
//     on-chain only on the first Poll that observes status=completed.
//   - Poll advances the job by one status check.
//   - Wait blocks until the job completes (convenience over Poll).
//   - Generate is Submit+Wait, kept for callers that want a single blocking call.
//
// Data[0].URL on the completed job is a permanent BlockRun-hosted MP4.
type Video struct {
	accountMode  bool
	sign         paymentSigner
	apiURL       string
	httpClient   *http.Client
	pollInterval time.Duration
	maxWait      time.Duration

	// pollKey is the Solana wallet's ed25519 key, set only on the Solana
	// wallet rail. When set, Poll proves job ownership with x-poll-* headers
	// instead of signing a payment per poll. nil on Base and the account rail.
	pollKey ed25519.PrivateKey
	now     func() time.Time                 // clock for the proof timestamp; time.Now
	logf    func(format string, args ...any) // log.Printf; swapped in tests
}

// Video job statuses returned by the gateway.
const (
	VideoStatusQueued     = "queued"
	VideoStatusInProgress = "in_progress"
	VideoStatusCompleted  = "completed"
	VideoStatusFailed     = "failed"
)

// VideoJob is a handle to an asynchronous video generation. Submit returns one;
// Poll/Wait advance it. When Status == VideoStatusCompleted, Response holds the
// finished job (Response.Data[0].URL is the MP4).
type VideoJob struct {
	ID       string         // gateway job id, e.g. "bytedance:video_xxx"
	Status   string         // queued | in_progress | completed | failed
	Response *VideoResponse // populated once completed
	PollURL  string         // absolute poll URL (empty if already completed at submit)
	Raw      []byte         // raw JSON of the most recent gateway response

	// paymentPolling is set once the gateway refused this job's ownership
	// proof; from then on the job is polled with x-payment only.
	paymentPolling bool
	// paidInDoubt counts consecutive Solana paid polls whose outcome is
	// unknown; retryAfter is the gateway's hint for the next poll (Solana).
	paidInDoubt int
	retryAfter  time.Duration
}

// Done reports whether the job has reached a terminal state.
func (j *VideoJob) Done() bool {
	return j.Status == VideoStatusCompleted || j.Status == VideoStatusFailed
}

// NewVideo returns a Seedance video client paid per call via x402 on Base.
//
//	video, _ := vip.NewVideo()
//	job, _ := video.Generate(ctx, "a neon-lit cyberpunk street, slow dolly", &vip.VideoGenerateOptions{
//	    Model: "bytedance/seedance-2.0-fast", DurationSeconds: 5,
//	})
//	fmt.Println(job.Data[0].URL)
func NewVideo(opts ...Option) (*Video, error) {
	cfg, sign, key, err := resolveSignerWithKey(opts...)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	if cfg.accountMode() {
		client = cfg.accountHTTPClient(60 * time.Second)
	}
	var pollKey ed25519.PrivateKey
	if !cfg.accountMode() && cfg.isSolana() {
		if pollKey, err = solanaPollKey(key); err != nil {
			return nil, err
		}
	}
	return &Video{
		accountMode:  cfg.accountMode(),
		sign:         sign,
		apiURL:       cfg.apiURL,
		httpClient:   client,
		pollInterval: defaultVideoPollInterval,
		maxWait:      defaultVideoMaxWait,
		pollKey:      pollKey,
		now:          time.Now,
		logf:         log.Printf,
	}, nil
}

// Submit starts a video job and returns immediately without waiting for it to
// finish. The x402 payment authorization is signed and verified here, but the
// gateway does NOT settle it: USDC is transferred only on the first Poll that
// observes status=completed. A job that fails upstream, or that the caller
// abandons without polling, is never charged. Advance the returned job with
// Poll or Wait.
func (v *Video) Submit(ctx context.Context, prompt string, opts *VideoGenerateOptions) (*VideoJob, error) {
	body, err := buildVideoBody(prompt, opts)
	if err != nil {
		return nil, err
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("vip: encode video request: %w", err)
	}

	status, respBytes, err := v.doX402(ctx, http.MethodPost, v.apiURL+videoEndpoint, jsonBody)
	if err != nil {
		return nil, err
	}

	switch status {
	case http.StatusOK:
		// Rare: gateway returned the finished job synchronously.
		resp, err := decodeVideoResponse(respBytes)
		if err != nil {
			return nil, err
		}
		return &VideoJob{Status: VideoStatusCompleted, Response: resp, Raw: respBytes}, nil
	case http.StatusAccepted:
		var meta struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			PollURL string `json:"poll_url"`
		}
		if err := json.Unmarshal(respBytes, &meta); err != nil {
			return nil, fmt.Errorf("vip: parse 202 submit body: %w", err)
		}
		if meta.PollURL == "" {
			return nil, fmt.Errorf("vip: 202 submit returned no poll_url: %s", string(respBytes))
		}
		pollURL, err := v.absoluteURL(meta.PollURL)
		if err != nil {
			return nil, err
		}
		if meta.Status == "" {
			meta.Status = VideoStatusQueued
		}
		return &VideoJob{ID: meta.ID, Status: meta.Status, PollURL: pollURL, Raw: respBytes}, nil
	default:
		return nil, &blockrun.APIError{StatusCode: status, Message: fmt.Sprintf("video submit failed: %s", string(respBytes))}
	}
}

// buildVideoBody validates the option combination and builds the gateway request
// body. Pure (no I/O) so it can be unit-tested without spending. Seedance accepts
// three mutually-exclusive ways to seed a clip: a single ImageURL (optionally with
// a LastFrameURL to interpolate to a final frame), a RealFaceAssetID (real-person /
// virtual-portrait identity), or up to 9 ReferenceImageURLs (omni / multi-reference).
func buildVideoBody(prompt string, opts *VideoGenerateOptions) (map[string]any, error) {
	body := map[string]any{"prompt": prompt}
	if opts == nil {
		return body, nil
	}

	seeds := 0
	if opts.ImageURL != "" {
		seeds++
	}
	if opts.RealFaceAssetID != "" {
		seeds++
	}
	if len(opts.ReferenceImageURLs) > 0 {
		seeds++
	}
	if seeds > 1 {
		return nil, fmt.Errorf("vip: ImageURL, RealFaceAssetID and ReferenceImageURLs are mutually exclusive; pass at most one")
	}
	if opts.RealFaceAssetID != "" && !strings.HasPrefix(opts.RealFaceAssetID, "ta_") {
		return nil, fmt.Errorf("vip: RealFaceAssetID must start with 'ta_' (enroll via NewRealFace or NewPortrait)")
	}
	if opts.LastFrameURL != "" && opts.ImageURL == "" {
		return nil, fmt.Errorf("vip: LastFrameURL (first-and-last-frame) requires ImageURL as the first frame")
	}
	if len(opts.ReferenceImageURLs) > 9 {
		return nil, fmt.Errorf("vip: ReferenceImageURLs accepts at most 9 URLs, got %d", len(opts.ReferenceImageURLs))
	}

	if opts.Model != "" {
		body["model"] = opts.Model
	}
	if opts.ImageURL != "" {
		body["image_url"] = opts.ImageURL
	}
	if opts.LastFrameURL != "" {
		body["last_frame_url"] = opts.LastFrameURL
	}
	if len(opts.ReferenceImageURLs) > 0 {
		body["reference_image_urls"] = opts.ReferenceImageURLs
	}
	if opts.RealFaceAssetID != "" {
		body["real_face_asset_id"] = opts.RealFaceAssetID
	}
	if opts.DurationSeconds > 0 {
		body["duration_seconds"] = opts.DurationSeconds
	}
	if opts.AspectRatio != "" {
		body["aspect_ratio"] = opts.AspectRatio
	}
	if opts.Resolution != "" {
		body["resolution"] = opts.Resolution
	}
	if opts.GenerateAudio != nil {
		body["generate_audio"] = *opts.GenerateAudio
	}
	if opts.Seed != nil {
		body["seed"] = *opts.Seed
	}
	if opts.Watermark != nil {
		body["watermark"] = *opts.Watermark
	}
	if opts.ReturnLastFrame {
		body["return_last_frame"] = true
	}
	return body, nil
}

// Poll advances the job by one status check. It updates job.Status/Raw and, on
// completion, job.Response. It is a no-op once the job is Done. This is where
// payment actually settles: the gateway transfers the USDC exactly once, on the
// first poll that observes status=completed. Polls that see queued/in_progress
// charge nothing, and a job that ends failed is never charged.
//
// On Solana each poll carries a fresh ed25519 ownership proof (x-poll-*
// headers) instead of a signed payment, so a pending poll signs no payment at
// all. When the video is ready the gateway answers that poll with a 402 whose
// body says status "completed"; Poll signs ONE payment from that challenge and
// re-sends the GET with it, which settles the job and returns the video. If the
// gateway refuses the proof for this job ("Invalid job id" — e.g. a job
// submitted before the gateway bound its payer, or a clock more than 300s off),
// Poll falls back once, for the rest of this job, to x-payment polling.
//
// On Solana a paid poll whose outcome is unknown — the answer was lost in
// transit, or the gateway answered 402 "settlement in progress" / "outcome
// unknown" / "settlement failed" — is not an error: Poll returns nil with the
// job still pending, and the next poll reads the job's settlement record (the
// video, with no new charge, if that payment landed) or asks again. The gateway
// settles a job at most once. After maxVideoPaidPollRetries such polls in a row
// Poll returns a *VideoPaymentInDoubtError naming the poll URL to resume from.
//
// On Base every poll re-signs x402 as before.
func (v *Video) Poll(ctx context.Context, job *VideoJob) error {
	if job.Done() {
		return nil
	}
	if job.PollURL == "" {
		return fmt.Errorf("vip: job has no poll URL")
	}
	if v.pollKey != nil && !v.accountMode {
		return v.pollSolana(ctx, job)
	}
	status, body, err := v.doX402(ctx, http.MethodGet, job.PollURL, nil)
	if err != nil {
		return err
	}
	return applyVideoPoll(job, status, body)
}

// applyVideoPoll records one poll answer (200 or 202) on the job.
func applyVideoPoll(job *VideoJob, status int, body []byte) error {
	job.Raw = body
	if status == http.StatusOK {
		// A job that failed upstream answers 200 {"status":"failed"} and is
		// never charged; it is not a completed video.
		var meta struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(body, &meta) == nil && meta.Status == VideoStatusFailed {
			job.Status = VideoStatusFailed
			return nil
		}
		resp, err := decodeVideoResponse(body)
		if err != nil {
			return err
		}
		job.Response = resp
		job.Status = VideoStatusCompleted
		return nil
	}
	if status != http.StatusAccepted {
		return &blockrun.APIError{StatusCode: status, Message: fmt.Sprintf("video poll failed: %s", string(body))}
	}
	var meta struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &meta) == nil && meta.Status != "" {
		job.Status = meta.Status
	} else {
		job.Status = VideoStatusInProgress
	}
	return nil
}

// pollSolana is one poll on the Solana wallet rail (see Poll). Every request it
// sends is accounted for separately, so a refusal is only ever read off the
// proof request itself and a payment is signed at most once per call.
func (v *Video) pollSolana(ctx context.Context, job *VideoJob) error {
	job.retryAfter = 0
	challenge := ""

	if !job.paymentPolling {
		proof := pollAuthHeaders(v.pollKey, pollKindVideo, pollJobID(job.PollURL, job.ID), v.clock())
		status, body, hdr, err := v.get(ctx, job.PollURL, proof)
		if err != nil {
			// The proof request carries no payment: nothing can have been charged.
			return err
		}
		switch {
		case isPollOwnershipRefusal(status, body):
			v.fallBackToPaymentPolling(job, "the gateway refused the poll-ownership proof (HTTP 400)")
		case status == http.StatusPaymentRequired:
			var meta struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(body, &meta) == nil && meta.Status == VideoStatusCompleted {
				// Finished and unpaid: this challenge is the one payment for the job.
				if challenge = paymentRequirementFrom(hdr, body); challenge == "" {
					return &blockrun.PaymentError{Message: "402 response but no payment requirements found"}
				}
			} else {
				v.fallBackToPaymentPolling(job, "the gateway answered the poll-ownership proof with a payment challenge")
			}
		case isTransientVideoPoll(status):
			job.Raw = body
			job.retryAfter = videoRetryAfter(hdr)
			return nil
		default:
			job.retryAfter = videoRetryAfter(hdr)
			return applyVideoPoll(job, status, body)
		}
	}

	if challenge == "" {
		// x-payment polling: an unpaid GET for a 402 carrying a fresh blockhash.
		status, body, hdr, err := v.get(ctx, job.PollURL, nil)
		if err != nil {
			return err
		}
		if status != http.StatusPaymentRequired {
			job.retryAfter = videoRetryAfter(hdr)
			return applyVideoPoll(job, status, body)
		}
		if challenge = paymentRequirementFrom(hdr, body); challenge == "" {
			return &blockrun.PaymentError{Message: "402 response but no payment requirements found"}
		}
	}

	signature, err := v.sign(challenge, job.PollURL)
	if err != nil {
		return err
	}
	status, body, hdr, err := v.get(ctx, job.PollURL, map[string]string{
		// The gateway accepts PAYMENT-SIGNATURE; the video poll docs also
		// mention an x-payment header, so set both for compatibility.
		"PAYMENT-SIGNATURE": signature,
		"X-PAYMENT":         signature,
	})
	if err != nil {
		// The payment may have reached the gateway and settled with only the
		// answer lost. Never give up on a job that may be paid for: the next
		// poll reads the settlement record, or asks again if it never settled.
		return v.paidPollInDoubt(ctx, job, fmt.Errorf("paid poll failed in transit: %w", err), 0)
	}
	if status == http.StatusPaymentRequired {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error == "Payment verification failed" {
			// Refused at verification: settlement never ran, nothing was charged.
			job.Raw = body
			return &blockrun.PaymentError{Message: fmt.Sprintf(
				"payment for video job %s was rejected at verification (nothing charged): %s. Check your Solana USDC balance, then resume by polling %s with the same wallet",
				job.ID, string(body), job.PollURL)}
		}
		// Settlement in progress on another poll, outcome unknown, or a failed
		// settle. Re-polling is the gateway's documented recovery and cannot
		// charge twice: a per-job claim lets only one poll settle, and a claim
		// whose settle timed out is checked on chain before anything new settles.
		job.Raw = body
		return v.paidPollInDoubt(ctx, job, &blockrun.APIError{StatusCode: status, Message: fmt.Sprintf("video poll failed: %s", string(body))}, videoRetryAfter(hdr))
	}
	if isTransientVideoPoll(status) {
		// A 503/504 answering a payment is as unknown as a lost answer.
		job.Raw = body
		return v.paidPollInDoubt(ctx, job, &blockrun.APIError{StatusCode: status, Message: fmt.Sprintf("video poll failed: %s", string(body))}, videoRetryAfter(hdr))
	}
	job.paidInDoubt = 0
	job.retryAfter = videoRetryAfter(hdr)
	return applyVideoPoll(job, status, body)
}

// paidPollInDoubt records a paid poll whose outcome is unknown and decides
// whether the job is still worth re-polling.
func (v *Video) paidPollInDoubt(ctx context.Context, job *VideoJob, cause error, retryAfter time.Duration) error {
	job.paidInDoubt++
	job.retryAfter = retryAfter
	if job.paidInDoubt >= maxVideoPaidPollRetries || ctx.Err() != nil {
		return &VideoPaymentInDoubtError{JobID: job.ID, PollURL: job.PollURL, Err: cause}
	}
	v.log("vip: payment for video job %s is not confirmed (%v); re-polling — the gateway settles a job at most once", job.ID, cause)
	return nil
}

func (v *Video) fallBackToPaymentPolling(job *VideoJob, why string) {
	job.paymentPolling = true
	v.log("vip: video job %s: %s; falling back to x-payment polling for this job", job.ID, why)
}

// VideoPaymentInDoubtError reports a payment for a finished video that was
// sent but not confirmed: it may have settled. Do NOT submit the video again —
// resume by polling PollURL with the same wallet (a VideoJob with this ID and
// PollURL, passed to Video.Poll or Video.Wait). If the payment settled, that
// poll returns the video with no new charge; if it did not, it settles once.
type VideoPaymentInDoubtError struct {
	JobID   string
	PollURL string
	Err     error
}

func (e *VideoPaymentInDoubtError) Error() string {
	return fmt.Sprintf("vip: payment for finished video job %s may have settled (%v); resume by polling %s with the same wallet before submitting again", e.JobID, e.Err, e.PollURL)
}

func (e *VideoPaymentInDoubtError) Unwrap() error { return e.Err }

// Wait blocks, polling at the client's interval, until the job completes or the
// max wait elapses. Returns the completed job's response.
func (v *Video) Wait(ctx context.Context, job *VideoJob) (*VideoResponse, error) {
	deadline := time.Now().Add(v.maxWait)
	for !job.Done() {
		if err := v.Poll(ctx, job); err != nil {
			return nil, err
		}
		if job.Done() {
			break
		}
		if time.Now().After(deadline) {
			if job.paidInDoubt > 0 {
				return nil, &VideoPaymentInDoubtError{JobID: job.ID, PollURL: job.PollURL, Err: fmt.Errorf("not confirmed after %s", v.maxWait)}
			}
			return nil, fmt.Errorf("vip: video not ready after %s (still generating; job %s)", v.maxWait, job.ID)
		}
		wait := v.pollInterval
		if job.retryAfter > 0 {
			wait = job.retryAfter
		}
		select {
		case <-ctx.Done():
			if job.paidInDoubt > 0 {
				return nil, &VideoPaymentInDoubtError{JobID: job.ID, PollURL: job.PollURL, Err: ctx.Err()}
			}
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	if job.Status == VideoStatusFailed {
		return nil, fmt.Errorf("vip: video job %s failed: %s", job.ID, string(job.Raw))
	}
	return job.Response, nil
}

// Generate submits a video job and blocks until it completes. It is shorthand
// for Submit followed by Wait, kept for callers that want a single call.
func (v *Video) Generate(ctx context.Context, prompt string, opts *VideoGenerateOptions) (*VideoResponse, error) {
	job, err := v.Submit(ctx, prompt, opts)
	if err != nil {
		return nil, err
	}
	return v.Wait(ctx, job)
}

// isPollOwnershipRefusal reports the gateway's refusal of an ownership proof:
// wrong wallet, bad signature, stale timestamp or a job with no bound payer all
// answer exactly like an unknown job (400 "Invalid job id"), so they cannot be
// told apart — nor need to be: the remedy for each is the x-payment poll.
func isPollOwnershipRefusal(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	var e struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(body, &e) == nil && e.Error == "Invalid job id"
}

// isTransientVideoPoll is a poll answer that means "not yet, ask again":
// a gateway or upstream 503/504.
func isTransientVideoPoll(status int) bool {
	return status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// videoRetryAfter is the poll's Retry-After (delta-seconds only), capped at
// maxVideoRetryAfter; 0 when absent or unusable.
func videoRetryAfter(hdr http.Header) time.Duration {
	raw := strings.TrimSpace(hdr.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return 0
	}
	if d := time.Duration(secs) * time.Second; d < maxVideoRetryAfter {
		return d
	}
	return maxVideoRetryAfter
}

// paymentRequirementFrom reads the x402 challenge from an already-read 402.
func paymentRequirementFrom(hdr http.Header, body []byte) string {
	return readPaymentRequirement(&http.Response{Header: hdr, Body: io.NopCloser(bytes.NewReader(body))})
}

// get sends one GET with the given extra headers and reads the whole answer.
func (v *Video) get(ctx context.Context, fullURL string, headers map[string]string) (int, []byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("vip: video request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("vip: video request: %w", err)
	}
	return resp.StatusCode, data, resp.Header, nil
}

func (v *Video) clock() time.Time {
	if v.now != nil {
		return v.now()
	}
	return time.Now()
}

func (v *Video) log(format string, args ...any) {
	if v.logf != nil {
		v.logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// doX402 performs an HTTP request and, on a 402, signs an x402 payment and
// retries once with the payment header.
func (v *Video) doX402(ctx context.Context, method, fullURL string, body []byte) (int, []byte, error) {
	newReq := func() (*http.Request, error) {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, r)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	}

	req, err := newReq()
	if err != nil {
		return 0, nil, err
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("vip: video request: %w", err)
	}
	if v.accountMode {
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, nil, err
		}
		if resp.StatusCode >= 400 {
			apiErr := &blockrun.APIError{StatusCode: resp.StatusCode, Message: string(data)}
			// Retry-After rides in Body: upstream's APIError has no field for
			// it, and dropping it would cost a caller the gateway's own hint
			// about when the job is worth asking about again.
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				apiErr.Body = map[string]any{"retry_after": ra}
			}
			return resp.StatusCode, nil, apiErr
		}
		return resp.StatusCode, data, nil
	}
	if resp.StatusCode != http.StatusPaymentRequired {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, data, nil
	}

	paymentHeader := readPaymentRequirement(resp)
	resp.Body.Close()
	if paymentHeader == "" {
		return resp.StatusCode, nil, &blockrun.PaymentError{Message: "402 response but no payment requirements found"}
	}
	signature, err := v.sign(paymentHeader, fullURL)
	if err != nil {
		return 0, nil, err
	}

	retry, err := newReq()
	if err != nil {
		return 0, nil, err
	}
	// The gateway accepts PAYMENT-SIGNATURE; the video poll docs also mention an
	// x-payment header, so set both for compatibility.
	retry.Header.Set("PAYMENT-SIGNATURE", signature)
	retry.Header.Set("X-PAYMENT", signature)
	resp2, err := v.httpClient.Do(retry)
	if err != nil {
		return 0, nil, fmt.Errorf("vip: video retry request: %w", err)
	}
	data, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	return resp2.StatusCode, data, nil
}

// absoluteURL resolves a possibly-relative poll_url against the gateway origin.
func (v *Video) absoluteURL(pollURL string) (string, error) {
	if strings.HasPrefix(pollURL, "http://") || strings.HasPrefix(pollURL, "https://") {
		return pollURL, nil
	}
	base, err := url.Parse(v.apiURL)
	if err != nil {
		return "", fmt.Errorf("vip: parse api url: %w", err)
	}
	return fmt.Sprintf("%s://%s%s", base.Scheme, base.Host, pollURL), nil
}

func decodeVideoResponse(b []byte) (*VideoResponse, error) {
	var resp VideoResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("vip: decode video response: %w", err)
	}
	return &resp, nil
}
