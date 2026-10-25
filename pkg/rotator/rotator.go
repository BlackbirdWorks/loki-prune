package rotator

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/blackbirdworks/loki-prune/pkg/compressor"
	"github.com/blackbirdworks/loki-prune/pkg/config"
	"github.com/blackbirdworks/loki-prune/pkg/loki"
)

// Rotator manages the monitoring, rotation, compression, and pruning for a path.
type Rotator struct {
	cfg        config.PathConfig
	lokiClient *loki.Client
	compressor *compressor.Compressor
	dryRun     bool
}

// New creates a new Rotator instance for a path configuration.
func New(pCfg config.PathConfig, lokiCfg config.LokiConfig, dryRun bool) (*Rotator, error) {
	comp, err := compressor.New(pCfg.Rotation.Type, pCfg.Rotation.Level)
	if err != nil {
		return nil, fmt.Errorf("initializing compressor for %s: %w", pCfg.Name, err)
	}

	return &Rotator{
		cfg:        pCfg,
		lokiClient: loki.NewClient(lokiCfg.URL, lokiCfg.Timeout),
		compressor: comp,
		dryRun:     dryRun,
	}, nil
}

// Process runs a single evaluation and rotation cycle.
func (r *Rotator) Process(ctx context.Context) error {
	slog.Info("Evaluating path", "name", r.cfg.Name, "path", r.cfg.Path)

	if _, err := os.Stat(r.cfg.Path); os.IsNotExist(err) {
		slog.Warn("Path does not exist, skipping", "path", r.cfg.Path)
		return nil
	}

	// 1. Measure directory size (ignoring archive directory if it sits inside the path)
	currentSize, fileCount, err := r.calculateDirSize(r.cfg.Path)
	if err != nil {
		return fmt.Errorf("calculating directory size for %s: %w", r.cfg.Path, err)
	}

	slog.Info("Path size status",
		"name", r.cfg.Name,
		"current", config.FormatBytes(currentSize),
		"trigger", config.FormatBytes(r.cfg.TriggerSizeBytes),
		"files", fileCount,
	)

	// 2. Check if rotation should trigger
	if currentSize > r.cfg.TriggerSizeBytes {
		slog.Warn("Path size exceeded trigger limit, beginning rotation",
			"name", r.cfg.Name,
			"current", config.FormatBytes(currentSize),
			"trigger", config.FormatBytes(r.cfg.TriggerSizeBytes),
		)

		if err := r.executeRotation(ctx, currentSize); err != nil {
			return fmt.Errorf("executing rotation on %s: %w", r.cfg.Name, err)
		}
	}

	// 3. Process delayed compression on existing archives
	if r.cfg.Rotation.Compress {
		if err := r.processCompression(); err != nil {
			slog.Error("Error during compression cycle", "name", r.cfg.Name, "err", err)
		}
	}

	// 4. Enforce max rotations limit
	if r.cfg.Rotation.MaxRotations > 0 {
		if err := r.enforceMaxRotations(); err != nil {
			slog.Error("Error enforcing max rotations", "name", r.cfg.Name, "err", err)
		}
	}

	return nil
}

type fileEntry struct {
	path    string
	size    int64
	modTime time.Time
	isDir   bool
}

func (r *Rotator) executeRotation(ctx context.Context, currentSize int64) error {
	// A. Flush in-memory streams to disk before touching files
	slog.Info("Flushing Loki before rotating", "loki_url", r.lokiClient)
	if !r.dryRun {
		if err := r.lokiClient.Flush(ctx); err != nil {
			slog.Warn("Loki flush failed (proceeding with caution)", "err", err)
		} else {
			slog.Info("Loki flush completed successfully")
		}
	}

	// B. Collect files/subdirs in the watched path sorted oldest first
	items, err := r.collectOldestItems(r.cfg.Path)
	if err != nil {
		return fmt.Errorf("collecting oldest items: %w", err)
	}

	bytesToFree := currentSize - r.cfg.TargetSizeBytes
	slog.Info("Rotation target calculation",
		"current", config.FormatBytes(currentSize),
		"target", config.FormatBytes(r.cfg.TargetSizeBytes),
		"to_free", config.FormatBytes(bytesToFree),
	)

	timestamp := time.Now().UTC().Format("2006-01-02-150405")
	rotationDirName := fmt.Sprintf("rotation-%s", timestamp)
	rotationDirPath := filepath.Join(r.cfg.ArchiveDir, rotationDirName)

	if !r.dryRun {
		if err := os.MkdirAll(rotationDirPath, 0755); err != nil {
			return fmt.Errorf("creating rotation directory %s: %w", rotationDirPath, err)
		}
	}

	var movedBytes int64
	var movedCount int

	for _, item := range items {
		if movedBytes >= bytesToFree {
			break
		}

		relPath, err := filepath.Rel(r.cfg.Path, item.path)
		if err != nil {
			continue
		}
		destPath := filepath.Join(rotationDirPath, relPath)

		slog.Debug("Moving item to rotation batch", "src", item.path, "dest", destPath, "size", config.FormatBytes(item.size))

		if !r.dryRun {
			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				return fmt.Errorf("creating dest dir %s: %w", filepath.Dir(destPath), err)
			}
			if err := os.Rename(item.path, destPath); err != nil {
				slog.Warn("Failed to move item, attempting copy/delete fallback", "err", err)
				continue
			}
		}

		movedBytes += item.size
		movedCount++
	}

	slog.Info("Rotation batch staged",
		"directory", rotationDirPath,
		"items_moved", movedCount,
		"freed_bytes", config.FormatBytes(movedBytes),
		"dry_run", r.dryRun,
	)

	// C. Trigger Loki reload / synchronization after rotation
	slog.Info("Synchronizing Loki after rotation")
	if !r.dryRun {
		if err := r.lokiClient.Reload(ctx); err != nil {
			slog.Warn("Loki reload after rotation returned error", "err", err)
		}
	}

	return nil
}

func (r *Rotator) processCompression() error {
	if _, err := os.Stat(r.cfg.ArchiveDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(r.cfg.ArchiveDir)
	if err != nil {
		return err
	}

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

	// Sort newest first
	sort.Slice(uncompressed, func(i, j int) bool {
		return uncompressed[i].ModTime().After(uncompressed[j].ModTime())
	})

	now := time.Now()
	delayCount := r.cfg.Rotation.Delay.Rotations
	delayAge := r.cfg.Rotation.Delay.Age

	for idx, dirInfo := range uncompressed {
		// Rule 1: Delay by rotation count (skip newest N rotations)
		if delayCount > 0 && idx < delayCount {
			slog.Debug("Skipping compression (delayed by rotation count)", "dir", dirInfo.Name(), "rank", idx+1, "delay_count", delayCount)
			continue
		}

		// Rule 2: Delay by age (skip rotations younger than delay age)
		if delayAge > 0 && now.Sub(dirInfo.ModTime()) < delayAge {
			slog.Debug("Skipping compression (delayed by age)", "dir", dirInfo.Name(), "age", now.Sub(dirInfo.ModTime()), "delay_age", delayAge)
			continue
		}

		// Eligible for compression!
		srcDir := filepath.Join(r.cfg.ArchiveDir, dirInfo.Name())
		archiveFile := srcDir + r.compressor.Extension()

		slog.Info("Compressing rotation archive",
			"source", srcDir,
			"target", archiveFile,
			"type", r.cfg.Rotation.Type,
			"level", r.cfg.Rotation.Level,
		)

		if r.dryRun {
			continue
		}

		if err := r.compressor.CompressDirectory(srcDir, archiveFile); err != nil {
			slog.Error("Failed to compress rotation", "dir", srcDir, "err", err)
			continue
		}

		// Compression succeeded; purge raw directory
		if err := os.RemoveAll(srcDir); err != nil {
			slog.Warn("Failed to remove raw directory after compression", "dir", srcDir, "err", err)
		} else {
			slog.Info("Compression complete, raw directory purged", "archive", archiveFile)
		}
	}

	return nil
}

func (r *Rotator) enforceMaxRotations() error {
	max := r.cfg.Rotation.MaxRotations
	if max <= 0 {
		return nil
	}

	if _, err := os.Stat(r.cfg.ArchiveDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(r.cfg.ArchiveDir)
	if err != nil {
		return err
	}

	type rotationItem struct {
		path    string
		modTime time.Time
	}

	var allRotations []rotationItem
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "rotation-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		allRotations = append(allRotations, rotationItem{
			path:    filepath.Join(r.cfg.ArchiveDir, name),
			modTime: info.ModTime(),
		})
	}

	if len(allRotations) <= max {
		return nil
	}

	// Sort oldest first
	sort.Slice(allRotations, func(i, j int) bool {
		return allRotations[i].modTime.Before(allRotations[j].modTime)
	})

	toDelete := len(allRotations) - max
	for i := 0; i < toDelete; i++ {
		target := allRotations[i].path
		slog.Info("Pruning expired rotation (max_rotations exceeded)", "target", target, "max", max)
		if !r.dryRun {
			if err := os.RemoveAll(target); err != nil {
				slog.Error("Failed to remove expired rotation", "target", target, "err", err)
			}
		}
	}

	return nil
}

func (r *Rotator) calculateDirSize(dir string) (int64, int, error) {
	var totalSize int64
	var fileCount int

	cleanArchive := filepath.Clean(r.cfg.ArchiveDir)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Don't count archive directory if it is inside the watched directory
		if d.IsDir() && filepath.Clean(path) == cleanArchive {
			return filepath.SkipDir
		}

		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return nil
			}
			totalSize += info.Size()
			fileCount++
		}
		return nil
	})

	return totalSize, fileCount, err
}

func (r *Rotator) collectOldestItems(dir string) ([]fileEntry, error) {
	var entries []fileEntry
	cleanArchive := filepath.Clean(r.cfg.ArchiveDir)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if d.IsDir() && filepath.Clean(path) == cleanArchive {
			return filepath.SkipDir
		}

		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return nil
			}
			entries = append(entries, fileEntry{
				path:    path,
				size:    info.Size(),
				modTime: info.ModTime(),
				isDir:   false,
			})
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Sort oldest first
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime.Before(entries[j].modTime)
	})

	return entries, nil
}
