package rotator_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blackbirdworks/loki-prune/pkg/config"
	"github.com/blackbirdworks/loki-prune/pkg/rotator"
)

func nullLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestNew(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		cType   string
		wantErr bool
	}

	tests := []testCase{
		{name: "valid zstd", cType: "zstd", wantErr: false},
		{name: "invalid type", cType: "invalid-type", wantErr: true},
		{name: "nil logger fallback", cType: "zstd", wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pCfg := config.PathConfig{
				Rotation: config.RotationConfig{Type: tc.cType},
			}
			var l *slog.Logger
			if tc.name != "nil logger fallback" {
				l = nullLogger()
			}
			r, err := rotator.New(pCfg, config.LokiConfig{}, l, false)
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, r)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, r)
			}
		})
	}
}

func TestRotator_Process_Scenarios(t *testing.T) {
	t.Parallel()

	type testCase struct {
		setup        func(t *testing.T, watchDir, archiveDir string)
		verify       func(t *testing.T, watchDir, archiveDir string)
		name         string
		triggerBytes int64
		targetBytes  int64
		dryRun       bool
	}

	tests := []testCase{
		{
			name: "size below trigger does not rotate",
			setup: func(t *testing.T, watchDir, _ string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(watchDir, "chunk-1.bin"), make([]byte, 500), 0600))
			},
			triggerBytes: 1000,
			targetBytes:  800,
			dryRun:       false,
			verify: func(t *testing.T, _, archiveDir string) {
				t.Helper()
				entries, err := os.ReadDir(archiveDir)
				require.NoError(t, err)
				assert.Empty(t, entries)
			},
		},
		{
			name: "size above trigger rotates and compresses",
			setup: func(t *testing.T, watchDir, _ string) {
				t.Helper()
				for i := 1; i <= 5; i++ {
					p := filepath.Join(watchDir, "chunk-"+string(rune('0'+i))+".bin")
					require.NoError(t, os.WriteFile(p, make([]byte, 1024), 0600))
					time.Sleep(5 * time.Millisecond)
				}
			},
			triggerBytes: 3000,
			targetBytes:  2000,
			dryRun:       false,
			verify: func(t *testing.T, _, archiveDir string) {
				t.Helper()
				entries, err := os.ReadDir(archiveDir)
				require.NoError(t, err)
				assert.NotEmpty(t, entries)
			},
		},
		{
			name: "dry run does not move files",
			setup: func(t *testing.T, watchDir, _ string) {
				t.Helper()
				for i := 1; i <= 4; i++ {
					p := filepath.Join(watchDir, "chunk-"+string(rune('0'+i))+".bin")
					require.NoError(t, os.WriteFile(p, make([]byte, 1024), 0600))
				}
			},
			triggerBytes: 2000,
			targetBytes:  1000,
			dryRun:       true,
			verify: func(t *testing.T, watchDir, _ string) {
				t.Helper()
				wEntries, err := os.ReadDir(watchDir)
				require.NoError(t, err)
				assert.Len(t, wEntries, 4)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tempDir := t.TempDir()
			watchDir := filepath.Join(tempDir, "chunks")
			archiveDir := filepath.Join(tempDir, "archive")
			require.NoError(t, os.MkdirAll(watchDir, 0750))
			require.NoError(t, os.MkdirAll(archiveDir, 0750))

			tc.setup(t, watchDir, archiveDir)

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			defer ts.Close()

			pCfg := config.PathConfig{
				Name:             "test",
				Path:             watchDir,
				ArchiveDir:       archiveDir,
				TriggerSizeBytes: tc.triggerBytes,
				TargetSizeBytes:  tc.targetBytes,
				Rotation: config.RotationConfig{
					MaxRotations: 5,
					Compress:     true,
					Type:         "zstd",
					Level:        1,
				},
			}

			lokiCfg := config.LokiConfig{
				URL:     ts.URL,
				Timeout: 2 * time.Second,
			}

			r, err := rotator.New(pCfg, lokiCfg, nullLogger(), tc.dryRun)
			require.NoError(t, err)

			require.NoError(t, r.Process(context.Background()))
			tc.verify(t, watchDir, archiveDir)
		})
	}
}

func TestRotator_DelayedCompression_Table(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name                 string
		folderCount          int
		delayCount           int
		expectedUncompressed int
		expectedCompressed   int
	}

	tests := []testCase{
		{
			name:                 "delay 2 keeps newest 2 uncompressed",
			folderCount:          3,
			delayCount:           2,
			expectedUncompressed: 2,
			expectedCompressed:   1,
		},
		{
			name:                 "delay 0 compresses all",
			folderCount:          2,
			delayCount:           0,
			expectedUncompressed: 0,
			expectedCompressed:   2,
		},
		{
			name:                 "delay greater than count keeps all",
			folderCount:          2,
			delayCount:           5,
			expectedUncompressed: 2,
			expectedCompressed:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tempDir := t.TempDir()
			archiveDir := filepath.Join(tempDir, "archive")
			require.NoError(t, os.MkdirAll(archiveDir, 0750))

			for i := 1; i <= tc.folderCount; i++ {
				rotDir := filepath.Join(archiveDir, "rotation-2026-01-0"+string(rune('0'+i)))
				require.NoError(t, os.MkdirAll(rotDir, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(rotDir, "data.bin"), []byte("data"), 0600))
				time.Sleep(10 * time.Millisecond)
			}

			pCfg := config.PathConfig{
				Name:             "delayed",
				Path:             tempDir,
				ArchiveDir:       archiveDir,
				TriggerSizeBytes: 100 * 1024,
				TargetSizeBytes:  50 * 1024,
				Rotation: config.RotationConfig{
					MaxRotations: 10,
					Compress:     true,
					Type:         "zstd",
					Level:        1,
					Delay: config.DelayConfig{
						Rotations: tc.delayCount,
					},
				},
			}

			r, err := rotator.New(pCfg, config.LokiConfig{}, nullLogger(), false)
			require.NoError(t, err)

			require.NoError(t, r.Process(context.Background()))

			entries, err := os.ReadDir(archiveDir)
			require.NoError(t, err)

			uncompressed := 0
			compressed := 0
			for _, e := range entries {
				if e.IsDir() {
					uncompressed++
				} else if filepath.Ext(e.Name()) == ".zst" {
					compressed++
				}
			}

			assert.Equal(t, tc.expectedUncompressed, uncompressed)
			assert.Equal(t, tc.expectedCompressed, compressed)
		})
	}
}

func TestRotator_EnforceMaxRotations(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	archiveDir := filepath.Join(tempDir, "archive")
	require.NoError(t, os.MkdirAll(archiveDir, 0750))

	for i := 1; i <= 5; i++ {
		f := filepath.Join(archiveDir, "rotation-2026-01-0"+string(rune('0'+i))+".tar.zst")
		require.NoError(t, os.WriteFile(f, []byte("archive content"), 0600))
		time.Sleep(5 * time.Millisecond)
	}

	pCfg := config.PathConfig{
		Name:             "retention",
		Path:             tempDir,
		ArchiveDir:       archiveDir,
		TriggerSizeBytes: 100 * 1024,
		TargetSizeBytes:  50 * 1024,
		Rotation: config.RotationConfig{
			MaxRotations: 3,
			Compress:     false,
		},
	}

	r, err := rotator.New(pCfg, config.LokiConfig{}, nullLogger(), false)
	require.NoError(t, err)

	require.NoError(t, r.Process(context.Background()))

	entries, err := os.ReadDir(archiveDir)
	require.NoError(t, err)
	assert.Len(t, entries, 3)
}

func TestRotator_SingleFileRotation(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "loki.log")
	archiveDir := filepath.Join(tempDir, "archive")
	require.NoError(t, os.WriteFile(logFile, make([]byte, 2048), 0600))

	pCfg := config.PathConfig{
		Name:             "single-file",
		Path:             logFile,
		ArchiveDir:       archiveDir,
		TriggerSizeBytes: 1000,
		TargetSizeBytes:  500,
		Rotation: config.RotationConfig{
			MaxRotations: 3,
			Compress:     true,
			Type:         "zstd",
			Level:        1,
		},
	}

	r, err := rotator.New(pCfg, config.LokiConfig{}, nullLogger(), false)
	require.NoError(t, err)

	require.NoError(t, r.Process(context.Background()))

	// Ensure the original file is recreated empty
	stat, sErr := os.Stat(logFile)
	require.NoError(t, sErr)
	assert.Equal(t, int64(0), stat.Size())

	// Ensure archive was created
	entries, err := os.ReadDir(archiveDir)
	require.NoError(t, err)
	assert.NotEmpty(t, entries)
}

func TestRotator_AgeDelayAndLokiErrors(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	archiveDir := filepath.Join(tempDir, "archive")
	require.NoError(t, os.MkdirAll(archiveDir, 0750))

	rotDir := filepath.Join(archiveDir, "rotation-recent")
	require.NoError(t, os.MkdirAll(rotDir, 0750))
	require.NoError(t, os.WriteFile(filepath.Join(rotDir, "data.log"), []byte("data"), 0600))

	// Mock server that returns 500 error to exercise error branch in flush/reload
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	pCfg := config.PathConfig{
		Name:             "age-delayed",
		Path:             tempDir,
		ArchiveDir:       archiveDir,
		TriggerSizeBytes: 1, // force trigger
		TargetSizeBytes:  0,
		Rotation: config.RotationConfig{
			MaxRotations: 0, // no pruning
			Compress:     true,
			Type:         "zstd",
			Level:        1,
			Delay: config.DelayConfig{
				Age: 24 * time.Hour, // very new, will be delayed
			},
		},
	}

	lokiCfg := config.LokiConfig{
		URL:     ts.URL,
		Timeout: 1 * time.Second,
	}

	r, err := rotator.New(pCfg, lokiCfg, nullLogger(), false)
	require.NoError(t, err)

	require.NoError(t, r.Process(context.Background()))

	// Should still be a directory (delayed compression)
	entries, err := os.ReadDir(archiveDir)
	require.NoError(t, err)
	var foundDir bool
	for _, e := range entries {
		if e.IsDir() && e.Name() == "rotation-recent" {
			foundDir = true
		}
	}
	assert.True(t, foundDir)
}

func TestRotator_NonExistentPath(t *testing.T) {
	t.Parallel()

	pCfg := config.PathConfig{
		Name:             "missing",
		Path:             "/non/existent/path/for/sure",
		ArchiveDir:       "/tmp/archive",
		TriggerSizeBytes: 1000,
		TargetSizeBytes:  500,
	}

	r, err := rotator.New(pCfg, config.LokiConfig{}, nullLogger(), false)
	require.NoError(t, err)

	require.NoError(t, r.Process(context.Background()))
}

func TestRotator_Errors(t *testing.T) {
	t.Parallel()

	t.Run("archive directory error during compression and pruning", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		watchDir := filepath.Join(tempDir, "watch")
		require.NoError(t, os.Mkdir(watchDir, 0750))
		archiveFile := filepath.Join(tempDir, "archive_is_a_file")
		require.NoError(t, os.WriteFile(archiveFile, []byte("blocker"), 0600))

		pCfg := config.PathConfig{
			Name:             "err-archive",
			Path:             watchDir,
			ArchiveDir:       archiveFile,
			TriggerSizeBytes: 1000,
			TargetSizeBytes:  500,
			Rotation: config.RotationConfig{
				MaxRotations: 3,
				Compress:     true,
			},
		}

		r, err := rotator.New(pCfg, config.LokiConfig{}, nullLogger(), false)
		require.NoError(t, err)

		// Process should not fatal, but log errors
		require.NoError(t, r.Process(context.Background()))
	})

	t.Run("rotation directory creation error", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		watchDir := filepath.Join(tempDir, "watch")
		require.NoError(t, os.Mkdir(watchDir, 0750))
		require.NoError(t, os.WriteFile(filepath.Join(watchDir, "data.log"), make([]byte, 2000), 0600))

		// Archive path is under a file, so MkdirAll fails
		fileAsDir := filepath.Join(tempDir, "blocker")
		require.NoError(t, os.WriteFile(fileAsDir, []byte("data"), 0600))
		archiveDir := filepath.Join(fileAsDir, "sub")

		pCfg := config.PathConfig{
			Name:             "mkdir-fail",
			Path:             watchDir,
			ArchiveDir:       archiveDir,
			TriggerSizeBytes: 1000,
			TargetSizeBytes:  500,
		}

		r, err := rotator.New(pCfg, config.LokiConfig{}, nullLogger(), false)
		require.NoError(t, err)

		require.Error(t, r.Process(context.Background()))
	})
}
