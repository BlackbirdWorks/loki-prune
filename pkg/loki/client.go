package loki

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client interacts with the Loki HTTP API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new Loki API client.
func NewClient(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// Flush triggers Loki to write all in-memory chunks/streams to disk.
// Endpoint: POST /flush
func (c *Client) Flush(ctx context.Context) error {
	url := fmt.Sprintf("%s/flush", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("creating flush request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling loki /flush: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("loki /flush returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// Reload triggers Loki's runtime configuration or service reload.
// Tries POST /flush first, then /runtime_config/reload if supported.
func (c *Client) Reload(ctx context.Context) error {
	// First ensure in-memory chunks are flushed so state is synchronized
	if err := c.Flush(ctx); err != nil {
		return err
	}

	// Attempt runtime config reload if endpoint exists (non-fatal if 404)
	url := fmt.Sprintf("%s/runtime_config/reload", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err == nil {
		if resp, err := c.httpClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}

	return nil
}

// Ready checks if Loki is responsive.
func (c *Client) Ready(ctx context.Context) bool {
	url := fmt.Sprintf("%s/ready", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}
