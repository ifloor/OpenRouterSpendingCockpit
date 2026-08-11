package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://openrouter.ai/api/v1"
)

// APIError represents a non-2xx response from the OpenRouter API.
type APIError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("openrouter: %s: %s", e.Status, truncate(e.Body, 300))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Client is a minimal REST client for the OpenRouter API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	logf    func(format string, args ...any)
}

// NewClient builds a client. apiKey is the management key used for
// /analytics, /credits and /generation endpoints.
func NewClient(apiKey string) *Client {
	return NewClientWithBaseURL(DefaultBaseURL, apiKey)
}

// NewClientWithBaseURL builds a client that talks to an arbitrary base URL
// (used by tests to point at a local fake server).
func NewClientWithBaseURL(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 10 * time.Second},
		logf:    func(string, ...any) {},
	}
}

// SetLogger attaches a logger for verbose output.
func (c *Client) SetLogger(f func(format string, args ...any)) {
	if f != nil {
		c.logf = f
	}
}

// MaskedAPIKey returns a masked version of the key for logs.
func (c *Client) MaskedAPIKey() string {
	if len(c.apiKey) <= 8 {
		return "****"
	}
	return c.apiKey[:4] + "…" + c.apiKey[len(c.apiKey)-4:]
}

// Get performs an authenticated GET request and decodes JSON into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// Post performs an authenticated POST request with a JSON body and decodes
// the response into out.
func (c *Client) Post(ctx context.Context, path string, body any, out any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Status: resp.Status, Body: string(body)}
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("openrouter: decode %s: %w (body: %s)", req.URL.Path, err, truncate(string(body), 300))
	}
	return nil
}
