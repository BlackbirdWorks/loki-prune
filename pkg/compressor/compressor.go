// Package compressor provides streaming tar archive creation with zstd and gzip support.
package compressor

import (
	"archive/tar"
	"archive/zip"
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// ErrUnsupportedCompression is returned when an invalid compression type is passed.
var ErrUnsupportedCompression = errors.New("unsupported compression type")

// CompressionType represents supported compression algorithms.
type CompressionType string

const (
	// TypeZstd represents Zstandard compression.
	TypeZstd CompressionType = "zstd"
	// TypeGzip represents Gzip compression.
	TypeGzip CompressionType = "gzip"
	// TypeZip represents standard Zip compression.
	TypeZip CompressionType = "zip"
	// TypeNone represents uncompressed tar archives.
	TypeNone CompressionType = "none"

	dirPerm  = 0750
	filePerm = 0600

	zstdLevelFastest = 1
	zstdLevelDefault = 4
	zstdLevelBetter  = 9
)

// Compressor compresses files and directories into tar archives.
type Compressor struct {
	bufPool    *sync.Pool
	writerPool *sync.Pool
	compType   CompressionType
	level      int
}

// New creates a new Compressor.
func New(cType string, level int) (*Compressor, error) {
	norm := strings.ToLower(strings.TrimSpace(cType))
	switch norm {
	case "zstd", "":
		return NewWithType(TypeZstd, level), nil
	case "gzip", "gz":
		return NewWithType(TypeGzip, level), nil
	case "zip":
		return NewWithType(TypeZip, level), nil
	case "none":
		return NewWithType(TypeNone, 0), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCompression, cType)
	}
}

// NewWithType creates a Compressor with an explicit CompressionType.
func NewWithType(compType CompressionType, level int) *Compressor {
	return &Compressor{
		compType:   compType,
		level:      level,
		writerPool: &sync.Pool{},
		bufPool: &sync.Pool{
			New: func() any {
				buf := make([]byte, copyBufSize)

				return &buf
			},
		},
	}
}

// Extension returns the file extension for this compression type.
func (c *Compressor) Extension() string {
	switch c.compType {
	case TypeZstd:
		return ".tar.zst"
	case TypeGzip:
		return ".tar.gz"
	case TypeZip:
		return ".zip"
	case TypeNone:
		return ".tar"
	default:
		return ".tar"
	}
}

// CompressDirectory archives sourceDir into targetArchive.
func (c *Compressor) CompressDirectory(sourceDir, targetArchive string) error {
	if c.compType == TypeZip {
		return c.compressZip(sourceDir, targetArchive)
	}

	return writeAtomicArchive(targetArchive, func(outFile io.Writer) error {
		compWriter, compErr := c.createCompWriter(outFile)
		if compErr != nil {
			return compErr
		}

		tarWriter := tar.NewWriter(compWriter)
		if walkErr := c.archiveWalk(sourceDir, tarWriter); walkErr != nil {
			_ = compWriter.Close()

			return walkErr
		}

		if tarCloseErr := tarWriter.Close(); tarCloseErr != nil {
			_ = compWriter.Close()

			return fmt.Errorf("closing tar writer: %w", tarCloseErr)
		}
		if compCloseErr := compWriter.Close(); compCloseErr != nil {
			return fmt.Errorf("closing compression writer: %w", compCloseErr)
		}

		return nil
	})
}

func writeAtomicArchive(targetArchive string, writeFn func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(targetArchive), dirPerm); err != nil {
		return fmt.Errorf("creating target parent directory: %w", err)
	}

	tempArchive := targetArchive + ".tmp"
	outFile, openErr := os.OpenFile(tempArchive, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
	if openErr != nil {
		return fmt.Errorf("creating temporary archive file: %w", openErr)
	}
	defer func() {
		_ = outFile.Close()
		_ = os.Remove(tempArchive)
	}()

	if err := writeFn(outFile); err != nil {
		return err
	}

	if syncErr := outFile.Sync(); syncErr != nil {
		return fmt.Errorf("syncing archive file: %w", syncErr)
	}
	if closeErr := outFile.Close(); closeErr != nil {
		return fmt.Errorf("closing file: %w", closeErr)
	}

	if renameErr := os.Rename(tempArchive, targetArchive); renameErr != nil {
		return fmt.Errorf("moving temporary archive to final location: %w", renameErr)
	}

	return nil
}

type pooledWriteCloser struct {
	w     io.WriteCloser
	pool  *sync.Pool
	reset func(io.Writer)
}

func (p *pooledWriteCloser) Write(b []byte) (int, error) {
	return p.w.Write(b)
}

func (p *pooledWriteCloser) Close() error {
	if p.w == nil {
		return nil
	}

	w := p.w
	p.w = nil
	err := w.Close()
	p.reset(nil)
	p.pool.Put(w)

	return err
}

func (c *Compressor) createCompWriter(w io.Writer) (io.WriteCloser, error) {
	switch c.compType {
	case TypeZstd:
		return c.acquireZstdWriter(w)
	case TypeGzip:
		return c.acquireGzipWriter(w)
	case TypeNone:
		return NopWriteCloser(w), nil
	default:
		return nil, ErrUnsupportedCompression
	}
}

func (c *Compressor) acquireZstdWriter(w io.Writer) (io.WriteCloser, error) {
	if pooled, ok := c.writerPool.Get().(*zstd.Encoder); ok {
		pooled.Reset(w)

		return &pooledWriteCloser{w: pooled, pool: c.writerPool, reset: pooled.Reset}, nil
	}

	zLevel := zstd.SpeedDefault
	if c.level > 0 {
		switch {
		case c.level <= zstdLevelFastest:
			zLevel = zstd.SpeedFastest
		case c.level <= zstdLevelDefault:
			zLevel = zstd.SpeedDefault
		case c.level <= zstdLevelBetter:
			zLevel = zstd.SpeedBetterCompression
		default:
			zLevel = zstd.SpeedBestCompression
		}
	}

	zEnc, zErr := zstd.NewWriter(
		w,
		zstd.WithEncoderLevel(zLevel),
		zstd.WithEncoderConcurrency(runtime.GOMAXPROCS(0)),
	)
	if zErr != nil {
		return nil, fmt.Errorf("initializing zstd writer: %w", zErr)
	}

	return &pooledWriteCloser{w: zEnc, pool: c.writerPool, reset: zEnc.Reset}, nil
}

func (c *Compressor) acquireGzipWriter(w io.Writer) (io.WriteCloser, error) {
	if pooled, ok := c.writerPool.Get().(*gzip.Writer); ok {
		pooled.Reset(w)

		return &pooledWriteCloser{w: pooled, pool: c.writerPool, reset: pooled.Reset}, nil
	}

	gzLevel := gzip.DefaultCompression
	if c.level >= 1 && c.level <= 9 {
		gzLevel = c.level
	}
	gzWriter, gzErr := gzip.NewWriterLevel(w, gzLevel)
	if gzErr != nil {
		return nil, fmt.Errorf("initializing gzip writer: %w", gzErr)
	}

	return &pooledWriteCloser{w: gzWriter, pool: c.writerPool, reset: gzWriter.Reset}, nil
}

func (c *Compressor) archiveWalk(sourceDir string, tw *tar.Writer) error {
	return filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if !d.IsDir() && !d.Type().IsRegular() {
			return nil
		}

		relPath, relErr := filepath.Rel(sourceDir, path)
		if relErr != nil {
			return fmt.Errorf("rel path calculation: %w", relErr)
		}

		if relPath == "." {
			return nil
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			return fmt.Errorf("getting file info for %s: %w", path, infoErr)
		}

		header, headErr := tar.FileInfoHeader(info, info.Name())
		if headErr != nil {
			return fmt.Errorf("creating tar header for %s: %w", path, headErr)
		}
		header.Name = filepath.ToSlash(relPath)

		if writeErr := tw.WriteHeader(header); writeErr != nil {
			return fmt.Errorf("writing tar header for %s: %w", path, writeErr)
		}

		if !d.IsDir() {
			return c.copyFileToTar(path, tw)
		}

		return nil
	})
}

func (c *Compressor) compressZip(sourceDir, targetArchive string) error {
	return writeAtomicArchive(targetArchive, func(outFile io.Writer) error {
		zw := zip.NewWriter(outFile)
		if c.level > 0 {
			zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
				lvl := flate.DefaultCompression
				if c.level >= 1 && c.level <= 9 {
					lvl = c.level
				}

				return flate.NewWriter(out, lvl)
			})
		}

		walkErr := filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}

			return c.zipWalkEntry(zw, sourceDir, path, d)
		})
		if walkErr != nil {
			return walkErr
		}

		if closeErr := zw.Close(); closeErr != nil {
			return fmt.Errorf("closing zip writer: %w", closeErr)
		}

		return nil
	})
}

func (c *Compressor) zipWalkEntry(zw *zip.Writer, sourceDir, path string, d fs.DirEntry) error {
	if !d.IsDir() && !d.Type().IsRegular() {
		return nil
	}

	relPath, relErr := filepath.Rel(sourceDir, path)
	if relErr != nil {
		return fmt.Errorf("rel path calculation: %w", relErr)
	}
	if relPath == "." {
		return nil
	}

	info, infoErr := d.Info()
	if infoErr != nil {
		return fmt.Errorf("getting file info for %s: %w", path, infoErr)
	}

	header, headErr := zip.FileInfoHeader(info)
	if headErr != nil {
		return fmt.Errorf("creating zip header for %s: %w", path, headErr)
	}
	header.Name = filepath.ToSlash(relPath)
	if d.IsDir() {
		header.Name += "/"
	} else {
		header.Method = zip.Deflate
	}

	w, createErr := zw.CreateHeader(header)
	if createErr != nil {
		return fmt.Errorf("creating zip entry for %s: %w", path, createErr)
	}

	if !d.IsDir() {
		return c.copyFileToWriter(path, w)
	}

	return nil
}

const copyBufSize = 64 * 1024

func (c *Compressor) copyFileToTar(path string, tw *tar.Writer) error {
	return c.copyFileToWriter(path, tw)
}

func (c *Compressor) copyFileToWriter(path string, w io.Writer) error {
	srcFile, openErr := os.Open(path)
	if openErr != nil {
		return fmt.Errorf("opening file %s: %w", path, openErr)
	}
	defer srcFile.Close()

	bufPtr, ok := c.bufPool.Get().(*[]byte)
	if !ok {
		buf := make([]byte, copyBufSize)
		bufPtr = &buf
	}
	defer c.bufPool.Put(bufPtr)

	if _, copyErr := io.CopyBuffer(w, srcFile, *bufPtr); copyErr != nil {
		return fmt.Errorf("copying file %s into archive: %w", path, copyErr)
	}

	return nil
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

// NopWriteCloser wraps an io.Writer with a no-op Close method.
func NopWriteCloser(w io.Writer) io.WriteCloser {
	return nopWriteCloser{w}
}
