package openrouter

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

// ModelPricing holds the per-token prices (in USD) for a model, derived from
// the /models endpoint. Prices rarely change, so it is safe to cache them for
// the whole run.
//
// Fallbacks are applied so that a model only ever needs `prompt` to produce a
// usable estimate:
//   - Cached falls back to Prompt when the model has no input_cache_read.
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

// pricingWire mirrors the pricing object returned by /models. Values arrive as
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

// ModelCatalog is a parsed, queryable view of GET /models. Prices are stored
// under the canonical slug ("author/model", e.g. "deepseek/deepseek-v4-flash")
// and indexed by the human-friendly display name as well, because callbacks
// (analytics rows, /generation) may report either form. The dated
// canonical_slug returned by /models is also indexed, so a lookup matches
// whether it arrives as the stable base id, the canonical slug or the display
// name; every spelling of the same model resolves to the same price entry.
//
// The endpoint reports a single aggregated price per model — OpenRouter does
// not expose per-provider prices here — so there is exactly one price per
// model, no matter which provider actually served a request.
type ModelCatalog struct {
	byModel map[string]ModelPricing // canonical slug -> price
	aliases map[string]string       // display name -> canonical slug
}

// Len returns the number of models in the catalog.
func (c *ModelCatalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.byModel)
}

// Lookup resolves a model id — a canonical slug or a display name — to its
// per-token price. It returns the price, the canonical slug it was indexed
// under (stable across lookups of the same model regardless of which spelling
// was used), and whether it matched.
func (c *ModelCatalog) Lookup(model string) (ModelPricing, string, bool) {
	if c == nil {
		return ModelPricing{}, "", false
	}
	// 1. exact canonical-slug hit
	if p, ok := c.byModel[model]; ok {
		return p, model, true
	}
	// 2. display-name alias -> slug
	if s, ok := c.aliases[model]; ok {
		if p, ok := c.byModel[s]; ok {
			return p, s, true
		}
	}
	// 3. tolerate variant/prefix spellings, e.g. "openai/gpt-4o:free"
	norm := normalizeModelID(model)
	if p, ok := c.byModel[norm]; ok {
		return p, norm, true
	}
	if s, ok := c.aliases[norm]; ok {
		if p, ok := c.byModel[s]; ok {
			return p, s, true
		}
	}
	return ModelPricing{}, "", false
}

// ListModels fetches the full model catalog. The endpoint is public; the
// management-key Authorization header is harmless to include.
func (c *Client) ListModels(ctx context.Context) (*ModelCatalog, error) {
	var raw modelsResponse
	if err := c.Get(ctx, "/models", &raw); err != nil {
		return nil, err
	}
	cat := &ModelCatalog{
		byModel: make(map[string]ModelPricing, len(raw.Data)),
		aliases: make(map[string]string, len(raw.Data)),
	}

	for _, it := range raw.Data {
		// Prefer the stable base id; fall back to the (dated) canonical slug
		// when the id is missing so the entry is not silently dropped.
		primary := normalizeModelID(it.ID)
		if primary == "" {
			primary = normalizeModelID(it.CanonicalSlug)
			if primary == "" {
				continue
			}
		}
		p := ModelPricing{
			Prompt:     parsePrice(it.Pricing.Prompt),
			Completion: parsePrice(it.Pricing.Completion),
			Cached:     parsePrice(it.Pricing.InputCacheRead),
			Reasoning:  parsePrice(it.Pricing.InternalReasoning),
		}
		// apply fallbacks
		if p.Cached == 0 {
			p.Cached = p.Prompt
		}
		if p.Reasoning == 0 {
			p.Reasoning = p.Completion
		}
		cat.byModel[primary] = p
		// Index the exact display name (colons, case and all) so lookups match
		// whatever raw form comes back from callbacks.
		if name := strings.TrimSpace(it.Name); name != "" && name != primary {
			cat.aliases[name] = primary
		}
		// A model may be addressed by its dated canonical slug (e.g. an
		// analytics row that pins a specific version); map it to the stable
		// base id so both spellings resolve to the same price entry.
		if cs := normalizeModelID(it.CanonicalSlug); cs != "" && cs != primary {
			cat.aliases[cs] = primary
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
