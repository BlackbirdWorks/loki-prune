package rotator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blackbirdworks/loki-prune/pkg/compressor"
	"github.com/blackbirdworks/loki-prune/pkg/config"
	"github.com/blackbirdworks/loki-prune/pkg/rotator"
)

func TestCalculateDirSize_And_CollectOldestItems(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "source")
	archiveDir := filepath.Join(sourceDir, "archive")
	require.NoError(t, os.MkdirAll(sourceDir, 0755))
	require.NoError(t, os.MkdirAll(archiveDir, 0755))

	f1 := filepath.Join(sourceDir, "file1.log")
	f2 := filepath.Join(sourceDir, "file2.log")
	ignored := filepath.Join(archiveDir, "ignored.log")

	require.NoError(t, os.WriteFile(f1, []byte("hello"), 0600))
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(f2, []byte("world!"), 0600))
	require.NoError(t, os.WriteFile(ignored, []byte("should-be-ignored"), 0600))

	sz, count, err := rotator.CalculateDirSize(sourceDir, archiveDir)
	require.NoError(t, err)
	assert.Equal(t, int64(11), sz)
	assert.Equal(t, 2, count)

	items, err := rotator.CollectOldestItems(sourceDir, archiveDir)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, f1, items[0].Path)
	assert.Equal(t, f2, items[1].Path)
}

func TestRotate(t *testing.T) {
	t.Parallel()

	t.Run("dry run does not move files", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		sourceDir := filepath.Join(tmp, "source")
		archiveDir := filepath.Join(tmp, "archive")
		require.NoError(t, os.MkdirAll(sourceDir, 0755))

		f1 := filepath.Join(sourceDir, "test.log")
		require.NoError(t, os.WriteFile(f1, []byte("data"), 0600))

		res, err := rotator.Rotate(context.Background(), rotator.RotateOptions{
			SourceDir:   sourceDir,
			ArchiveDir:  archiveDir,
			BytesToFree: 4,
			DryRun:      true,
		})
		require.NoError(t, err)
		assert.Equal(t, 1, res.MovedCount)
		assert.Equal(t, int64(4), res.MovedBytes)

		// File should still exist in source
		_, statErr := os.Stat(f1)
		assert.NoError(t, statErr)
	})

	t.Run("moves items to archive directory", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		sourceDir := filepath.Join(tmp, "source")
		archiveDir := filepath.Join(tmp, "archive")
		require.NoError(t, os.MkdirAll(sourceDir, 0755))

		f1 := filepath.Join(sourceDir, "test.log")
		require.NoError(t, os.WriteFile(f1, []byte("data"), 0600))

		res, err := rotator.Rotate(context.Background(), rotator.RotateOptions{
			SourceDir:   sourceDir,
			ArchiveDir:  archiveDir,
			BytesToFree: 4,
			DryRun:      false,
		})
		require.NoError(t, err)
		assert.Equal(t, 1, res.MovedCount)
		assert.Equal(t, int64(4), res.MovedBytes)

		// File should no longer exist in source
		_, statErr := os.Stat(f1)
		assert.True(t, os.IsNotExist(statErr))

		// Target file exists in rotation directory
		archivedFile := filepath.Join(res.RotationDir, "test.log")
		_, archStat := os.Stat(archivedFile)
		assert.NoError(t, archStat)
	})
}

func TestCompress_And_EnforceRetention(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	archiveDir := filepath.Join(tmp, "archive")
	require.NoError(t, os.MkdirAll(archiveDir, 0755))

	rot1 := filepath.Join(archiveDir, "rotation-2026-01-01-000001")
	rot2 := filepath.Join(archiveDir, "rotation-2026-01-01-000002")
	require.NoError(t, os.MkdirAll(rot1, 0755))
	require.NoError(t, os.MkdirAll(rot2, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(rot1, "data.log"), []byte("sample1"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(rot2, "data.log"), []byte("sample2"), 0600))

	past := time.Now().Add(-10 * time.Minute)
	now := time.Now()
	require.NoError(t, os.Chtimes(rot1, past, past))
	require.NoError(t, os.Chtimes(rot2, now, now))

	comp, err := compressor.New("zstd", 1)
	require.NoError(t, err)

	rotCfg := config.RotationConfig{
		Type:  "zstd",
		Level: 1,
		Delay: config.DelayConfig{
			Rotations: 1, // Keep newest 1 uncompressed
		},
	}

	err = rotator.Compress(context.Background(), archiveDir, rotCfg, comp, false)
	require.NoError(t, err)

	// rot1 should be compressed to .tar.zst and raw dir deleted
	_, statRot1Raw := os.Stat(rot1)
	assert.True(t, os.IsNotExist(statRot1Raw))
	_, statRot1Comp := os.Stat(rot1 + ".tar.zst")
	require.NoError(t, statRot1Comp)

	// rot2 should remain uncompressed due to Rotations: 1 delay
	_, statRot2Raw := os.Stat(rot2)
	require.NoError(t, statRot2Raw)

	// Now test retention: keep only 1 rotation
	err = rotator.EnforceRetention(context.Background(), archiveDir, 1, false)
	require.NoError(t, err)

	// Oldest rotation (.tar.zst) should be pruned
	_, statRot1After := os.Stat(rot1 + ".tar.zst")
	assert.True(t, os.IsNotExist(statRot1After))

	// Newer rotation should still exist
	_, statRot2After := os.Stat(rot2)
	assert.NoError(t, statRot2After)
}
