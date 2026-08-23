package openrouter

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

// ModelPricing holds the per-token prices (in USD) for a model as served by a
// single provider, derived from GET /models/{author}/{model}/endpoints.
//
// Fallbacks are applied so that a model only ever needs `prompt` to produce a
// usable estimate:
//   - Cached falls back to Prompt when the provider has no input_cache_read.
//   - Reasoning falls back to Completion when there is no internal_reasoning.
type ModelPricing struct {
	Prompt     float64 // $ per non-cached input token
	Completion float64 // $ per output token
	Cached     float64 // $ per cached input token
	Reasoning  float64 // $ per reasoning token
}

// modelsItem is one entry of GET /models response data[].
type modelsItem struct {
	ID string `json:"id"`
	// CanonicalSlug is the fully-qualified, dated slug for this model (e.g.
	// "meta/muse-spark-1.2-contributor-20260805"). Callbacks and analytics
	// rows may reference a model by either this or ID.
	CanonicalSlug       string             `json:"canonical_slug"`
	HuggingFaceID       *string            `json:"hugging_face_id"`
	Name                string             `json:"name"`
	Created             int64              `json:"created"`
	Description         string             `json:"description"`
	ContextLength       int                `json:"context_length"`
	Architecture        *modelArchitecture `json:"architecture"`
	Pricing             pricingWire        `json:"pricing"`
	TopProvider         *modelTopProvider  `json:"top_provider"`
	PerRequestLimits    json.RawMessage    `json:"per_request_limits"`
	SupportedParameters []string           `json:"supported_parameters"`
	DefaultParameters   map[string]any     `json:"default_parameters"`
	SupportedVoices     json.RawMessage    `json:"supported_voices"`
	KnowledgeCutoff     *string            `json:"knowledge_cutoff"`
	ExpirationDate      *string            `json:"expiration_date"`
	Links               *modelLinks        `json:"links"`
	Reasoning           *modelReasoning    `json:"reasoning"`
}

// modelArchitecture describes the model's input/output modality.
type modelArchitecture struct {
	Modality         string   `json:"modality"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
	Tokenizer        string   `json:"tokenizer"`
	InstructType     *string  `json:"instruct_type"`
}

// modelTopProvider summarizes the model as served by its top provider.
type modelTopProvider struct {
	ContextLength       int  `json:"context_length"`
	MaxCompletionTokens *int `json:"max_completion_tokens"`
	IsModerated         bool `json:"is_moderated"`
}

// modelLinks holds API links related to the model.
type modelLinks struct {
	Details string `json:"details"`
}

// modelReasoning describes the model's reasoning/thinking configuration.
type modelReasoning struct {
	Mandatory        bool     `json:"mandatory"`
	SupportedEfforts []string `json:"supported_efforts"`
	DefaultEffort    string   `json:"default_effort"`
}

// pricingWire mirrors the pricing object returned by the API. Values arrive as
// decimal strings (e.g. "0.00000095").
type pricingWire struct {
	Prompt            string `json:"prompt"`
	Completion        string `json:"completion"`
	WebSearch         string `json:"web_search"`
	InputCacheRead    string `json:"input_cache_read"`
	InternalReasoning string `json:"internal_reasoning"`
}

// modelsResponse is the envelope of GET /models.
type modelsResponse struct {
	Data []modelsItem `json:"data"`
}

// ModelCatalog is a parsed, queryable view of GET /models, kept in memory for
// the whole run. It answers one question: "what does model X cost when served
// by provider P?".
//
// The aggregated /models endpoint tells us which models exist and how a model
// id/name maps to a canonical slug, but its pricing is a blend across
// providers. Real provider pricing lives behind
// GET /models/{author}/{model}/endpoints, which this type loads lazily (on
// first use of a model) and caches in memory for the whole run — per-provider
// prices are static for practical purposes.
type ModelCatalog struct {
	client *Client

	mu      sync.Mutex
	aliases map[string]string // display name OR canonical slug -> canonical slug
	// known records every canonical slug reported by /models so lookups for
	// models outside the catalog short-circuit without a network call.
	known map[string]bool

	// providers holds, per canonical slug, the price per provider.
	providers map[string]map[string]ModelPricing
	// loaded tracks canonical slugs whose endpoint list was already fetched,
	// so a model is only fetched once even across poll ticks.
	loaded map[string]bool
}

// Len returns the number of models known to the catalog.
func (c *ModelCatalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.known)
}

// Canonical resolves any spelling of a model — a canonical slug, the dated
// canonical_slug or the display name — to the canonical slug.
func (c *ModelCatalog) Canonical(model string) string {
	return c.canonical(model)
}

// canonical resolves any spelling of a model — a canonical slug, the dated
// canonical_slug or the display name — to the canonical slug.
func (c *ModelCatalog) canonical(model string) string {
	if c == nil {
		return ""
	}
	if s, ok := c.aliases[model]; ok {
		return s
	}
	// tolerate variant/prefix spellings, e.g. "openai/gpt-4o:free"
	norm := normalizeModelID(model)
	if s, ok := c.aliases[norm]; ok {
		return s
	}
	return normalizeModelID(model)
}

// ProviderPrice returns the per-token price for a (model, provider) pair. It
// resolves the model to its canonical slug and, if the per-provider list for
// that model is not loaded yet, attempts a lazy fetch. ok is false when the
// model is unknown, the provider is not among the model's endpoints, or the
// endpoint list could not be fetched.
func (c *ModelCatalog) ProviderPrice(ctx context.Context, model, provider string) (ModelPricing, bool) {
	if c == nil {
		return ModelPricing{}, false
	}
	canonical := c.canonical(model)

	// Unknown model: short-circuit without touching the network.
	c.mu.Lock()
	known := c.known[canonical]
	loaded := c.loaded[canonical]
	c.mu.Unlock()
	if !known {
		return ModelPricing{}, false
	}

	if !loaded {
		if err := c.loadProviders(ctx, canonical); err != nil {
			return ModelPricing{}, false
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	byProv := c.providers[canonical]
	if p, ok := byProv[provider]; ok {
		return p, true
	}
	// Case-insensitive fallback for provider names.
	for name, p := range byProv {
		if strings.EqualFold(name, provider) {
			return p, true
		}
	}
	return ModelPricing{}, false
}

// loadProviders fetches and caches the per-provider pricing for a model. It is
// safe to call concurrently; cache population is guarded by the mutex. Network
// failures are not cached so a transient error retries on the next call.
func (c *ModelCatalog) loadProviders(ctx context.Context, canonical string) error {
	if canonical == "" {
		return nil
	}
	endpoints, err := c.client.ProviderPricing(ctx, canonical)
	if err != nil {
		return err
	}
	prices := make(map[string]ModelPricing, len(endpoints))
	for _, ep := range endpoints {
		if ep.ProviderName == "" {
			continue
		}
		p := ModelPricing{
			Prompt:     parsePrice(ep.Pricing.Prompt),
			Completion: parsePrice(ep.Pricing.Completion),
			Cached:     parsePrice(ep.Pricing.InputCacheRead),
			Reasoning:  parsePrice(ep.Pricing.InternalReasoning),
		}
		if p.Cached == 0 {
			p.Cached = p.Prompt
		}
		if p.Reasoning == 0 {
			p.Reasoning = p.Completion
		}
		prices[ep.ProviderName] = p
	}

	c.mu.Lock()
	c.providers[canonical] = prices
	// Mark as loaded even if empty — the model is known; it may have no
	// advertised per-provider pricing.
	c.loaded[canonical] = true
	c.mu.Unlock()
	return nil
}

// ListModels fetches the model catalog and returns it ready for lazy
// per-provider lookups. The management-key Authorization header is harmless to
// include despite the endpoint being public.
func (c *Client) ListModels(ctx context.Context) (*ModelCatalog, error) {
	var raw modelsResponse
	if err := c.Get(ctx, "/models", &raw); err != nil {
		return nil, err
	}
	cat := &ModelCatalog{
		client:    c,
		aliases:   make(map[string]string),
		known:     make(map[string]bool),
		providers: make(map[string]map[string]ModelPricing),
		loaded:    make(map[string]bool),
	}

	for _, it := range raw.Data {
		slug := normalizeModelID(it.ID)
		if slug == "" {
			continue
		}
		cat.known[slug] = true
		// Index the exact display name (colons, case and all) so lookups match
		// whatever raw form comes back from callbacks.
		if name := strings.TrimSpace(it.Name); name != "" && name != slug {
			cat.aliases[name] = slug
		}
		// A model may be addressed by its dated canonical slug (e.g. an
		// analytics row that pins a specific version); map it to the stable
		// base id so both spellings resolve to the same price entry.
		if cs := normalizeModelID(it.CanonicalSlug); cs != "" && cs != slug {
			cat.aliases[cs] = slug
		}
	}
	return cat, nil
}

// NormalizeModelID canonicalizes a model id for cache lookups: it drops a
// leading provider-wildcard "~" and returns the base id (variant suffix like
// ":free" / ":nitro" is stripped so a lookup still hits the catalog entry).
func NormalizeModelID(id string) string {
	return normalizeModelID(id)
}

func normalizeModelID(id string) string {
	s := strings.TrimPrefix(strings.TrimSpace(id), "~")
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	return s
}

func parsePrice(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}
