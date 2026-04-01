package main

import (
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-billy/v5"
	lru "github.com/hashicorp/golang-lru/v2"
)

// CachedFS wraps a billy.Filesystem with file descriptor and stat caching.
//
// Critical for streaming: go-nfs opens and closes a file on every NFS READ.
// For a 100 GB file at 1 MB chunks = 100k open/close cycles.
// This layer keeps FDs open in an LRU cache, eliminating that overhead.
//
// Thread safety: ReadAt uses pread(2) which is safe for concurrent use.
// Eviction uses reference counting — a file is only closed when both
// evicted from cache AND all active readers have released it.
type CachedFS struct {
	billy.Filesystem
	fdCache   *lru.Cache[string, *cachedFD]
	statCache *lru.Cache[string, *statEntry]
	statTTL   time.Duration
	mu        sync.Mutex
}

type cachedFD struct {
	file    billy.File
	refs    int64
	evicted int32
}

type statEntry struct {
	info    os.FileInfo
	fetched time.Time
}

func NewCachedFS(inner billy.Filesystem, fdCacheSize int, statTTL time.Duration) billy.Filesystem {
	fdCache, _ := lru.NewWithEvict(fdCacheSize, func(_ string, fd *cachedFD) {
		atomic.StoreInt32(&fd.evicted, 1)
		if atomic.LoadInt64(&fd.refs) == 0 {
			fd.file.Close()
		}
	})
	statCache, _ := lru.New[string, *statEntry](fdCacheSize * 4)
	return &CachedFS{
		Filesystem: inner,
		fdCache:    fdCache,
		statCache:  statCache,
		statTTL:    statTTL,
	}
}

func (c *CachedFS) Open(filename string) (billy.File, error) {
	if fd, ok := c.fdCache.Get(filename); ok {
		atomic.AddInt64(&fd.refs, 1)
		return &fileRef{fd: fd}, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if fd, ok := c.fdCache.Get(filename); ok {
		atomic.AddInt64(&fd.refs, 1)
		return &fileRef{fd: fd}, nil
	}

	f, err := c.Filesystem.Open(filename)
	if err != nil {
		return nil, err
	}

	fd := &cachedFD{file: f, refs: 1}
	c.fdCache.Add(filename, fd)
	return &fileRef{fd: fd}, nil
}

func (c *CachedFS) Stat(filename string) (os.FileInfo, error) {
	if se, ok := c.statCache.Get(filename); ok {
		if time.Since(se.fetched) < c.statTTL {
			return se.info, nil
		}
	}

	info, err := c.Filesystem.Stat(filename)
	if err != nil {
		return nil, err
	}

	c.statCache.Add(filename, &statEntry{info: info, fetched: time.Now()})
	return info, nil
}

// fileRef wraps a cached FD. ReadAt delegates to pread(2) — thread-safe,
// no locking needed for concurrent reads to the same file.
// Close decrements refcount; actual file close happens only when
// both evicted from cache AND refcount reaches zero.
type fileRef struct {
	fd *cachedFD
}

func (f *fileRef) Name() string                              { return f.fd.file.Name() }
func (f *fileRef) Read(p []byte) (int, error)                { return f.fd.file.Read(p) }
func (f *fileRef) ReadAt(p []byte, off int64) (int, error)   { return f.fd.file.ReadAt(p, off) }
func (f *fileRef) Write(p []byte) (int, error)               { return f.fd.file.Write(p) }
func (f *fileRef) Seek(off int64, whence int) (int64, error) { return f.fd.file.Seek(off, whence) }
func (f *fileRef) Truncate(size int64) error                 { return f.fd.file.Truncate(size) }
func (f *fileRef) Lock() error                               { return f.fd.file.Lock() }
func (f *fileRef) Unlock() error                             { return f.fd.file.Unlock() }

func (f *fileRef) Close() error {
	if atomic.AddInt64(&f.fd.refs, -1) == 0 && atomic.LoadInt32(&f.fd.evicted) == 1 {
		return f.fd.file.Close()
	}
	return nil
}
