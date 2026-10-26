//go:build linux

package disk

import "golang.org/x/sys/unix"

func statBsize(stat *unix.Statfs_t) int64 {
	return stat.Bsize
}
