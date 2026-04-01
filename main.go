package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

func main() {
	port := flag.Int("port", 2049, "TCP port")
	root := flag.String("root", ".", "Directory to export")
	readOnly := flag.Bool("ro", true, "Read-only mode")
	fdCache := flag.Int("fdcache", 1024, "Open file descriptor cache size")
	handleCache := flag.Int("hcache", 4096, "NFS handle cache size")
	sendKB := flag.Int("sndbuf", 16384, "TCP send buffer size in KB")
	recvKB := flag.Int("rcvbuf", 8192, "TCP receive buffer size in KB")
	statSec := flag.Int("statttl", 30, "Stat cache TTL in seconds")
	gcPercent := flag.Int("gc", 400, "Go GC target percentage (higher = less GC, more RAM)")
	flag.Parse()

	debug.SetGCPercent(*gcPercent)
	runtime.GOMAXPROCS(runtime.NumCPU())

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		log.Fatalf("Bad root path: %v", err)
	}
	if info, err := os.Stat(absRoot); err != nil || !info.IsDir() {
		log.Fatalf("%q is not a valid directory", absRoot)
	}

	listener, err := newTunedListener(*port, *sendKB*1024, *recvKB*1024)
	if err != nil {
		log.Fatalf("Listen failed: %v", err)
	}

	innerFS := osfs.New(absRoot, osfs.WithBoundOS())
	cachedFS := NewCachedFS(innerFS, *fdCache, time.Duration(*statSec)*time.Second)

	handler := NewFSHandler(cachedFS, absRoot, *readOnly)
	cached := nfshelper.NewCachingHandler(handler, *handleCache)

	log.Printf("=== gonfs-server ===")
	log.Printf("  Export:       %s", absRoot)
	log.Printf("  Listen:       :%d", *port)
	log.Printf("  Read-only:    %v", *readOnly)
	log.Printf("  FD cache:     %d", *fdCache)
	log.Printf("  Handle cache: %d", *handleCache)
	log.Printf("  TCP sndbuf:   %d KB", *sendKB)
	log.Printf("  TCP rcvbuf:   %d KB", *recvKB)
	log.Printf("  Stat TTL:     %d s", *statSec)
	log.Printf("  GOGC:         %d", *gcPercent)
	log.Printf("  CPUs:         %d", runtime.NumCPU())
	log.Printf("")
	log.Printf("  Mount (Linux):")
	log.Printf("    mount -t nfs -o port=%d,mountport=%d,nfsvers=3,noacl,tcp,rsize=1048576,wsize=1048576,noatime,nodiratime,actimeo=600 <host>:/ <dir>", *port, *port)
	log.Printf("")
	log.Printf("  Kernel tuning (run on server if Linux):")
	log.Printf("    sysctl -w net.core.rmem_max=16777216")
	log.Printf("    sysctl -w net.core.wmem_max=16777216")

	done := make(chan struct{})
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		sig := <-ch
		log.Printf("Received %v, shutting down...", sig)
		listener.Close()
		close(done)
	}()

	err = nfs.Serve(listener, cached)
	select {
	case <-done:
		log.Println("Clean shutdown.")
	default:
		if err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}
}

type tunedListener struct {
	*net.TCPListener
	sendBuf int
	recvBuf int
}

func newTunedListener(port, sendBuf, recvBuf int) (*tunedListener, error) {
	addr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &tunedListener{TCPListener: l, sendBuf: sendBuf, recvBuf: recvBuf}, nil
}

func (l *tunedListener) Accept() (net.Conn, error) {
	tc, err := l.TCPListener.AcceptTCP()
	if err != nil {
		return nil, err
	}
	tc.SetNoDelay(true)
	tc.SetWriteBuffer(l.sendBuf)
	tc.SetReadBuffer(l.recvBuf)
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(30 * time.Second)
	return tc, nil
}
