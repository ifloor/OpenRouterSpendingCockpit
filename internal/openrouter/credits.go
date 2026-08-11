package openrouter

import (
	"context"
)

// Credits holds the balance returned by GET /credits.
type Credits struct {
	TotalCredits float64 `json:"total_credits"`
	TotalUsage   float64 `json:"total_usage"`
}

// creditsResponse is the wire shape of GET /credits.
type creditsResponse struct {
	Data Credits `json:"data"`
}

// GetCredits calls GET /credits and returns the balance.
func (c *Client) GetCredits(ctx context.Context) (*Credits, error) {
	var raw creditsResponse
	if err := c.Get(ctx, "/credits", &raw); err != nil {
		return nil, err
	}
	return &raw.Data, nil
}
