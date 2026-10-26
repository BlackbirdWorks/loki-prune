package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

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
			name: "invalid path config causes service error",
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
