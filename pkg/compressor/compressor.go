package compressor

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// CompressionType represents supported compression algorithms.
type CompressionType string

const (
	TypeZstd CompressionType = "zstd"
	TypeGzip CompressionType = "gzip"
	TypeNone CompressionType = "none"
)

// Compressor compresses files and directories into tar archives.
type Compressor struct {
	compType CompressionType
	level    int
}

// New creates a new Compressor.
func New(cType string, level int) (*Compressor, error) {
	norm := strings.ToLower(strings.TrimSpace(cType))
	switch norm {
	case "zstd", "":
		return &Compressor{compType: TypeZstd, level: level}, nil
	case "gzip", "gz":
		return &Compressor{compType: TypeGzip, level: level}, nil
	case "none":
		return &Compressor{compType: TypeNone, level: 0}, nil
	default:
		return nil, fmt.Errorf("unsupported compression type: %s", cType)
	}
}

// Extension returns the file extension for this compression type.
func (c *Compressor) Extension() string {
	switch c.compType {
	case TypeZstd:
		return ".tar.zst"
	case TypeGzip:
		return ".tar.gz"
	case TypeNone:
		return ".tar"
	default:
		return ".tar"
	}
}

// CompressDirectory archives sourceDir into targetArchive.
func (c *Compressor) CompressDirectory(sourceDir, targetArchive string) error {
	if err := os.MkdirAll(filepath.Dir(targetArchive), 0755); err != nil {
		return fmt.Errorf("creating target parent directory: %w", err)
	}

	tempArchive := targetArchive + ".tmp"
	outFile, err := os.Create(tempArchive)
	if err != nil {
		return fmt.Errorf("creating temporary archive file: %w", err)
	}
	defer func() {
		outFile.Close()
		os.Remove(tempArchive) // no-op if renamed
	}()

	var compWriter io.WriteCloser
	switch c.compType {
	case TypeZstd:
		zLevel := zstd.SpeedDefault
		if c.level > 0 {
			switch {
			case c.level <= 1:
				zLevel = zstd.SpeedFastest
			case c.level <= 4:
				zLevel = zstd.SpeedDefault
			case c.level <= 9:
				zLevel = zstd.SpeedBetterCompression
			default:
				zLevel = zstd.SpeedBestCompression
			}
		}
		zEnc, err := zstd.NewWriter(outFile, zstd.WithEncoderLevel(zLevel))
		if err != nil {
			return fmt.Errorf("initializing zstd writer: %w", err)
		}
		compWriter = zEnc
	case TypeGzip:
		gzLevel := gzip.DefaultCompression
		if c.level >= 1 && c.level <= 9 {
			gzLevel = c.level
		}
		gzWriter, err := gzip.NewWriterLevel(outFile, gzLevel)
		if err != nil {
			return fmt.Errorf("initializing gzip writer: %w", err)
		}
		compWriter = gzWriter
	case TypeNone:
		compWriter = NopWriteCloser(outFile)
	}

	tarWriter := tar.NewWriter(compWriter)

	// Walk sourceDir and write to tarWriter
	err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return fmt.Errorf("rel path calculation: %w", err)
		}

		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return fmt.Errorf("creating tar header for %s: %w", path, err)
		}
		header.Name = filepath.ToSlash(relPath)

		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("writing tar header for %s: %w", path, err)
		}

		if !info.IsDir() {
			srcFile, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("opening file %s: %w", path, err)
			}
			defer srcFile.Close()

			if _, err := io.Copy(tarWriter, srcFile); err != nil {
				return fmt.Errorf("copying file %s into tar: %w", path, err)
			}
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("tar traversal failed: %w", err)
	}

	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("closing tar writer: %w", err)
	}
	if err := compWriter.Close(); err != nil {
		return fmt.Errorf("closing compression writer: %w", err)
	}
	if err := outFile.Sync(); err != nil {
		return fmt.Errorf("syncing archive file: %w", err)
	}
	if err := outFile.Close(); err != nil {
		return fmt.Errorf("closing file: %w", err)
	}

	if err := os.Rename(tempArchive, targetArchive); err != nil {
		return fmt.Errorf("moving temporary archive to final location: %w", err)
	}

	return nil
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

func NopWriteCloser(w io.Writer) io.WriteCloser {
	return nopWriteCloser{w}
}
