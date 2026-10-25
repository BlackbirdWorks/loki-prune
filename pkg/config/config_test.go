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

	type testCase struct {
		err      error
		name     string
		input    string
		expected int64
	}

	tests := []testCase{
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

	type testCase struct {
		name     string
		expected string
		input    int64
	}

	tests := []testCase{
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

	type testCase struct {
		wantErr     error
		verify      func(t *testing.T, cfg *config.Config)
		name        string
		yamlContent string
	}

	tests := []testCase{
		{
			name: "valid full configuration",
			yamlContent: `
loki:
  url: "http://localhost:3100"
  timeout: "15s"
  flush_before_rotate: true
  reload_after_rotate: true
daemon:
  interval: "2m"
  log_level: "debug"
paths:
  - name: "test-chunks"
    path: "/tmp/loki/chunks"
    archive_dir: "/tmp/loki/archive"
    trigger_size: "100MB"
    target_size: "80MB"
    rotation:
      max_rotations: 5
      compress: true
      type: "zstd"
      level: 3
      delay:
        rotations: 2
        age: "12h"
`,
			wantErr: nil,
			verify: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.Equal(t, "http://localhost:3100", cfg.Loki.URL)
				assert.Equal(t, 15*time.Second, cfg.Loki.Timeout)
				assert.Equal(t, 2*time.Minute, cfg.Daemon.Interval)
				assert.Equal(t, "debug", cfg.Daemon.LogLevel)
				require.Len(t, cfg.Paths, 1)

				p := cfg.Paths[0]
				assert.Equal(t, "test-chunks", p.Name)
				assert.Equal(t, int64(100*1000*1000), p.TriggerSizeBytes)
				assert.Equal(t, int64(80*1000*1000), p.TargetSizeBytes)
				assert.Equal(t, 5, p.Rotation.MaxRotations)
				assert.Equal(t, "zstd", p.Rotation.Type)
				assert.Equal(t, 2, p.Rotation.Delay.Rotations)
				assert.Equal(t, 12*time.Hour, p.Rotation.Delay.Age)
			},
		},
		{
			name: "defaults applied",
			yamlContent: `
paths:
  - path: "/tmp/loki/chunks"
    trigger_size: "10GiB"
`,
			wantErr: nil,
			verify: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.Equal(t, "http://127.0.0.1:3100", cfg.Loki.URL)
				assert.Equal(t, 30*time.Second, cfg.Loki.Timeout)
				assert.Equal(t, 5*time.Minute, cfg.Daemon.Interval)
				assert.Equal(t, "info", cfg.Daemon.LogLevel)
				require.Len(t, cfg.Paths, 1)

				p := cfg.Paths[0]
				assert.Equal(t, "path-1", p.Name)
				assert.Equal(t, filepath.Join("/tmp/loki/chunks", "archive"), p.ArchiveDir)
				assert.Equal(t, "zstd", p.Rotation.Type)
				assert.Equal(t, int64(float64(p.TriggerSizeBytes)*0.8), p.TargetSizeBytes)
			},
		},
		{
			name: "no paths defined",
			yamlContent: `
loki:
  url: "http://localhost:3100"
`,
			wantErr: config.ErrNoPaths,
		},
		{
			name: "missing path attribute",
			yamlContent: `
paths:
  - trigger_size: "10GB"
`,
			wantErr: config.ErrMissingPath,
		},
		{
			name: "missing trigger_size",
			yamlContent: `
paths:
  - path: "/tmp/chunks"
`,
			wantErr: config.ErrMissingTriggerSize,
		},
		{
			name: "invalid trigger size format",
			yamlContent: `
paths:
  - path: "/tmp/chunks"
    trigger_size: "notanumber"
`,
			wantErr: config.ErrInvalidSizeFormat,
		},
		{
			name: "invalid target size format",
			yamlContent: `
paths:
  - path: "/tmp/chunks"
    trigger_size: "10GB"
    target_size: "notanumber"
`,
			wantErr: config.ErrInvalidSizeFormat,
		},
		{
			name: "target size greater than trigger",
			yamlContent: `
paths:
  - path: "/tmp/chunks"
    trigger_size: "10GB"
    target_size: "12GB"
`,
			wantErr: config.ErrTargetGteTrigger,
		},
		{
			name: "unsupported compression type",
			yamlContent: `
paths:
  - path: "/tmp/chunks"
    trigger_size: "10GB"
    rotation:
      type: "bzip2"
`,
			wantErr: config.ErrUnsupportedCompression,
		},
		{
			name: "invalid loki timeout",
			yamlContent: `
loki:
  timeout: "invalid"
paths:
  - path: "/tmp/chunks"
    trigger_size: "10GB"
`,
			wantErr: config.ErrInvalidDuration,
		},
		{
			name: "invalid daemon interval",
			yamlContent: `
daemon:
  interval: "invalid"
paths:
  - path: "/tmp/chunks"
    trigger_size: "10GB"
`,
			wantErr: config.ErrInvalidDuration,
		},
		{
			name: "invalid delay age",
			yamlContent: `
paths:
  - path: "/tmp/chunks"
    trigger_size: "10GB"
    rotation:
      delay:
        age: "not-a-duration"
`,
			wantErr: config.ErrInvalidDuration,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmpFile := filepath.Join(t.TempDir(), "config.yaml")
			err := os.WriteFile(tmpFile, []byte(tc.yamlContent), 0600)
			require.NoError(t, err)

			cfg, err := config.Load(tmpFile)
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				if tc.verify != nil {
					tc.verify(t, cfg)
				}
			}
		})
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	t.Parallel()
	_, err := config.Load("/path/does/not/exist.yaml")
	require.Error(t, err)
}

func TestLoad_InvalidYAML(t *testing.T) {
	t.Parallel()
	tmpFile := filepath.Join(t.TempDir(), "invalid.yaml")
	require.NoError(t, os.WriteFile(tmpFile, []byte("invalid: [yaml"), 0600))
	_, err := config.Load(tmpFile)
	require.Error(t, err)
}
