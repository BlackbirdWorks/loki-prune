package rotator_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackbirdworks/loki-prune/pkg/rotator"
)

func createBenchmarkTree(b *testing.B, fileCount int) (string, string) {
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

func BenchmarkCalculateDirSize(b *testing.B) {
	sourceDir, archiveDir := createBenchmarkTree(b, 500)
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		_, _, _ = rotator.CalculateDirSize(sourceDir, archiveDir)
	}
}

func BenchmarkCollectOldestItems(b *testing.B) {
	sourceDir, archiveDir := createBenchmarkTree(b, 500)
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		_, _ = rotator.CollectOldestItems(sourceDir, archiveDir)
	}
}
