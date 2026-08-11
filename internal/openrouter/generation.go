package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Generation holds the per-call metadata returned by GET /generation.
type Generation struct {
	ID                     string  `json:"id"`
	ProviderName           string  `json:"provider_name"`
	Model                  string  `json:"model"`
	ModelPermaSlug         string  `json:"model_permaslug"`
	TokensPrompt           int64   `json:"tokens_prompt"`
	TokensCompletion       int64   `json:"tokens_completion"`
	NativeTokensPrompt     int64   `json:"native_tokens_prompt"`
	NativeTokensCompletion int64   `json:"native_tokens_completion"`
	NativeTokensReasoning  int64   `json:"native_tokens_reasoning"`
	NativeTokensCached     int64   `json:"native_tokens_cached"`
	TotalCost              float64 `json:"total_cost"`
	Latency                float64 `json:"latency"`
	GenerationTime         float64 `json:"generation_time"`
	FinishReason           string  `json:"finish_reason"`
	CreatedAt              string  `json:"created_at"`
	IsByok                 bool    `json:"is_byok"`
	Streamed               *bool   `json:"streamed,omitempty"`
	ServiceTier            string  `json:"service_tier"`
	RequestID              string  `json:"request_id"`
	UpstreamID             string  `json:"upstream_id"`
	WorkspaceID            string  `json:"workspace_id"`
}

// Usage holds token usage breakdown (when present in /generation).
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
}

// generationResponse is the wire shape of GET /generation.
type generationResponse struct {
	Data Generation `json:"data"`
}

// GetGeneration calls GET /generation?id=<id>.
func (c *Client) GetGeneration(ctx context.Context, id string) (*Generation, error) {
	var raw generationResponse
	if err := c.Get(ctx, "/generation?id="+id, &raw); err != nil {
		return nil, err
	}
	g := raw.Data
	// Some responses nest usage; if the flattened fields are empty but usage
	// is present, fall back. The response does not always include usage; when
	// it does we surface it through the standard fields where possible.
	if g.CreatedAt == "" {
		return nil, fmt.Errorf("openrouter: /generation returned empty data for %s", id)
	}
	return &g, nil
}

// ParseGenerationTime converts a generation created_at into a time.Time.
// The API may return either an ISO string or a Unix timestamp (number/string),
// possibly in a millisecond/microsecond/nanosecond scale. Out-of-range results
// are clamped to the zero time so JSON marshaling never fails.
func ParseGenerationTime(raw any) time.Time {
	if raw == nil {
		return time.Time{}
	}
	switch v := raw.(type) {
	case float64:
		return normalizeUnix(v)
	case int64:
		return normalizeUnix(float64(v))
	case int:
		return normalizeUnix(float64(v))
	case json.Number:
		i, err := v.Int64()
		if err == nil {
			return normalizeUnix(float64(i))
		}
		f, _ := v.Float64()
		return normalizeUnix(f)
	case string:
		t, err := time.Parse(time.RFC3339, v)
		if err == nil {
			if !validTime(t) {
				return time.Time{}
			}
			return t.UTC()
		}
		var i int64
		if _, err := fmt.Sscanf(v, "%d", &i); err == nil {
			return normalizeUnix(float64(i))
		}
	}
	return time.Time{}
}

// normalizeUnix converts a Unix timestamp to time, probing common scales
// (seconds, ms, µs, ns) and returning the first that yields a sane year.
func normalizeUnix(v float64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	for _, divisor := range []float64{1, 1e3, 1e6, 1e9} {
		t := time.Unix(int64(v/divisor), 0).UTC()
		if validTime(t) {
			return t
		}
	}
	return time.Time{}
}

// validTime reports whether t can be marshaled to JSON (ISO-8601 years must
// be within [0, 9999]; we additionally require a sane modern year).
func validTime(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	y := t.Year()
	return y >= 2000 && y <= 2200
}
