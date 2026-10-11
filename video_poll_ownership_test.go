package vip

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mr-tron/base58"
)

func newTestPollKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return priv
}

func TestPollAuthHeaders_SignatureVerifiesOverExactMessage(t *testing.T) {
	priv := newTestPollKey(t)
	now := time.Unix(1_791_590_400, 0)
	jobID := "vjt_AbC-123.x:y"
	h := pollAuthHeaders(priv, "video", jobID, now)

	pub, err := base58.Decode(h[pollHeaderWallet])
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("x-poll-wallet must be base58 of 32 bytes: len=%d err=%v", len(pub), err)
	}
	if !ed25519.PublicKey(pub).Equal(priv.Public()) {
		t.Fatal("x-poll-wallet is not the signing wallet's public key")
	}
	sig, err := base58.Decode(h[pollHeaderSignature])
	if err != nil || len(sig) != ed25519.SignatureSize {
		t.Fatalf("x-poll-signature must be base58 of 64 bytes: len=%d err=%v", len(sig), err)
	}
	if h[pollHeaderTimestamp] != "1791590400" {
		t.Fatalf("x-poll-timestamp = %q, want unix seconds 1791590400", h[pollHeaderTimestamp])
	}

	// The literal the gateway builds (poll-ownership.ts pollAuthMessage).
	want := "blockrun-poll:v1:video:vjt_AbC-123.x:y:1791590400"
	if !ed25519.Verify(pub, []byte(want), sig) {
		t.Fatalf("signature does not verify over %q", want)
	}
	// Controls: the same signature must NOT verify for another kind, job or
	// second — otherwise the positive check above licenses nothing.
	for _, other := range []string{
		"blockrun-poll:v1:image:vjt_AbC-123.x:y:1791590400",
		"blockrun-poll:v1:video:vjt_AbC-123.x:z:1791590400",
		"blockrun-poll:v1:video:vjt_AbC-123.x:y:1791590401",
	} {
		if ed25519.Verify(pub, []byte(other), sig) {
			t.Fatalf("signature unexpectedly verifies over %q", other)
		}
	}
}

func TestSolanaPollKey_AcceptsKeypairAndSeed(t *testing.T) {
	priv := newTestPollKey(t)
	for name, enc := range map[string]string{
		"64-byte keypair": base58.Encode(priv),
		"32-byte seed":    base58.Encode(priv.Seed()),
	} {
		got, err := solanaPollKey(enc)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !got.Equal(priv) {
			t.Fatalf("%s: decoded a different key", name)
		}
	}
	if _, err := solanaPollKey(base58.Encode(make([]byte, 31))); err == nil {
		t.Fatal("31-byte key: want error")
	}
}

func TestPollJobID_IsTheDecodedPathSegment(t *testing.T) {
	cases := []struct{ pollURL, fallback, want string }{
		{"https://sol.blockrun.ai/api/v1/videos/generations/vjt_abc?model=m&duration=5", "x", "vjt_abc"},
		{"https://sol.blockrun.ai/api/v1/videos/generations/bytedance%3Avideo_1?model=m", "x", "bytedance:video_1"},
		{"https://sol.blockrun.ai/api/v1/videos/generations/a%2Fb", "x", "a/b"},
		{"https://sol.blockrun.ai/api/v1/videos/generations/vjt_abc/", "x", "vjt_abc"},
		{"::not a url", "fallback-id", "fallback-id"},
	}
	for _, c := range cases {
		if got := pollJobID(c.pollURL, c.fallback); got != c.want {
			t.Errorf("pollJobID(%q) = %q, want %q", c.pollURL, got, c.want)
		}
	}
}

// fakeSolGateway models sol.blockrun.ai's video poll route
// (src/app/api/v1/videos/generations/[id]/route.ts) for one job bound to one
// payer: the ownership proof answers status only, a finished job answers 402
// {"status":"completed"} to the proof, and the x-payment poll settles it.
type fakeSolGateway struct {
	jobID       string
	payer       ed25519.PublicKey
	pendingFor  int  // ownership/payment polls answered 202 before the job completes
	refuseProof bool // answer every proof with 400 Invalid job id (unbound job)

	mu         sync.Mutex
	polls      int
	proofPolls int // requests carrying x-poll-*
	paidPolls  int // requests carrying x-payment
	mixed      int // requests carrying both (must stay 0)
	settled    bool
	badProof   []string
}

func (g *fakeSolGateway) handler(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	seg := strings.TrimPrefix(r.URL.EscapedPath(), "/v1/videos/generations/")
	id, _ := url.PathUnescape(seg)
	if id != g.jobID {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"Invalid job id"}`)
		return
	}
	payment := r.Header.Get("X-Payment")
	hasProof := r.Header.Get(pollHeaderWallet) != "" || r.Header.Get(pollHeaderTimestamp) != "" || r.Header.Get(pollHeaderSignature) != ""
	if payment != "" && hasProof {
		g.mixed++
	}
	if payment == "" && hasProof {
		g.proofPolls++
		reason := g.verifyProof(r, id)
		if reason != "" {
			g.badProof = append(g.badProof, reason)
		}
		if reason != "" || g.refuseProof {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"Invalid job id"}`)
			return
		}
		g.polls++
		completed := g.polls > g.pendingFor
		if !completed {
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, `{"id":%q,"status":"in_progress","payment_status":"not_charged"}`, id)
			return
		}
		if g.settled {
			fmt.Fprintf(w, `{"id":%q,"status":"completed","data":[{"url":"https://cdn/v.mp4"}],"payment":{"status":"already_settled"}}`, id)
			return
		}
		w.Header().Set("Payment-Required", "challenge-completed")
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprintf(w, `{"error":"Payment Required","id":%q,"status":"completed"}`, id)
		return
	}
	if payment == "" {
		// Unpaid, unproven poll: the classic x402 challenge.
		w.Header().Set("Payment-Required", "challenge-poll")
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"error":"Payment Required"}`)
		return
	}
	g.paidPolls++
	g.polls++
	completed := g.polls > g.pendingFor
	if !completed {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"id":%q,"status":"in_progress","payment_status":"verified"}`, id)
		return
	}
	g.settled = true
	fmt.Fprintf(w, `{"id":%q,"status":"completed","data":[{"url":"https://cdn/v.mp4"}]}`, id)
}

// verifyProof mirrors readPollOwnership + the route's payer comparison.
func (g *fakeSolGateway) verifyProof(r *http.Request, jobID string) string {
	wallet := r.Header.Get(pollHeaderWallet)
	ts := r.Header.Get(pollHeaderTimestamp)
	sigB58 := r.Header.Get(pollHeaderSignature)
	if wallet == "" || ts == "" || sigB58 == "" {
		return "incomplete"
	}
	tsN, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return "timestamp"
	}
	if d := time.Now().Unix() - tsN; d > 300 || d < -300 {
		return "stale"
	}
	pub, err1 := base58.Decode(wallet)
	sig, err2 := base58.Decode(sigB58)
	if err1 != nil || err2 != nil || len(pub) != 32 || len(sig) != 64 {
		return "encoding"
	}
	if !ed25519.Verify(pub, []byte(fmt.Sprintf("blockrun-poll:v1:video:%s:%s", jobID, ts)), sig) {
		return "signature"
	}
	if !ed25519.PublicKey(pub).Equal(g.payer) {
		return "not the submitter"
	}
	return ""
}

type countingSigner struct {
	mu         sync.Mutex
	calls      int
	challenges []string
}

func (c *countingSigner) sign(paymentHeader, _ string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.challenges = append(c.challenges, paymentHeader)
	return fmt.Sprintf("signed-payment-%d", c.calls), nil
}

func newOwnershipVideo(srvURL string, key ed25519.PrivateKey, signer *countingSigner, logs *[]string) *Video {
	return &Video{
		sign:         signer.sign,
		apiURL:       srvURL,
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		pollInterval: time.Millisecond,
		maxWait:      5 * time.Second,
		pollKey:      key,
		now:          time.Now,
		logf: func(format string, args ...any) {
			*logs = append(*logs, fmt.Sprintf(format, args...))
		},
	}
}

func TestVideoPoll_SolanaOwnership_PendingSignsNoPaymentCompletionSignsOne(t *testing.T) {
	key := newTestPollKey(t)
	jobID := "vjt_sealed.Token-1"
	g := &fakeSolGateway{jobID: jobID, payer: key.Public().(ed25519.PublicKey), pendingFor: 3}
	srv := httptest.NewServer(http.HandlerFunc(g.handler))
	defer srv.Close()

	signer := &countingSigner{}
	var logs []string
	v := newOwnershipVideo(srv.URL, key, signer, &logs)
	job := &VideoJob{ID: jobID, Status: VideoStatusQueued, PollURL: srv.URL + "/v1/videos/generations/" + jobID + "?model=bytedance/seedance-2.0-fast&duration=5"}

	// Pending polls: proof only, never the payment signer.
	for i := 0; i < 3; i++ {
		if err := v.Poll(context.Background(), job); err != nil {
			t.Fatalf("pending poll %d: %v", i, err)
		}
		if job.Status != VideoStatusInProgress {
			t.Fatalf("pending poll %d: status %q", i, job.Status)
		}
	}
	if signer.calls != 0 {
		t.Fatalf("pending polls invoked the payment signer %d times; want 0", signer.calls)
	}
	if g.proofPolls != 3 || g.paidPolls != 0 {
		t.Fatalf("pending polls: proof=%d paid=%d; want 3/0", g.proofPolls, g.paidPolls)
	}

	resp, err := v.Wait(context.Background(), job)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].URL != "https://cdn/v.mp4" {
		t.Fatalf("completed response: %+v", resp)
	}
	if signer.calls != 1 {
		t.Fatalf("payment signer called %d times; want exactly 1", signer.calls)
	}
	if signer.challenges[0] != "challenge-completed" {
		t.Fatalf("payment signed from %q; want the completion 402's challenge", signer.challenges[0])
	}
	if g.paidPolls != 1 || !g.settled {
		t.Fatalf("paid polls %d settled %v; want 1/true", g.paidPolls, g.settled)
	}
	if len(g.badProof) != 0 || g.mixed != 0 {
		t.Fatalf("gateway refused proofs %v / mixed requests %d", g.badProof, g.mixed)
	}
	if len(logs) != 0 {
		t.Fatalf("unexpected fallback log: %v", logs)
	}
}

func TestVideoPoll_SolanaOwnership_RefusedFallsBackOnceToPaymentPolling(t *testing.T) {
	key := newTestPollKey(t)
	jobID := "vjt_legacy"
	// A job with no bound payer: the gateway refuses every proof.
	g := &fakeSolGateway{jobID: jobID, payer: key.Public().(ed25519.PublicKey), pendingFor: 2, refuseProof: true}
	srv := httptest.NewServer(http.HandlerFunc(g.handler))
	defer srv.Close()

	signer := &countingSigner{}
	var logs []string
	v := newOwnershipVideo(srv.URL, key, signer, &logs)
	job := &VideoJob{ID: jobID, Status: VideoStatusQueued, PollURL: srv.URL + "/v1/videos/generations/" + jobID}

	resp, err := v.Wait(context.Background(), job)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if resp.Data[0].URL != "https://cdn/v.mp4" {
		t.Fatalf("response: %+v", resp)
	}
	if g.proofPolls != 1 {
		t.Fatalf("proof sent on %d polls; want exactly 1 (fallback is sticky for the job)", g.proofPolls)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "falling back to x-payment polling") || !strings.Contains(logs[0], jobID) {
		t.Fatalf("want exactly one fallback log naming the job, got %v", logs)
	}
	// Two payment polls pending + one completing, each its own x402 signature.
	if signer.calls != 3 || g.paidPolls != 3 {
		t.Fatalf("signer calls %d paid polls %d; want 3/3", signer.calls, g.paidPolls)
	}
}

func TestVideoPoll_SolanaOwnership_FailedJobIsNeverCharged(t *testing.T) {
	key := newTestPollKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(pollHeaderSignature) == "" {
			t.Errorf("poll carried no ownership proof")
		}
		fmt.Fprint(w, `{"id":"vjt_f","status":"failed","error":"upstream failed","payment_status":"not_charged"}`)
	}))
	defer srv.Close()
	signer := &countingSigner{}
	var logs []string
	v := newOwnershipVideo(srv.URL, key, signer, &logs)
	job := &VideoJob{ID: "vjt_f", Status: VideoStatusQueued, PollURL: srv.URL + "/v1/videos/generations/vjt_f"}
	if _, err := v.Wait(context.Background(), job); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("Wait on a failed job: err=%v, want a failed-job error", err)
	}
	if job.Status != VideoStatusFailed || signer.calls != 0 {
		t.Fatalf("status %q signer calls %d; want failed/0", job.Status, signer.calls)
	}
}

func TestVideoPoll_BaseRailSendsNoOwnershipProof(t *testing.T) {
	var sawProof bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(pollHeaderWallet) != "" || r.Header.Get(pollHeaderSignature) != "" {
			sawProof = true
		}
		if r.Header.Get("X-Payment") == "" {
			w.Header().Set("Payment-Required", "challenge-poll")
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"status":"in_progress"}`)
	}))
	defer srv.Close()
	signer := &countingSigner{}
	var logs []string
	v := newOwnershipVideo(srv.URL, nil, signer, &logs) // Base: no poll key
	job := &VideoJob{ID: "j", Status: VideoStatusQueued, PollURL: srv.URL + "/v1/videos/generations/j"}
	if err := v.Poll(context.Background(), job); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if sawProof || signer.calls != 1 {
		t.Fatalf("Base poll: proof=%v signer calls=%d; want no proof and one payment", sawProof, signer.calls)
	}
}

func TestNewVideo_WiresPollKeyOnlyOnSolana(t *testing.T) {
	priv := newTestPollKey(t)
	v, err := NewVideo(WithChain("solana"), WithWalletKey(base58.Encode(priv)), WithBaseURL("http://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("NewVideo solana: %v", err)
	}
	if v.pollKey == nil || !v.pollKey.Equal(priv) {
		t.Fatal("Solana Video client does not hold the wallet's ed25519 key for poll proofs")
	}

	base, err := NewVideo(WithChain("base"), WithWalletKey("0x"+strings.Repeat("11", 32)), WithBaseURL("http://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("NewVideo base: %v", err)
	}
	if base.pollKey != nil {
		t.Fatal("Base Video client must not send Solana poll proofs")
	}
}
