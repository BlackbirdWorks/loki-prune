//go:build windows

// Package disk provides filesystem metrics and space utilization helpers.
package disk

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const percentMultiplier = 100.0

// Usage returns the disk capacity and utilization statistics for the filesystem at path.
func Usage(path string) (UsageStats, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return UsageStats{}, fmt.Errorf("converting path to UTF16: %w", err)
	}

	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes int64
	err = windows.GetDiskFreeSpaceEx(
		pathPtr,
		(*uint64)(unsafe.Pointer(&freeBytesAvailable)),
		(*uint64)(unsafe.Pointer(&totalNumberOfBytes)),
		(*uint64)(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)
	if err != nil {
		return UsageStats{}, fmt.Errorf("GetDiskFreeSpaceEx %s: %w", path, err)
	}

	used := totalNumberOfBytes - totalNumberOfFreeBytes
	var pct float64
	if totalNumberOfBytes > 0 {
		pct = (float64(used) / float64(totalNumberOfBytes)) * percentMultiplier
	}

	return UsageStats{
		TotalBytes:  totalNumberOfBytes,
		FreeBytes:   totalNumberOfFreeBytes,
		UsedBytes:   used,
		UsedPercent: pct,
	}, nil
}
