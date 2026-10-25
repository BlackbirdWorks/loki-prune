// Package loki provides a client for interacting with Grafana Loki's HTTP API.
package loki

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrLokiFlushFailed indicates the /flush endpoint returned an unexpected status code.
	ErrLokiFlushFailed = errors.New("loki flush endpoint failed")
	// ErrLokiReadyFailed indicates the /ready endpoint returned a non-200 status code.
	ErrLokiReadyFailed = errors.New("loki ready endpoint failed")
)

const (
	maxResponseBodyRead = 1024
	defaultTimeout      = 30 * time.Second
	defaultMaxIdleConns = 100
	defaultIdlePerHost  = 10
	defaultIdleConnWait = 90 * time.Second
)

// Client interacts with the Loki HTTP API.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient creates a new Loki API client.
func NewClient(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	transport := &http.Transport{
		MaxIdleConns:        defaultMaxIdleConns,
		MaxIdleConnsPerHost: defaultIdlePerHost,
		IdleConnTimeout:     defaultIdleConnWait,
	}

	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
	}
}

// Flush triggers Loki to write all in-memory chunks/streams to disk.
// Endpoint: POST /flush.
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyRead))

		return fmt.Errorf("%w: HTTP %d: %s", ErrLokiFlushFailed, resp.StatusCode, string(body))
	}

	return nil
}

// Reload triggers Loki's runtime configuration or service reload.
// It ensures memory chunks are flushed and requests a runtime reload.
func (c *Client) Reload(ctx context.Context) error {
	if err := c.Flush(ctx); err != nil {
		return err
	}

	url := fmt.Sprintf("%s/runtime_config/reload", c.baseURL)
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if reqErr == nil {
		if resp, doErr := c.httpClient.Do(req); doErr == nil {
			_ = resp.Body.Close()
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
