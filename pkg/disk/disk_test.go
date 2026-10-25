package disk_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blackbirdworks/loki-prune/pkg/disk"
)

func TestUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup   func(t *testing.T) string
		wantErr func(t *testing.T, err error)
		name    string
	}{
		{
			name: "valid directory path",
			setup: func(t *testing.T) string {
				t.Helper()
				tmp := t.TempDir()

				return tmp
			},
			wantErr: nil,
		},
		{
			name: "valid file path",
			setup: func(t *testing.T) string {
				t.Helper()
				f, err := os.CreateTemp(t.TempDir(), "test-file-*.txt")
				require.NoError(t, err)
				defer f.Close()

				return f.Name()
			},
			wantErr: nil,
		},
		{
			name: "non-existent path returns error",
			setup: func(_ *testing.T) string {
				return "/non/existent/path/for/sure/12345"
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
			path := tc.setup(t)
			usage, err := disk.Usage(path)
			if tc.wantErr != nil {
				tc.wantErr(t, err)
			} else {
				require.NoError(t, err)
				assert.Positive(t, usage.TotalBytes)
				assert.GreaterOrEqual(t, usage.FreeBytes, int64(0))
				assert.GreaterOrEqual(t, usage.UsedBytes, int64(0))
				assert.GreaterOrEqual(t, usage.UsedPercent, float64(0))
				assert.LessOrEqual(t, usage.UsedPercent, float64(100))
			}
		})
	}
}
