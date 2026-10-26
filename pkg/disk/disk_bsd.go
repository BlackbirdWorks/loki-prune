//go:build darwin || freebsd || openbsd || netbsd

package disk

import "golang.org/x/sys/unix"

func statBsize(stat *unix.Statfs_t) int64 {
	return int64(stat.Bsize)
}
