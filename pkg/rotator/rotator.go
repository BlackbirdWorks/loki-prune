// Package rotator manages file evaluation, rotation, delayed compression, and retention enforcement.
package rotator

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/blackbirdworks/loki-prune/pkg/compressor"
	"github.com/blackbirdworks/loki-prune/pkg/config"
	"github.com/blackbirdworks/loki-prune/pkg/disk"
	"github.com/blackbirdworks/loki-prune/pkg/logger"
	"github.com/blackbirdworks/loki-prune/pkg/loki"
	"github.com/blackbirdworks/loki-prune/pkg/tracker"
)

const (
	dirPerm       = 0750
	filePerm      = 0600
	percentFactor = 100.0
)

// Rotator manages the monitoring, rotation, compression, and pruning for a path.
type Rotator struct {
	tracker    *tracker.Tracker
	lokiClient *loki.Client
	compressor *compressor.Compressor
	cfg        config.PathConfig
	dryRun     bool
}

// New creates a new Rotator instance for a path configuration.
func New(pCfg config.PathConfig, lokiCfg config.LokiConfig, dryRun bool) (*Rotator, error) {
	if pCfg.DiskGuardPercent <= 0 {
		pCfg.DiskGuardPercent = 90.0
	}

	comp, err := compressor.New(pCfg.Rotation.Type, pCfg.Rotation.Level)
	if err != nil {
		return nil, fmt.Errorf("initializing compressor for %s: %w", pCfg.Name, err)
	}

	trk, trkErr := tracker.New(pCfg.Path, pCfg.ArchiveDir)
	if trkErr != nil {
		return nil, fmt.Errorf("initializing tracker for %s: %w", pCfg.Name, trkErr)
	}
	_, _, _ = trk.Sync()

	return &Rotator{
		cfg:        pCfg,
		lokiClient: loki.NewClient(lokiCfg.URL, lokiCfg.Timeout),
		compressor: comp,
		tracker:    trk,
		dryRun:     dryRun,
	}, nil
}

// Tracker returns the underlying in-memory file tracker.
func (r *Rotator) Tracker() *tracker.Tracker {
	return r.tracker
}

// StartTracker starts the real-time filesystem event listener on the tracker.
func (r *Rotator) StartTracker(ctx context.Context) {
	if r.tracker != nil {
		r.tracker.Start(ctx)
	}
}

// Close releases resources held by the rotator, including the file tracker.
func (r *Rotator) Close() error {
	if r.tracker != nil {
		return r.tracker.Close()
	}

	return nil
}

// Process runs a single evaluation and rotation cycle.
func (r *Rotator) Process(ctx context.Context) error {
	_, err := r.ProcessWithRatio(ctx)

	return err
}

// ProcessWithRatio runs an evaluation cycle and returns the quota/disk pressure ratio (0.0 to >=1.0).
func (r *Rotator) ProcessWithRatio(ctx context.Context) (float64, error) {
	log := logger.FromContext(ctx)
	log.InfoContext(ctx, "Evaluating path", "name", r.cfg.Name, "path", r.cfg.Path)

	if _, err := os.Stat(r.cfg.Path); os.IsNotExist(err) {
		log.WarnContext(ctx, "Path does not exist, skipping", "path", r.cfg.Path)

		return 0, nil
	}

	currentSize, fileCount, err := r.calculateDirSize(r.cfg.Path)
	if err != nil {
		return 0, fmt.Errorf("calculating directory size for %s: %w", r.cfg.Path, err)
	}

	log.InfoContext(ctx, "Path size status",
		"name", r.cfg.Name,
		"current", config.FormatBytes(currentSize),
		"trigger", config.FormatBytes(r.cfg.TriggerSizeBytes),
		"files", fileCount,
	)

	var ratio float64
	if r.cfg.TriggerSizeBytes > 0 {
		ratio = float64(currentSize) / float64(r.cfg.TriggerSizeBytes)
	}

	rotated := false
	if currentSize > r.cfg.TriggerSizeBytes {
		log.WarnContext(ctx, "Path size exceeded trigger limit, beginning rotation",
			"name", r.cfg.Name,
			"current", config.FormatBytes(currentSize),
			"trigger", config.FormatBytes(r.cfg.TriggerSizeBytes),
		)

		if rotErr := r.executeRotation(ctx, currentSize); rotErr != nil {
			return ratio, fmt.Errorf("executing rotation on %s: %w", r.cfg.Name, rotErr)
		}
		rotated = true
	}

	if usage, dErr := disk.Usage(r.cfg.Path); dErr == nil {
		diskRatio := usage.UsedPercent / percentFactor
		ratio = math.Max(ratio, diskRatio)
		if usage.UsedPercent >= r.cfg.DiskGuardPercent && !rotated {
			log.WarnContext(ctx, "Disk guard threshold exceeded, triggering emergency rotation",
				"name", r.cfg.Name,
				"used_percent", fmt.Sprintf("%.2f%%", usage.UsedPercent),
				"guard_threshold", fmt.Sprintf("%.2f%%", r.cfg.DiskGuardPercent),
			)
			if rotErr := r.executeRotation(ctx, currentSize); rotErr != nil {
				return ratio, fmt.Errorf("executing emergency disk guard rotation on %s: %w", r.cfg.Name, rotErr)
			}
		}
	}

	if r.cfg.Rotation.Compress {
		if compErr := r.processCompression(ctx); compErr != nil {
			log.ErrorContext(ctx, "Error during compression cycle", "name", r.cfg.Name, "err", compErr)
		}
	}

	if r.cfg.Rotation.MaxRotations > 0 {
		if pruneErr := r.enforceMaxRotations(ctx); pruneErr != nil {
			log.ErrorContext(ctx, "Error enforcing max rotations", "name", r.cfg.Name, "err", pruneErr)
		}
	}

	return ratio, nil
}

type fileEntry struct {
	modTime time.Time
	path    string
	size    int64
}

func (r *Rotator) executeRotation(ctx context.Context, currentSize int64) error {
	r.flushLokiBefore(ctx)

	items, err := r.collectOldestItems(r.cfg.Path)
	if err != nil {
		return fmt.Errorf("collecting oldest items: %w", err)
	}

	bytesToFree := currentSize - r.cfg.TargetSizeBytes
	timestamp := time.Now().UTC().Format("2006-01-02-150405")
	rotationDirName := fmt.Sprintf("rotation-%s", timestamp)
	rotationDirPath := filepath.Join(r.cfg.ArchiveDir, rotationDirName)

	if !r.dryRun {
		if mkErr := os.MkdirAll(rotationDirPath, dirPerm); mkErr != nil {
			return fmt.Errorf("creating rotation directory %s: %w", rotationDirPath, mkErr)
		}
	}

	movedBytes, movedCount := r.moveBatch(ctx, items, bytesToFree, rotationDirPath)

	log := logger.FromContext(ctx)
	log.InfoContext(ctx, "Rotation batch staged",
		"directory", rotationDirPath,
		"items_moved", movedCount,
		"freed_bytes", config.FormatBytes(movedBytes),
		"dry_run", r.dryRun,
	)

	r.syncLokiAfter(ctx)

	return nil
}

func (r *Rotator) flushLokiBefore(ctx context.Context) {
	log := logger.FromContext(ctx)
	log.InfoContext(ctx, "Flushing Loki before rotating")
	if !r.dryRun {
		if err := r.lokiClient.Flush(ctx); err != nil {
			log.WarnContext(ctx, "Loki flush failed (proceeding with caution)", "err", err)
		} else {
			log.InfoContext(ctx, "Loki flush completed successfully")
		}
	}
}

func (r *Rotator) syncLokiAfter(ctx context.Context) {
	log := logger.FromContext(ctx)
	log.InfoContext(ctx, "Synchronizing Loki after rotation")
	if !r.dryRun {
		if err := r.lokiClient.Reload(ctx); err != nil {
			log.WarnContext(ctx, "Loki reload after rotation returned error", "err", err)
		}
	}
}

func (r *Rotator) moveSingleItem(item fileEntry, targetDir string) error {
	relPath, relErr := filepath.Rel(r.cfg.Path, item.path)
	if relErr != nil {
		return relErr
	}
	if relPath == "." {
		relPath = filepath.Base(item.path)
	}
	destPath := filepath.Join(targetDir, relPath)

	if mkErr := os.MkdirAll(filepath.Dir(destPath), dirPerm); mkErr != nil {
		return fmt.Errorf("creating dest dir %s: %w", filepath.Dir(destPath), mkErr)
	}

	if renErr := os.Rename(item.path, destPath); renErr != nil {
		if copyErr := r.moveFileFallback(item.path, destPath); copyErr != nil {
			return fmt.Errorf("moving %s to %s: %w", item.path, destPath, copyErr)
		}
	}

	if r.tracker != nil {
		r.tracker.Remove(item.path)
	}

	if item.path == r.cfg.Path {
		emptyFile, createErr := os.OpenFile(r.cfg.Path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
		if createErr == nil {
			_ = emptyFile.Close()
		}
		if r.tracker != nil {
			_, _, _ = r.tracker.Sync()
		}
	}

	return nil
}

func (r *Rotator) moveFileFallback(src, dst string) error {
	in, openErr := os.Open(src)
	if openErr != nil {
		return fmt.Errorf("opening source file for cross-device copy: %w", openErr)
	}
	defer in.Close()

	out, createErr := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
	if createErr != nil {
		return fmt.Errorf("creating destination file for cross-device copy: %w", createErr)
	}
	defer out.Close()

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

func (r *Rotator) moveBatch(
	ctx context.Context,
	items []fileEntry,
	bytesToFree int64,
	targetDir string,
) (int64, int) {
	log := logger.FromContext(ctx)
	var movedBytes int64
	var movedCount int

	for _, item := range items {
		if movedBytes >= bytesToFree {
			break
		}

		if !r.dryRun {
			if err := r.moveSingleItem(item, targetDir); err != nil {
				log.WarnContext(ctx, "Failed to move item, skipping", "err", err)

				continue
			}
		}

		movedBytes += item.size
		movedCount++
	}

	return movedBytes, movedCount
}

func (r *Rotator) processCompression(ctx context.Context) error {
	if _, err := os.Stat(r.cfg.ArchiveDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(r.cfg.ArchiveDir)
	if err != nil {
		return err
	}

	log := logger.FromContext(ctx)
	uncompressed := r.getUncompressedRotations(entries)
	now := time.Now()

	for idx, dirInfo := range uncompressed {
		if r.shouldDelayCompression(idx, dirInfo.ModTime(), now) {
			continue
		}

		srcDir := filepath.Join(r.cfg.ArchiveDir, dirInfo.Name())
		archiveFile := srcDir + r.compressor.Extension()

		log.InfoContext(ctx, "Compressing rotation archive",
			"source", srcDir,
			"target", archiveFile,
			"type", r.cfg.Rotation.Type,
			"level", r.cfg.Rotation.Level,
		)

		if r.dryRun {
			continue
		}

		if compErr := r.compressor.CompressDirectory(srcDir, archiveFile); compErr != nil {
			log.ErrorContext(ctx, "Failed to compress rotation", "dir", srcDir, "err", compErr)

			continue
		}

		if rmErr := os.RemoveAll(srcDir); rmErr != nil {
			log.WarnContext(ctx, "Failed to remove raw directory after compression", "dir", srcDir, "err", rmErr)
		} else {
			log.InfoContext(ctx, "Compression complete, raw directory purged", "archive", archiveFile)
		}
	}

	return nil
}

func (r *Rotator) getUncompressedRotations(entries []os.DirEntry) []os.FileInfo {
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

	sort.Slice(uncompressed, func(i, j int) bool {
		return uncompressed[i].ModTime().After(uncompressed[j].ModTime())
	})

	return uncompressed
}

func (r *Rotator) shouldDelayCompression(rank int, modTime, now time.Time) bool {
	delayCount := r.cfg.Rotation.Delay.Rotations
	if delayCount > 0 && rank < delayCount {
		return true
	}

	delayAge := r.cfg.Rotation.Delay.Age
	if delayAge > 0 && now.Sub(modTime) < delayAge {
		return true
	}

	return false
}

func (r *Rotator) enforceMaxRotations(ctx context.Context) error {
	limit := r.cfg.Rotation.MaxRotations
	if limit <= 0 {
		return nil
	}

	if _, err := os.Stat(r.cfg.ArchiveDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(r.cfg.ArchiveDir)
	if err != nil {
		return err
	}

	allRotations := r.collectAllRotations(entries)
	if len(allRotations) <= limit {
		return nil
	}

	sort.Slice(allRotations, func(i, j int) bool {
		return allRotations[i].modTime.Before(allRotations[j].modTime)
	})

	log := logger.FromContext(ctx)
	toDelete := len(allRotations) - limit
	for idx := range toDelete {
		target := allRotations[idx].path
		log.InfoContext(ctx, "Pruning expired rotation", "target", target, "limit", limit)
		if !r.dryRun {
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

func (r *Rotator) collectAllRotations(entries []os.DirEntry) []rotationItem {
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
			path:    filepath.Join(r.cfg.ArchiveDir, name),
			modTime: info.ModTime(),
		})
	}

	return items
}

func (r *Rotator) calculateDirSize(dir string) (int64, int, error) {
	if r.tracker != nil && filepath.Clean(dir) == filepath.Clean(r.cfg.Path) {
		sz, count := r.tracker.Size()
		if count > 0 || sz > 0 {
			return sz, count, nil
		}

		return r.tracker.Sync()
	}

	st, err := os.Stat(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("stat path %s: %w", dir, err)
	}
	if !st.IsDir() {
		return st.Size(), 1, nil
	}

	var totalSize int64
	var fileCount int

	cleanArchive := filepath.Clean(r.cfg.ArchiveDir)

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() && filepath.Clean(path) == cleanArchive {
			return filepath.SkipDir
		}

		if !d.IsDir() {
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

func (r *Rotator) collectOldestItems(dir string) ([]fileEntry, error) {
	if r.tracker != nil && filepath.Clean(dir) == filepath.Clean(r.cfg.Path) {
		cached := r.tracker.OldestItems()
		if len(cached) > 0 {
			entries := make([]fileEntry, len(cached))
			for i, e := range cached {
				entries[i] = fileEntry{
					path:    e.Path,
					size:    e.Size,
					modTime: e.ModTime,
				}
			}

			return entries, nil
		}
	}

	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat path %s: %w", dir, err)
	}
	if !st.IsDir() {
		return []fileEntry{{
			path:    dir,
			size:    st.Size(),
			modTime: st.ModTime(),
		}}, nil
	}

	var entries []fileEntry
	cleanArchive := filepath.Clean(r.cfg.ArchiveDir)

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() && filepath.Clean(path) == cleanArchive {
			return filepath.SkipDir
		}

		if !d.IsDir() {
			info, statErr := d.Info()
			if statErr != nil {
				return statErr
			}
			entries = append(entries, fileEntry{
				path:    path,
				size:    info.Size(),
				modTime: info.ModTime(),
			})
		}

		return nil
	})

	if walkErr != nil {
		return nil, walkErr
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime.Before(entries[j].modTime)
	})

	return entries, nil
}
