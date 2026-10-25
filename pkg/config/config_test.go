package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blackbirdworks/loki-prune/pkg/config"
)

func TestParseBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err      error
		name     string
		input    string
		expected int64
	}{
		{name: "bytes without unit", input: "100", expected: 100, err: nil},
		{name: "bytes with B", input: "100B", expected: 100, err: nil},
		{name: "kilobytes", input: "10KB", expected: 10000, err: nil},
		{name: "kibibytes", input: "10KiB", expected: 10240, err: nil},
		{name: "megabytes", input: "500MB", expected: 500000000, err: nil},
		{name: "mebibytes", input: "500MiB", expected: 500 * 1024 * 1024, err: nil},
		{name: "gigabytes", input: "48GB", expected: 48 * 1000 * 1000 * 1000, err: nil},
		{name: "gibibytes", input: "48GiB", expected: 48 * 1024 * 1024 * 1024, err: nil},
		{name: "terabytes", input: "1TB", expected: 1000000000000, err: nil},
		{name: "tebibytes", input: "1TiB", expected: 1024 * 1024 * 1024 * 1024, err: nil},
		{name: "lowercase kib", input: "5kib", expected: 5120, err: nil},
		{name: "float value", input: "1.5GB", expected: 1500000000, err: nil},
		{name: "invalid string", input: "invalid", expected: 0, err: config.ErrInvalidSizeFormat},
		{name: "unknown unit", input: "100XB", expected: 0, err: config.ErrUnknownUnit},
		{name: "empty input", input: "", expected: 0, err: config.ErrInvalidSizeFormat},
		{name: "float overflow", input: "1e309GB", expected: 0, err: config.ErrInvalidSizeFormat},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := config.ParseBytes(tc.input)
			if tc.err != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expected, got)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expected string
		input    int64
	}{
		{name: "small bytes", input: 512, expected: "512 B"},
		{name: "kibibytes", input: 2048, expected: "2.00 KiB"},
		{name: "mebibytes", input: 10485760, expected: "10.00 MiB"},
		{name: "gibibytes", input: 10737418240, expected: "10.00 GiB"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, config.FormatBytes(tc.input))
		})
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup   func(t *testing.T) string
		verify  func(t *testing.T, cfg *config.Config)
		wantErr func(t *testing.T, err error)
		name    string
	}{
		{
			name: "valid full configuration",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				content := `
loki:
  url: "http://localhost:3100"
  timeout: "15s"
  flush_before_rotate: true
  reload_after_rotate: true
daemon:
  interval: "5m"
paths:
  - name: "chunks"
    path: "/tmp/chunks"
    trigger_size: "48GB"
    target_size: "40GB"
    rotation:
      compress: true
      type: "zstd"
      level: 3
      delay:
        rotations: 2
        age: "24h"
      max_rotations: 10
`
				require.NoError(t, os.WriteFile(tmp, []byte(content), 0600))

				return tmp
			},
			verify: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.Equal(t, "http://localhost:3100", cfg.Loki.URL)
				assert.Equal(t, 15*time.Second, cfg.Loki.Timeout)
				assert.True(t, cfg.Loki.FlushBeforeRotate)
				assert.True(t, cfg.Loki.ReloadAfterRotate)
				assert.Equal(t, 5*time.Minute, cfg.Daemon.Interval)
				require.Len(t, cfg.Paths, 1)

				p := cfg.Paths[0]
				assert.Equal(t, "chunks", p.Name)
				assert.Equal(t, int64(48*1000*1000*1000), p.TriggerSizeBytes)
				assert.Equal(t, int64(40*1000*1000*1000), p.TargetSizeBytes)
				assert.True(t, p.Rotation.Compress)
				assert.Equal(t, "zstd", p.Rotation.Type)
				assert.Equal(t, 3, p.Rotation.Level)
				assert.Equal(t, 2, p.Rotation.Delay.Rotations)
				assert.Equal(t, 24*time.Hour, p.Rotation.Delay.Age)
				assert.Equal(t, 10, p.Rotation.MaxRotations)
			},
			wantErr: nil,
		},
		{
			name: "defaults applied",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				content := `
paths:
  - path: "/tmp/chunks"
    trigger_size: "10GB"
`
				require.NoError(t, os.WriteFile(tmp, []byte(content), 0600))

				return tmp
			},
			verify: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.Equal(t, 30*time.Second, cfg.Loki.Timeout)
				assert.Equal(t, 5*time.Minute, cfg.Daemon.Interval)
				require.Len(t, cfg.Paths, 1)

				p := cfg.Paths[0]
				assert.Equal(t, "path-1", p.Name)
				assert.Equal(t, filepath.Join("/tmp/chunks", "archive"), p.ArchiveDir)
				assert.Equal(t, "zstd", p.Rotation.Type)
				assert.Equal(t, int64(float64(p.TriggerSizeBytes)*0.8), p.TargetSizeBytes)
				assert.True(t, cfg.Daemon.Adaptive)
				assert.Equal(t, 5*time.Second, cfg.Daemon.MinInterval)
				assert.InDelta(t, 90.0, p.DiskGuardPercent, 0.001)
			},
			wantErr: nil,
		},
		{
			name: "valid zip rotation type",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				content := `
paths:
  - path: "/tmp/chunks"
    trigger_size: "10GB"
    rotation:
      type: "zip"
`
				require.NoError(t, os.WriteFile(tmp, []byte(content), 0600))

				return tmp
			},
			verify: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				require.Len(t, cfg.Paths, 1)
				assert.Equal(t, "zip", cfg.Paths[0].Rotation.Type)
			},
			wantErr: nil,
		},
		{
			name: "file not found error",
			setup: func(_ *testing.T) string {
				return "/path/does/not/exist.yaml"
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				assert.Error(t, err)
			},
		},
		{
			name: "invalid yaml syntax error",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "invalid.yaml")
				require.NoError(t, os.WriteFile(tmp, []byte("invalid: [yaml"), 0600))

				return tmp
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				assert.Error(t, err)
			},
		},
		{
			name: "no paths defined error",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(tmp, []byte("daemon:\n  interval: '5m'\n"), 0600))

				return tmp
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, config.ErrNoPaths)
			},
		},
		{
			name: "missing path in path item error",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(tmp, []byte("paths:\n  - trigger_size: '10GB'\n"), 0600))

				return tmp
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, config.ErrMissingPath)
			},
		},
		{
			name: "missing trigger size error",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(tmp, []byte("paths:\n  - path: '/tmp/chunks'\n"), 0600))

				return tmp
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, config.ErrMissingTriggerSize)
			},
		},
		{
			name: "invalid trigger size error",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				content := "paths:\n  - path: '/tmp/chunks'\n    trigger_size: 'notanumber'\n"
				require.NoError(t, os.WriteFile(tmp, []byte(content), 0600))

				return tmp
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, config.ErrInvalidSizeFormat)
			},
		},
		{
			name: "invalid target size error",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				content := "paths:\n  - path: '/tmp/chunks'\n    trigger_size: '10GB'\n    target_size: 'notanumber'\n"
				require.NoError(t, os.WriteFile(tmp, []byte(content), 0600))

				return tmp
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, config.ErrInvalidSizeFormat)
			},
		},
		{
			name: "target size greater than trigger error",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				content := "paths:\n  - path: '/tmp/chunks'\n    trigger_size: '10GB'\n    target_size: '12GB'\n"
				require.NoError(t, os.WriteFile(tmp, []byte(content), 0600))

				return tmp
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, config.ErrTargetGteTrigger)
			},
		},
		{
			name: "unsupported compression type error",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				content := "paths:\n  - path: '/tmp/chunks'\n    trigger_size: '10GB'\n    rotation:\n      type: 'bzip2'\n"
				require.NoError(t, os.WriteFile(tmp, []byte(content), 0600))

				return tmp
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, config.ErrUnsupportedCompression)
			},
		},
		{
			name: "invalid loki timeout error",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				content := "loki:\n  timeout: 'invalid'\npaths:\n  - path: '/tmp/chunks'\n    trigger_size: '10GB'\n"
				require.NoError(t, os.WriteFile(tmp, []byte(content), 0600))

				return tmp
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, config.ErrInvalidDuration)
			},
		},
		{
			name: "invalid daemon interval error",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				content := "daemon:\n  interval: 'invalid'\npaths:\n  - path: '/tmp/chunks'\n    trigger_size: '10GB'\n"
				require.NoError(t, os.WriteFile(tmp, []byte(content), 0600))

				return tmp
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, config.ErrInvalidDuration)
			},
		},
		{
			name: "invalid daemon min_interval error",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				content := "daemon:\n  min_interval: 'invalid'\npaths:\n  - path: '/tmp/chunks'\n    trigger_size: '10GB'\n"
				require.NoError(t, os.WriteFile(tmp, []byte(content), 0600))

				return tmp
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, config.ErrInvalidDuration)
			},
		},
		{
			name: "invalid delay age error",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := filepath.Join(t.TempDir(), "config.yaml")
				content := "paths:\n  - path: '/tmp/chunks'\n" +
					"    trigger_size: '10GB'\n    rotation:\n      delay:\n        age: 'bad-age'\n"
				require.NoError(t, os.WriteFile(tmp, []byte(content), 0600))

				return tmp
			},
			verify: nil,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, config.ErrInvalidDuration)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			filePath := tc.setup(t)
			cfg, err := config.Load(filePath)
			if tc.wantErr != nil {
				tc.wantErr(t, err)
			} else {
				require.NoError(t, err)
				if tc.verify != nil {
					tc.verify(t, cfg)
				}
			}
		})
	}
}
