package compressor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blackbirdworks/loki-prune/pkg/compressor"
)

func TestNew(t *testing.T) {
	t.Parallel()

	type testCase struct {
		wantErr   error
		name      string
		cType     string
		expectExt string
		level     int
	}

	tests := []testCase{
		{name: "zstd default", cType: "zstd", level: 0, wantErr: nil, expectExt: ".tar.zst"},
		{name: "zstd empty string", cType: "", level: 0, wantErr: nil, expectExt: ".tar.zst"},
		{name: "zstd with level 1", cType: "zstd", level: 1, wantErr: nil, expectExt: ".tar.zst"},
		{name: "zstd with level 5", cType: "zstd", level: 5, wantErr: nil, expectExt: ".tar.zst"},
		{name: "zstd with level 12", cType: "zstd", level: 12, wantErr: nil, expectExt: ".tar.zst"},
		{name: "gzip standard", cType: "gzip", level: 6, wantErr: nil, expectExt: ".tar.gz"},
		{name: "gz alias", cType: "gz", level: 1, wantErr: nil, expectExt: ".tar.gz"},
		{name: "none uncompressed", cType: "none", level: 0, wantErr: nil, expectExt: ".tar"},
		{
			name:      "unsupported format",
			cType:     "bzip2",
			level:     0,
			wantErr:   compressor.ErrUnsupportedCompression,
			expectExt: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := compressor.New(tc.cType, tc.level)
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectExt, c.Extension())
			}
		})
	}
}

func TestCompressDirectory(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name  string
		cType string
		level int
	}

	tests := []testCase{
		{name: "zstd default level 0", cType: "zstd", level: 0},
		{name: "zstd level 1 fastest", cType: "zstd", level: 1},
		{name: "zstd level 3 default", cType: "zstd", level: 3},
		{name: "zstd level 5 better", cType: "zstd", level: 5},
		{name: "zstd level 12 best", cType: "zstd", level: 12},
		{name: "gzip level 0 default", cType: "gzip", level: 0},
		{name: "gzip level 1 fast", cType: "gzip", level: 1},
		{name: "gzip level 9 best", cType: "gzip", level: 9},
		{name: "none uncompressed", cType: "none", level: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tempDir := t.TempDir()
			sourceDir := filepath.Join(tempDir, "source")
			require.NoError(t, os.MkdirAll(filepath.Join(sourceDir, "sub"), 0750))

			file1 := filepath.Join(sourceDir, "file1.txt")
			file2 := filepath.Join(sourceDir, "sub", "file2.txt")
			require.NoError(t, os.WriteFile(file1, []byte("hello world"), 0600))
			require.NoError(t, os.WriteFile(file2, []byte("nested file content"), 0600))

			comp, err := compressor.New(tc.cType, tc.level)
			require.NoError(t, err)

			archivePath := filepath.Join(tempDir, "output"+comp.Extension())
			cErr := comp.CompressDirectory(sourceDir, archivePath)
			require.NoError(t, cErr)

			info, statErr := os.Stat(archivePath)
			require.NoError(t, statErr)
			assert.Positive(t, info.Size())
		})
	}
}

func TestCompressDirectory_ErrorCases(t *testing.T) {
	t.Parallel()

	comp, err := compressor.New("zstd", 1)
	require.NoError(t, err)

	type testCase struct {
		setup func(t *testing.T) (src, dst string)
		name  string
	}

	tests := []testCase{
		{
			name: "source directory does not exist",
			setup: func(t *testing.T) (string, string) {
				t.Helper()

				return "/non/existent/path", filepath.Join(t.TempDir(), "out.tar.zst")
			},
		},
		{
			name: "destination parent cannot be created",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				tmp := t.TempDir()
				fileAsDir := filepath.Join(tmp, "blocker")
				require.NoError(t, os.WriteFile(fileAsDir, []byte("data"), 0600))
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.Mkdir(src, 0750))

				return src, filepath.Join(fileAsDir, "sub", "out.tar.zst")
			},
		},
		{
			name: "destination file cannot be opened for writing",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				tmp := t.TempDir()
				roDir := filepath.Join(tmp, "ro")
				require.NoError(t, os.Mkdir(roDir, 0500))
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.Mkdir(src, 0750))

				return src, filepath.Join(roDir, "out.tar.zst")
			},
		},
		{
			name: "unreadable file inside source directory",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				tmp := t.TempDir()
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.Mkdir(src, 0750))
				badFile := filepath.Join(src, "unreadable.bin")
				require.NoError(t, os.WriteFile(badFile, []byte("secret"), 0000))

				return src, filepath.Join(tmp, "out.tar.zst")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src, dst := tc.setup(t)
			cErr := comp.CompressDirectory(src, dst)
			require.Error(t, cErr)
		})
	}
}

func TestCompressor_UnknownType(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	require.NoError(t, os.Mkdir(src, 0750))

	// Directly constructing an unknown compressor type
	invalidComp := compressor.NewWithType("invalid_algorithm", 0)
	assert.Equal(t, ".tar", invalidComp.Extension())

	err := invalidComp.CompressDirectory(src, filepath.Join(tmp, "out.tar"))
	require.Error(t, err)
	assert.ErrorIs(t, err, compressor.ErrUnsupportedCompression)
}
