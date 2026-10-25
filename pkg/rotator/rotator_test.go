package rotator_test

import (
	"context"
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

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cType   string
		wantErr bool
	}{
		{name: "valid zstd", cType: "zstd", wantErr: false},
		{name: "invalid type", cType: "invalid-type", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pCfg := config.PathConfig{
				Rotation: config.RotationConfig{Type: tc.cType},
			}
			r, err := rotator.New(pCfg, config.LokiConfig{}, false)
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

func TestRotator_Process(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup   func(t *testing.T) (*rotator.Rotator, func(t *testing.T))
		wantErr func(t *testing.T, err error)
		name    string
	}{
		{
			name: "size below trigger does not rotate",
			setup: func(t *testing.T) (*rotator.Rotator, func(t *testing.T)) {
				t.Helper()
				tmp := t.TempDir()
				watchDir := filepath.Join(tmp, "chunks")
				archiveDir := filepath.Join(tmp, "archive")
				require.NoError(t, os.MkdirAll(watchDir, 0750))
				require.NoError(t, os.MkdirAll(archiveDir, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(watchDir, "chunk-1.bin"), make([]byte, 500), 0600))

				pCfg := config.PathConfig{
					Name:             "test",
					Path:             watchDir,
					ArchiveDir:       archiveDir,
					TriggerSizeBytes: 1000,
					TargetSizeBytes:  800,
				}
				r, err := rotator.New(pCfg, config.LokiConfig{}, false)
				require.NoError(t, err)

				return r, func(t *testing.T) {
					t.Helper()
					entries, readErr := os.ReadDir(archiveDir)
					require.NoError(t, readErr)
					assert.Empty(t, entries)
				}
			},
			wantErr: nil,
		},
		{
			name: "size above trigger rotates and compresses",
			setup: func(t *testing.T) (*rotator.Rotator, func(t *testing.T)) {
				t.Helper()
				tmp := t.TempDir()
				watchDir := filepath.Join(tmp, "chunks")
				archiveDir := filepath.Join(tmp, "archive")
				require.NoError(t, os.MkdirAll(watchDir, 0750))
				require.NoError(t, os.MkdirAll(archiveDir, 0750))
				for i := 1; i <= 5; i++ {
					p := filepath.Join(watchDir, "chunk-"+string(rune('0'+i))+".bin")
					require.NoError(t, os.WriteFile(p, make([]byte, 1024), 0600))
					time.Sleep(5 * time.Millisecond)
				}

				pCfg := config.PathConfig{
					Name:             "test",
					Path:             watchDir,
					ArchiveDir:       archiveDir,
					TriggerSizeBytes: 3000,
					TargetSizeBytes:  2000,
					Rotation: config.RotationConfig{
						MaxRotations: 5,
						Compress:     true,
						Type:         "zstd",
						Level:        1,
					},
				}
				r, err := rotator.New(pCfg, config.LokiConfig{}, false)
				require.NoError(t, err)

				return r, func(t *testing.T) {
					t.Helper()
					entries, readErr := os.ReadDir(archiveDir)
					require.NoError(t, readErr)
					assert.NotEmpty(t, entries)
				}
			},
			wantErr: nil,
		},
		{
			name: "dry run does not move files",
			setup: func(t *testing.T) (*rotator.Rotator, func(t *testing.T)) {
				t.Helper()
				tmp := t.TempDir()
				watchDir := filepath.Join(tmp, "chunks")
				archiveDir := filepath.Join(tmp, "archive")
				require.NoError(t, os.MkdirAll(watchDir, 0750))
				require.NoError(t, os.MkdirAll(archiveDir, 0750))
				for i := 1; i <= 4; i++ {
					p := filepath.Join(watchDir, "chunk-"+string(rune('0'+i))+".bin")
					require.NoError(t, os.WriteFile(p, make([]byte, 1024), 0600))
				}

				pCfg := config.PathConfig{
					Name:             "test",
					Path:             watchDir,
					ArchiveDir:       archiveDir,
					TriggerSizeBytes: 2000,
					TargetSizeBytes:  1000,
					Rotation: config.RotationConfig{
						MaxRotations: 5,
						Compress:     true,
						Type:         "zstd",
					},
				}
				r, err := rotator.New(pCfg, config.LokiConfig{}, true)
				require.NoError(t, err)

				return r, func(t *testing.T) {
					t.Helper()
					wEntries, readErr := os.ReadDir(watchDir)
					require.NoError(t, readErr)
					assert.Len(t, wEntries, 4)
				}
			},
			wantErr: nil,
		},
		{
			name: "delayed compression keeps specified newest uncompressed",
			setup: func(t *testing.T) (*rotator.Rotator, func(t *testing.T)) {
				t.Helper()
				tmp := t.TempDir()
				archiveDir := filepath.Join(tmp, "archive")
				require.NoError(t, os.MkdirAll(archiveDir, 0750))

				for i := 1; i <= 3; i++ {
					rotDir := filepath.Join(archiveDir, "rotation-2026-01-0"+string(rune('0'+i)))
					require.NoError(t, os.MkdirAll(rotDir, 0750))
					require.NoError(t, os.WriteFile(filepath.Join(rotDir, "data.bin"), []byte("data"), 0600))
					time.Sleep(10 * time.Millisecond)
				}

				pCfg := config.PathConfig{
					Name:             "delayed",
					Path:             tmp,
					ArchiveDir:       archiveDir,
					TriggerSizeBytes: 100 * 1024,
					TargetSizeBytes:  50 * 1024,
					Rotation: config.RotationConfig{
						MaxRotations: 10,
						Compress:     true,
						Type:         "zstd",
						Level:        1,
						Delay: config.DelayConfig{
							Rotations: 2,
						},
					},
				}
				r, err := rotator.New(pCfg, config.LokiConfig{}, false)
				require.NoError(t, err)

				return r, func(t *testing.T) {
					t.Helper()
					entries, readErr := os.ReadDir(archiveDir)
					require.NoError(t, readErr)
					uncompressed := 0
					compressed := 0
					for _, e := range entries {
						if e.IsDir() {
							uncompressed++
						} else if filepath.Ext(e.Name()) == ".zst" {
							compressed++
						}
					}
					assert.Equal(t, 2, uncompressed)
					assert.Equal(t, 1, compressed)
				}
			},
			wantErr: nil,
		},
		{
			name: "enforce max rotations prunes oldest archives",
			setup: func(t *testing.T) (*rotator.Rotator, func(t *testing.T)) {
				t.Helper()
				tmp := t.TempDir()
				archiveDir := filepath.Join(tmp, "archive")
				require.NoError(t, os.MkdirAll(archiveDir, 0750))

				for i := 1; i <= 5; i++ {
					f := filepath.Join(archiveDir, "rotation-2026-01-0"+string(rune('0'+i))+".tar.zst")
					require.NoError(t, os.WriteFile(f, []byte("archive content"), 0600))
					time.Sleep(5 * time.Millisecond)
				}

				pCfg := config.PathConfig{
					Name:             "retention",
					Path:             tmp,
					ArchiveDir:       archiveDir,
					TriggerSizeBytes: 100 * 1024,
					TargetSizeBytes:  50 * 1024,
					Rotation: config.RotationConfig{
						MaxRotations: 3,
						Compress:     false,
					},
				}
				r, err := rotator.New(pCfg, config.LokiConfig{}, false)
				require.NoError(t, err)

				return r, func(t *testing.T) {
					t.Helper()
					entries, readErr := os.ReadDir(archiveDir)
					require.NoError(t, readErr)
					assert.Len(t, entries, 3)
				}
			},
			wantErr: nil,
		},
		{
			name: "single file rotation empties source file and archives",
			setup: func(t *testing.T) (*rotator.Rotator, func(t *testing.T)) {
				t.Helper()
				tmp := t.TempDir()
				logFile := filepath.Join(tmp, "loki.log")
				archiveDir := filepath.Join(tmp, "archive")
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
				r, err := rotator.New(pCfg, config.LokiConfig{}, false)
				require.NoError(t, err)

				return r, func(t *testing.T) {
					t.Helper()
					stat, sErr := os.Stat(logFile)
					require.NoError(t, sErr)
					assert.Equal(t, int64(0), stat.Size())
					entries, readErr := os.ReadDir(archiveDir)
					require.NoError(t, readErr)
					assert.NotEmpty(t, entries)
				}
			},
			wantErr: nil,
		},
		{
			name: "age delay keeps uncompressed and handles loki error",
			setup: func(t *testing.T) (*rotator.Rotator, func(t *testing.T)) {
				t.Helper()
				tmp := t.TempDir()
				archiveDir := filepath.Join(tmp, "archive")
				require.NoError(t, os.MkdirAll(archiveDir, 0750))

				rotDir := filepath.Join(archiveDir, "rotation-recent")
				require.NoError(t, os.MkdirAll(rotDir, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(rotDir, "data.log"), []byte("data"), 0600))

				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				t.Cleanup(ts.Close)

				pCfg := config.PathConfig{
					Name:             "age-delayed",
					Path:             tmp,
					ArchiveDir:       archiveDir,
					TriggerSizeBytes: 1,
					TargetSizeBytes:  0,
					Rotation: config.RotationConfig{
						Compress: true,
						Type:     "zstd",
						Level:    1,
						Delay: config.DelayConfig{
							Age: 24 * time.Hour,
						},
					},
				}
				lokiCfg := config.LokiConfig{
					URL:     ts.URL,
					Timeout: 1 * time.Second,
				}
				r, err := rotator.New(pCfg, lokiCfg, false)
				require.NoError(t, err)

				return r, func(t *testing.T) {
					t.Helper()
					entries, readErr := os.ReadDir(archiveDir)
					require.NoError(t, readErr)
					var foundDir bool
					for _, e := range entries {
						if e.IsDir() && e.Name() == "rotation-recent" {
							foundDir = true
						}
					}
					assert.True(t, foundDir)
				}
			},
			wantErr: nil,
		},
		{
			name: "non existent path skips cleanly",
			setup: func(t *testing.T) (*rotator.Rotator, func(t *testing.T)) {
				t.Helper()
				pCfg := config.PathConfig{
					Name:             "missing",
					Path:             "/non/existent/path/for/sure",
					ArchiveDir:       "/tmp/archive",
					TriggerSizeBytes: 1000,
					TargetSizeBytes:  500,
				}
				r, err := rotator.New(pCfg, config.LokiConfig{}, false)
				require.NoError(t, err)

				return r, func(_ *testing.T) {}
			},
			wantErr: nil,
		},
		{
			name: "archive dir is file logs error without failing process",
			setup: func(t *testing.T) (*rotator.Rotator, func(t *testing.T)) {
				t.Helper()
				tmp := t.TempDir()
				watchDir := filepath.Join(tmp, "watch")
				require.NoError(t, os.Mkdir(watchDir, 0750))
				archiveFile := filepath.Join(tmp, "archive_file")
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
				r, err := rotator.New(pCfg, config.LokiConfig{}, false)
				require.NoError(t, err)

				return r, func(_ *testing.T) {}
			},
			wantErr: nil,
		},
		{
			name: "rotation directory creation error fails process",
			setup: func(t *testing.T) (*rotator.Rotator, func(t *testing.T)) {
				t.Helper()
				tmp := t.TempDir()
				watchDir := filepath.Join(tmp, "watch")
				require.NoError(t, os.Mkdir(watchDir, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(watchDir, "data.log"), make([]byte, 2000), 0600))

				fileAsDir := filepath.Join(tmp, "blocker")
				require.NoError(t, os.WriteFile(fileAsDir, []byte("data"), 0600))
				archiveDir := filepath.Join(fileAsDir, "sub")

				pCfg := config.PathConfig{
					Name:             "mkdir-fail",
					Path:             watchDir,
					ArchiveDir:       archiveDir,
					TriggerSizeBytes: 1000,
					TargetSizeBytes:  500,
				}
				r, err := rotator.New(pCfg, config.LokiConfig{}, false)
				require.NoError(t, err)

				return r, func(_ *testing.T) {}
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
			r, verify := tc.setup(t)
			err := r.Process(context.Background())
			if tc.wantErr != nil {
				tc.wantErr(t, err)
			} else {
				require.NoError(t, err)
			}
			verify(t)
		})
	}
}
