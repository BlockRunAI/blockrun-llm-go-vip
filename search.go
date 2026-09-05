package vip

import (
	"context"

	blockrun "github.com/BlockRunAI/blockrun-llm-go"
)

// Search (xAI Grok Live Search) and Exa (neural web search) through the BlockRun
// gateway, paid per call in USDC (x402) on Base.
//
// In blockrun-llm-go these live as methods on the catch-all LLMClient. VIP exposes
// them as focused Search and Exa clients — each wraps an internal LLMClient with VIP
// wallet resolution and surfaces only the search methods, so the VIP API stays
// scoped to AI generation. Responses are returned verbatim.

// SearchOptions / SearchResult re-exported for ergonomic use with Search.Search.
type (
	SearchOptions = blockrun.SearchOptions
	SearchResult  = blockrun.SearchResult
)

// Search runs xAI Grok Live Search over X (Twitter), the web, and/or news, returning
// a grounded summary plus citations.
type Search struct {
	llm *blockrun.LLMClient
}

// NewSearch returns a Grok Live Search client, paid per call via x402 on Base
// (default) or Solana (WithChain("solana")).
//
//	s, _ := vip.NewSearch()
//	r, _ := s.Search(ctx, "latest on x402 micropayments", &vip.SearchOptions{
//	    Sources: []string{"x", "news"}, MaxResults: 15,
//	})
func NewSearch(opts ...Option) (*Search, error) {
	llm, err := newLLM(opts...)
	if err != nil {
		return nil, err
	}
	return &Search{llm: llm}, nil
}

// Search performs a single live search and returns the grounded result.
func (s *Search) Search(ctx context.Context, query string, opts *SearchOptions) (*SearchResult, error) {
	return s.llm.Search(ctx, query, opts)
}

// Exa is the Exa neural web-search surface: Search, FindSimilar, Contents, and Answer.
// Each returns Exa's response verbatim as a decoded JSON map. The optional extra map
// forwards Exa parameters (numResults, category, includeDomains, …).
type Exa struct {
	llm *blockrun.LLMClient
}

// NewExa returns an Exa web-search client, paid per call via x402 on Base
// (default) or Solana (WithChain("solana")).
//
//	exa, _ := vip.NewExa()
//	hits, _ := exa.Search(ctx, "x402 protocol", map[string]any{"numResults": 5})
func NewExa(opts ...Option) (*Exa, error) {
	llm, err := newLLM(opts...)
	if err != nil {
		return nil, err
	}
	return &Exa{llm: llm}, nil
}

// Search runs a neural/keyword Exa web search.
func (e *Exa) Search(ctx context.Context, query string, extra map[string]any) (map[string]any, error) {
	return e.llm.ExaSearch(ctx, query, extra)
}

// FindSimilar returns pages similar to a reference URL.
func (e *Exa) FindSimilar(ctx context.Context, pageURL string, extra map[string]any) (map[string]any, error) {
	return e.llm.ExaFindSimilar(ctx, pageURL, extra)
}

// Contents extracts the full text of the given URLs (priced per URL).
func (e *Exa) Contents(ctx context.Context, urls []string, extra map[string]any) (map[string]any, error) {
	return e.llm.ExaContents(ctx, urls, extra)
}

// Answer returns a grounded answer with supporting sources.
func (e *Exa) Answer(ctx context.Context, query string, extra map[string]any) (map[string]any, error) {
	return e.llm.ExaAnswer(ctx, query, extra)
}

// newLLM builds an internal blockrun-llm-go LLMClient with VIP wallet resolution. It
// backs the Search and Exa wrappers, which surface only the search methods.
func newLLM(opts ...Option) (*blockrun.LLMClient, error) {
	cfg, key, err := resolveKey(opts...)
	if err != nil {
		return nil, err
	}
	if cfg.accountMode() {
		return blockrun.NewLLMClientWithAPIKey(cfg.apiKey, blockrun.WithAPIURL(cfg.apiURL))
	}
	if cfg.isSolana() {
		return blockrun.NewLLMClientSolana(key, cfg.solanaRPCURL, blockrun.WithAPIURL(cfg.apiURL))
	}
	return blockrun.NewLLMClient(key, blockrun.WithAPIURL(cfg.apiURL))
}
