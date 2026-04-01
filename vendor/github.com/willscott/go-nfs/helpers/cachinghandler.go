package helpers

import (
	"crypto/sha256"
	"encoding/binary"
	"io/fs"
	"sync"

	"github.com/willscott/go-nfs"

	"github.com/go-git/go-billy/v5"
	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2"
)

func NewCachingHandler(h nfs.Handler, limit int) nfs.Handler {
	return NewCachingHandlerWithVerifierLimit(h, limit, limit)
}

func NewCachingHandlerWithVerifierLimit(h nfs.Handler, limit int, verifierLimit int) nfs.Handler {
	if limit < 2 || verifierLimit < 2 {
		nfs.Log.Warnf("Caching handler created with insufficient cache to support directory listing", "size", limit, "verifiers", verifierLimit)
	}
	cache, _ := lru.New[uuid.UUID, entry](limit)
	reverseCache := make(map[string][]uuid.UUID)
	verifiers, _ := lru.New[uint64, verifier](verifierLimit)
	return &CachingHandler{
		Handler:         h,
		activeHandles:   cache,
		reverseHandles:  reverseCache,
		activeVerifiers: verifiers,
		cacheLimit:      limit,
	}
}

type CachingHandler struct {
	nfs.Handler
	mu              sync.Mutex
	activeHandles   *lru.Cache[uuid.UUID, entry]
	reverseHandles  map[string][]uuid.UUID
	activeVerifiers *lru.Cache[uint64, verifier]
	cacheLimit      int
}

type entry struct {
	f billy.Filesystem
	p []string
}

// ToHandle takes a file and represents it with an opaque handle to reference it.
func (c *CachingHandler) ToHandle(f billy.Filesystem, path []string) []byte {
	joinedPath := f.Join(path...)

	c.mu.Lock()
	defer c.mu.Unlock()

	if handle := c.searchReverseCacheLocked(f, joinedPath); handle != nil {
		return handle
	}

	id := uuid.New()
	newPath := make([]string, len(path))
	copy(newPath, path)

	evictedKey, evictedEntry, hasOldest := c.activeHandles.GetOldest()
	if evicted := c.activeHandles.Add(id, entry{f, newPath}); evicted && hasOldest {
		rk := evictedEntry.f.Join(evictedEntry.p...)
		c.evictReverseCacheLocked(rk, evictedKey)
	}

	c.reverseHandles[joinedPath] = append(c.reverseHandles[joinedPath], id)
	b, _ := id.MarshalBinary()
	return b
}

// FromHandle converts from an opaque handle to the file it represents.
// O(1) — just an LRU Get. The original code had an O(N) scan of ALL
// cache keys on every call, which destroyed throughput during streaming.
func (c *CachingHandler) FromHandle(fh []byte) (billy.Filesystem, []string, error) {
	id, err := uuid.FromBytes(fh)
	if err != nil {
		return nil, []string{}, err
	}

	if f, ok := c.activeHandles.Get(id); ok {
		newP := make([]string, len(f.p))
		copy(newP, f.p)
		return f.f, newP, nil
	}
	return nil, []string{}, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale}
}

// searchReverseCacheLocked looks up a handle by path. Caller must hold c.mu.
func (c *CachingHandler) searchReverseCacheLocked(f billy.Filesystem, path string) []byte {
	uuids, exists := c.reverseHandles[path]
	if !exists {
		return nil
	}
	for _, id := range uuids {
		if candidate, ok := c.activeHandles.Get(id); ok {
			if candidate.f == f {
				return id[:]
			}
		}
	}
	return nil
}

// evictReverseCacheLocked removes a handle from the reverse cache. Caller must hold c.mu.
func (c *CachingHandler) evictReverseCacheLocked(path string, handle uuid.UUID) {
	uuids, exists := c.reverseHandles[path]
	if !exists {
		return
	}
	for i, u := range uuids {
		if u == handle {
			uuids[i] = uuids[len(uuids)-1]
			uuids = uuids[:len(uuids)-1]
			if len(uuids) == 0 {
				delete(c.reverseHandles, path)
			} else {
				c.reverseHandles[path] = uuids
			}
			return
		}
	}
}

func (c *CachingHandler) InvalidateHandle(fs billy.Filesystem, handle []byte) error {
	id, _ := uuid.FromBytes(handle)

	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.activeHandles.Get(id); ok {
		rk := e.f.Join(e.p...)
		c.evictReverseCacheLocked(rk, id)
	}
	c.activeHandles.Remove(id)
	return nil
}

func (c *CachingHandler) HandleLimit() int {
	return c.cacheLimit
}

func hasPrefix(path, prefix []string) bool {
	if len(prefix) > len(path) {
		return false
	}
	for i, e := range prefix {
		if path[i] != e {
			return false
		}
	}
	return true
}

type verifier struct {
	path     string
	contents []fs.FileInfo
}

func hashPathAndContents(path string, contents []fs.FileInfo) uint64 {
	vHash := sha256.New()
	vHash.Write(binary.BigEndian.AppendUint64([]byte{}, uint64(len(path))))
	vHash.Write([]byte(path))
	for _, c := range contents {
		vHash.Write([]byte(c.Name()))
	}
	verify := vHash.Sum(nil)[0:8]
	return binary.BigEndian.Uint64(verify)
}

func (c *CachingHandler) VerifierFor(path string, contents []fs.FileInfo) uint64 {
	id := hashPathAndContents(path, contents)
	c.activeVerifiers.Add(id, verifier{path, contents})
	return id
}

func (c *CachingHandler) DataForVerifier(path string, id uint64) []fs.FileInfo {
	if cache, ok := c.activeVerifiers.Get(id); ok {
		return cache.contents
	}
	return nil
}
