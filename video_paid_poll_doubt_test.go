package vip

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// After maxVideoPaidPollRetries in-doubt paid polls in a row, Wait reports a
// typed error carrying the poll URL to resume from — never a bare 402 that
// reads as "nothing happened, submit again".
func TestVideoWait_Solana_PersistentDoubtReturnsResumableError(t *testing.T) {
	g := &settlingGateway{t: t, jobID: "vjt_stuck", paidAnswers: []string{"inflight"}}
	v, signer, job, _, done := newSettlingVideo(t, g)
	defer done()
	v.maxWait = 0 // never stop on the clock: the retry bound must end it
	v.pollInterval = 0
	job.retryAfter = 0

	// Shrink the 1s Retry-After to keep the test fast.
	orig := g.handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orig(stripRetryAfter{w}, r)
	}))
	defer srv.Close()
	job.PollURL = srv.URL + "/v1/videos/generations/" + g.jobID

	for i := 0; i < maxVideoPaidPollRetries+2 && !job.Done(); i++ {
		err := v.Poll(context.Background(), job)
		if err == nil {
			continue
		}
		var doubt *VideoPaymentInDoubtError
		if !errors.As(err, &doubt) {
			t.Fatalf("poll %d: err %T %v; want *VideoPaymentInDoubtError", i, err, err)
		}
		if doubt.PollURL != job.PollURL || doubt.JobID != job.ID {
			t.Fatalf("in-doubt error names %q/%q; want %q/%q", doubt.JobID, doubt.PollURL, job.ID, job.PollURL)
		}
		if signer.calls != maxVideoPaidPollRetries {
			t.Fatalf("signed %d payments before giving up; want %d", signer.calls, maxVideoPaidPollRetries)
		}
		return
	}
	t.Fatalf("never reported the payment as in doubt (signer calls %d)", signer.calls)
}

type stripRetryAfter struct{ http.ResponseWriter }

func (s stripRetryAfter) WriteHeader(code int) {
	s.ResponseWriter.Header().Del("Retry-After")
	s.ResponseWriter.WriteHeader(code)
}

// The account rail never sends a wallet proof, even if a key were present.
func TestVideoPoll_AccountRailSendsNoOwnershipProof(t *testing.T) {
	var sawProof bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(pollHeaderSignature) != "" {
			sawProof = true
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"status":"in_progress"}`)
	}))
	defer srv.Close()
	signer := &countingSigner{}
	var logs []string
	v := newOwnershipVideo(srv.URL, newTestPollKey(t), signer, &logs)
	v.accountMode = true
	job := &VideoJob{ID: "j", Status: VideoStatusQueued, PollURL: srv.URL + "/v1/videos/generations/j"}
	if err := v.Poll(context.Background(), job); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if sawProof || signer.calls != 0 {
		t.Fatalf("account poll: proof=%v signer=%d; want neither", sawProof, signer.calls)
	}
}

// NewVideo on Solana now decodes the wallet key up front (for the poll proof):
// an undecodable key fails at construction, not at the first payment.
func TestNewVideo_SolanaRejectsUndecodableKeyAtConstruction(t *testing.T) {
	if _, err := NewVideo(WithChain("solana"), WithWalletKey("not-base58-0OIl"), WithBaseURL("http://127.0.0.1:1")); err == nil {
		t.Fatal("want an error for an undecodable Solana key")
	}
}
