package service_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blackbirdworks/loki-prune/internal/service"
)

func TestTracker_BasicAndSync(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	archiveDir := filepath.Join(tmp, "archive")
	require.NoError(t, os.MkdirAll(archiveDir, 0755))

	f1 := filepath.Join(tmp, "file1.log")
	require.NoError(t, os.WriteFile(f1, []byte("data-1"), 0600))

	trk, err := service.NewTracker(tmp, archiveDir, 50*time.Millisecond)
	require.NoError(t, err)
	defer trk.Close()

	totalSize, count, err := trk.Sync()
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Equal(t, int64(6), totalSize)

	sz, c := trk.Size()
	assert.Equal(t, totalSize, sz)
	assert.Equal(t, count, c)

	items := trk.OldestItems()
	require.Len(t, items, 1)
	assert.Equal(t, f1, items[0].Path)

	trk.Remove(f1)
	szAfter, cAfter := trk.Size()
	assert.Equal(t, int64(0), szAfter)
	assert.Equal(t, 0, cAfter)
}

func TestTracker_DebouncedEvents(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	archiveDir := filepath.Join(tmp, "archive")
	require.NoError(t, os.MkdirAll(archiveDir, 0755))

	trk, err := service.NewTracker(tmp, archiveDir, 50*time.Millisecond)
	require.NoError(t, err)
	defer trk.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	trk.Start(ctx)

	// Write a file
	testFile := filepath.Join(tmp, "chunk.log")
	require.NoError(t, os.WriteFile(testFile, []byte("log data"), 0600))

	select {
	case <-trk.Events():
		// Received debounced event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for debounced event")
	}

	sz, count := trk.Size()
	assert.Equal(t, 1, count)
	assert.Equal(t, int64(len("log data")), sz)
}

func TestTracker_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	archiveDir := filepath.Join(tmp, "archive")
	require.NoError(t, os.MkdirAll(archiveDir, 0755))

	trk, err := service.NewTracker(tmp, archiveDir, 10*time.Millisecond)
	require.NoError(t, err)

	ctx := t.Context()
	trk.Start(ctx)

	var wg sync.WaitGroup
	const workers = 10
	const iterations = 50

	// Concurrent file writers & syncers
	for i := range workers {
		wg.Go(func() {
			for j := range iterations {
				p := filepath.Join(tmp, fmt.Sprintf("worker-%d-%d.log", i, j))
				_ = os.WriteFile(p, []byte("data"), 0600)
				_, _, _ = trk.Sync()
				_ = os.Remove(p)
			}
		})
	}

	// Concurrent readers
	for range workers {
		wg.Go(func() {
			for range iterations {
				_, _ = trk.Size()
				_ = trk.OldestItems()
				trk.Remove(filepath.Join(tmp, "non-existent.log"))
			}
		})
	}

	wg.Wait()
	require.NoError(t, trk.Close())
}
