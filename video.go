package vip

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	blockrun "github.com/BlockRunAI/blockrun-llm-go"
)

const (
	videoEndpoint = "/v1/videos/generations"
	// The gateway now runs video generation asynchronously: POST returns 202
	// with a poll_url, and the client must GET it (re-signing x402) until the
	// job is completed. These bound that poll loop.
	defaultVideoPollInterval = 12 * time.Second
	defaultVideoMaxWait      = 5 * time.Minute
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
	cfg, sign, err := resolveSigner(opts...)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	if cfg.accountMode() {
		client = cfg.accountHTTPClient(60 * time.Second)
	}
	return &Video{
		accountMode:  cfg.accountMode(),
		sign:         sign,
		apiURL:       cfg.apiURL,
		httpClient:   client,
		pollInterval: defaultVideoPollInterval,
		maxWait:      defaultVideoMaxWait,
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

// Poll advances the job by one status check (re-signing x402). It updates
// job.Status/Raw and, on completion, job.Response. It is a no-op once the job
// is Done. This is where payment actually settles: the gateway transfers the
// USDC exactly once, on the first poll that observes status=completed. Polls
// that see queued/in_progress charge nothing, and a job that ends failed is
// never charged.
func (v *Video) Poll(ctx context.Context, job *VideoJob) error {
	if job.Done() {
		return nil
	}
	if job.PollURL == "" {
		return fmt.Errorf("vip: job has no poll URL")
	}
	status, body, err := v.doX402(ctx, http.MethodGet, job.PollURL, nil)
	if err != nil {
		return err
	}
	job.Raw = body
	if status == http.StatusOK {
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
			return nil, fmt.Errorf("vip: video not ready after %s (still generating; job %s)", v.maxWait, job.ID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(v.pollInterval):
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
			return resp.StatusCode, nil, &blockrun.APIError{StatusCode: resp.StatusCode, Message: string(data), RetryAfter: resp.Header.Get("Retry-After")}
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
