// Package rotator provides stateless functions for directory size calculation,
// file rotation staging, cross-device file moves, delayed compression, and retention enforcement.
package rotator

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/blackbirdworks/loki-prune/pkg/compressor"
	"github.com/blackbirdworks/loki-prune/pkg/config"
	"github.com/blackbirdworks/loki-prune/pkg/logger"
)

const (
	dirPerm              = 0750
	filePerm             = 0600
	defaultBatchCapacity = 128
)

// FileItem represents a candidate file for rotation.
type FileItem struct {
	ModTime time.Time
	Path    string
	Size    int64
}

// RotateOptions controls the execution of a file rotation batch.
type RotateOptions struct {
	SourceDir   string
	ArchiveDir  string
	Items       []FileItem
	BytesToFree int64
	DryRun      bool
}

// RotateResult contains metadata about the performed rotation batch.
type RotateResult struct {
	RotationDir string
	MovedItems  []FileItem
	MovedBytes  int64
	MovedCount  int
}

// Rotate executes a single rotation cycle, moving items up to bytesToFree into a timestamped archive directory.
func Rotate(ctx context.Context, opts RotateOptions) (RotateResult, error) {
	var result RotateResult
	log := logger.FromContext(ctx)

	items := opts.Items
	if len(items) == 0 {
		var err error
		items, err = CollectOldestItems(opts.SourceDir, opts.ArchiveDir)
		if err != nil {
			return result, fmt.Errorf("collecting items to rotate: %w", err)
		}
	}

	timestamp := time.Now().UTC().Format("2006-01-02-150405")
	rotationDirName := fmt.Sprintf("rotation-%s", timestamp)
	rotationDirPath := filepath.Join(opts.ArchiveDir, rotationDirName)
	result.RotationDir = rotationDirPath

	if !opts.DryRun {
		if mkErr := os.MkdirAll(rotationDirPath, dirPerm); mkErr != nil {
			return result, fmt.Errorf("creating rotation directory %s: %w", rotationDirPath, mkErr)
		}
	}

	for _, item := range items {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}

		if result.MovedBytes >= opts.BytesToFree {
			break
		}

		if !opts.DryRun {
			if err := moveSingleItem(item, opts.SourceDir, rotationDirPath); err != nil {
				log.WarnContext(ctx, "Failed to move item, skipping", "path", item.Path, "err", err)

				continue
			}
		}

		result.MovedBytes += item.Size
		result.MovedCount++
		result.MovedItems = append(result.MovedItems, item)
	}

	log.InfoContext(ctx, "Rotation batch staged",
		"directory", rotationDirPath,
		"items_moved", result.MovedCount,
		"freed_bytes", config.FormatBytes(result.MovedBytes),
		"dry_run", opts.DryRun,
	)

	return result, nil
}

func moveSingleItem(item FileItem, sourceRoot, targetDir string) error {
	relPath, relErr := filepath.Rel(sourceRoot, item.Path)
	if relErr != nil {
		return relErr
	}
	if relPath == "." {
		relPath = filepath.Base(item.Path)
	}
	destPath := filepath.Join(targetDir, relPath)

	if mkErr := os.MkdirAll(filepath.Dir(destPath), dirPerm); mkErr != nil {
		return fmt.Errorf("creating dest dir %s: %w", filepath.Dir(destPath), mkErr)
	}

	if renErr := os.Rename(item.Path, destPath); renErr != nil {
		if copyErr := moveFileFallback(item.Path, destPath); copyErr != nil {
			return fmt.Errorf("moving %s to %s: %w", item.Path, destPath, copyErr)
		}
	}

	if item.Path == sourceRoot {
		emptyFile, createErr := os.OpenFile(sourceRoot, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
		if createErr == nil {
			_ = emptyFile.Close()
		}
	}

	return nil
}

func moveFileFallback(src, dst string) (err error) {
	in, openErr := os.Open(src)
	if openErr != nil {
		return fmt.Errorf("opening source file for cross-device copy: %w", openErr)
	}
	defer in.Close()

	out, createErr := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
	if createErr != nil {
		return fmt.Errorf("creating destination file for cross-device copy: %w", createErr)
	}
	defer func() {
		_ = out.Close()
		if err != nil {
			_ = os.Remove(dst)
		}
	}()

	if _, copyErr := io.Copy(out, in); copyErr != nil {
		return fmt.Errorf("copying across devices: %w", copyErr)
	}
	if syncErr := out.Sync(); syncErr != nil {
		return fmt.Errorf("syncing copied file: %w", syncErr)
	}
	if closeErr := out.Close(); closeErr != nil {
		return fmt.Errorf("closing copied file: %w", closeErr)
	}
	_ = in.Close()

	return os.Remove(src)
}

// Compress evaluates uncompressed rotation batches in archiveDir and compresses eligible batches.
func Compress(
	ctx context.Context,
	archiveDir string,
	rotCfg config.RotationConfig,
	comp *compressor.Compressor,
	dryRun bool,
) error {
	if _, err := os.Stat(archiveDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		return fmt.Errorf("reading archive directory %s: %w", archiveDir, err)
	}

	log := logger.FromContext(ctx)
	uncompressed := getUncompressedRotations(entries)
	now := time.Now()

	for idx, dirInfo := range uncompressed {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if shouldDelayCompression(idx, dirInfo.ModTime(), now, rotCfg.Delay) {
			continue
		}

		srcDir := filepath.Join(archiveDir, dirInfo.Name())
		archiveFile := srcDir + comp.Extension()

		log.InfoContext(ctx, "Compressing rotation archive",
			"source", srcDir,
			"target", archiveFile,
			"type", rotCfg.Type,
			"level", rotCfg.Level,
		)

		if dryRun {
			continue
		}

		if compErr := comp.CompressDirectory(srcDir, archiveFile); compErr != nil {
			log.ErrorContext(ctx, "Failed to compress rotation", "dir", srcDir, "err", compErr)

			continue
		}

		_ = os.Chtimes(archiveFile, dirInfo.ModTime(), dirInfo.ModTime())

		if rmErr := os.RemoveAll(srcDir); rmErr != nil {
			log.WarnContext(ctx, "Failed to remove raw directory after compression", "dir", srcDir, "err", rmErr)
		} else {
			log.InfoContext(ctx, "Compression complete, raw directory purged", "archive", archiveFile)
		}
	}

	return nil
}

func getUncompressedRotations(entries []os.DirEntry) []os.FileInfo {
	var uncompressed []os.FileInfo
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "rotation-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		uncompressed = append(uncompressed, info)
	}

	slices.SortFunc(uncompressed, func(a, b os.FileInfo) int {
		return b.ModTime().Compare(a.ModTime())
	})

	return uncompressed
}

func shouldDelayCompression(rank int, modTime, now time.Time, delay config.DelayConfig) bool {
	if delay.Rotations > 0 && rank < delay.Rotations {
		return true
	}

	if delay.Age > 0 && now.Sub(modTime) < delay.Age {
		return true
	}

	return false
}

// EnforceRetention cleans up expired rotation archives exceeding maxRotations.
func EnforceRetention(ctx context.Context, archiveDir string, maxRotations int, dryRun bool) error {
	if maxRotations <= 0 {
		return nil
	}

	if _, err := os.Stat(archiveDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		return fmt.Errorf("reading archive directory %s: %w", archiveDir, err)
	}

	allRotations := collectAllRotations(archiveDir, entries)
	if len(allRotations) <= maxRotations {
		return nil
	}

	slices.SortFunc(allRotations, func(a, b rotationItem) int {
		return a.modTime.Compare(b.modTime)
	})

	log := logger.FromContext(ctx)
	toDelete := len(allRotations) - maxRotations
	for idx := range toDelete {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		target := allRotations[idx].path
		log.InfoContext(ctx, "Pruning expired rotation", "target", target, "limit", maxRotations)
		if !dryRun {
			if rmErr := os.RemoveAll(target); rmErr != nil {
				log.ErrorContext(ctx, "Failed to remove expired rotation", "target", target, "err", rmErr)
			}
		}
	}

	return nil
}

type rotationItem struct {
	modTime time.Time
	path    string
}

func collectAllRotations(archiveDir string, entries []os.DirEntry) []rotationItem {
	var items []rotationItem
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "rotation-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, rotationItem{
			path:    filepath.Join(archiveDir, name),
			modTime: info.ModTime(),
		})
	}

	return items
}

// CalculateDirSize walks dir recursively and returns the total byte size and file count, skipping ignoreDir.
func CalculateDirSize(dir, ignoreDir string) (int64, int, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("stat path %s: %w", dir, err)
	}
	if !st.IsDir() {
		return st.Size(), 1, nil
	}

	var totalSize int64
	var fileCount int
	cleanIgnore := filepath.Clean(ignoreDir)
	cleanIgnorePrefix := cleanIgnore + string(filepath.Separator)

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}

		if d.IsDir() {
			if path == cleanIgnore || strings.HasPrefix(path, cleanIgnorePrefix) {
				return filepath.SkipDir
			}
		} else {
			info, statErr := d.Info()
			if statErr != nil {
				return statErr
			}
			totalSize += info.Size()
			fileCount++
		}

		return nil
	})

	return totalSize, fileCount, walkErr
}

// CollectOldestItems walks dir recursively and returns all files sorted oldest to newest by ModTime.
func CollectOldestItems(dir, ignoreDir string) ([]FileItem, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat path %s: %w", dir, err)
	}
	if !st.IsDir() {
		return []FileItem{{
			Path:    dir,
			Size:    st.Size(),
			ModTime: st.ModTime(),
		}}, nil
	}

	cleanIgnore := filepath.Clean(ignoreDir)
	cleanIgnorePrefix := cleanIgnore + string(filepath.Separator)
	entries := make([]FileItem, 0, defaultBatchCapacity)

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}

		if d.IsDir() {
			if path == cleanIgnore || strings.HasPrefix(path, cleanIgnorePrefix) {
				return filepath.SkipDir
			}
		} else {
			info, statErr := d.Info()
			if statErr != nil {
				return statErr
			}
			entries = append(entries, FileItem{
				Path:    path,
				Size:    info.Size(),
				ModTime: info.ModTime(),
			})
		}

		return nil
	})

	if walkErr != nil {
		return nil, walkErr
	}

	slices.SortFunc(entries, func(a, b FileItem) int {
		return a.ModTime.Compare(b.ModTime)
	})

	return entries, nil
}
