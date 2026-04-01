//go:build !windows

package main

import "syscall"

type diskStats struct {
	totalSize  uint64
	freeSize   uint64
	availSize  uint64
	totalFiles uint64
	freeFiles  uint64
}

func getDiskStats(path string) (diskStats, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return diskStats{}, err
	}
	bsize := uint64(stat.Bsize)
	return diskStats{
		totalSize:  stat.Blocks * bsize,
		freeSize:   stat.Bfree * bsize,
		availSize:  stat.Bavail * bsize,
		totalFiles: stat.Files,
		freeFiles:  stat.Ffree,
	}, nil
}
