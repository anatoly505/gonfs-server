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
// The key optimization: go-nfs opens and closes a file on every NFS READ RPC.
// For a 100 GB remux file read in 1 MB chunks, that's ~100k open/close cycles.
// This layer keeps FDs open across reads, eliminating that overhead entirely.
type CachedFS struct {
	billy.Filesystem
	fdCache   *lru.Cache[string, *cachedFD]
	statCache *lru.Cache[string, *statEntry]
	statTTL   time.Duration
	mu        sync.Mutex
}

type cachedFD struct {
	file billy.File
	refs int64
}

type statEntry struct {
	info    os.FileInfo
	fetched time.Time
}

func NewCachedFS(inner billy.Filesystem, fdCacheSize int, statTTL time.Duration) billy.Filesystem {
	fdCache, _ := lru.NewWithEvict(fdCacheSize, func(_ string, fd *cachedFD) {
		fd.file.Close()
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
	// Fast path: FD already cached
	if fd, ok := c.fdCache.Get(filename); ok {
		atomic.AddInt64(&fd.refs, 1)
		return &fileRef{fd: fd}, nil
	}

	// Slow path: open file and cache the FD (double-checked locking)
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

// fileRef wraps a cached FD. ReadAt is thread-safe (uses pread),
// so concurrent NFS READs to the same file work without locking.
// Close is a no-op — the real FD lives in the LRU cache.
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
	atomic.AddInt64(&f.fd.refs, -1)
	return nil
}
