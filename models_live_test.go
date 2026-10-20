package vip

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"
)

// TestModelCatalogMatchesLiveGateway diffs the snapshot against the live
// GET /v1/models: every listed model must be in the snapshot with the same
// metadata and prices, and every snapshot entry must still be listed. The
// per-model "available" health flag is ignored; it flaps with load. Opt-in because it needs network:
//
//	BLOCKRUN_LIVE_CATALOG=1 go test -run TestModelCatalogMatchesLiveGateway
func TestModelCatalogMatchesLiveGateway(t *testing.T) {
	if os.Getenv("BLOCKRUN_LIVE_CATALOG") == "" {
		t.Skip("BLOCKRUN_LIVE_CATALOG not set, skipping live catalog diff")
	}
	url := os.Getenv("BLOCKRUN_CATALOG_URL")
	if url == "" {
		url = "https://blockrun.ai/api/v1/models"
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	var catalog struct {
		Data []struct {
			ID            string         `json:"id"`
			Name          string         `json:"name"`
			OwnedBy       string         `json:"owned_by"`
			Categories    []string       `json:"categories"`
			ContextWindow int            `json:"context_window"`
			MaxOutput     int            `json:"max_output"`
			BillingMode   string         `json:"billing_mode"`
			Available     *bool          `json:"available"`
			Pricing       map[string]any `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	num := func(p map[string]any, key string) float64 {
		v, _ := p[key].(float64)
		return v
	}
	live := map[string]bool{}
	for _, m := range catalog.Data {
		live[m.ID] = true
		got, ok := FindModel(m.ID)
		if !ok {
			t.Errorf("%s: listed live but missing from the snapshot", m.ID)
			continue
		}
		want := Model{
			ID: m.ID, Name: m.Name, Provider: m.OwnedBy, Categories: m.Categories,
			ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput, BillingMode: m.BillingMode,
			Pricing: ModelPricing{
				InputPerMillion:        num(m.Pricing, "input"),
				OutputPerMillion:       num(m.Pricing, "output"),
				PerImage:               num(m.Pricing, "per_image"),
				PerSecond:              num(m.Pricing, "per_second"),
				PerThousandChars:       num(m.Pricing, "per_1k_chars"),
				PerTrack:               num(m.Pricing, "per_track"),
				PerGeneration:          num(m.Pricing, "per_generation"),
				MaxInputChars:          int(num(m.Pricing, "max_input_chars")),
				DefaultDurationSeconds: int(num(m.Pricing, "default_duration_seconds")),
				MaxDurationSeconds:     int(num(m.Pricing, "max_duration_seconds")),
			},
		}
		if got.Name != want.Name || got.Provider != want.Provider || !slices.Equal(got.Categories, want.Categories) ||
			got.ContextWindow != want.ContextWindow || got.MaxOutput != want.MaxOutput ||
			got.BillingMode != want.BillingMode || got.Pricing != want.Pricing {
			t.Errorf("%s: snapshot %+v, live %+v", m.ID, got, want)
		}
	}
	for _, m := range Models() {
		if !live[m.ID] {
			t.Errorf("%s: in the snapshot but no longer listed by the gateway", m.ID)
		}
	}
}
