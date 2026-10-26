package service_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blackbirdworks/loki-prune/internal/service"
	"github.com/blackbirdworks/loki-prune/pkg/config"
)

type mockLokiClient struct {
	flushCalls  atomic.Int32
	reloadCalls atomic.Int32
}

func (m *mockLokiClient) Flush(_ context.Context) error {
	m.flushCalls.Add(1)

	return nil
}

func (m *mockLokiClient) Reload(_ context.Context) error {
	m.reloadCalls.Add(1)

	return nil
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
			got := service.ComputeNextInterval(tc.interval, tc.minInterval, tc.adaptive, tc.ratio)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestService_RunOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode string
	}{
		{name: "poll mode once", mode: config.ModePoll},
		{name: "fsevent mode once", mode: config.ModeFSEvent},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			sourceDir := filepath.Join(tmp, "source")
			archiveDir := filepath.Join(tmp, "archive")
			require.NoError(t, os.MkdirAll(sourceDir, 0755))
			require.NoError(t, os.MkdirAll(archiveDir, 0755))

			f1 := filepath.Join(sourceDir, "test.log")
			require.NoError(t, os.WriteFile(f1, []byte("some-test-bytes"), 0600))

			cfg := &config.Config{
				Paths: []config.PathConfig{
					{
						Name:             "test-path",
						Path:             sourceDir,
						ArchiveDir:       archiveDir,
						TriggerSizeBytes: 5,
						TargetSizeBytes:  2,
						Rotation: config.RotationConfig{
							Type:         "zstd",
							Level:        1,
							Compress:     true,
							MaxRotations: 2,
						},
					},
				},
				Daemon: config.DaemonConfig{
					Mode:        tc.mode,
					Interval:    10 * time.Millisecond,
					MinInterval: 5 * time.Millisecond,
					Adaptive:    true,
				},
			}

			mockLoki := &mockLokiClient{}
			svc, err := service.New(cfg,
				service.WithOnce(true),
				service.WithLokiClient(mockLoki),
				service.WithDebounceDelay(10*time.Millisecond),
			)
			require.NoError(t, err)

			err = svc.Run(context.Background())
			require.NoError(t, err)

			assert.Equal(t, int32(1), mockLoki.flushCalls.Load())
			assert.Equal(t, int32(1), mockLoki.reloadCalls.Load())
		})
	}
}

func TestService_FSEventDaemon(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "source")
	archiveDir := filepath.Join(tmp, "archive")
	require.NoError(t, os.MkdirAll(sourceDir, 0755))
	require.NoError(t, os.MkdirAll(archiveDir, 0755))

	cfg := &config.Config{
		Paths: []config.PathConfig{
			{
				Name:             "test-path",
				Path:             sourceDir,
				ArchiveDir:       archiveDir,
				TriggerSizeBytes: 10,
				TargetSizeBytes:  2,
				Rotation: config.RotationConfig{
					Type:         "zstd",
					Level:        1,
					Compress:     false,
					MaxRotations: 2,
				},
			},
		},
		Daemon: config.DaemonConfig{
			Mode:        config.ModeFSEvent,
			Interval:    10 * time.Second, // Long fallback timer so only fsevent triggers
			MinInterval: 1 * time.Second,
			Adaptive:    false,
		},
	}

	mockLoki := &mockLokiClient{}
	svc, err := service.New(cfg,
		service.WithOnce(false),
		service.WithLokiClient(mockLoki),
		service.WithDebounceDelay(20*time.Millisecond),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = svc.Run(ctx)
		close(done)
	}()

	// Wait for event pipeline initialization
	time.Sleep(50 * time.Millisecond)

	// Write file exceeding trigger size
	f1 := filepath.Join(sourceDir, "chunk.log")
	require.NoError(t, os.WriteFile(f1, []byte("large-payload-exceeding-trigger-limit"), 0600))

	assert.Eventually(t, func() bool {
		return mockLoki.flushCalls.Load() >= 1
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
	<-done
}

func TestService_ConcurrentRunCycle(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "source")
	archiveDir := filepath.Join(tmp, "archive")
	require.NoError(t, os.MkdirAll(sourceDir, 0755))
	require.NoError(t, os.MkdirAll(archiveDir, 0755))

	for i := range 10 {
		p := filepath.Join(sourceDir, fmt.Sprintf("chunk-%d.log", i))
		require.NoError(t, os.WriteFile(p, []byte("data-chunk-to-rotate"), 0600))
	}

	cfg := &config.Config{
		Paths: []config.PathConfig{
			{
				Name:             "test-concurrent",
				Path:             sourceDir,
				ArchiveDir:       archiveDir,
				TriggerSizeBytes: 10,
				TargetSizeBytes:  5,
				Rotation: config.RotationConfig{
					Type:         "none",
					Compress:     false,
					MaxRotations: 5,
				},
			},
		},
	}

	mockLoki := &mockLokiClient{}
	svc, err := service.New(cfg,
		service.WithLokiClient(mockLoki),
	)
	require.NoError(t, err)

	const goroutines = 8
	var wg sync.WaitGroup
	ctx := context.Background()

	for range goroutines {
		wg.Go(func() {
			for range 5 {
				_ = svc.RunCycle(ctx)
			}
		})
	}

	wg.Wait()
	assert.GreaterOrEqual(t, mockLoki.flushCalls.Load(), int32(1))
}
