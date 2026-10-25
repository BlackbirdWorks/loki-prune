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

func TestClient_Flush(t *testing.T) {
	t.Parallel()

	type testCase struct {
		wantErr    error
		name       string
		statusCode int
	}

	tests := []testCase{
		{
			name:       "success 204 no content",
			statusCode: http.StatusNoContent,
			wantErr:    nil,
		},
		{
			name:       "success 200 ok",
			statusCode: http.StatusOK,
			wantErr:    nil,
		},
		{
			name:       "error 500 internal server error",
			statusCode: http.StatusInternalServerError,
			wantErr:    loki.ErrLokiFlushFailed,
		},
		{
			name:       "error 404 not found",
			statusCode: http.StatusNotFound,
			wantErr:    loki.ErrLokiFlushFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/flush", r.URL.Path)
				assert.Equal(t, http.MethodPost, r.Method)
				w.WriteHeader(tc.statusCode)
			}))
			defer ts.Close()

			client := loki.NewClient(ts.URL, 5*time.Second)
			err := client.Flush(context.Background())
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestNewClient_DefaultTimeout(t *testing.T) {
	t.Parallel()
	client := loki.NewClient("http://localhost:3100", 0)
	assert.NotNil(t, client)
}

func TestClient_Flush_InvalidURL(t *testing.T) {
	t.Parallel()
	client := loki.NewClient("://invalid-url", 5*time.Second)
	err := client.Flush(context.Background())
	require.Error(t, err)
}

func TestClient_Reload_FlushError(t *testing.T) {
	t.Parallel()
	client := loki.NewClient("http://127.0.0.1:9", 50*time.Millisecond)
	err := client.Reload(context.Background())
	require.Error(t, err)
}

func TestClient_Ready_InvalidURL(t *testing.T) {
	t.Parallel()
	client := loki.NewClient("://invalid-url", 5*time.Second)
	assert.False(t, client.Ready(context.Background()))
}

func TestClient_Reload(t *testing.T) {
	t.Parallel()

	flushed := false
	reloaded := false

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/flush":
			flushed = true
			w.WriteHeader(http.StatusNoContent)
		case "/runtime_config/reload":
			reloaded = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client := loki.NewClient(ts.URL, 5*time.Second)
	err := client.Reload(context.Background())
	require.NoError(t, err)
	assert.True(t, flushed)
	assert.True(t, reloaded)
}

func TestClient_Ready(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name       string
		statusCode int
		expected   bool
	}

	tests := []testCase{
		{name: "ready 200 ok", statusCode: http.StatusOK, expected: true},
		{name: "not ready 503 service unavailable", statusCode: http.StatusServiceUnavailable, expected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/ready", r.URL.Path)
				w.WriteHeader(tc.statusCode)
			}))
			defer ts.Close()

			client := loki.NewClient(ts.URL, 5*time.Second)
			assert.Equal(t, tc.expected, client.Ready(context.Background()))
		})
	}

	t.Run("network error returns false", func(t *testing.T) {
		t.Parallel()
		client := loki.NewClient("http://127.0.0.1:9", 50*time.Millisecond)
		assert.False(t, client.Ready(context.Background()))
	})
}
