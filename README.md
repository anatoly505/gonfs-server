# gonfs-server

Lightweight, streaming-optimized **NFSv3 server** written in pure Go. Single binary, zero dependencies, cross-platform (Linux / Windows / macOS).

Built for one job: serving large files at maximum throughput. Perfect for 4K UHD Blu-ray remux libraries (50–200 GB per file) to media players like Plex, Jellyfin, Kodi, or Infuse over a local network.

## Features

- **NFSv3** protocol — widely supported, stateless, simple
- **Patched go-nfs core** — vendored with critical performance patches (see below)
- **Concurrent RPC handling** — up to 32 NFS requests processed in parallel per connection
- **Zero-alloc read path** — `sync.Pool` for read buffers and response writers, no GC pressure
- **FD caching** — keeps file descriptors open across NFS READ calls, eliminating ~100k `open()/close()` syscalls per 100 GB file
- **Stat caching** — configurable TTL, avoids redundant `stat()` on static media files
- **TCP tuning** — large send/receive buffers, `TCP_NODELAY`, keepalive
- **Real disk stats** — `FSStat` reports actual free space via OS APIs
- **Read-only mode** — on by default, reduces attack surface and code paths
- **Single binary** — `go build` and run, no installers, no runtimes
- **Cross-platform** — builds for Linux, Windows, macOS with `GOOS=...`

## Quick Start

### Build

```bash
# Linux / macOS
go build -mod=vendor -o gonfs-server .

# Windows (cross-compile)
GOOS=windows GOARCH=amd64 go build -mod=vendor -o gonfs-server.exe .
```

> Uses `-mod=vendor` because go-nfs is vendored with performance patches.

### Run

```bash
# Linux — export /mnt/media on default port 2049
sudo ./gonfs-server -root /mnt/media

# Windows — export D:\Movies on port 2049
gonfs-server.exe -root D:\Movies

# Custom port (no root required)
./gonfs-server -port 12049 -root /mnt/media
```

### Mount on client

```bash
# Linux
mount -t nfs -o port=2049,mountport=2049,nfsvers=3,noacl,tcp,rsize=1048576,wsize=1048576 <server-ip>:/ /mnt/nfs

# macOS
mount -o port=2049,mountport=2049 -t nfs <server-ip>:/ /mnt/nfs
```

> **Tip:** `rsize=1048576` (1 MB) is critical for streaming performance. Default NFS read sizes (32–64 KB) will bottleneck playback of high-bitrate content.

## CLI Options

| Flag | Default | Description |
|------|---------|-------------|
| `-port` | `2049` | TCP port to listen on |
| `-root` | `.` | Directory to export |
| `-ro` | `true` | Read-only mode |
| `-fdcache` | `256` | Open file descriptor cache size |
| `-hcache` | `2048` | NFS handle cache size |
| `-sndbuf` | `4096` | TCP send buffer size (KB) |
| `-rcvbuf` | `2048` | TCP receive buffer size (KB) |
| `-statttl` | `5` | Stat cache TTL (seconds) |

## Architecture

```
┌─────────────────────────────────────────────┐
│                  NFS Client                  │
│          (Plex / Jellyfin / mount)           │
└──────────────────┬──────────────────────────┘
                   │ TCP :2049
┌──────────────────▼──────────────────────────┐
│             tunedListener                    │
│   TCP_NODELAY · SO_SNDBUF 4MB · KeepAlive   │
├─────────────────────────────────────────────┤
│             go-nfs (NFSv3)                   │
│   ONC RPC · XDR · Mount · NLM · Read/Write  │
├─────────────────────────────────────────────┤
│          CachingHandler (LRU)                │
│        file handle ↔ path mapping            │
├─────────────────────────────────────────────┤
│              FSHandler                       │
│     Mount · Change · FSStat (real disk)      │
├─────────────────────────────────────────────┤
│              CachedFS                        │
│   FD cache (LRU) · Stat cache (TTL-based)   │
├─────────────────────────────────────────────┤
│           go-billy / osfs                    │
│          OS filesystem access                │
└─────────────────────────────────────────────┘
```

### Vendored go-nfs patches

The upstream `go-nfs` library has several performance bottlenecks that limit throughput for large file streaming. This project vendors go-nfs with the following patches:

**`conn.go` — concurrent request pipeline:**
| Problem | Fix |
|---------|-----|
| Requests processed sequentially (READ N blocks READ N+1) | Goroutine-per-request with 32-slot semaphore |
| `writeSerializer` channel buffer = 1 (pipeline stall) | Increased to 64 |
| `bytes.Buffer` allocated per request (~2 MB, GC churn) | `sync.Pool` with pre-allocated 2 MB buffers |
| `bufio.Reader/Writer` at default 4 KB | 1 MB reader, 2 MB writer |
| Request body read from TCP stream (blocks next request) | Eagerly buffered into memory before dispatch |

**`nfs_onread.go` — zero-copy read path:**
| Problem | Fix |
|---------|-----|
| `make([]byte, count)` per READ (~12 MB/s GC churn at 100 Mbps) | `sync.Pool` for read buffers |
| Intermediate `bytes.Buffer` + double copy of data | Direct write to response writer (1 copy instead of 3) |
| Unnecessary `Stat()` before every large read (CheckRead) | Removed — `ReadAt` returns actual bytes read |
| XDR struct encoding via reflection | Manual binary encoding for data payload |

### Why FD caching matters

The `go-nfs` library opens and closes a file on **every** NFS `READ` RPC. For a 100 GB file read in 1 MB chunks, that's ~100,000 `open()/close()` cycles — each one a kernel roundtrip with path resolution and permission checks.

`CachedFS` wraps the filesystem and keeps file descriptors alive in an LRU cache. `Close()` on the wrapper is a no-op; the real FD is only closed on cache eviction. Since `ReadAt` uses `pread(2)` under the hood, concurrent reads to the same file are thread-safe without any locking.

## Recommended System Tuning

### Linux server

```bash
# Increase max open files
ulimit -n 65536

# TCP buffer auto-tuning (critical for sustained throughput)
sysctl -w net.core.rmem_max=16777216
sysctl -w net.core.wmem_max=16777216
sysctl -w net.ipv4.tcp_rmem="4096 1048576 16777216"
sysctl -w net.ipv4.tcp_wmem="4096 1048576 16777216"
sysctl -w net.ipv4.tcp_window_scaling=1
sysctl -w net.core.netdev_max_backlog=5000
```

### Linux client (mount options)

```bash
# Optimal mount for 4K streaming
mount -t nfs -o port=2049,mountport=2049,nfsvers=3,noacl,tcp,\
rsize=1048576,wsize=1048576,\
noatime,nodiratime,\
ac,acregmin=60,acregmax=120,\
acdirmin=60,acdirmax=120 \
<server>:/ /mnt/media
```

Key options:
- `rsize=1048576` — 1 MB read size (vs default 32-64 KB)
- `noatime,nodiratime` — skip access time updates
- `acregmin/max=60/120` — cache file attributes 1-2 minutes (static media)

### Windows server

- Ensure Windows Firewall allows inbound TCP on the chosen port
- Run from an elevated (Administrator) command prompt if using port 2049
- For best I/O: place media on NTFS volume (not ReFS), disable Windows Defender real-time scanning for the export directory

## Project Structure

```
├── main.go              Entry point, config, TCP listener
├── handler.go           NFS handler (mount, fsstat)
├── fdcache.go           File descriptor & stat caching layer
├── fsstat_unix.go       Disk stats via statfs() (Linux/macOS)
├── fsstat_windows.go    Disk stats via GetDiskFreeSpaceExW
├── go.mod
├── go.sum
└── vendor/              Vendored dependencies (patched go-nfs)
    └── github.com/willscott/go-nfs/
        ├── conn.go          ← patched: concurrency + buffer pools
        └── nfs_onread.go    ← patched: zero-alloc read path
```

## License

MIT
