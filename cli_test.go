package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCLI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup   func(t *testing.T) []string
		wantErr func(t *testing.T, err error)
		name    string
	}{
		{
			name: "help flag",
			setup: func(_ *testing.T) []string {
				return []string{"--help"}
			},
			wantErr: nil,
		},
		{
			name: "version flag",
			setup: func(_ *testing.T) []string {
				return []string{"--version"}
			},
			wantErr: nil,
		},
		{
			name: "invalid flag",
			setup: func(_ *testing.T) []string {
				return []string{"--invalid-flag-that-does-not-exist"}
			},
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				assert.Error(t, err)
			},
		},
		{
			name: "valid config with once and dry-run",
			setup: func(t *testing.T) []string {
				t.Helper()
				tempDir := t.TempDir()
				cfgPath := filepath.Join(tempDir, "config.yaml")
				content := "paths:\n  - path: \"" + tempDir + "\"\n    trigger_size: \"10GB\"\n"
				require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0600))

				return []string{"--config", cfgPath, "--once", "--dry-run"}
			},
			wantErr: nil,
		},
		{
			name: "missing config file",
			setup: func(_ *testing.T) []string {
				return []string{"--config", "/non/existent/config.yaml", "--once"}
			},
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				assert.Error(t, err)
			},
		},
		{
			name: "invalid path config causes rotator error",
			setup: func(t *testing.T) []string {
				t.Helper()
				tempDir := t.TempDir()
				cfgPath := filepath.Join(tempDir, "config.yaml")
				content := "paths:\n  - path: \"" + tempDir + "\"\n" +
					"    trigger_size: \"10GB\"\n    rotation:\n      type: \"unsupported\"\n"
				require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0600))

				return []string{"--config", cfgPath, "--once"}
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
			args := tc.setup(t)

			var out bytes.Buffer
			var errOut bytes.Buffer
			err := RunCLI(args, &out, &errOut)
			if tc.wantErr != nil {
				tc.wantErr(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRunMain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		args         []string
		expectedCode int
	}{
		{
			name:         "help returns zero exit code",
			args:         []string{"--help"},
			expectedCode: 0,
		},
		{
			name:         "invalid argument returns exit code 1",
			args:         []string{"--invalid-flag"},
			expectedCode: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code := runMain(tc.args)
			assert.Equal(t, tc.expectedCode, code)
		})
	}
}

func TestRunDaemon(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		interval time.Duration
	}{
		{name: "daemon runs tick and shuts down on context cancel", interval: 10 * time.Millisecond},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cli := &CLI{}
			ctx, cancel := context.WithCancel(context.Background())
			var count atomic.Int32

			done := make(chan struct{})
			go func() {
				_ = cli.runDaemon(ctx, tc.interval, func() {
					count.Add(1)
				})
				close(done)
			}()

			assert.Eventually(t, func() bool {
				return count.Load() >= 1
			}, 2*time.Second, 10*time.Millisecond)

			cancel()
			<-done
		})
	}
}
