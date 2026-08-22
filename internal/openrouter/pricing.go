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

// ModelCatalog is a parsed, queryable view of GET /models. Prices are stored
// under the canonical slug ("author/model", e.g. "deepseek/deepseek-v4-flash")
// and indexed by the human-friendly display name as well, because callbacks
// (analytics rows, /generation) may report either form.
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
		slug := normalizeModelID(it.ID)
		if slug == "" {
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
		cat.byModel[slug] = p
		// Index the exact display name (colons, case and all) so lookups match
		// whatever raw form comes back from callbacks.
		if name := strings.TrimSpace(it.Name); name != "" && name != slug {
			cat.aliases[name] = slug
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
