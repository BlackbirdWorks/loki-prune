package loki_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blackbirdworks/loki-prune/pkg/loki"
)

func TestNewClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		timeout time.Duration
	}{
		{name: "standard with timeout", baseURL: "http://localhost:3100", timeout: 5 * time.Second},
		{name: "default timeout on zero", baseURL: "http://localhost:3100", timeout: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := loki.NewClient(tc.baseURL, tc.timeout)
			assert.NotNil(t, c)
		})
	}
}

func TestClient_Flush(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup   func(t *testing.T) *loki.Client
		wantErr func(t *testing.T, err error)
		name    string
	}{
		{
			name: "success 204 no content",
			setup: func(t *testing.T) *loki.Client {
				t.Helper()
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, "/flush", r.URL.Path)
					assert.Equal(t, http.MethodPost, r.Method)
					w.WriteHeader(http.StatusNoContent)
				}))
				t.Cleanup(ts.Close)

				return loki.NewClient(ts.URL, 5*time.Second)
			},
			wantErr: nil,
		},
		{
			name: "success 200 ok",
			setup: func(t *testing.T) *loki.Client {
				t.Helper()
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, "/flush", r.URL.Path)
					assert.Equal(t, http.MethodPost, r.Method)
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(ts.Close)

				return loki.NewClient(ts.URL, 5*time.Second)
			},
			wantErr: nil,
		},
		{
			name: "error 500 internal server error",
			setup: func(t *testing.T) *loki.Client {
				t.Helper()
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				t.Cleanup(ts.Close)

				return loki.NewClient(ts.URL, 5*time.Second)
			},
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, loki.ErrLokiFlushFailed)
			},
		},
		{
			name: "error 404 not found",
			setup: func(t *testing.T) *loki.Client {
				t.Helper()
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				}))
				t.Cleanup(ts.Close)

				return loki.NewClient(ts.URL, 5*time.Second)
			},
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, loki.ErrLokiFlushFailed)
			},
		},
		{
			name: "error invalid url",
			setup: func(t *testing.T) *loki.Client {
				t.Helper()

				return loki.NewClient("://invalid-url", 5*time.Second)
			},
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				assert.Error(t, err)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := tc.setup(t)
			err := c.Flush(context.Background())
			if tc.wantErr != nil {
				tc.wantErr(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestClient_Reload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup   func(t *testing.T) *loki.Client
		wantErr func(t *testing.T, err error)
		name    string
	}{
		{
			name: "success reload commits flush and reload endpoints",
			setup: func(t *testing.T) *loki.Client {
				t.Helper()
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/flush":
						w.WriteHeader(http.StatusNoContent)
					case "/runtime_config/reload":
						w.WriteHeader(http.StatusOK)
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				t.Cleanup(ts.Close)

				return loki.NewClient(ts.URL, 5*time.Second)
			},
			wantErr: nil,
		},
		{
			name: "error flush fails before reload",
			setup: func(t *testing.T) *loki.Client {
				t.Helper()

				return loki.NewClient("http://127.0.0.1:9", 50*time.Millisecond)
			},
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				assert.Error(t, err)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := tc.setup(t)
			err := c.Reload(context.Background())
			if tc.wantErr != nil {
				tc.wantErr(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestClient_Ready(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup    func(t *testing.T) *loki.Client
		name     string
		expected bool
	}{
		{
			name: "ready 200 ok",
			setup: func(t *testing.T) *loki.Client {
				t.Helper()
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, "/ready", r.URL.Path)
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(ts.Close)

				return loki.NewClient(ts.URL, 5*time.Second)
			},
			expected: true,
		},
		{
			name: "not ready 503 service unavailable",
			setup: func(t *testing.T) *loki.Client {
				t.Helper()
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				t.Cleanup(ts.Close)

				return loki.NewClient(ts.URL, 5*time.Second)
			},
			expected: false,
		},
		{
			name: "not ready invalid url",
			setup: func(t *testing.T) *loki.Client {
				t.Helper()

				return loki.NewClient("://invalid-url", 5*time.Second)
			},
			expected: false,
		},
		{
			name: "not ready network failure",
			setup: func(t *testing.T) *loki.Client {
				t.Helper()

				return loki.NewClient("http://127.0.0.1:9", 50*time.Millisecond)
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := tc.setup(t)
			assert.Equal(t, tc.expected, c.Ready(context.Background()))
		})
	}
}
