package openrouter

import (
	"context"
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
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Pricing pricingWire `json:"pricing"`
}

// pricingWire mirrors the pricing object returned by /models. Values arrive as
// decimal strings (e.g. "0.00000095").
type pricingWire struct {
	Prompt            string `json:"prompt"`
	Completion        string `json:"completion"`
	InputCacheRead    string `json:"input_cache_read"`
	InternalReasoning string `json:"internal_reasoning"`
}

// modelsResponse is the envelope of GET /models.
type modelsResponse struct {
	Data []modelsItem `json:"data"`
}

// ModelCatalog is the parsed /models catalog: per-token prices keyed by model
// slug (Pricing) plus an alias index (Aliases) that maps a normalized display
// name to its slug, so lookups work whether a call reports the slug or the
// human-readable model name.
type ModelCatalog struct {
	Pricing map[string]ModelPricing
	Aliases map[string]string
}

// ListModels fetches the full model catalog. The endpoint is public; the
// management-key Authorization header is harmless to include.
func (c *Client) ListModels(ctx context.Context) (*ModelCatalog, error) {
	var raw modelsResponse
	if err := c.Get(ctx, "/models", &raw); err != nil {
		return nil, err
	}
	cat := &ModelCatalog{
		Pricing: make(map[string]ModelPricing, len(raw.Data)),
		Aliases: make(map[string]string, len(raw.Data)),
	}
	for _, it := range raw.Data {
		id := normalizeModelID(it.ID)
		if id == "" {
			continue
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
		cat.Pricing[id] = p
		// alias the display name -> slug (only if it differs from the id)
		if name := normalizeModelID(it.Name); name != "" && name != id {
			cat.Aliases[name] = id
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
