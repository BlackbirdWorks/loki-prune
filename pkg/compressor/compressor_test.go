package compressor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompressDirectory(t *testing.T) {
	types := []string{"zstd", "gzip", "none"}

	for _, cType := range types {
		t.Run(cType, func(t *testing.T) {
			tempDir := t.TempDir()
			sourceDir := filepath.Join(tempDir, "source")
			if err := os.MkdirAll(sourceDir, 0755); err != nil {
				t.Fatal(err)
			}

			// Create some test files
			file1 := filepath.Join(sourceDir, "chunk-1.bin")
			file2 := filepath.Join(sourceDir, "chunk-2.bin")
			_ = os.WriteFile(file1, []byte("test log chunk data 1234567890"), 0644)
			_ = os.WriteFile(file2, []byte("second log chunk data ABCDEFGHIJKLMNOPQRSTUVWXYZ"), 0644)

			comp, err := New(cType, 1)
			if err != nil {
				t.Fatalf("New(%s) error: %v", cType, err)
			}

			targetArchive := filepath.Join(tempDir, "archive"+comp.Extension())
			if err := comp.CompressDirectory(sourceDir, targetArchive); err != nil {
				t.Fatalf("CompressDirectory failed: %v", err)
			}

			info, err := os.Stat(targetArchive)
			if err != nil {
				t.Fatalf("archive file does not exist: %v", err)
			}
			if info.Size() == 0 {
				t.Errorf("archive file is empty")
			}
		})
	}
}
