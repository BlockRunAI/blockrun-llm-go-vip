package vip

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// settlingGateway models the paid half of sol's video poll route for one
// finished, bound job: a proof poll answers 402 {"status":"completed"} until
// the job settles and 200 already_settled after; a paid poll runs the script in
// paidAnswers (one entry per paid poll, last entry repeats).
type settlingGateway struct {
	t           *testing.T
	jobID       string
	payer       ed25519.PublicKey
	paidAnswers []string // "settle", "lost" (settle, then drop the connection), "inflight", "inflight-lands" (a held settle lands), "verifyfail", "invalid"
	noProofs    bool     // a gateway without poll proofs: answers every unpaid GET with a plain challenge

	mu         sync.Mutex
	proofPolls int
	paidPolls  int
	settled    bool
}

func (g *settlingGateway) handler(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	payment := r.Header.Get("X-Payment")
	if payment == "" {
		if r.Header.Get(pollHeaderSignature) != "" && !g.noProofs {
			g.proofPolls++
			if g.settled {
				fmt.Fprintf(w, `{"id":%q,"status":"completed","data":[{"url":"https://cdn/v.mp4"}],"payment":{"status":"already_settled"}}`, g.jobID)
				return
			}
			w.Header().Set("Payment-Required", "challenge-completed")
			w.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprintf(w, `{"error":"Payment Required","id":%q,"status":"completed"}`, g.jobID)
			return
		}
		w.Header().Set("Payment-Required", "challenge-poll")
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"error":"Payment Required"}`)
		return
	}
	g.paidPolls++
	answer := g.paidAnswers[len(g.paidAnswers)-1]
	if g.paidPolls <= len(g.paidAnswers) {
		answer = g.paidAnswers[g.paidPolls-1]
	}
	switch answer {
	case "settle":
		g.settled = true
		fmt.Fprintf(w, `{"id":%q,"status":"completed","data":[{"url":"https://cdn/v.mp4"}]}`, g.jobID)
	case "lost":
		// Settled on chain; the answer never reaches the client.
		g.settled = true
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			g.t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	case "inflight", "inflight-lands":
		g.settled = answer == "inflight-lands"
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"error":"Payment settlement outcome unknown"}`)
	case "pending":
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"id":%q,"status":"in_progress","payment_status":"verified"}`, g.jobID)
	case "verifyfail":
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"error":"Payment verification failed"}`)
	case "invalid":
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"Invalid job id"}`)
	default:
		g.t.Errorf("unknown scripted answer %q", answer)
	}
}

func newSettlingVideo(t *testing.T, g *settlingGateway) (*Video, *countingSigner, *VideoJob, *[]string, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(g.handler))
	key := newTestPollKey(t)
	g.payer = key.Public().(ed25519.PublicKey)
	signer := &countingSigner{}
	logs := &[]string{}
	v := newOwnershipVideo(srv.URL, key, signer, logs)
	job := &VideoJob{ID: g.jobID, Status: VideoStatusInProgress, PollURL: srv.URL + "/v1/videos/generations/" + g.jobID}
	return v, signer, job, logs, srv.Close
}

// A paid completion poll the gateway answers "settlement outcome unknown" must
// not end Wait: the next proof poll reads the job settled and returns the video
// with no second payment.
func TestVideoWait_Solana_PaidPollInDoubtRepollsInsteadOfFailing(t *testing.T) {
	// The held claim's settle lands while this poll waits it out.
	g := &settlingGateway{t: t, jobID: "vjt_doubt", paidAnswers: []string{"inflight-lands"}}
	v, signer, job, _, done := newSettlingVideo(t, g)
	defer done()

	start := time.Now()
	resp, err := v.Wait(context.Background(), job)
	if err != nil {
		t.Fatalf("Wait after an in-doubt paid poll: %v", err)
	}
	if resp.Data[0].URL != "https://cdn/v.mp4" {
		t.Fatalf("response %+v", resp)
	}
	if signer.calls != 1 || g.paidPolls != 1 {
		t.Fatalf("signer %d paid polls %d; want exactly one payment", signer.calls, g.paidPolls)
	}
	if waited := time.Since(start); waited < time.Second {
		t.Fatalf("re-polled after %s; want the gateway's Retry-After (1s) honoured", waited)
	}
}

// A paid completion poll whose answer is lost in transit: the payment settled,
// so Wait must re-poll (free, with the proof) and deliver — not return a
// transport error that invites the caller to submit and pay again.
func TestVideoWait_Solana_PaidPollLostInTransitIsRecovered(t *testing.T) {
	g := &settlingGateway{t: t, jobID: "vjt_lost", paidAnswers: []string{"lost"}}
	v, signer, job, _, done := newSettlingVideo(t, g)
	defer done()

	resp, err := v.Wait(context.Background(), job)
	if err != nil {
		t.Fatalf("Wait after a lost paid answer: %v", err)
	}
	if resp.Data[0].URL != "https://cdn/v.mp4" {
		t.Fatalf("response %+v", resp)
	}
	// net/http may itself re-send an idempotent GET whose connection dropped
	// (same request, same payment bytes); what must hold is ONE signed payment.
	if signer.calls != 1 {
		t.Fatalf("signer called %d times; want exactly one payment", signer.calls)
	}
}

// A 400 answering the PAYMENT (wallet binding) is not a refusal of the proof:
// one Poll must sign one payment, not re-run the payment flow.
func TestVideoPoll_Solana_PaidRetry400IsNotAProofRefusal(t *testing.T) {
	g := &settlingGateway{t: t, jobID: "vjt_bind", paidAnswers: []string{"invalid"}, noProofs: true}
	v, signer, job, _, done := newSettlingVideo(t, g)
	defer done()

	err := v.Poll(context.Background(), job)
	if err == nil {
		t.Fatal("Poll: want the paid poll's 400 as an error")
	}
	if signer.calls != 1 || g.paidPolls != 1 {
		t.Fatalf("one Poll signed %d payments over %d paid polls; want 1/1", signer.calls, g.paidPolls)
	}
}

// A payment refused at verification was never settled: report it at once,
// without signing more payments.
func TestVideoWait_Solana_VerificationFailureStopsWithoutRetrying(t *testing.T) {
	g := &settlingGateway{t: t, jobID: "vjt_broke", paidAnswers: []string{"verifyfail"}}
	v, signer, job, _, done := newSettlingVideo(t, g)
	defer done()

	_, err := v.Wait(context.Background(), job)
	if err == nil || !strings.Contains(err.Error(), "nothing charged") || !strings.Contains(err.Error(), job.PollURL) {
		t.Fatalf("Wait err=%v; want a not-charged error naming the poll URL", err)
	}
	if signer.calls != 1 {
		t.Fatalf("signer calls %d; want 1", signer.calls)
	}
}

// A transient 503 on a proof poll is "not yet", not a failed job.
func TestVideoPoll_Solana_Transient503KeepsPolling(t *testing.T) {
	key := newTestPollKey(t)
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"upstream unavailable"}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"status":"in_progress"}`)
	}))
	defer srv.Close()
	signer := &countingSigner{}
	var logs []string
	v := newOwnershipVideo(srv.URL, key, signer, &logs)
	job := &VideoJob{ID: "vjt_t", Status: VideoStatusQueued, PollURL: srv.URL + "/v1/videos/generations/vjt_t"}
	if err := v.Poll(context.Background(), job); err != nil {
		t.Fatalf("Poll on a 503: %v", err)
	}
	if err := v.Poll(context.Background(), job); err != nil || job.Status != VideoStatusInProgress {
		t.Fatalf("second Poll: err=%v status=%q", err, job.Status)
	}
	if signer.calls != 0 {
		t.Fatalf("signer calls %d; want 0", signer.calls)
	}
}

// A gateway that answers the proof with a plain payment challenge (no poll
// proofs) is switched to x-payment polling for the rest of the job, once.
func TestVideoPoll_Solana_PlainChallengeToProofFallsBackStickily(t *testing.T) {
	g := &settlingGateway{t: t, jobID: "vjt_old", paidAnswers: []string{"inflight"}, noProofs: true}
	var proofs int
	v, signer, job, logs, done := newSettlingVideo(t, g)
	defer done()
	inner := v.httpClient.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	v.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get(pollHeaderSignature) != "" {
			proofs++
		}
		return inner.RoundTrip(r)
	})
	// Make the paid polls answer "pending" so the job keeps going.
	g.paidAnswers = []string{"pending"}
	for i := 0; i < 3; i++ {
		if err := v.Poll(context.Background(), job); err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
	}
	if proofs != 1 {
		t.Fatalf("proof sent on %d polls; want 1 (fallback is sticky)", proofs)
	}
	if len(*logs) != 1 || !strings.Contains((*logs)[0], "falling back to x-payment polling") {
		t.Fatalf("want one fallback log, got %v", *logs)
	}
	if signer.calls != 3 {
		t.Fatalf("signer calls %d; want one per poll (3)", signer.calls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Base rail: a job that failed upstream (200 {"status":"failed"}) is failed,
// not "completed" with no video. This changed on every rail with the PR.
func TestVideoWait_BaseRail_FailedJobIsFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") == "" {
			w.Header().Set("Payment-Required", "challenge-poll")
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		fmt.Fprint(w, `{"id":"j","status":"failed","error":"moderated"}`)
	}))
	defer srv.Close()
	signer := &countingSigner{}
	var logs []string
	v := newOwnershipVideo(srv.URL, nil, signer, &logs)
	job := &VideoJob{ID: "j", Status: VideoStatusQueued, PollURL: srv.URL + "/v1/videos/generations/j"}
	if _, err := v.Wait(context.Background(), job); err == nil || job.Status != VideoStatusFailed {
		t.Fatalf("Base failed job: err=%v status=%q; want failed", err, job.Status)
	}
}
