package cli_test

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

	"github.com/blackbirdworks/loki-prune/cli"
)

func TestRun(t *testing.T) {
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
			err := cli.Run(args, &out, &errOut)
			if tc.wantErr != nil {
				tc.wantErr(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRunCLI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "version alias call", args: []string{"--version"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			var errOut bytes.Buffer
			err := cli.RunCLI(tc.args, &out, &errOut)
			require.NoError(t, err)
		})
	}
}

func TestComputeNextInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		interval    time.Duration
		minInterval time.Duration
		adaptive    bool
		ratio       float64
		want        time.Duration
	}{
		{
			name:        "non-adaptive returns base interval",
			interval:    60 * time.Second,
			minInterval: 5 * time.Second,
			adaptive:    false,
			ratio:       0.9,
			want:        60 * time.Second,
		},
		{
			name:        "minInterval zero returns base interval",
			interval:    60 * time.Second,
			minInterval: 0,
			adaptive:    true,
			ratio:       0.9,
			want:        60 * time.Second,
		},
		{
			name:        "minInterval equal or greater returns base interval",
			interval:    60 * time.Second,
			minInterval: 60 * time.Second,
			adaptive:    true,
			ratio:       0.9,
			want:        60 * time.Second,
		},
		{
			name:        "ratio under threshold returns base interval",
			interval:    60 * time.Second,
			minInterval: 5 * time.Second,
			adaptive:    true,
			ratio:       0.4,
			want:        60 * time.Second,
		},
		{
			name:        "ratio over one returns min interval",
			interval:    60 * time.Second,
			minInterval: 5 * time.Second,
			adaptive:    true,
			ratio:       1.2,
			want:        5 * time.Second,
		},
		{
			name:        "ratio at threshold 0.75 scales midway",
			interval:    60 * time.Second,
			minInterval: 10 * time.Second,
			adaptive:    true,
			ratio:       0.75,
			want:        35 * time.Second,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cli.ComputeNextInterval(tc.interval, tc.minInterval, tc.adaptive, tc.ratio)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCLI_RunDaemon(t *testing.T) {
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
			c := &cli.CLI{}
			ctx, cancel := context.WithCancel(context.Background())
			var count atomic.Int32

			done := make(chan struct{})
			go func() {
				_ = c.RunDaemon(ctx, tc.interval, func() {
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

func TestCLI_RunDaemonAdaptive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setupEvents  func() chan struct{}
		name         string
		interval     time.Duration
		minInterval  time.Duration
		initialRatio float64
		adaptive     bool
	}{
		{
			name:         "adaptive daemon runs and stops on cancel without events",
			interval:     20 * time.Millisecond,
			minInterval:  5 * time.Millisecond,
			initialRatio: 0.9,
			adaptive:     true,
			setupEvents:  nil,
		},
		{
			name:         "adaptive daemon reacts to realtime events",
			interval:     500 * time.Millisecond,
			minInterval:  5 * time.Millisecond,
			initialRatio: 0.2,
			adaptive:     true,
			setupEvents: func() chan struct{} {
				ch := make(chan struct{}, 1)
				ch <- struct{}{}

				return ch
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &cli.CLI{}
			ctx, cancel := context.WithCancel(context.Background())
			var count atomic.Int32

			var events chan struct{}
			if tc.setupEvents != nil {
				events = tc.setupEvents()
			}

			done := make(chan struct{})
			go func() {
				_ = c.RunDaemonAdaptive(
					ctx,
					tc.interval,
					tc.minInterval,
					tc.adaptive,
					tc.initialRatio,
					events,
					func() float64 {
						count.Add(1)

						return 0.95
					},
				)
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
