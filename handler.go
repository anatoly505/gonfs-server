package main

import (
	"context"
	"net"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
)

type FSHandler struct {
	fs       billy.Filesystem
	rootPath string
	readOnly bool
}

func NewFSHandler(fs billy.Filesystem, rootPath string, readOnly bool) *FSHandler {
	return &FSHandler{fs: fs, rootPath: rootPath, readOnly: readOnly}
}

func (h *FSHandler) Mount(_ context.Context, _ net.Conn, _ nfs.MountRequest) (nfs.MountStatus, billy.Filesystem, []nfs.AuthFlavor) {
	return nfs.MountStatusOk, h.fs, []nfs.AuthFlavor{nfs.AuthFlavorNull}
}

func (h *FSHandler) Change(fs billy.Filesystem) billy.Change {
	if h.readOnly {
		return nil
	}
	if c, ok := fs.(billy.Change); ok {
		return c
	}
	return nil
}

func (h *FSHandler) FSStat(_ context.Context, _ billy.Filesystem, s *nfs.FSStat) error {
	stats, err := getDiskStats(h.rootPath)
	if err != nil {
		return nil
	}
	s.TotalSize = stats.totalSize
	s.FreeSize = stats.freeSize
	s.AvailableSize = stats.availSize
	s.TotalFiles = stats.totalFiles
	s.FreeFiles = stats.freeFiles
	s.AvailableFiles = stats.freeFiles
	return nil
}

// Handle methods are delegated to CachingHandler wrapper.
func (h *FSHandler) ToHandle(_ billy.Filesystem, _ []string) []byte        { return nil }
func (h *FSHandler) FromHandle([]byte) (billy.Filesystem, []string, error) { return nil, nil, nil }
func (h *FSHandler) InvalidateHandle(billy.Filesystem, []byte) error       { return nil }
func (h *FSHandler) HandleLimit() int                                      { return -1 }
