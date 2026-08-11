package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Meta holds the enumerated names returned by GET /analytics/meta.
type Meta struct {
	Metrics       NameList `json:"metrics"`
	Dimensions    NameList `json:"dimensions"`
	Granularities NameList `json:"granularities"`
	Operators     NameList `json:"operators"`
}

// NameList decodes an array whose elements may be either plain strings or
// objects carrying a "name" field (the OpenRouter API returns the latter,
// e.g. {"name":"request_count","display_label":"...","display_format":"..."}).
type NameList []string

func (n *NameList) UnmarshalJSON(b []byte) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			out = append(out, s)
			continue
		}
		var obj struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(item, &obj); err == nil && obj.Name != "" {
			out = append(out, obj.Name)
		}
	}
	*n = out
	return nil
}

// metaResponse is the raw wire shape of GET /analytics/meta.
type metaResponse struct {
	Data *Meta `json:"data"`
}

// GetMeta calls GET /analytics/meta and returns the discovered names.
func (c *Client) GetMeta(ctx context.Context) (*Meta, error) {
	var raw metaResponse
	if err := c.Get(ctx, "/analytics/meta", &raw); err != nil {
		return nil, err
	}
	if raw.Data == nil {
		return nil, fmt.Errorf("openrouter: /analytics/meta returned empty data")
	}
	return raw.Data, nil
}

// AnalyticsQuery is the body of POST /analytics/query.
type AnalyticsQuery struct {
	Metrics     []string          `json:"metrics"`
	Dimensions  []string          `json:"dimensions,omitempty"`
	Granularity string            `json:"granularity,omitempty"`
	TimeRange   *TimeRange        `json:"time_range,omitempty"`
	Filters     []AnalyticsFilter `json:"filters,omitempty"`
	OrderBy     *OrderBy          `json:"order_by,omitempty"`
	Limit       int               `json:"limit,omitempty"`
}

type TimeRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type AnalyticsFilter struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

type OrderBy struct {
	Field string `json:"field"`
	Order string `json:"order"`
}

// AnalyticsResponse is the envelope of POST /analytics/query:
// { "data": { "data": [ ...rows... ], "cachedAt": number, "metadata": {...} } }
type AnalyticsResponse struct {
	Data AnalyticsData `json:"data"`
}

type AnalyticsData struct {
	Rows     []map[string]any `json:"data"`
	CachedAt float64          `json:"cachedAt"`
	Metadata analyticsMeta    `json:"metadata"`
}

type analyticsMeta struct {
	QueryTimeMS float64  `json:"query_time_ms"`
	RowCount    int      `json:"row_count"`
	Truncated   bool     `json:"truncated"`
	Warnings    []string `json:"warnings"`
}

// Truncated reports whether the response was partial.
func (a *AnalyticsResponse) Truncated() bool {
	return a.Data.Metadata.Truncated
}

// CachedAtTime returns the cachedAt as a time.Time (zero if absent).
func (a *AnalyticsResponse) CachedAtTime() time.Time {
	if a.Data.CachedAt <= 0 {
		return time.Time{}
	}
	// cachedAt may be seconds, ms, µs or ns — probe the scale to a sane year.
	return normalizeUnix(a.Data.CachedAt)
}

// QueryAnalytics calls POST /analytics/query.
func (c *Client) QueryAnalytics(ctx context.Context, q *AnalyticsQuery) (*AnalyticsResponse, error) {
	var out AnalyticsResponse
	if err := c.Post(ctx, "/analytics/query", q, &out); err != nil {
		return nil, err
	}
	if out.Data.Rows == nil {
		out.Data.Rows = []map[string]any{}
	}
	return &out, nil
}

// RowValue safely extracts a numeric value from an analytics row. Counts may
// be encoded as strings, so both string and number forms are accepted.
func RowValue(row map[string]any, key string) float64 {
	v, ok := row[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		var f float64
		fmt.Sscanf(n, "%f", &f)
		return f
	case bool:
		if n {
			return 1
		}
		return 0
	}
	return 0
}

// RowTimeKey finds the time-dimension key in a row. Analytics returns either
// "date__<granularity>" or "created_at__<granularity>". It returns the key name
// and the raw string value.
func RowTimeKey(row map[string]any) (name, value string, ok bool) {
	for k, v := range row {
		if len(k) < 5 {
			continue
		}
		prefix := k[:5]
		if prefix == "date__" || prefix == "creat" {
			if s, isStr := v.(string); isStr {
				return k, s, true
			}
			return k, fmt.Sprintf("%v", v), true
		}
	}
	return "", "", false
}
