package service_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackbirdworks/loki-prune/internal/service"
)

func createTrackerBenchTree(b *testing.B, fileCount int) (string, string) {
	b.Helper()
	tmp := b.TempDir()
	sourceDir := filepath.Join(tmp, "source")
	archiveDir := filepath.Join(sourceDir, "archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		b.Fatal(err)
	}

	payload := []byte("loki-log-chunk-payload-data")
	now := time.Now()

	for i := range fileCount {
		subDir := filepath.Join(sourceDir, fmt.Sprintf("sub-%d", i%10))
		if err := os.MkdirAll(subDir, 0755); err != nil {
			b.Fatal(err)
		}
		filePath := filepath.Join(subDir, fmt.Sprintf("chunk-%d.log", i))
		if err := os.WriteFile(filePath, payload, 0600); err != nil {
			b.Fatal(err)
		}
		fileTime := now.Add(time.Duration(i) * time.Millisecond)
		_ = os.Chtimes(filePath, fileTime, fileTime)
	}

	return sourceDir, archiveDir
}

func BenchmarkTracker_Sync(b *testing.B) {
	sourceDir, archiveDir := createTrackerBenchTree(b, 500)
	trk, err := service.NewTracker(sourceDir, archiveDir, 1*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	defer trk.Close()

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		_, _, _ = trk.Sync()
	}
}

func BenchmarkTracker_OldestItems(b *testing.B) {
	sourceDir, archiveDir := createTrackerBenchTree(b, 500)
	trk, err := service.NewTracker(sourceDir, archiveDir, 1*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	defer trk.Close()
	_, _, _ = trk.Sync()

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		_ = trk.OldestItems()
	}
}
