//go:build !windows

// Package disk provides filesystem metrics and space utilization helpers.
package disk

import (
	"fmt"
	"math"

	"golang.org/x/sys/unix"
)

const percentMultiplier = 100.0

// Usage returns the disk capacity and utilization statistics for the filesystem at path.
func Usage(path string) (UsageStats, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return UsageStats{}, fmt.Errorf("statfs %s: %w", path, err)
	}

	bsize := statBsize(&stat)
	total := safeBlocksToInt64(stat.Blocks) * bsize
	free := safeBlocksToInt64(stat.Bavail) * bsize
	used := total - free

	var pct float64
	if total > 0 {
		pct = (float64(used) / float64(total)) * percentMultiplier
	}

	return UsageStats{
		TotalBytes:  total,
		FreeBytes:   free,
		UsedBytes:   used,
		UsedPercent: pct,
	}, nil
}

func safeBlocksToInt64(blocks uint64) int64 {
	if blocks > math.MaxInt64 {
		return math.MaxInt64
	}

	return int64(blocks)
}
