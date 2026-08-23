package openrouter

import (
	"context"
	"fmt"
	"strings"
)

// ProviderEndpoint is one entry of GET /models/{author}/{model}/endpoints.
// Each endpoint belongs to a single provider and carries that provider's own
// per-token pricing, which may differ from the aggregated pricing on /models.
type ProviderEndpoint struct {
	Name             string      `json:"name"`
	ModelID          string      `json:"model_id"`
	ModelName        string      `json:"model_name"`
	ProviderName     string      `json:"provider_name"`
	Tag              string      `json:"tag"`
	Pricing          pricingWire `json:"pricing"`
	ContextLength    int         `json:"context_length"`
	MaxTokenCapacity int         `json:"max_token_capacity"`
}

// modelEndpointsResponse is the envelope of GET /models/{author}/{model}/endpoints.
type modelEndpointsResponse struct {
	Data struct {
		ID        string             `json:"id"`
		Endpoints []ProviderEndpoint `json:"endpoints"`
	} `json:"data"`
}

// ProviderPricing fetches the per-provider pricing list for a model. The model
// argument is the stable "author/slug" id (a date-less canonical slug). The
// returned slice is never nil, so callers can range over it directly.
func (c *Client) ProviderPricing(ctx context.Context, model string) ([]ProviderEndpoint, error) {
	model = strings.TrimLeft(model, "/")
	if model == "" {
		return nil, fmt.Errorf("openrouter: empty model for endpoints lookup")
	}
	// The canonical endpoint path uses the OpenRouter id "author/slug".
	path := "/models/" + model + "/endpoints"

	var raw modelEndpointsResponse
	if err := c.Get(ctx, path, &raw); err != nil {
		return nil, err
	}
	if raw.Data.Endpoints == nil {
		return []ProviderEndpoint{}, nil
	}
	return raw.Data.Endpoints, nil
}
