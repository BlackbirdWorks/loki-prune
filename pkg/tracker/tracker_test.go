package tracker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blackbirdworks/loki-prune/pkg/tracker"
)

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup   func(t *testing.T) (root, archive string)
		name    string
		wantErr bool
	}{
		{
			name: "valid paths with existing directory",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				tmp := t.TempDir()

				return tmp, filepath.Join(tmp, "archive")
			},
			wantErr: false,
		},
		{
			name: "valid paths with non-existing directory",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				tmp := t.TempDir()

				return filepath.Join(tmp, "future"), filepath.Join(tmp, "archive")
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, archive := tc.setup(t)
			trk, err := tracker.New(root, archive)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, trk)
				_ = trk.Close()
			}
		})
	}
}

func TestTracker_Start(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "start processes file events"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			trk, err := tracker.New(tmp, filepath.Join(tmp, "archive"))
			require.NoError(t, err)
			defer trk.Close()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			trk.Start(ctx)

			// Create a file and verify event
			testFile := filepath.Join(tmp, "chunk.log")
			require.NoError(t, os.WriteFile(testFile, []byte("log data"), 0600))

			assert.Eventually(t, func() bool {
				sz, count := trk.Size()

				return count == 1 && sz == int64(len("log data"))
			}, 2*time.Second, 20*time.Millisecond)
		})
	}
}

func TestTracker_Sync(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup     func(t *testing.T) (*tracker.Tracker, func())
		wantErr   func(t *testing.T, err error)
		name      string
		wantCount int
		wantSize  int64
	}{
		{
			name: "sync directory with subdirectories and ignores archive",
			setup: func(t *testing.T) (*tracker.Tracker, func()) {
				t.Helper()
				tmp := t.TempDir()
				archive := filepath.Join(tmp, "archive")
				require.NoError(t, os.MkdirAll(archive, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(archive, "archived.log"), []byte("12345"), 0600))

				sub := filepath.Join(tmp, "sub")
				require.NoError(t, os.MkdirAll(sub, 0750))
				require.NoError(t, os.WriteFile(filepath.Join(sub, "f1.log"), []byte("123"), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(tmp, "f2.log"), []byte("4567"), 0600))

				trk, err := tracker.New(tmp, archive)
				require.NoError(t, err)

				return trk, func() { _ = trk.Close() }
			},
			wantSize:  7,
			wantCount: 2,
			wantErr:   nil,
		},
		{
			name: "sync single file fast path",
			setup: func(t *testing.T) (*tracker.Tracker, func()) {
				t.Helper()
				tmp := t.TempDir()
				filePath := filepath.Join(tmp, "app.log")
				require.NoError(t, os.WriteFile(filePath, []byte("single-file-content"), 0600))

				trk, err := tracker.New(filePath, filepath.Join(tmp, "archive"))
				require.NoError(t, err)

				return trk, func() { _ = trk.Close() }
			},
			wantSize:  int64(len("single-file-content")),
			wantCount: 1,
			wantErr:   nil,
		},
		{
			name: "sync non-existent root returns zero",
			setup: func(t *testing.T) (*tracker.Tracker, func()) {
				t.Helper()
				tmp := t.TempDir()
				trk, err := tracker.New(filepath.Join(tmp, "missing"), filepath.Join(tmp, "archive"))
				require.NoError(t, err)

				return trk, func() { _ = trk.Close() }
			},
			wantSize:  0,
			wantCount: 0,
			wantErr:   nil,
		},
		{
			name: "sync closed tracker returns error",
			setup: func(t *testing.T) (*tracker.Tracker, func()) {
				t.Helper()
				tmp := t.TempDir()
				trk, err := tracker.New(tmp, filepath.Join(tmp, "archive"))
				require.NoError(t, err)
				require.NoError(t, trk.Close())

				return trk, func() {}
			},
			wantSize:  0,
			wantCount: 0,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.ErrorIs(t, err, tracker.ErrTrackerClosed)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			trk, cleanup := tc.setup(t)
			defer cleanup()

			sz, count, err := trk.Sync()
			if tc.wantErr != nil {
				tc.wantErr(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantSize, sz)
				assert.Equal(t, tc.wantCount, count)
			}
		})
	}
}

func TestTracker_Size(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup     func(t *testing.T) *tracker.Tracker
		name      string
		wantCount int
		wantSize  int64
	}{
		{
			name: "returns cached sizes accurately",
			setup: func(t *testing.T) *tracker.Tracker {
				t.Helper()
				tmp := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(tmp, "a.log"), []byte("1234"), 0600))
				trk, err := tracker.New(tmp, filepath.Join(tmp, "archive"))
				require.NoError(t, err)
				t.Cleanup(func() { _ = trk.Close() })
				_, _, syncErr := trk.Sync()
				require.NoError(t, syncErr)

				return trk
			},
			wantSize:  4,
			wantCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			trk := tc.setup(t)
			sz, count := trk.Size()
			assert.Equal(t, tc.wantSize, sz)
			assert.Equal(t, tc.wantCount, count)
		})
	}
}

func TestTracker_OldestItems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup func(t *testing.T) *tracker.Tracker
		name  string
	}{
		{
			name: "returns entries sorted by modtime",
			setup: func(t *testing.T) *tracker.Tracker {
				t.Helper()
				tmp := t.TempDir()
				f1 := filepath.Join(tmp, "f1.log")
				f2 := filepath.Join(tmp, "f2.log")
				require.NoError(t, os.WriteFile(f1, []byte("first"), 0600))
				time.Sleep(10 * time.Millisecond)
				require.NoError(t, os.WriteFile(f2, []byte("second"), 0600))

				trk, err := tracker.New(tmp, filepath.Join(tmp, "archive"))
				require.NoError(t, err)
				t.Cleanup(func() { _ = trk.Close() })
				_, _, syncErr := trk.Sync()
				require.NoError(t, syncErr)

				return trk
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			trk := tc.setup(t)
			items := trk.OldestItems()
			require.Len(t, items, 2)
			assert.True(t, items[0].ModTime.Before(items[1].ModTime))
		})
	}
}

func TestTracker_Remove(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup func(t *testing.T) (*tracker.Tracker, string)
		name  string
	}{
		{
			name: "removes item and decreases size",
			setup: func(t *testing.T) (*tracker.Tracker, string) {
				t.Helper()
				tmp := t.TempDir()
				f := filepath.Join(tmp, "remove.log")
				require.NoError(t, os.WriteFile(f, []byte("12345"), 0600))

				trk, err := tracker.New(tmp, filepath.Join(tmp, "archive"))
				require.NoError(t, err)
				t.Cleanup(func() { _ = trk.Close() })
				_, _, syncErr := trk.Sync()
				require.NoError(t, syncErr)

				return trk, f
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			trk, path := tc.setup(t)
			trk.Remove(path)

			sz, count := trk.Size()
			assert.Equal(t, int64(0), sz)
			assert.Equal(t, 0, count)
		})
	}
}

func TestTracker_Events(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "events channel is non-nil"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			trk, err := tracker.New(tmp, filepath.Join(tmp, "archive"))
			require.NoError(t, err)
			defer trk.Close()

			assert.NotNil(t, trk.Events())
		})
	}
}

func TestTracker_Close(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "close can be called repeatedly without error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			trk, err := tracker.New(tmp, filepath.Join(tmp, "archive"))
			require.NoError(t, err)

			assert.NoError(t, trk.Close())
			assert.NoError(t, trk.Close())
		})
	}
}
