// Package disk provides filesystem metrics and space utilization helpers.
package disk

// UsageStats represents the capacity and utilization of a filesystem.
type UsageStats struct {
	TotalBytes  int64
	FreeBytes   int64
	UsedBytes   int64
	UsedPercent float64
}
