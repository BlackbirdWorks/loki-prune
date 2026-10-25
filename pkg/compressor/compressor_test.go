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

	tests := []struct {
		wantErr   error
		name      string
		cType     string
		expectExt string
		level     int
	}{
		{name: "zstd default", cType: "zstd", level: 0, wantErr: nil, expectExt: ".tar.zst"},
		{name: "zstd empty string", cType: "", level: 0, wantErr: nil, expectExt: ".tar.zst"},
		{name: "zstd level 1", cType: "zstd", level: 1, wantErr: nil, expectExt: ".tar.zst"},
		{name: "zstd level 5", cType: "zstd", level: 5, wantErr: nil, expectExt: ".tar.zst"},
		{name: "zstd level 12", cType: "zstd", level: 12, wantErr: nil, expectExt: ".tar.zst"},
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

func TestNewWithType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cType     compressor.CompressionType
		expectExt string
		level     int
	}{
		{name: "zstd", cType: compressor.TypeZstd, level: 3, expectExt: ".tar.zst"},
		{name: "gzip", cType: compressor.TypeGzip, level: 6, expectExt: ".tar.gz"},
		{name: "none", cType: compressor.TypeNone, level: 0, expectExt: ".tar"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := compressor.NewWithType(tc.cType, tc.level)
			assert.NotNil(t, c)
			assert.Equal(t, tc.expectExt, c.Extension())
		})
	}
}

func TestCompressor_Extension(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cType     compressor.CompressionType
		expectExt string
	}{
		{name: "zstd", cType: compressor.TypeZstd, expectExt: ".tar.zst"},
		{name: "gzip", cType: compressor.TypeGzip, expectExt: ".tar.gz"},
		{name: "none", cType: compressor.TypeNone, expectExt: ".tar"},
		{name: "unknown fallback", cType: "unknown", expectExt: ".tar"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := compressor.NewWithType(tc.cType, 0)
			assert.Equal(t, tc.expectExt, c.Extension())
		})
	}
}

func TestCompressor_CompressDirectory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup   func(t *testing.T) (comp *compressor.Compressor, src, dst string)
		wantErr func(t *testing.T, err error)
		name    string
	}{
		{
			name: "zstd default level 0",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.MkdirAll(filepath.Join(src, "sub"), 0750))
				require.NoError(t, os.WriteFile(filepath.Join(src, "file1.txt"), []byte("hello"), 0600))
				c, err := compressor.New("zstd", 0)
				require.NoError(t, err)

				return c, src, filepath.Join(tmp, "out.tar.zst")
			},
			wantErr: nil,
		},
		{
			name: "zstd level 1 fastest",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.MkdirAll(src, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(src, "file.txt"), []byte("fastest"), 0600))
				c, err := compressor.New("zstd", 1)
				require.NoError(t, err)

				return c, src, filepath.Join(tmp, "out.tar.zst")
			},
			wantErr: nil,
		},
		{
			name: "zstd level 3 default",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.MkdirAll(src, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(src, "file.txt"), []byte("default"), 0600))
				c, err := compressor.New("zstd", 3)
				require.NoError(t, err)

				return c, src, filepath.Join(tmp, "out.tar.zst")
			},
			wantErr: nil,
		},
		{
			name: "zstd level 5 better",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.MkdirAll(src, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(src, "file.txt"), []byte("better"), 0600))
				c, err := compressor.New("zstd", 5)
				require.NoError(t, err)

				return c, src, filepath.Join(tmp, "out.tar.zst")
			},
			wantErr: nil,
		},
		{
			name: "zstd level 12 best",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.MkdirAll(src, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(src, "file.txt"), []byte("best"), 0600))
				c, err := compressor.New("zstd", 12)
				require.NoError(t, err)

				return c, src, filepath.Join(tmp, "out.tar.zst")
			},
			wantErr: nil,
		},
		{
			name: "gzip level 0 default",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.MkdirAll(src, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(src, "file.txt"), []byte("gzip-default"), 0600))
				c, err := compressor.New("gzip", 0)
				require.NoError(t, err)

				return c, src, filepath.Join(tmp, "out.tar.gz")
			},
			wantErr: nil,
		},
		{
			name: "gzip level 1 fast",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.MkdirAll(src, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(src, "file.txt"), []byte("gzip-fast"), 0600))
				c, err := compressor.New("gzip", 1)
				require.NoError(t, err)

				return c, src, filepath.Join(tmp, "out.tar.gz")
			},
			wantErr: nil,
		},
		{
			name: "gzip level 9 best",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.MkdirAll(src, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(src, "file.txt"), []byte("gzip-best"), 0600))
				c, err := compressor.New("gzip", 9)
				require.NoError(t, err)

				return c, src, filepath.Join(tmp, "out.tar.gz")
			},
			wantErr: nil,
		},
		{
			name: "none uncompressed",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.MkdirAll(src, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(src, "file.txt"), []byte("uncompressed"), 0600))
				c, err := compressor.New("none", 0)
				require.NoError(t, err)

				return c, src, filepath.Join(tmp, "out.tar")
			},
			wantErr: nil,
		},
		{
			name: "error source directory does not exist",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				c, err := compressor.New("zstd", 1)
				require.NoError(t, err)

				return c, "/non/existent/path", filepath.Join(t.TempDir(), "out.tar.zst")
			},
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				assert.Error(t, err)
			},
		},
		{
			name: "error destination parent cannot be created",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				blocker := filepath.Join(tmp, "blocker")
				require.NoError(t, os.WriteFile(blocker, []byte("file"), 0600))
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.Mkdir(src, 0750))
				c, err := compressor.New("zstd", 1)
				require.NoError(t, err)

				return c, src, filepath.Join(blocker, "sub", "out.tar.zst")
			},
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				assert.Error(t, err)
			},
		},
		{
			name: "error destination file cannot be opened for writing",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				roDir := filepath.Join(tmp, "ro")
				require.NoError(t, os.Mkdir(roDir, 0500))
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.Mkdir(src, 0750))
				c, err := compressor.New("zstd", 1)
				require.NoError(t, err)

				return c, src, filepath.Join(roDir, "out.tar.zst")
			},
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				assert.Error(t, err)
			},
		},
		{
			name: "error unreadable file inside source directory",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.Mkdir(src, 0750))
				badFile := filepath.Join(src, "unreadable.bin")
				require.NoError(t, os.WriteFile(badFile, []byte("secret"), 0000))
				c, err := compressor.New("zstd", 1)
				require.NoError(t, err)

				return c, src, filepath.Join(tmp, "out.tar.zst")
			},
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				assert.Error(t, err)
			},
		},
		{
			name: "error unsupported compression type",
			setup: func(t *testing.T) (*compressor.Compressor, string, string) {
				t.Helper()
				tmp := t.TempDir()
				src := filepath.Join(tmp, "src")
				require.NoError(t, os.Mkdir(src, 0750))
				c := compressor.NewWithType("invalid_algo", 0)

				return c, src, filepath.Join(tmp, "out.tar")
			},
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, compressor.ErrUnsupportedCompression)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, src, dst := tc.setup(t)
			err := c.CompressDirectory(src, dst)
			if tc.wantErr != nil {
				tc.wantErr(t, err)
			} else {
				require.NoError(t, err)
				info, statErr := os.Stat(dst)
				require.NoError(t, statErr)
				assert.Positive(t, info.Size())
			}
		})
	}
}
