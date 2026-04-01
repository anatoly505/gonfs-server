# gonfs-server

Lightweight, streaming-optimized **NFSv3 server** written in pure Go. Single binary, zero dependencies, cross-platform (Linux / Windows / macOS).

Built for one job: serving large files at maximum throughput. Perfect for 4K UHD Blu-ray remux libraries (50–200 GB per file) to media players like Plex, Jellyfin, Kodi, or Infuse over a local network.

## Features

- **NFSv3** protocol — widely supported, stateless, simple
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
go build -o gonfs-server .

# Windows (cross-compile)
GOOS=windows GOARCH=amd64 go build -o gonfs-server.exe .
```

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

### Why FD caching matters

The `go-nfs` library opens and closes a file on **every** NFS `READ` RPC. For a 100 GB file read in 1 MB chunks, that's ~100,000 `open()/close()` cycles — each one a kernel roundtrip with path resolution and permission checks.

`CachedFS` wraps the filesystem and keeps file descriptors alive in an LRU cache. `Close()` on the wrapper is a no-op; the real FD is only closed on cache eviction. Since `ReadAt` uses `pread(2)` under the hood, concurrent reads to the same file are thread-safe without any locking.

## Recommended System Tuning

### Linux server

```bash
# Increase max open files
ulimit -n 65536

# TCP buffer auto-tuning
sysctl -w net.core.rmem_max=8388608
sysctl -w net.core.wmem_max=8388608
sysctl -w net.ipv4.tcp_rmem="4096 1048576 8388608"
sysctl -w net.ipv4.tcp_wmem="4096 1048576 8388608"
```

### Windows server

- Ensure Windows Firewall allows inbound TCP on the chosen port
- Run from an elevated (Administrator) command prompt if using port 2049

## Project Structure

```
├── main.go              Entry point, config, TCP listener
├── handler.go           NFS handler (mount, fsstat)
├── fdcache.go           File descriptor & stat caching layer
├── fsstat_unix.go       Disk stats via statfs() (Linux/macOS)
├── fsstat_windows.go    Disk stats via GetDiskFreeSpaceExW
├── go.mod
└── go.sum
```

## License

MIT
