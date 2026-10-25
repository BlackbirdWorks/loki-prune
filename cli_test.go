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

	type testCase struct {
		setup   func(t *testing.T) (args []string, cleanup func())
		name    string
		wantErr bool
	}

	tests := []testCase{
		{
			name: "help flag",
			setup: func(_ *testing.T) ([]string, func()) {
				return []string{"--help"}, func() {}
			},
			wantErr: false,
		},
		{
			name: "version flag",
			setup: func(_ *testing.T) ([]string, func()) {
				return []string{"--version"}, func() {}
			},
			wantErr: false,
		},
		{
			name: "invalid flag",
			setup: func(_ *testing.T) ([]string, func()) {
				return []string{"--invalid-flag-that-does-not-exist"}, func() {}
			},
			wantErr: true,
		},
		{
			name: "valid config with once and dry-run",
			setup: func(t *testing.T) ([]string, func()) {
				t.Helper()
				tempDir := t.TempDir()
				cfgPath := filepath.Join(tempDir, "config.yaml")
				content := `
paths:
  - path: "` + tempDir + `"
    trigger_size: "10GB"
`
				require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0600))

				return []string{"--config", cfgPath, "--once", "--dry-run"}, func() {}
			},
			wantErr: false,
		},
		{
			name: "missing config file",
			setup: func(_ *testing.T) ([]string, func()) {
				return []string{"--config", "/non/existent/config.yaml", "--once"}, func() {}
			},
			wantErr: true,
		},
		{
			name: "invalid path config causes rotator error",
			setup: func(t *testing.T) ([]string, func()) {
				t.Helper()
				tempDir := t.TempDir()
				cfgPath := filepath.Join(tempDir, "config.yaml")
				content := `
paths:
  - path: "` + tempDir + `"
    trigger_size: "10GB"
    rotation:
      type: "unsupported"
`
				require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0600))

				return []string{"--config", cfgPath, "--once"}, func() {}
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args, cleanup := tc.setup(t)
			defer cleanup()

			var out bytes.Buffer
			var errOut bytes.Buffer

			err := RunCLI(args, &out, &errOut)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRunMain(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name         string
		args         []string
		expectedCode int
	}

	tests := []testCase{
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

	cli := &CLI{}
	ctx, cancel := context.WithCancel(context.Background())
	var count atomic.Int32

	done := make(chan struct{})
	go func() {
		_ = cli.runDaemon(ctx, 10*time.Millisecond, func() {
			count.Add(1)
		})
		close(done)
	}()

	assert.Eventually(t, func() bool {
		return count.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	<-done
}
