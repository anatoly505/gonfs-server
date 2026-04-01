//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

type diskStats struct {
	totalSize  uint64
	freeSize   uint64
	availSize  uint64
	totalFiles uint64
	freeFiles  uint64
}

func getDiskStats(path string) (diskStats, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return diskStats{}, err
	}

	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")

	var avail, total, free uint64
	r1, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&avail)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&free)),
	)
	if r1 == 0 {
		return diskStats{}, callErr
	}

	return diskStats{
		totalSize:  total,
		freeSize:   free,
		availSize:  avail,
		totalFiles: 1 << 20,
		freeFiles:  1 << 19,
	}, nil
}
