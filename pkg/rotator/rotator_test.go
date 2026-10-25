package rotator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackbirdworks/loki-prune/pkg/config"
)

func TestRotator_Process(t *testing.T) {
	tempDir := t.TempDir()
	watchDir := filepath.Join(tempDir, "chunks")
	archiveDir := filepath.Join(tempDir, "archive")
	_ = os.MkdirAll(watchDir, 0755)

	// Create 5 chunk files, 1KB each
	for i := 1; i <= 5; i++ {
		p := filepath.Join(watchDir, "chunk-"+string(rune('0'+i))+".bin")
		_ = os.WriteFile(p, make([]byte, 1024), 0644)
		// Small sleep to ensure different mod times
		time.Sleep(10 * time.Millisecond)
	}

	pCfg := config.PathConfig{
		Name:             "test",
		Path:             watchDir,
		ArchiveDir:       archiveDir,
		TriggerSizeBytes: 3 * 1024, // 3KB trigger
		TargetSizeBytes:  2 * 1024, // 2KB target
		Rotation: config.RotationConfig{
			MaxRotations: 3,
			Compress:     true,
			Type:         "zstd",
			Level:        1,
			Delay: config.DelayConfig{
				Rotations: 0, // compress immediately
			},
		},
	}

	lokiCfg := config.LokiConfig{
		URL:     "http://127.0.0.1:9999", // mock / invalid url, flush fails safely
		Timeout: 50 * time.Millisecond,
	}

	r, err := New(pCfg, lokiCfg, false)
	if err != nil {
		t.Fatalf("New rotator error: %v", err)
	}

	ctx := context.Background()
	if err := r.Process(ctx); err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	// Verify remaining size in watchDir is <= TargetSizeBytes
	remainingSize, _, err := r.calculateDirSize(watchDir)
	if err != nil {
		t.Fatal(err)
	}
	if remainingSize > pCfg.TargetSizeBytes {
		t.Errorf("expected remaining size <= %d, got %d", pCfg.TargetSizeBytes, remainingSize)
	}

	// Verify an archive file exists
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Errorf("expected archive entries in %s, got 0", archiveDir)
	}
}

func TestRotator_DelayedCompression(t *testing.T) {
	tempDir := t.TempDir()
	archiveDir := filepath.Join(tempDir, "archive")
	_ = os.MkdirAll(archiveDir, 0755)

	// Create 3 uncompressed rotation folders
	for i := 1; i <= 3; i++ {
		rotDir := filepath.Join(archiveDir, "rotation-2026-01-0"+string(rune('0'+i)))
		_ = os.MkdirAll(rotDir, 0755)
		_ = os.WriteFile(filepath.Join(rotDir, "data.bin"), []byte("sample"), 0644)
		time.Sleep(10 * time.Millisecond)
	}

	pCfg := config.PathConfig{
		Name:             "delayed-test",
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
				Rotations: 2, // Keep newest 2 uncompressed, compress the 3rd (oldest)
			},
		},
	}

	r, err := New(pCfg, config.LokiConfig{}, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.processCompression(); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(archiveDir)
	uncompressedCount := 0
	compressedCount := 0
	for _, e := range entries {
		if e.IsDir() {
			uncompressedCount++
		} else if filepath.Ext(e.Name()) == ".zst" {
			compressedCount++
		}
	}

	if uncompressedCount != 2 {
		t.Errorf("expected 2 uncompressed folders, got %d", uncompressedCount)
	}
	if compressedCount != 1 {
		t.Errorf("expected 1 compressed archive, got %d", compressedCount)
	}
}
